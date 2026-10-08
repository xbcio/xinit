package render

import (
	"regexp"
	"testing"

	"github.com/xbcio/xinit/internal/manifest"
)

func mustRe(t *testing.T, s string) *regexp.Regexp {
	t.Helper()
	r, err := regexp.Compile(s)
	if err != nil {
		t.Fatalf("regexp.Compile(%q): %v", s, err)
	}
	return r
}

// preceded / followed 复刻 manifest.Compile 使用的锚定约定。
//
// 直接写裸模式会得到错误的期望值 —— Rule 里的谓词必须是已锚定的形式。
// 与 manifest 侧的一致性由 TestApplyPredicatesThroughManifest 端到端保证。
func preceded(t *testing.T, pat string) *regexp.Regexp {
	t.Helper()
	return mustRe(t, "^(?:.*)(?:"+pat+")$")
}

func followed(t *testing.T, pat string) *regexp.Regexp {
	t.Helper()
	return mustRe(t, "^(?:"+pat+")")
}

func TestApplyBasicRuleOrder(t *testing.T) {
	// Test H：规则顺序压过位置与长度。实测 "a-b-c" 配 [b-c→R1, a-b-c→R2]
	// 得 "a-R1" 而非 "a-R2"。
	rules := []Rule{
		{From: "b-c", To: "R1"},
		{From: "a-b-c", To: "R2"},
	}
	out, _, _ := Apply([]byte("a-b-c"), rules, nil)
	if string(out) != "a-R1" {
		t.Errorf("Apply = %q, want %q（规则顺序优先，不是最长匹配优先）", out, "a-R1")
	}
}

func TestApplyOverlappingOccurrences(t *testing.T) {
	// Test A：第 2 条规则的扫描指针不能落在已被第 1 条占用的位置上。
	// 不重叠枚举实测得 "Q-x-x"，漏改了第二处。
	rules := []Rule{
		{From: "x", To: "X", PrecededBy: nil},
	}
	out, _, _ := Apply([]byte("q-x-x-x"), rules, nil)
	if string(out) != "q-X-X-X" {
		t.Errorf("Apply = %q, want %q", out, "q-X-X-X")
	}
}

func TestApplyProtectedUsesOverlappingEnumeration(t *testing.T) {
	// Test C：禁区也必须重叠枚举，否则第二个 1 会漏出禁区被改写
	out, _, _ := Apply([]byte("-1-1-"), []Rule{{From: "1", To: "9"}}, []string{"1-1"})
	if string(out) != "-1-1-" {
		t.Errorf("Apply = %q, want %q（禁区内的内容不得被改写）", out, "-1-1-")
	}
}

func TestApplyProtectedPartialOverlapRejects(t *testing.T) {
	// Test I：保护区只覆盖匹配的一半时，整个匹配都必须放弃
	out, _, _ := Apply([]byte("xkitora/ui"), []Rule{{From: "xkitora", To: "xdesk"}}, []string{"kitora/ui"})
	if string(out) != "xkitora/ui" {
		t.Errorf("Apply = %q, want 原样（部分重叠也不许改写）", out)
	}
}

func TestApplyLetterBoundary(t *testing.T) {
	rules := []Rule{{From: "xkitora", To: "xdesk"}}
	tests := []struct {
		in, want string
	}{
		{"xkitora", "xdesk"},
		{"xkitora/x", "xdesk/x"},
		{"libxkitora", "libxkitora"}, // 前邻是字母
		{"xkitorax", "xkitorax"},     // 后邻是字母
		{"github.com/xbcio/xkitora", "github.com/xbcio/xdesk"},
		{"_xkitora_", "_xdesk_"}, // 下划线不是字母
	}
	for _, tt := range tests {
		out, _, _ := Apply([]byte(tt.in), rules, nil)
		if string(out) != tt.want {
			t.Errorf("Apply(%q) = %q, want %q", tt.in, out, tt.want)
		}
	}
}

func TestApplyMultiRuleNoPartialRewrite(t *testing.T) {
	// Test G：github.com/xbcio/xkitora 在多规则下必须整体被正确改写
	rules := []Rule{
		{From: "Xkitora", To: "Xdesk"},
		{From: "xkitora", To: "xdesk"},
	}
	out, _, _ := Apply([]byte("github.com/xbcio/xkitora"), rules, nil)
	if string(out) != "github.com/xbcio/xdesk" {
		t.Errorf("Apply = %q, want %q", out, "github.com/xbcio/xdesk")
	}
}

func TestApplyPrecededBy(t *testing.T) {
	rules := []Rule{{
		From:       "xkitora",
		To:         "xdesk",
		PrecededBy: preceded(t, `module[ \t]+`),
	}}
	out, _, _ := Apply([]byte("module xkitora\n"), rules, nil)
	if string(out) != "module xdesk\n" {
		t.Errorf("谓词命中时应替换: %q", out)
	}
	out, _, _ = Apply([]byte("// xkitora\n"), rules, nil)
	if string(out) != "// xkitora\n" {
		t.Errorf("谓词不命中时不应替换: %q", out)
	}
}

func TestApplyPrecededByMustAbandonAfterMatch(t *testing.T) {
	// 窗口末尾必须紧贴谓词 —— 窗口中间出现过不算
	rules := []Rule{{
		From:       "xkitora",
		To:         "xdesk",
		PrecededBy: preceded(t, `module[ \t]+`),
	}}
	out, _, _ := Apply([]byte("module foo bar xkitora\n"), rules, nil)
	if string(out) != "module foo bar xkitora\n" {
		t.Errorf("module 与 xkitora 之间还有内容，不应替换: %q", out)
	}
}

func TestApplyFollowedBy(t *testing.T) {
	rules := []Rule{{
		From:       "xkitora",
		To:         "xdesk",
		FollowedBy: followed(t, `[ \t]*#`),
	}}
	out, _, _ := Apply([]byte("import xkitora # note\n"), rules, nil)
	if string(out) != "import xdesk # note\n" {
		t.Errorf("应替换: %q", out)
	}
	out, _, _ = Apply([]byte("import xkitora\n"), rules, nil)
	if string(out) != "import xkitora\n" {
		t.Errorf("不命中时不应替换: %q", out)
	}
}

func TestApplyAdjacentBoundaryJudgedOnOriginalText(t *testing.T) {
	// 与 rebrand.sh 的已声明偏离，见 Task 9 的「必须写死的语义」第 4 条。
	//
	// 规则 A 的字面量以字母结尾，规则 B 的字面量以非字母开头且紧贴 A 的
	// 右边界。rebrand.sh 先把 A 换成哨兵再跑 B，B 的左侧邻字符是 \x00
	// 因而通过；本实现判原文，B 的左侧邻字符是 X（字母）因而拒绝。
	//
	// 这个测试的作用是**钉住**选择，不是为了通过才写的 —— 断言的就是
	// 偏离那一侧的结果。
	rules := []Rule{
		{From: "xX", To: "Zzz"}, // 以字母结尾
		{From: "-a", To: "zzz"}, // 以非字母开头
	}
	in := " xX-a\n"

	// 先确认两条规则单独看都会命中，否则下面的「只命中一条」什么也证明不了
	onlyA, _, _ := Apply([]byte(in), rules[:1], nil)
	if string(onlyA) != " Zzz-a\n" {
		t.Fatalf("规则 A 应单独命中: %q", onlyA)
	}
	onlyB, _, _ := Apply([]byte(" -a xX\n"), rules[1:], nil)
	if string(onlyB) != " zzz xX\n" {
		t.Fatalf("规则 B 应单独命中: %q", onlyB)
	}

	out, hits, _ := Apply([]byte(in), rules, nil)
	if string(out) != " Zzz-a\n" {
		t.Errorf("边界必须判在原文上: %q（rebrand.sh 会给出 %q）", out, " Zzzzzz\n")
	}
	if hits[0] != 1 || hits[1] != 0 {
		t.Errorf("命中数 = %v, want [1 0] —— 规则 B 必须被边界拒绝", hits)
	}
}

func TestApplyTruncatedWindowNeverMatches(t *testing.T) {
	// 4 KiB 上限：超限一律判为不命中，绝不截断后匹配 —— 截断会让
	// '.*' 与锚定谓词在截断点产生假阳性。
	rules := []Rule{{
		From:       "xkitora",
		To:         "xdesk",
		PrecededBy: preceded(t, `module[ \t]+`),
	}}

	// 先确认这个谓词在窗口内**确实会命中**，否则下面那句「不命中」
	// 什么也证明不了
	short := "abcmodule" + spaces(10) + "xkitora"
	if out, _, _ := Apply([]byte(short), rules, nil); string(out) != "abcmodule"+spaces(10)+"xdesk" {
		t.Fatalf("短窗口下谓词应命中，实际 %q", out)
	}

	// 同一形态，仅把间隔拉过上限
	in := "abcmodule" + spaces(4090) + "xkitora"
	out, _, warns := Apply([]byte(in), rules, nil)
	if string(out) != in {
		t.Error("窗口超限时必须判为不命中，绝不允许假阳性替换")
	}
	if len(warns) == 0 {
		t.Error("窗口超限应给出警告")
	}
}

func TestApplyWindowWithinLimitMatches(t *testing.T) {
	// 窗口在上限内时谓词正常工作
	rules := []Rule{{
		From:       "xkitora",
		To:         "xdesk",
		PrecededBy: preceded(t, `module[ \t]+`),
	}}
	out, _, _ := Apply([]byte("module xkitora"), rules, nil)
	if string(out) != "module xdesk" {
		t.Errorf("窗口内的谓词应正常匹配: %q", out)
	}
}

func TestApplyPredicateWindowIsSingleLine(t *testing.T) {
	// 谓词窗口不跨 \n —— 这是对旧实现的刻意偏离，旧版对整段前缀匹配是 O(n²)
	rules := []Rule{{
		From:       "xkitora",
		To:         "xdesk",
		PrecededBy: preceded(t, `module[ \t]+`),
	}}
	// 第一行命中、第二行不命中，正说明窗口是按行切的
	out, _, _ := Apply([]byte("module xkitora\nbar xkitora\n"), rules, nil)
	if string(out) != "module xdesk\nbar xkitora\n" {
		t.Errorf("窗口必须按行切：第一行应替换、第二行不应: %q", out)
	}
}

// TestApplyPredicatesThroughManifest 端到端验证锚定约定。
//
// 这个测试是必要的：锚定在 manifest 侧完成、窗口切分在 render 侧完成，
// 两处对不齐就会「单测全绿、实际谓词永不生效」。
func TestApplyPredicatesThroughManifest(t *testing.T) {
	m, err := manifest.Parse([]byte(`
manifest_version: 1
substitute:
  - from: "xkitora"
    to: "xdesk"
    paths: ["**"]
    when_preceded_by: 'module[ \t]+'
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cs := c.Subst[0]
	rules := []Rule{{
		From:       cs.Spec.From,
		To:         cs.Spec.To,
		PrecededBy: cs.PrecededBy,
		FollowedBy: cs.FollowedBy,
		Case:       cs.Case,
	}}

	out, _, _ := Apply([]byte("module xkitora\n"), rules, nil)
	if string(out) != "module xdesk\n" {
		t.Errorf("经 manifest 编译的谓词命中时应替换: %q", out)
	}
	out, _, _ = Apply([]byte("// xkitora\n"), rules, nil)
	if string(out) != "// xkitora\n" {
		t.Errorf("经 manifest 编译的谓词不命中时不应替换: %q", out)
	}
}

func TestApplyTargetCase(t *testing.T) {
	tests := []struct {
		c    manifest.RenderCase
		want string
	}{
		{manifest.CaseNone, "Xdesk"},
		{manifest.CaseLower, "xdesk"},
		{manifest.CaseUpper, "XDESK"},
	}
	for _, tt := range tests {
		out, _, _ := Apply([]byte("VALUE"), []Rule{{From: "VALUE", To: "Xdesk", Case: tt.c}}, nil)
		if string(out) != tt.want {
			t.Errorf("Case=%v: Apply = %q, want %q", tt.c, out, tt.want)
		}
	}
}

func TestApplyReportsHitCounts(t *testing.T) {
	// 夹具用 "a-a-a" 而不是 "aaa"：字母边界是无条件的（规范 §7.3 step 3a），
	// "aaa" 里每个 a 的邻居都是字母，三个候选位置全被边界拒绝，hits[0] 会是 0
	// —— 与这个测试的意图（3 次命中）矛盾。rebrand.sh 对同一输入也是 0 改动
	// （实测其 boundary_ok 同样拒绝），所以是夹具写错了，不是引擎错了。
	rules := []Rule{
		{From: "a", To: "A"},
		{From: "zzz", To: "Z"}, // 零命中
	}
	_, hits, _ := Apply([]byte("a-a-a"), rules, nil)
	if len(hits) != 2 {
		t.Fatalf("hits 长度 = %d, want 2", len(hits))
	}
	if hits[0] != 3 {
		t.Errorf("规则 0 命中数 = %d, want 3", hits[0])
	}
	if hits[1] != 0 {
		t.Errorf("规则 1 命中数 = %d, want 0（零替换要能被调用方察觉）", hits[1])
	}
}

func TestApplyNoMatchReturnsSameBytes(t *testing.T) {
	in := []byte("nothing here")
	out, _, _ := Apply(in, []Rule{{From: "zzz", To: "x"}}, nil)
	if string(out) != string(in) {
		t.Errorf("无命中应原样返回: %q", out)
	}
}

func TestApplyHandlesNonASCII(t *testing.T) {
	out, _, _ := Apply([]byte("你好 xkitora 世界"), []Rule{{From: "xkitora", To: "xdesk"}}, nil)
	if string(out) != "你好 xdesk 世界" {
		t.Errorf("Apply = %q", out)
	}
}

func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
