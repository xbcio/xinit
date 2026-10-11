package template

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/xbcio/xinit/internal/fsutil"
)

// implicitExcluded 与 render.implicitExcludes 保持一致。
//
// 这里重复一份而不是从 render 导入，是为了保持 template 包不反向依赖
// render。TestTreeHashIgnoresGitDir 只钉住 .git 一条；跨包一致性没有
// 测试能强制，改 render 侧名单时这份必须同步改 —— 靠这条注释提醒。
var implicitExcluded = []string{".git", ".xinit", "xinit.yml"}

// OpenDir 把一个本地目录当模板源，只做 stat 校验，返回可遍历的 fs.FS。
//
// 不再在这里算树 hash：dir 源的树 hash 要按清单 exclude 排除（规范
// §5.4），而 exclude 要等清单编译完才有 —— 调用方须在编译清单之后另行
// 调用 TreeHashOf。
func OpenDir(dir string) (fs.FS, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("模板目录 %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("模板路径 %s 不是目录", dir)
	}
	return os.DirFS(dir), nil
}

// TreeHashOf 计算一个目录的树 hash（规范 §5.4）。
//
// 按 **Src** 的字节序排序、写入 **Src**，不是 Dst —— Dst 依赖 vars 和
// 清单，用 Dst 会让同一个目录换个 vars 就得到不同的「版本锚点」，
// 而且算锚点之前还得先解析清单。
//
// 每条记录带分隔符与类型（见 fsutil.RenderTreeHash），否则
// a="ab", b="" 与 a="a", b="b" 会得到相同的 hash。
//
// exclude 为 nil 时不额外排除（只应用隐式排除）。遍历顺序：先隐式排除
// （现状不动），再 exclude —— 目录命中 → fs.SkipDir、文件命中 →
// skip，与 render.Paths 的 §3.2 语义一致（目录被排除后其下 ! 无法
// 重新包含）。
//
// §22.7 R4c 立注：dir 源树 hash **含可执行位**（输入元组/版本锚点契约不破坏，
// 维持现状）。可观测差异：跨平台移动模板目录（如 macOS → Windows）后 exec
// 记账可能整体翻转、hash 随之变化，update 可能报告「模板版本变化」类输出而
// 模板内容实际未变。与快照键「exec 不入键」（§21.9）互指——对象不同：
// 那里是输入元组，这里是版本锚点。
func TreeHashOf(dir string, exclude *fsutil.Matcher) (string, error) {
	fsys := os.DirFS(dir)

	var entries []fsutil.TreeEntry
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if isImplicitlyExcluded(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if exclude != nil && exclude.Match(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		if d.Type()&fs.ModeSymlink != 0 {
			// 符号链接按**链接文本** hash —— 与 git 的模型一致（git 里
			// symlink 的 blob 就是目标路径字符串）。绝不能 fs.ReadFile
			// 跟随：指向目录或悬空时它会直接报错，把整棵树的 hash 拖垮；
			// 指向文件时又会静默 hash 目标内容，让两个链接碰撞。
			target, err := os.Readlink(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil {
				return err
			}
			entries = append(entries, fsutil.TreeEntry{
				Src:  path.Clean(p),
				Kind: "symlink",
				Exec: false,
				Hash: fsutil.CompareHash([]byte(target), false),
			})
			return nil
		}

		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}

		entries = append(entries, fsutil.TreeEntry{
			Src:  path.Clean(p),
			Kind: "regular",
			Exec: info.Mode()&0o111 != 0,
			Hash: fsutil.CompareHash(content, fsutil.IsBinary(content)),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("遍历模板目录: %w", err)
	}

	return fsutil.RenderTreeHash(entries), nil
}

func isImplicitlyExcluded(p string) bool {
	for _, ex := range implicitExcluded {
		if p == ex || len(p) > len(ex) && p[:len(ex)+1] == ex+"/" {
			return true
		}
	}
	return false
}
