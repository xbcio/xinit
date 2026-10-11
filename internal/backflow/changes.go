package backflow

import (
	"fmt"
	"strings"
)

// Change 是一条改动记录（`git diff-tree --raw` 的五字段 + 路径）。
type Change struct {
	Status  string // "A"/"M"/"D"/"T"/"R100"/"C75"（原始 status 字段，含 score）
	OldMode string // "100644"
	NewMode string
	OldSha  string // 40 位（A 记录为全 0）
	NewSha  string
	Path    string // 派生侧路径（Dst；R/C 时为**新**路径）
	OldPath string // 仅 R/C：旧路径
}

// EnumerateChanges 跑 §20.4 守卫 6：`git diff-tree --no-commit-id --raw -r -M -z <c>^ <c>`
// 的原始输出交给 parseRawChanges。必须 `-M` 与 `--raw`：plumbing 未设 diff.renames，
// 无 -M 时纯改名会被拆成 A+D；--raw 才带 mode（exec 位是 §8.3 一等公民）。
// 空提交（无任何记录）返回 error。
func EnumerateChanges(repoDir, commit string) ([]Change, error) {
	// 守卫 4 前置：`<c>^` 必须存在且唯一，否则 diff-tree 只会给出难读的 git 报错。
	sha, err := checkCommitShape(repoDir, commit)
	if err != nil {
		return nil, err
	}
	out, err := runGit(repoDir, nil, "diff-tree", "--no-commit-id", "--raw", "-r", "-M", "-z", sha+"^", sha)
	if err != nil {
		return nil, err
	}
	return parseRawChanges(out)
}

// parseRawChanges 纯解析 `--raw -z` 字节流（与子进程解耦，供单测直喂）。
// 逐条形状：`: <old mode> <new mode> <old sha> <new sha> <status>` NUL <path> NUL；
// R/C 记录两条路径段，**旧路径在前、新路径在后**（实测钉死，与 non-z 输出同序）。
func parseRawChanges(z []byte) ([]Change, error) {
	if len(z) == 0 {
		return nil, fmt.Errorf("改动枚举为空：该提交没有任何改动（空提交），没有可回流的内容")
	}
	segs := splitNUL(z)
	out := make([]Change, 0, len(segs))
	for i := 0; i < len(segs); {
		head := segs[i]
		if !strings.HasPrefix(head, ":") {
			return nil, fmt.Errorf("解析 git diff-tree --raw -z 失败：段 %q 不是 `:<old mode> <new mode> <old sha> <new sha> <status>` 记录头", head)
		}
		f := strings.Fields(head[1:])
		if len(f) != 5 {
			return nil, fmt.Errorf("解析 git diff-tree --raw -z 失败：记录头 %q 应有 5 个字段（old mode/new mode/old sha/new sha/status），实际 %d 个", head, len(f))
		}
		c := Change{OldMode: f[0], NewMode: f[1], OldSha: f[2], NewSha: f[3], Status: f[4]}
		i++
		nPath := 1
		if strings.HasPrefix(c.Status, "R") || strings.HasPrefix(c.Status, "C") {
			nPath = 2
		}
		if i+nPath > len(segs) {
			return nil, fmt.Errorf("解析 git diff-tree --raw -z 失败：%s 记录需要 %d 条路径段，只剩 %d 条", c.Status, nPath, len(segs)-i)
		}
		if nPath == 2 {
			c.OldPath, c.Path = segs[i], segs[i+1]
		} else {
			c.Path = segs[i]
		}
		i += nPath
		out = append(out, c)
	}
	return foldAddedDeletedRename(out), nil
}

// foldAddedDeletedRename 是守卫 6 的 A+D 同 hash 兜底：同一 blob 同时出现 `A`（新
// 路径）与 `D`（旧路径）时判为改名——合并为一条 `R100`，与真实 R 记录同构，后续
// 统一按 R 类「警告不导出」处理（模板侧不会多出一个重复文件）。
func foldAddedDeletedRename(chs []Change) []Change {
	pairOf := make(map[int]int) // A 的下标 → 配对的 D 的下标
	paired := make([]bool, len(chs))
	for i, a := range chs {
		if a.Status != "A" || isZeroSha(a.NewSha) {
			continue
		}
		for j, d := range chs {
			if paired[j] || d.Status != "D" || d.OldSha != a.NewSha {
				continue
			}
			pairOf[i], paired[i], paired[j] = j, true, true
			break
		}
	}
	if len(pairOf) == 0 {
		return chs
	}
	out := make([]Change, 0, len(chs))
	for i, c := range chs {
		if j, ok := pairOf[i]; ok {
			d := chs[j]
			out = append(out, Change{
				Status:  "R100",
				OldMode: d.OldMode,
				NewMode: c.NewMode,
				OldSha:  d.OldSha,
				NewSha:  c.NewSha,
				Path:    c.Path,
				OldPath: d.Path,
			})
			continue
		}
		if paired[i] { // 已被配对的 D 记录
			continue
		}
		out = append(out, c)
	}
	return out
}

// isZeroSha 判一段 sha 是否为全 0（git 用全 0 表示该侧不存在）。
func isZeroSha(s string) bool { return strings.Trim(s, "0") == "" }
