package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
)

// promptVarsYAML 是本文件的清单夹具：三个可直接回答的变量、一个引用前序
// 变量的模板默认值、一个未声明 prompt 的变量（§19.3 提问条件）。
const promptVarsYAML = `manifest_version: 1
vars:
  - name: product
    prompt: 产品显示名
    default: Demo
  - name: team
    prompt: 团队
    default: ""
  - name: component
    prompt: 组件名
    required: true
    pattern: '^[a-z][a-z0-9-]*$'
  - name: derived
    prompt: 组件全名
    default: '{{ .product }}-{{ .component }}'
  - name: silent
    default: x
`

func compiledVars(t *testing.T) *manifest.Compiled {
	t.Helper()
	m, err := manifest.Parse([]byte(promptVarsYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newTestSession 构造注入式会话：answers 逐行喂入，返回会话与输出缓冲。
func newTestSession(answers string) (*promptSession, *bytes.Buffer) {
	var out bytes.Buffer
	return newPromptSession(strings.NewReader(answers), &out), &out
}

// merged 是 Init 第 3 步的合并语义（--config 优先，回答补缺）。
func merged(config, answers map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range config {
		out[k] = v
	}
	for k, v := range answers {
		out[k] = v
	}
	return out
}

func TestCollectVarsOnlyPrompted(t *testing.T) {
	c := compiledVars(t)

	// 无 --config：四个声明了 prompt 的变量被问；silent 没有 prompt ⇒ 零提示。
	ps, out := newTestSession("MyApp\n\ncore\n\n")
	if _, err := collectVars(c, nil, ps); err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "silent") {
		t.Errorf("未声明 prompt 的变量不应被问：%q", got)
	}
	for _, want := range []string{"产品显示名", "团队", "组件名", "组件全名"} {
		if !strings.Contains(got, want) {
			t.Errorf("应提问 %q：%q", want, got)
		}
	}

	// --config 已提供的不问（§19.3 提问条件、不变量 5）：被提供的值也不进
	// answers —— 它们本来就在 provided 里，collectVars 只返回「补缺的回答」。
	ps, out = newTestSession("\n\n")
	answers, err := collectVars(c, map[string]string{"product": "FromCfg", "component": "core"}, ps)
	if err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	got = out.String()
	if strings.Contains(got, "产品显示名") || strings.Contains(got, "组件名") {
		t.Errorf("--config 已提供的变量不应被问：%q", got)
	}
	if !strings.Contains(got, "团队（可留空）: ") || !strings.Contains(got, "组件全名 [FromCfg-core]: ") {
		t.Errorf("其余变量照问，且提示值取 --config 值：%q", got)
	}
	if _, ok := answers["product"]; ok {
		t.Errorf("--config 提供的值不应出现在 answers 里：%v", answers)
	}
}

func TestCollectVarsDeclarationOrder(t *testing.T) {
	// 提示行按清单声明顺序出现，且连续（回答不回显，§19.2.5）——整段逐字相等
	// 同时锁住顺序、V1/V2/V3 三态与逐字文案。
	c := compiledVars(t)
	ps, out := newTestSession("MyApp\n\ncore\n\n")
	if _, err := collectVars(c, nil, ps); err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	const want = "产品显示名 [Demo]: 团队（可留空）: 组件名（必填）: 组件全名 [MyApp-core]: "
	if got := out.String(); got != want {
		t.Errorf("提示行序列：\n得到 %q\n期望 %q", got, want)
	}
}

func TestCollectVarsEmptyAnswerTakesDefault(t *testing.T) {
	// 空答 = 取 default（提示值），不入 answers ⇒ EvalVars 走既有 default 分支。
	c := compiledVars(t)
	config := map[string]string{"component": "core"}
	ps, _ := newTestSession("\n\n\n")
	answers, err := collectVars(c, config, ps)
	if err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	if len(answers) != 0 {
		t.Fatalf("空答不应进 answers 表：%v", answers)
	}

	vars, err := render.EvalVars(c, merged(config, answers))
	if err != nil {
		t.Fatalf("EvalVars: %v", err)
	}
	if vars["product"] != "Demo" {
		t.Errorf("空答应取 default：product = %q", vars["product"])
	}
	if vars["team"] != "" {
		t.Errorf("无 default 的可选变量空答应取空串：team = %q", vars["team"])
	}
	// 模板默认值仍由 EvalVars 渲染：product 落回 default("Demo") 后参与 derived
	if vars["derived"] != "Demo-core" {
		t.Errorf("derived = %q，期望 Demo-core", vars["derived"])
	}
}

func TestCollectVarsHintRendersPrior(t *testing.T) {
	c := compiledVars(t)

	// 前序回答参与渲染（§19.7 #8）。
	ps, out := newTestSession("MyApp\n\ncore\n\n")
	if _, err := collectVars(c, nil, ps); err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "组件全名 [MyApp-core]: ") {
		t.Errorf("V1 应显示渲染后的默认值：%q", got)
	}

	// 前序的 default 与 --config 值同样参与渲染（product/team/derived 三问）。
	ps, out = newTestSession("\n\n\n")
	if _, err := collectVars(c, map[string]string{"component": "core"}, ps); err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "组件全名 [Demo-core]: ") {
		t.Errorf("前序 default 应参与渲染：%q", got)
	}
}

func TestCollectVarsPatternReask(t *testing.T) {
	// pattern 失败 → V4 文案 + 重问；坏值不进结果（§19.3 重问判据）。
	c := compiledVars(t)
	config := map[string]string{"product": "P", "team": "T", "derived": "D"}
	ps, out := newTestSession("Bad..Name\nok-name\n")
	answers, err := collectVars(c, config, ps)
	if err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	got := out.String()
	const wantReason = "变量 \"component\" 的值 \"Bad..Name\" 不匹配 pattern \"^[a-z][a-z0-9-]*$\"；请重答（§19.3）\n"
	if !strings.Contains(got, wantReason) {
		t.Errorf("应有 V4 文案：\n得到 %q\n期望含 %q", got, wantReason)
	}
	if n := strings.Count(got, "组件名（必填）: "); n != 2 {
		t.Errorf("应重问一次（提示行两遍），实际 %d 遍：%q", n, got)
	}
	if len(answers) != 1 || answers["component"] != "ok-name" {
		t.Fatalf("坏值不得落地、好值应入表：%v", answers)
	}
	if _, err := render.EvalVars(c, merged(config, answers)); err != nil {
		t.Errorf("收敛后的回答应通过 EvalVars：%v", err)
	}
}

func TestCollectVarsRequiredReask(t *testing.T) {
	// required 且无默认值：空答生效值为空 ⇒ V5 文案 + 重问（§19.3）。
	c := compiledVars(t)
	config := map[string]string{"product": "P", "team": "T", "derived": "D"}
	ps, out := newTestSession("\nok-name\n")
	answers, err := collectVars(c, config, ps)
	if err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	got := out.String()
	const wantReason = "变量 \"component\" 是必填项且无默认值；请重答（§19.3）\n"
	if !strings.Contains(got, wantReason) {
		t.Errorf("应有 V5 文案：\n得到 %q\n期望含 %q", got, wantReason)
	}
	if n := strings.Count(got, "组件名（必填）: "); n != 2 {
		t.Errorf("应重问一次（提示行两遍），实际 %d 遍：%q", n, got)
	}
	if len(answers) != 1 || answers["component"] != "ok-name" {
		t.Fatalf("空答不得作为必填项的结果：%v", answers)
	}
}

func TestCollectVarsEOFAborts(t *testing.T) {
	// 采集中 EOF ⇒ 中止：普通 error（非 ExitError，退 1）、点名 --config、
	// 不产生任何回答（§19.3 EOF 表）。
	c := compiledVars(t)
	config := map[string]string{"product": "P", "team": "T", "derived": "D"}
	ps, out := newTestSession("")
	answers, err := collectVars(c, config, ps)
	if err == nil {
		t.Fatal("EOF 应中止采集")
	}
	msg := err.Error()
	for _, want := range []string{
		`变量 "component"`,
		"输入已结束（EOF）",
		"--config",
		"（§19.3）",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("中止文案应含 %q：%q", want, msg)
		}
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		t.Errorf("EOF 中止是普通错误（退 1），不应是 ExitError：%v", err)
	}
	if _, ok := answers["component"]; ok {
		t.Errorf("中止时不得产生该键：%v", answers)
	}
	// 中止前已问了 component（提示行照打一次），此后不再打印任何提示。
	if got := out.String(); got != "组件名（必填）: " {
		t.Errorf("中止路径的输出应只有首问提示行：%q", got)
	}
}

func TestCollectVarsHintRenderFailureDegrades(t *testing.T) {
	// 默认值渲染失败（引用了未声明变量）不新开失败路径：提示降级为「无默认值」
	// 形态，判定后移给 EvalVars（同一文案、同一时机，§19.3）。
	m, err := manifest.Parse([]byte(`manifest_version: 1
vars:
  - name: broken
    prompt: 坏默认
    default: '{{ .nope }}'
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}

	ps, out := newTestSession("\n")
	answers, err := collectVars(c, nil, ps)
	if err != nil {
		t.Fatalf("提示值渲染失败不得让采集失败：%v", err)
	}
	if got := out.String(); got != "坏默认（可留空）: " {
		t.Errorf("提示行应降级为 V3（无 […]）：%q", got)
	}
	if len(answers) != 0 {
		t.Errorf("空答不入表：%v", answers)
	}

	// 判定后移到 EvalVars：与非交互路径同一文案。
	_, err = render.EvalVars(c, answers)
	if err == nil || !strings.Contains(err.Error(), "vars.broken.default") {
		t.Errorf("应交给 EvalVars 报 vars.broken.default：%v", err)
	}
}

func TestCollectVarsNilSession(t *testing.T) {
	// ps == nil = 本轮不可交互（§19.2.2）：零提问、零输出（没有会话就没有输出
	// 通道，采集循环整体跳过）、空表 —— 与非交互路径零回归（§19.7 #12）。
	c := compiledVars(t)
	answers, err := collectVars(c, nil, nil)
	if err != nil {
		t.Fatalf("无会话不应报错：%v", err)
	}
	if len(answers) != 0 {
		t.Fatalf("无会话应返回空表：%v", answers)
	}

	// 对照：同一清单在会话下必有提示与回答 —— 空结果确实来自「无会话」，
	// 而不是因为清单里没有被问的变量。
	ps, out := newTestSession("MyApp\n\ncore\n\n")
	if _, err := collectVars(c, nil, ps); err != nil {
		t.Fatalf("collectVars: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("同一清单在会话下应产生提示行")
	}
}
