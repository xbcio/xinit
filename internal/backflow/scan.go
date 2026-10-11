package backflow

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
)

// MaxScanHits 是逐条打印的命中上限（§20.5：命中 ≤40 条逐条打印，>40 截断）。
const MaxScanHits = 40

// AddedLine 是配方 diff 里的一条 `+` 行，坐标为**新文件侧**（模板坐标系）。
//
// Text 不含行尾：前导 '+' 剥掉，行尾 CR（CRLF 模板的行尾）也剥掉 —— CRLF 的
// 「\r\n」在本仓一律按一个行终止符看待（同 fsutil 的 EOL 口径），留下的 CR
// 会让报告行被回车打断。
type AddedLine struct {
	File string
	Line int
	Text string
}

// ScanHit 是一条夹带扫描命中。
//
//   - 内容面：Where = 文件、Line = 行号、Context = 该行文本、IsPath = false，
//     报告层打 `file:line: token — 内容`；
//   - 路径面：Where = 路径（模板坐标系，与 AddedLine.File 同口径）、Line = 0、
//     Context = 命中的那一段、IsPath = true，报告层打 `path: token`。
type ScanHit struct {
	Where   string
	Line    int
	Token   string
	Context string
	IsPath  bool
}

// BuildTokens 构造 §20.5 的夹带扫描 token 集。
//
// 三条判据：
//
//   - ① = subst 规则 `to` 渲染值 ∪ rename 规则 `to` 渲染值（在 vars 下重放，
//     即 `state.Vars` 原样）。**不枚举 vars 的值本身**：路径变量的值（`src`
//     这类通用词）进内容扫描等于让全仓命中 —— §20.5「token 集」① 明写
//     「路径变量的值不进内容扫描」。
//   - ② 渲染后为空串的 token 剔除并逐条记进 dropped —— 空 token 进 `|` 连接
//     等价「匹配一切」，报告层据此打印一行提示。
//   - ③ 空集 ⇒ vacuous（报告层打印显式 vacuous 行，对齐 xkitora「table empty」）。
//
// 大小写不敏感去重（ASCII）、保留首个拼写：判据本身大小写不敏感，同一条
// 渲染值的不同拼写重复进集只会把同一条命中报两遍。
//
// 本函数不返回 error：扫描是警告级面（rc 恒不影响导出完成）。`to` 渲染失败
// （引用了不存在的变量）与渲染为空串同列记进 dropped，只是不产 token。
func BuildTokens(c *manifest.Compiled, vars render.Vars) (tokens []string, dropped []string, vacuous bool) {
	labels := make([]string, 0, len(c.Subst)+len(c.Rename))
	values := make([]string, 0, len(c.Subst)+len(c.Rename))
	for i, cs := range c.Subst {
		labels = append(labels, fmt.Sprintf("substitute[%d].to", i))
		values = append(values, cs.Spec.To)
	}
	for i, cr := range c.Rename {
		labels = append(labels, fmt.Sprintf("rename[%d].to", i))
		values = append(values, cr.Spec.To)
	}

	seen := make(map[string]bool, len(values))
	for i, to := range values {
		if strings.Contains(to, "{{") {
			// 与 render 的正向管线同判据：含 {{ 才走模板渲染（那边是
			// containsTemplateAction，未导出，这里按同一行的谓词重写）。
			rendered, err := render.RenderTemplate(labels[i], []byte(to), vars)
			if err != nil {
				dropped = append(dropped, fmt.Sprintf("%s（渲染失败：%v）", labels[i], err))
				continue
			}
			to = string(rendered)
		}
		if to == "" {
			dropped = append(dropped, labels[i]+"（渲染为空串）")
			continue
		}
		key := foldASCII(to)
		if seen[key] {
			continue
		}
		seen[key] = true
		tokens = append(tokens, to)
	}
	return tokens, dropped, len(tokens) == 0
}

// Scan 做 §20.5 的夹带扫描：扫描面 = 待导出补丁的**新增行**（含新增文件的
// 全部行）＋ **新增行/新增文件的路径段**（派生词泄漏可走路径而非内容）。
//
// added 由 AddedLinesFromPatch 产出；newFilePaths 是新增文件的路径（各自按
// "/" 切好的段，模板坐标系；空的新增文件没有新增行，其路径只能从这里来，
// 否则「纯路径名夹带」在空文件上无人认领）。路径按首次出现去重。
//
// 判据 = 边界正则 `(?<![A-Za-z])(?:tok)(?![A-Za-z])` 的字节级等价：匹配处
// 前后字节都不是 ASCII 字母，匹配本身按 ASCII 大小写不敏感比较。**不用 `\b`
// 近似** —— 单词边界含数字与下划线，`foo_bar`/`foo1` 会被误判成不命中。
//
// 返回顺序确定：先内容面（新增行序 × token 集序），后路径面（路径首见序 ×
// token 集序），最后按 MaxScanHits 截断。空 token 集 = vacuous，返回 nil。
//
// 不返回 error：命中不影响导出完成（rc 恒不变），报告层打警告 + 自陈
// 「未经硬闸门」。
func Scan(added []AddedLine, newFilePaths [][]string, tokens []string) []ScanHit {
	if len(tokens) == 0 {
		return nil
	}

	var hits []ScanHit

	// 内容面。
	for _, ln := range added {
		for _, tok := range tokens {
			if tok == "" || indexTokenFold(ln.Text, tok) < 0 {
				continue
			}
			hits = append(hits, ScanHit{
				Where:   ln.File,
				Line:    ln.Line,
				Token:   tok,
				Context: ln.Text,
			})
		}
	}

	// 路径面。
	var paths []string
	seenPath := make(map[string]bool)
	addPath := func(p string) {
		if p == "" || seenPath[p] {
			return
		}
		seenPath[p] = true
		paths = append(paths, p)
	}
	for _, ln := range added {
		addPath(ln.File)
	}
	for _, segs := range newFilePaths {
		addPath(strings.Join(segs, "/"))
	}
	for _, p := range paths {
		segs := strings.Split(p, "/")
		for _, tok := range tokens {
			if tok == "" {
				continue
			}
			for _, seg := range segs {
				if indexTokenFold(seg, tok) < 0 {
					continue
				}
				hits = append(hits, ScanHit{Where: p, Token: tok, Context: seg, IsPath: true})
				break // 一条路径对一个 token 只报一次（首个命中的段）
			}
		}
	}

	if len(hits) > MaxScanHits {
		hits = hits[:MaxScanHits]
	}
	return hits
}

// indexTokenFold 返回 tok 在 text 中首个满足边界判据的匹配位置，无匹配返回 -1。
//
// Go 的 RE2 没有 lookbehind，`\b` 又是含数字/下划线的单词边界（语义偏差），
// 故逐字节手工扫描。与 render §7.3 的正向判据同口径：边界只看 ASCII 字母。
func indexTokenFold(text, tok string) int {
	if tok == "" || len(tok) > len(text) {
		return -1
	}
	for i := 0; i+len(tok) <= len(text); i++ {
		if !foldEqualAt(text, tok, i) {
			continue
		}
		if i > 0 && isASCIILetter(text[i-1]) {
			continue
		}
		if i+len(tok) < len(text) && isASCIILetter(text[i+len(tok)]) {
			continue
		}
		return i
	}
	return -1
}

// foldEqualAt 报告 text[i:i+len(tok)] 与 tok 在 ASCII 大小写不敏感意义下相等。
func foldEqualAt(text, tok string, i int) bool {
	for j := 0; j < len(tok); j++ {
		if foldASCIIByte(text[i+j]) != foldASCIIByte(tok[j]) {
			return false
		}
	}
	return true
}

// foldASCII 折叠 ASCII 大写字母（非 ASCII 字节原样保留）。判据是「ASCII 大小写
// 不敏感」（对齐 xkitora），故不走 fsutil.Fold —— 那是路径名的 Unicode 折叠。
func foldASCII(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = foldASCIIByte(b[i])
	}
	return string(b)
}

func foldASCIIByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// AddedLinesFromPatch 解析配方 diff（§20.6 配方第 2 步的
// `git diff --no-index --full-index --no-prefix` 输出）里的 `+` 行。
//
// 可以直接喂整份 mailbox 补丁：**只有见过 `+++ ` 段头之后才按 `@@` 进入 hunk**，
// 所以提交消息正文与 diffstat 段里的 `@@`/`+`/`---` 形状不会被误读。
//
// 其余判据：
//   - 新文件侧路径取 `+++ ` 行（配方把 T 与 F_new 分别写在 `<tmp>/a/<Src>` 与
//     `<tmp>/b/<Src>`，`--no-prefix` 下路径带的就是这一层 `b/` 前缀，剥掉即
//     Src）；git 对含空格/非 ASCII 的路径加引号并做 C 风格转义，去引号后再剥。
//     `+++ /dev/null`（删除侧）不产 + 行，路径记空。
//   - 行号自 hunk 头的 `+<start>` 起按新文件侧推进：上下文行与 `+` 行各进 1，
//     `-` 行不进；`\ No newline at end of file` 不进。
//   - hunk 内一切以行首字符定性，故新增行内容形如 `++ x`（落到字节 `+++ x`）
//     不会被误当段头 —— 段头只在非 hunk 态才解析。
func AddedLinesFromPatch(patch []byte) []AddedLine {
	var out []AddedLine
	file := ""
	inHunk := false
	line := 0

	for _, raw := range strings.Split(string(patch), "\n") {
		l := strings.TrimSuffix(raw, "\r") // 行终止符 CRLF 的 CR 不属于任何一方的文本

		if inHunk {
			if l == "" {
				inHunk = false // 真 diff 不会在 hunk 内出现空行（上下文行必带前导空格）
				continue
			}
			switch l[0] {
			case ' ':
				line++
				continue
			case '+':
				out = append(out, AddedLine{File: file, Line: line, Text: l[1:]})
				line++
				continue
			case '-', '\\':
				continue // 删除行不动新文件侧行号；`\ No newline...` 不是内容
			default:
				inHunk = false // 不是 hunk 行：按段头再看一眼（如紧跟的 diff --git）
			}
		}

		switch {
		case strings.HasPrefix(l, "diff --git "):
			file = ""
		case strings.HasPrefix(l, "+++ "):
			file = diffNewPath(l[len("+++ "):])
		case strings.HasPrefix(l, "@@"):
			if file == "" {
				break // 没有 +++ 段头：不是真 hunk
			}
			if start, ok := hunkNewStart(l); ok {
				line, inHunk = start, true
			}
		}
	}
	return out
}

// diffNewPath 从 `+++ ` 行取新文件侧路径：去引号（git 的 C 风格转义）后剥掉
// 配方临时目录的那一层前缀（`b/`）。/dev/null（删除侧）返回空串。
func diffNewPath(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i] // GNU-diff 风格的制表符 + 时间戳尾巴（git 配方不产，防御）
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if unq, err := strconv.Unquote(s); err == nil {
			s = unq
		}
	}
	if rest, ok := strings.CutPrefix(s, "b/"); ok {
		return rest
	}
	return s
}

// hunkNewStart 解析 `@@ -a,b +c,d @@ ...` 的新文件侧起始行号。
func hunkNewStart(l string) (int, bool) {
	rest, ok := strings.CutPrefix(l, "@@ ")
	if !ok {
		return 0, false
	}
	i := strings.Index(rest, " +")
	if i < 0 {
		return 0, false
	}
	rest = rest[i+2:]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0, false
	}
	return n, true
}
