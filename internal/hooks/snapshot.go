package hooks

import (
	"io/fs"
	"path/filepath"
	"sort"
	"time"
)

// Entry 是快照里单个常规文件的观察值（§10.6：路径 + mtime + size）。
type Entry struct {
	ModTime time.Time
	Size    int64
}

// Snapshot 对 root 做一次目录快照。
//
// 跳过任意层级的 .git 目录（钩子里跑 git 命令会重写索引/对象，属噪声而非
// 副作用）；只记常规文件——符号链接与目录本身不入表（目录的新增/删除会由
// 其中文件的出现/消失反映；空目录的创建检测不到，v1 接受，§10.6 不承诺
// 穷尽）。key 是相对 root 的 / 分隔路径，与 Dst 同一语法。
func Snapshot(root string) (map[string]Entry, error) {
	out := map[string]Entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if p != root && d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = Entry{ModTime: fi.ModTime(), Size: fi.Size()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Classify 比较两次快照（§10.6、M3-R6）：
//
//	planned 里的路径被触碰   → modified（「属于计划内但内容被改写」——go mod
//	                          tidy / 格式化工具会这么做，是下一轮「本地改过」
//	                          误判的来源，必须报告）；
//	generated ∧ ¬planned 被触碰 → 静默（钩子重建生成物是其职责）；
//	其余被触碰                → unplanned（「计划外副作用」，逐条报告）。
//
// 两个返回均按路径排序。
func Classify(before, after map[string]Entry, planned, generated map[string]bool) (unplanned, modified []string) {
	touched := func(p string) bool {
		b, ok := before[p]
		if !ok {
			return true // 新增
		}
		a, ok := after[p]
		if !ok {
			return true // 消失
		}
		return b.ModTime != a.ModTime || b.Size != a.Size
	}
	report := func(p string) {
		switch {
		case planned[p]:
			modified = append(modified, p)
		case generated[p]:
			// 钩子重建 generated 生成物：预期行为。
		default:
			unplanned = append(unplanned, p)
		}
	}
	for p := range after {
		if touched(p) {
			report(p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			report(p) // 消失（touched 对两边都成立，这里补 before 侧独有的键）
		}
	}
	sort.Strings(unplanned)
	sort.Strings(modified)
	return unplanned, modified
}
