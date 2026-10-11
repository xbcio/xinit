package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/xbcio/xinit/internal/backflow"
)

// 各出口的文案（§20.10 文案基准表的散文句；全角标点）。报告层在行首加稳定
// 前缀（backflow-ok / backflow-excluded / backflow-warn / backflow-noop）。
const (
	noteRender     = "该路径在模板中是 render 模式（Go template），模板求值非单射、翻译对它是猜测（§20.7）；出口：人工判断（M8 不回送 render 模式文件）"
	noteBinary     = "二进制或混合 EOL 文件（T/B/O 任一方 Binary ∨ mixed-EOL，§20.7）：内容级合并的前提是文本；出口：人工判断"
	noteSymlink    = "符号链接：git 里 symlink 是 blob 内容=链接文本，文本合并会「翻译」出语义错误的链接（§20.7）；出口：人工判断"
	noteTypechange = "类型变更（typechange）：与 binary 同列警告不导出（§20.7）；出口：人工判断"
	noteDeleted    = "删除不导出（M8 非目标）：模板侧删除 = prune/拿回语义的对称问题（§20.7）；出口：人工判断"
	noteRenamed    = "重命名不导出（M8 非目标；含 A+D 同 hash 兜底判出的改名，§20.7）；出口：人工判断"
	noteCopied     = "拷贝不导出（M8 非目标，§20.7）；出口：人工判断"
	noteGenerated  = "generated 位文件（钩子产物）：回送钩子产物无意义（§20.7）；出口：人工判断"
	noteConflict   = "翻译冲突（fail-closed，§20.3）：冲突路径不产补丁；把冲突块内的行改回模板措辞后重导出"
	// noteConflictHold 是「本轮有冲突、其余路径随整单不导出」的文案（§20.3
	// fail-closed 是全链整体拒绝，不是逐路径跳过）。
	noteConflictHold = "本轮存在翻译冲突：整体 fail-closed 不产补丁（§20.3）——该路径本身翻译通过，但随整单未导出"
	// noteSourceGone 与 internal/backflow 的 noteSourceGone（domain.go）逐字同形
	// （T2 评审 Minor-2 方案①：双源同形由 backflow_warns.txtar 钉住两面）。
	noteSourceGone = "该路径的模板来源已不存在（模板已移除或改名）；如需回送请用 `--map` 显式给出模板路径，或人工判断"
)

// noteResolveAmbiguous 是新增文件路径解析零/多命中的出口文案（§20.10 行 10：
// 「打印候选与失败原因」+ `--map Dst=Src`）。
func noteResolveAmbiguous(dst string, cands []string) string {
	if len(cands) == 0 {
		return fmt.Sprintf(
			"新增路径的模板态路径解析零命中（候选集里没有能重放回该路径的模板路径）；请用 `--map %s=<Src>` 显式给出模板路径（§20.7 规则 3）", dst)
	}
	return fmt.Sprintf(
		"新增路径的模板态路径解析多命中（候选：%s）——无法确定唯一模板路径；请用 `--map %s=<Src>` 显式给出模板路径（§20.7 规则 3）",
		strings.Join(cands, "、"), dst)
}

// ConflictBlock 是一个翻译冲突块（§20.10 行 11 的报告载体）。
type ConflictBlock struct {
	Path  string   // 模板坐标系路径（Src）
	Line  int      // 冲突块起始行（合并输出、1-based）
	Lines []string // 冲突块原文（含 <<<<<<< / ||||||| / ======= / >>>>>>> 标记）
}

// BackflowEntry 是一条路径的报告行（ok 或 excluded）。
type BackflowEntry struct {
	Excluded bool
	Dst      string
	Src      string // 仅 ok 行：模板坐标系路径
	Note     string
}

// BackflowReport 是 backflow 的完整结果；Print 的输出就是用户看到的报告（§20.8）。
type BackflowReport struct {
	Header     string // "xinit backflow"
	Source     string // 模板来源
	Revision   string // 记录的 revision
	TmplCommit string // 记录的模板 commit（产物文件名与接收指引的锚）
	DerivedSha string // 派生提交（解析后的 40 位 sha）
	Subject    string // 派生提交主题（逐字；slug 与 mailbox 同源）
	DryRun     bool

	Entries  []BackflowEntry // 按 Dst 排序
	Noops    []string        // noop 剔除的 Src（排序）
	Kept     []string        // 保留进补丁的 Src（排序）
	Artifact string          // 产物展示路径（项目内相对；--dry-run 时为将写入的位置）

	Conflicts []ConflictBlock

	Untracked []string // 未跟踪文件（守卫 3 警告面）

	Scans         []backflow.ScanHit // 夹带扫描命中（≤MaxScanHits）
	ScanTruncated bool               // 命中达上限（打「≥40」）
	Dropped       []string           // 未产出扫描 token 的规则
	Vacuous       bool               // token 集为空

	Failure  []string // 失败段（逐行；非空 ⇒ rc=1）
	ExitCode int
}

// Print 输出报告。行前缀（backflow-ok / backflow-excluded / backflow-warn /
// backflow-noop）是稳定标识，供脚本 grep（§20.8）。
func (r *BackflowReport) Print(w io.Writer) {
	fmt.Fprintf(w, "%s: %s@%s\n", r.Header, r.Source, orNone(r.Revision))
	fmt.Fprintf(w, "提交: %s %s\n", short7(r.DerivedSha), r.Subject)
	fmt.Fprintf(w, "模板: %s\n", short7(r.TmplCommit))

	for _, e := range r.Entries {
		if e.Excluded {
			fmt.Fprintf(w, "backflow-excluded %s: %s\n", e.Dst, e.Note)
			continue
		}
		if e.Src != "" && e.Src != e.Dst {
			fmt.Fprintf(w, "backflow-ok %s -> %s（%s）\n", e.Dst, e.Src, e.Note)
			continue
		}
		fmt.Fprintf(w, "backflow-ok %s（%s）\n", e.Dst, e.Note)
	}
	for _, s := range r.Noops {
		fmt.Fprintf(w, "backflow-noop %s\n", s)
	}

	if len(r.Untracked) > 0 {
		fmt.Fprintf(w, "backflow-warn 未跟踪文件（只警告、不挡导出、不进补丁）：%s（§20.4 守卫 3）\n",
			strings.Join(r.Untracked, "、"))
	}

	r.printConflicts(w)
	r.printScan(w)

	if r.Artifact != "" {
		label := "产物"
		if r.DryRun {
			label = "产物（--dry-run 未写盘）"
		}
		fmt.Fprintf(w, "%s: %s\n", label, r.Artifact)
		if len(r.Kept) > 0 {
			fmt.Fprintf(w, "保留 %d 条：%s\n", len(r.Kept), strings.Join(r.Kept, "、"))
		}
	}

	for _, ln := range r.Failure {
		fmt.Fprintln(w, ln)
	}

	if r.ExitCode == CodeOK && r.Artifact != "" {
		r.printReceiveGuide(w)
	}
	if r.DryRun {
		fmt.Fprintln(w, "（--dry-run：全部分类/翻译/扫描已照跑，未写产物、不加锁，§20.8）")
	}
}

// printConflicts 打印冲突段：逐条 `文件:行` + 冲突块三侧 + 出口（§20.10 行 11）。
func (r *BackflowReport) printConflicts(w io.Writer) {
	if len(r.Conflicts) == 0 {
		return
	}
	fmt.Fprintf(w, "翻译冲突（%d 条）——冲突路径不产补丁（fail-closed，§20.3）：\n", len(r.Conflicts))
	for _, c := range r.Conflicts {
		if c.Line > 0 {
			fmt.Fprintf(w, "  conflict %s:%d\n", c.Path, c.Line)
		} else {
			// 块解析缺位时的兜底行（判据仍 fail-closed；见 runBackflow 注释）。
			fmt.Fprintf(w, "  conflict %s\n", c.Path)
		}
		for _, ln := range c.Lines {
			fmt.Fprintln(w, ln)
		}
	}
	fmt.Fprintln(w, "冲突块三侧：`ours` 段 = 模板措辞（基准侧）、`base` 段 = 渲染基线（.xinit/base 的重建）、`theirs` 段 = 你的提交内容。")
	fmt.Fprintln(w, "出口：把冲突块内的行改回模板措辞后重导出（§20.3 冲突半径：替换点行 ±1 与紧邻插入都会落进同一 hunk）。")
}

// printScan 打印夹带扫描段（§20.5）：命中行 / vacuous / dropped + 「未经硬闸门」自陈。
func (r *BackflowReport) printScan(w io.Writer) {
	if r.Vacuous {
		fmt.Fprintln(w, "夹带扫描：替换/改名规则的 `to` 渲染后为空集（vacuous），无 token 可查（§20.5）")
		fmt.Fprintln(w, "未经硬闸门：模板作者需人工审查新增行与新增文件路径（§20.5）。")
		return
	}
	r.printDropped(w)
	if len(r.Scans) == 0 {
		fmt.Fprintln(w, "夹带扫描：新增行与新增文件路径未命中派生词 token")
		fmt.Fprintln(w, "未经硬闸门：模板作者仍需人工审查新增行（§20.5）。")
		return
	}
	fmt.Fprintln(w, "夹带扫描命中（新增行与新增文件路径的派生词，§20.5）：")
	for _, h := range r.Scans {
		if h.IsPath {
			fmt.Fprintf(w, "backflow-warn %s: %s\n", h.Where, h.Token)
			continue
		}
		fmt.Fprintf(w, "backflow-warn %s:%d: %s — %s\n", h.Where, h.Line, h.Token, h.Context)
	}
	if r.ScanTruncated {
		fmt.Fprintf(w, "backflow-warn 命中已达上限（≥%d 条，仅列前 %d 条）\n", backflow.MaxScanHits, backflow.MaxScanHits)
	}
	fmt.Fprintln(w, "未经硬闸门：模板作者需人工改写命中处（§20.5）。新增行/新增文件不做翻译（无预像即无翻译）——命中若为派生词，请在模板侧改成模板措辞（§20.3 翻译边界）。")
}

// printDropped 打印「未产出扫描 token」的规则（T4 移交②：渲染为空串 / 渲染失败两类同措辞）。
func (r *BackflowReport) printDropped(w io.Writer) {
	if len(r.Dropped) == 0 {
		return
	}
	fmt.Fprintf(w, "backflow-warn 以下规则未产出扫描 token（渲染为空串或渲染失败），未参与夹带扫描：%s\n",
		strings.Join(r.Dropped, "、"))
}

// printReceiveGuide 打印接收指引（§20.6 报告尾）：四条前置（含 CRLF 模板
// `--keep-cr`）+ `am -3` + abort/continue + 模板 HEAD 前进口径 + 换史警告。
// xinit 在接收侧不提供命令，产物自足（模板作者对 xinit 零依赖）。
func (r *BackflowReport) printReceiveGuide(w io.Writer) {
	fmt.Fprintln(w, "接收指引（模板作者，在模板仓内；xinit 不参与接收侧）：")
	fmt.Fprintf(w, "  前置一：接收仓必须含模板 commit %s 的历史——补丁的 pre-image 就是它；强推/换史会让 `git am -3` 的索引回退失效（§20.6 可接收性①）。建议先 `git checkout -b backflow/%s`。\n",
		r.TmplCommit, short7(r.DerivedSha))
	fmt.Fprintln(w, "  前置二：CRLF 模板必须 `git am -3 --keep-cr`（或接收仓 `git config am.keepcr true`）——默认 am 会剥掉补丁内容行的 CR，rc=128 硬失败并留中断态（§20.6 规则⑥）。")
	fmt.Fprintln(w, "  前置三：接收仓工作树干净（`am` 直接改写工作树）。")
	fmt.Fprintln(w, "  前置四：补丁由 `--full-index` 生成（40 位 blob），接收仓 git 需 ≥ 2.38（同 xinit 依赖）。")
	fmt.Fprintln(w, "  应用：git am -3 <patch>（CRLF 模板加 --keep-cr）")
	fmt.Fprintln(w, "  中断态：git am --abort 放弃；解决冲突后 git am --continue")
	fmt.Fprintln(w, "  模板 HEAD 已前进：`-3` 用 blob 索引重建基线、提交落在当前 HEAD 之上；目标 commit 已不在历史里时 am 会失败并留中断态，`--abort` 后用 `git am -3` 重试或人工移植。")
	fmt.Fprintln(w, "  注意：模板仓换史（强推）会破坏 `-3` 回退（pre-image 不可解析）；补丁正文里的独立 `---` 行会被 mailinfo 静默截断（同 git format-patch→am 行为）。")
}

// fill 由 items 生成条目行：已定出口的进 excluded，成功导出的进 ok
// （kept 之外仍处 ExitExport 的条目必是 noop，由 Noops 行单独承载）。
func (r *BackflowReport) fill(items []backflow.Item, kept map[string]bool) {
	r.Entries = make([]BackflowEntry, 0, len(items))
	for _, it := range items {
		if it.Exit != backflow.ExitExport {
			r.Entries = append(r.Entries, BackflowEntry{Excluded: true, Dst: it.Dst, Note: it.ExitNote})
			continue
		}
		if kept[it.Src] {
			r.Entries = append(r.Entries, BackflowEntry{Dst: it.Dst, Src: it.Src, Note: it.ExitNote})
		}
	}
}
