package render

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/xbcio/xinit/internal/manifest"
)

// Rule 是一条待应用的替换规则（已编译）。
//
// PrecededBy / FollowedBy 必须是**已锚定**的形式：前者表达「窗口以该模式
// 结尾」，后者表达「窗口以该模式开头」。锚定与「先单独编译校验」是一对
// 的，由 manifest.Compile 负责（见 compilePrecededBy 的说明）。
type Rule struct {
	From       string
	To         string
	PrecededBy *regexp.Regexp // nil = 无约束；已锚定为「窗口以 pat 结尾」
	FollowedBy *regexp.Regexp // nil = 无约束；已锚定为「窗口以 pat 开头」
	Case       manifest.RenderCase
}

// interval 是 [start, end) 的字节区间。
type interval struct{ start, end int }

// span 是一个已接受的替换。
type span struct {
	interval
	repl string
}

// Apply 执行两遍 span 替换。
//
// 语义（规范 §7.3）：
//   - 候选位置用**重叠枚举** —— 每个字节起点都试。不重叠会静默漏改：
//     实测 "q-x-x-x" 在单条规则下得 "Q-x-x"，漏掉一处。
//   - 禁区（protected_literals）同样重叠枚举。否则 "-1-1-" 的第二个 1
//     会漏出禁区被改写。
//   - 规则内部从左到右贪心；跨规则「首个命中的规则在该位置获胜」，
//     由已接受 span 的重叠检查实现。注意这不等于最长匹配优先：实测
//     "a-b-c" 配 [b-c→R1, a-b-c→R2] 得 "a-R1"。
//   - 谓词窗口限制在当前行且有字节上限，超限即判不命中并给警告。
//
// 返回输出、每条规则的命中数（零命中要能被调用方察觉）、以及警告。
func Apply(text []byte, rules []Rule, protected []string) ([]byte, []int, []string) {
	hits := make([]int, len(rules))
	var warnings []string

	forbidden := collectIntervals(text, protected)

	var spans []span
	for i, r := range rules {
		if r.From == "" {
			continue
		}
		fromLen := len(r.From)
		for pos := 0; pos+fromLen <= len(text); pos++ {
			if string(text[pos:pos+fromLen]) != r.From {
				continue
			}
			end := pos + fromLen

			if !letterBoundaryOK(text, pos, end) {
				continue
			}
			if overlapsAny(interval{pos, end}, forbidden) {
				continue
			}
			if overlapsSpans(interval{pos, end}, spans) {
				continue
			}

			ok, truncated := predicateOK(text, pos, end, r)
			if truncated {
				warnings = append(warnings, fmt.Sprintf(
					"规则 %d (%q): 谓词窗口超过 %d 字节上限，该处判为不命中",
					i, r.From, predicateWindowCap))
			}
			if !ok {
				continue
			}

			spans = append(spans, span{interval{pos, end}, applyCase(r.To, r.Case)})
			hits[i]++
		}
	}

	sort.SliceStable(spans, func(a, b int) bool { return spans[a].start < spans[b].start })

	return splice(text, spans), hits, warnings
}

// splice 一次性构建输出：未覆盖区间取原文，覆盖区间取替换值。
func splice(text []byte, spans []span) []byte {
	if len(spans) == 0 {
		return text
	}
	var out []byte
	prev := 0
	for _, s := range spans {
		out = append(out, text[prev:s.start]...)
		out = append(out, s.repl...)
		prev = s.end
	}
	out = append(out, text[prev:]...)
	return out
}

// collectIntervals 用重叠枚举收集全部出现区间。
//
// 必须重叠：不重叠会让部分重叠的禁区漏掉一部分，实测 "-1-1-" 且禁区含
// "1-1" 时，第二个 1 会漏出禁区被改写。
func collectIntervals(text []byte, lits []string) []interval {
	var out []interval
	for _, lit := range lits {
		if lit == "" {
			continue
		}
		n := len(lit)
		for pos := 0; pos+n <= len(text); pos++ {
			if string(text[pos:pos+n]) == lit {
				out = append(out, interval{pos, pos + n})
			}
		}
	}
	return out
}

func overlapsAny(iv interval, others []interval) bool {
	for _, o := range others {
		if iv.start < o.end && o.start < iv.end {
			return true
		}
	}
	return false
}

// overlapsSpans 是已接受 span 的重叠检查。单独一个函数而不是复用
// overlapsAny：span 内嵌了 interval，但 Go 不会把 []span 自动转成
// []interval，直接传会编译不过。
func overlapsSpans(iv interval, spans []span) bool {
	for _, s := range spans {
		if iv.start < s.end && s.start < iv.end {
			return true
		}
	}
	return false
}

// letterBoundaryOK 要求匹配的两侧不是 ASCII 字母。
//
// 已知边界：只认 ASCII。'é' 这类非 ASCII 字母会被当作合法邻居 —— 与旧
// 实现一致，属已声明的取舍（规范 §7.3）。
func letterBoundaryOK(text []byte, start, end int) bool {
	if start > 0 && isASCIILetter(text[start-1]) {
		return false
	}
	if end < len(text) && isASCIILetter(text[end]) {
		return false
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// predicateOK 判定前后置谓词。返回 (是否命中, 窗口是否被截断)。
//
// 窗口是当前行内 [行首, pos) 与 [end, 行尾)，各有 predicateWindowCap 上限。
// 超限时返回不命中 —— 见 predicateWindowCap 的说明。
//
// 两处都只调 Match：锚定已经烘进正则里（见 Rule 的说明）。这里**不要**
// 自己再判断匹配位置 —— 在切片上 Find 出来的偏移是相对切片的，拿它跟
// 绝对位置比是错的。
func predicateOK(text []byte, pos, end int, r Rule) (ok bool, truncated bool) {
	if r.PrecededBy != nil {
		ls := lineStart(text, pos)
		if pos-ls > predicateWindowCap {
			return false, true
		}
		if !r.PrecededBy.Match(text[ls:pos]) {
			return false, false
		}
	}
	if r.FollowedBy != nil {
		le := lineEnd(text, end)
		if le-end > predicateWindowCap {
			return false, true
		}
		if !r.FollowedBy.Match(text[end:le]) {
			return false, false
		}
	}
	return true, false
}

// lineStart 返回 pos 所在行的起始偏移。'\n' 是唯一的分行符 ——
// 单独的 '\r' 不是（整文件按字节处理，不按行切分）。
func lineStart(text []byte, pos int) int {
	for i := pos - 1; i >= 0; i-- {
		if text[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// lineEnd 返回 pos 所在行的结束偏移（不含 '\n'）。
func lineEnd(text []byte, pos int) int {
	for i := pos; i < len(text); i++ {
		if text[i] == '\n' {
			return i
		}
	}
	return len(text)
}

func applyCase(s string, c manifest.RenderCase) string {
	switch c {
	case manifest.CaseLower:
		return strings.ToLower(s)
	case manifest.CaseUpper:
		return strings.ToUpper(s)
	default:
		return s
	}
}
