package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// CheckOptions 是 check 的输入（R13：作用于 cwd）。
// 只读：不加锁、项目内不写任何文件（§18.1）。
type CheckOptions struct {
	ProjectDir string
	Fetch      bool
}

// checkKind 是检查行的级别。
type checkKind int

const (
	kindFail checkKind = iota
	kindWarn
	kindSkip
)

// String 返回级别词。FAIL/warn/skip 是稳定标识、供脚本 grep（§18.6），
// 拼写不可改。
func (k checkKind) String() string {
	switch k {
	case kindFail:
		return "FAIL"
	case kindWarn:
		return "warn"
	case kindSkip:
		return "skip"
	}
	return "unknown"
}

// checkLine 是报告中的一条检查结果。Detail 是预排版的附加行（不含基础缩进）。
type checkLine struct {
	Kind   checkKind
	Text   string
	Detail []string
}

// checkSection 是一个检查组。
type checkSection struct {
	Title string
	Lines []checkLine
}

// checkReport 是 §18.6 报告的数据；Print 只管排版。
type checkReport struct {
	Source   string
	Sections []checkSection

	Fails, Warns, Skips int
}

// 组索引（与 newCheckReport 的标题顺序一致）。
const (
	secA = 0
	secB = 1
	secC = 2
	secD = 3
)

// newCheckReport 构造带四组标题的空报告：组标题序列本身即检查清单（§18.6）。
func newCheckReport(source string) *checkReport {
	return &checkReport{
		Source: source,
		Sections: []checkSection{
			{Title: "A 状态与 base 树（§4.3 / §5.1 / §5.2）"},
			{Title: "B 冲突残留（§10.4）"},
			{Title: "C 配置与 git 契约（§3.2 / §5.5）"},
			{Title: "D 模板面（§5.2 / §7.6 / §11.4）"},
		},
	}
}

// add 追加一行并返回其指针（调用方可挂 Detail）。
func (r *checkReport) add(sec int, k checkKind, format string, args ...any) *checkLine {
	s := &r.Sections[sec]
	s.Lines = append(s.Lines, checkLine{Kind: k, Text: fmt.Sprintf(format, args...)})
	return &s.Lines[len(s.Lines)-1]
}

func (r *checkReport) fail(sec int, format string, args ...any) *checkLine {
	r.Fails++
	return r.add(sec, kindFail, format, args...)
}

func (r *checkReport) warn(sec int, format string, args ...any) *checkLine {
	r.Warns++
	return r.add(sec, kindWarn, format, args...)
}

func (r *checkReport) skip(sec int, format string, args ...any) *checkLine {
	r.Skips++
	return r.add(sec, kindSkip, format, args...)
}

// Print 输出报告（§18.6）：组标题恒打印（含空组），末行为汇总。
func (r *checkReport) Print(w io.Writer) {
	fmt.Fprintf(w, "xinit check: %s\n", r.Source)
	for _, s := range r.Sections {
		fmt.Fprintf(w, "%s:\n", s.Title)
		for _, l := range s.Lines {
			fmt.Fprintf(w, "  %-4s %s\n", l.Kind, l.Text)
			for _, d := range l.Detail {
				fmt.Fprintf(w, "  %s\n", d)
			}
		}
	}
	if r.Fails > 0 {
		fmt.Fprintf(w, "检查汇总: %d 项失败、%d 项警告、%d 项跳过（退出码 %d）\n",
			r.Fails, r.Warns, r.Skips, CodeCheck)
	} else {
		fmt.Fprintf(w, "检查汇总: 通过（0 项失败、%d 项警告、%d 项跳过）\n", r.Warns, r.Skips)
	}
}

// baseFile 是 base 树中的一个普通文件。
type baseFile struct {
	content []byte
	exec    bool
}

// listBaseTree 一次列举 base 树（§18.2）：普通文件进 regular，其余非目录
// 条目（符号链接/设备/FIFO 等）进 nonRegular[rel]=类型串；目录不入表——
// 嵌套路径的目录项是正常结构。base 目录缺失 = 两张空表。
func listBaseTree(base string) (regular map[string]baseFile, nonRegular map[string]string, err error) {
	regular = map[string]baseFile{}
	nonRegular = map[string]string{}
	if _, err := os.Lstat(base); os.IsNotExist(err) {
		return regular, nonRegular, nil
	} else if err != nil {
		return nil, nil, err
	}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == base || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			nonRegular[rel] = info.Mode().Type().String()
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		regular[rel] = baseFile{content: data, exec: info.Mode().Perm()&0o111 != 0}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("读取 base 树: %w", err)
	}
	return regular, nonRegular, nil
}

// validCheckKey 判定 files 键是否为规范相对路径（§4.3）：非空、非绝对、
// 按 / 分段无空/"."/".." 段、path.Clean 不变。state.Load 不查，check 补全。
func validCheckKey(p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// checkGroupA 执行 A1–A10（§18.2）。返回的错误只来自 IO 意外；
// 判定结果一律进报告。
func checkGroupA(r *checkReport, projectDir string, st *state.State) error {
	regular, nonRegular, err := listBaseTree(state.BaseDir(projectDir))
	if err != nil {
		return err
	}
	keys := sortedStringKeys(st.Files)

	// A1：非法键仅报 A1，不参与 A2–A9。
	var legal []string
	legalSet := map[string]bool{}
	for _, p := range keys {
		if !validCheckKey(p) {
			r.fail(secA, "files 键 %q 不是规范的相对路径（§4.3）", p)
			continue
		}
		legal = append(legal, p)
		legalSet[p] = true
	}

	// A2：按折叠键分组，组内首键与其余每个键各报一条。
	firstByFold := map[string]string{}
	for _, p := range legal {
		f := fsutil.Fold(p)
		if first, ok := firstByFold[f]; ok {
			r.fail(secA, "%q 与 %q 折叠等价冲突（§9.3）", first, p)
			continue
		}
		firstByFold[f] = p
	}

	// A3
	for _, p := range legal {
		if st.Files[p].Src == "" {
			r.fail(secA, "files[%q].src 为空（§4.3）", p)
		}
	}

	// A4：只查 regular 表；nonRegular 中的键由 A6 独占报告。
	for _, p := range legal {
		f, ok := regular[p]
		if !ok {
			if _, nr := nonRegular[p]; !nr {
				r.fail(secA, "base/%s 缺失（§5.1——update 将按 §5.2 处理）", p)
			}
			continue
		}
		fst := st.Files[p]
		if got := fsutil.CompareHash(f.content, fst.Binary); got != fst.Hash {
			r.fail(secA, "base/%s 与记账 hash 不符（got %s, want %s；§5.1——update 将按 §5.2 处理）",
				p, got, fst.Hash)
		}
	}

	// A5
	for _, p := range sortedStringKeys(regular) {
		if !legalSet[p] {
			r.fail(secA, "base/%s 不在记账中（残片；update 将清除）", p)
		}
	}

	// A6
	for _, p := range sortedStringKeys(nonRegular) {
		r.fail(secA, "base/%s 不是普通文件（%s）", p, nonRegular[p])
	}

	// A7–A9：记录位与 base 内容/mode 对照，仅对 regular 中的键。
	for _, p := range legal {
		f, ok := regular[p]
		if !ok {
			continue
		}
		fst := st.Files[p]
		if bin := fsutil.IsBinary(f.content); fst.Binary != bin {
			r.fail(secA, "files[%q].binary=%v 但 base 内容是 %v", p, fst.Binary, bin)
		}
		if mixed := fsutil.DetectEOL(f.content) == fsutil.EOLMixed; fst.MixedEOL != mixed {
			r.fail(secA, "files[%q].mixed_eol=%v 但 base 内容是 %v", p, fst.MixedEOL, mixed)
		}
		if fst.Exec != f.exec {
			has := "无"
			if f.exec {
				has = "有"
			}
			r.fail(secA, "files[%q].exec=%v 但 base 文件%s可执行位", p, fst.Exec, has)
		}
	}

	// A10
	if k := st.Template.Kind; k != template.KindGit.String() && k != template.KindDir.String() {
		r.fail(secA, "template.kind %q 非法（§4.2）", k)
	}
	if st.Template.Source == "" {
		r.fail(secA, "template.source 为空（§4.2）")
	}
	if st.Template.Commit == "" {
		r.fail(secA, "template.commit 为空（§4.2）")
	}
	return nil
}
