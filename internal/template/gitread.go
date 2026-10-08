// Package template 把模板项目适配成 io/fs.FS。
//
// 两种来源：
//   - git 仓库的某个 commit（internal/template/gitread.go + gitfs.go）
//   - 本地目录（internal/template/dirfs.go）
//
// 读数一律走 git ls-tree + git cat-file，**绝不用 git archive** ——
// 实测 archive 会静默丢弃标记 export-ignore 的文件、展开 export-subst，
// 并应用 text/eol/core.autocrlf 与 filter 驱动（含 LFS smudge），
// 同一 commit 可能取出不同字节。
package template

import (
	"bytes"
	"fmt"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
)

// TreeEntry 是 ls-tree 列出的一个条目。
type TreeEntry struct {
	Path     string
	Mode     fs.FileMode
	Exec     bool
	Symlink  bool
	BlobHash string
}

// runGit 执行一个 git 子进程。repoDir 为空时不传 -C（给 clone 用）。
//
// 参数安全三层：
//   - 调用方先用 checkRefArg 拒绝以 '-' 开头的 revision / source；
//   - 位置参数前加 --end-of-options（实测 ls-tree 与 rev-parse 都支持，
//     且 '--upload-pack=evil' 会被拒绝、rc=128）；
//   - 禁用 ext:: 传输协议。
func runGit(repoDir string, args ...string) ([]byte, error) {
	return runGitStdin(repoDir, "", args...)
}

// runGitStdin 与 runGit 相同，但把 stdin 也接上（cat-file --batch 从
// stdin 读对象名）。错误里同样带上 git 的 stderr —— 只报
// "exit status 128" 会把真正的原因（仓库不存在、对象损坏）丢掉。
func runGitStdin(repoDir, stdin string, args ...string) ([]byte, error) {
	var base []string
	if repoDir != "" {
		base = append(base, "-C", repoDir)
	}
	base = append(base, "-c", "protocol.ext.allow=never")
	cmd := exec.Command("git", append(base, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w\n%s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// checkRefArg 拒绝会被 git 当成选项的参数。
func checkRefArg(name, v string) error {
	if v == "" {
		return fmt.Errorf("%s 不能为空", name)
	}
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("%s 不能以 '-' 开头（会被 git 当成选项）: %q", name, v)
	}
	return nil
}

// ListTree 列出某个 commit 的全部文件。
//
// 用 -r -z：-r 递归展开目录，-z 用 NUL 分隔 —— 不加 -z 的话含换行的
// 文件名会被 git 加引号，解析出来是错的。
//
// 注意 -r **不输出目录**：目录信息由调用方从路径前缀合成。
func ListTree(repoDir, commit string) ([]TreeEntry, error) {
	if err := checkRefArg("commit", commit); err != nil {
		return nil, err
	}

	out, err := runGit(repoDir, "ls-tree", "-r", "-z", "--end-of-options", commit)
	if err != nil {
		return nil, err
	}

	var entries []TreeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		// 格式：<mode> SP <type> SP <hash> TAB <path>
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("ls-tree 输出格式异常: %q", rec)
		}
		meta := string(rec[:tab])
		path := string(rec[tab+1:])

		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, fmt.Errorf("ls-tree 元信息格式异常: %q", meta)
		}
		entry, err := mapGitMode(fields[0], fields[2])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		entry.Path = path
		entries = append(entries, entry)
	}
	return entries, nil
}

// mapGitMode 把 git 的 mode 字符串映射成 TreeEntry。
//
// 完整映射 —— 缺任何一项都会在遇到时才炸：
//
//	100644 / 100664  普通文件
//	100755           可执行文件
//	120000           symlink（fs.FS 层报 ModeSymlink，由 §9.2 拒绝跟随）
//	160000           submodule —— 既不是 blob，也没有对应 Kind，明确拒绝
func mapGitMode(mode, blobHash string) (TreeEntry, error) {
	switch mode {
	case "100644", "100664":
		return TreeEntry{Mode: 0o644, BlobHash: blobHash}, nil
	case "100755":
		return TreeEntry{Mode: 0o755, Exec: true, BlobHash: blobHash}, nil
	case "120000":
		return TreeEntry{Mode: fs.ModeSymlink, Symlink: true, BlobHash: blobHash}, nil
	case "160000":
		return TreeEntry{}, fmt.Errorf(
			"模板包含 submodule（mode 160000）；xinit 不支持 submodule，" +
				"请把它替换成普通文件或从模板中移除")
	default:
		return TreeEntry{}, fmt.Errorf("未知的 git mode %q", mode)
	}
}

// CatBlob 读取一个 blob 的原始字节。
//
// --batch 的输出格式是：
//
//	<sha> SP blob SP <size> LF
//	<size 字节的内容> LF
//
// 解析必须按 header 里的 size **精确读取 size+1 字节**，不能按行或按
// 分隔符扫描 —— 否则文件内容恰好形如 "<40hex> blob 5" 时会串位。
func CatBlob(repoDir, blobHash string) ([]byte, error) {
	if err := checkRefArg("blobHash", blobHash); err != nil {
		return nil, err
	}

	out, err := runGitStdin(repoDir, blobHash+"\n", "cat-file", "--batch")
	if err != nil {
		return nil, err
	}

	lf := bytes.IndexByte(out, '\n')
	if lf < 0 {
		return nil, fmt.Errorf("cat-file 输出缺少 header: %q", out)
	}
	header := string(out[:lf])
	fields := strings.Fields(header)
	if len(fields) != 3 {
		// 缺失对象时 git 输出 "<sha> missing"
		return nil, fmt.Errorf("cat-file 未能读取 blob %s: %s", blobHash, header)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("cat-file header 里的 size 无法解析: %q", header)
	}

	body := out[lf+1:]
	if len(body) < size {
		return nil, fmt.Errorf("cat-file 输出被截断：期望 %d 字节，实得 %d", size, len(body))
	}
	return body[:size], nil
}

// ReadBlobs 一次性读取一批 blob，避免为每个文件起一个进程。
func ReadBlobs(repoDir string, hashes []string) (map[string][]byte, error) {
	if len(hashes) == 0 {
		return map[string][]byte{}, nil
	}
	for _, h := range hashes {
		if err := checkRefArg("blobHash", h); err != nil {
			return nil, err
		}
	}

	out, err := runGitStdin(repoDir, strings.Join(hashes, "\n")+"\n", "cat-file", "--batch")
	if err != nil {
		return nil, err
	}

	result := make(map[string][]byte, len(hashes))
	rest := out
	for range hashes {
		lf := bytes.IndexByte(rest, '\n')
		if lf < 0 {
			return nil, fmt.Errorf("cat-file 输出不足：期望 %d 个对象", len(hashes))
		}
		header := string(rest[:lf])
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return nil, fmt.Errorf("cat-file 未能读取对象: %s", header)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("size 无法解析: %q", header)
		}
		body := rest[lf+1:]
		if len(body) < size {
			return nil, fmt.Errorf("cat-file 输出被截断：期望 %d 字节", size)
		}
		result[fields[0]] = body[:size]
		rest = body[size+1:] // 跳过后面的 LF
	}
	return result, nil
}
