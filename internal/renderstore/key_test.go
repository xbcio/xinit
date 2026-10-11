package renderstore

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// baseState 造一个可用于键敏感性对照的最小 state（engine "e1" / dir 源 /tpl）。
func baseState() *state.State {
	st := state.New("e1")
	st.Template = state.TemplateRef{Source: "/tpl", Kind: "dir", Commit: "sha256/c1"}
	st.Vars = map[string]string{"a": "1"}
	return st
}

// testKey 与 Write/Fetch 同源地从 state 现算家族键（四个分量逐字段取用）。
func testKey(st *state.State) string {
	return familyKey(st.EngineVersion, st.Template.Source, st.Template.Kind, st.Vars)
}

func TestFamilyKeyDeterministic(t *testing.T) {
	// §21.3：同 state 两次现算 → 同键；小写 hex、64 字符。
	st := baseState()
	k1, k2 := testKey(st), testKey(st)
	if k1 != k2 {
		t.Fatalf("同 state 两次现算键不同：%q vs %q", k1, k2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(k1) {
		t.Errorf("键必须是 64 位小写 hex：%q", k1)
	}
}

func TestFamilyKeyTupleSensitive(t *testing.T) {
	// 元组敏感：engine / source / kind / vars 各改一项 → 键必变。
	want := testKey(baseState())

	cases := []struct {
		name   string
		mutate func(*state.State)
	}{
		{"engine", func(st *state.State) { st.EngineVersion = "e2" }},
		{"source", func(st *state.State) { st.Template.Source = "/tpl2" }},
		{"kind", func(st *state.State) { st.Template.Kind = "git" }},
		{"vars-value", func(st *state.State) { st.Vars["a"] = "2" }},
		{"vars-key", func(st *state.State) { st.Vars = map[string]string{"b": "1"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := baseState()
			tc.mutate(st)
			if got := testKey(st); got == want {
				t.Errorf("元组分量 %s 变了键却没变：%q", tc.name, got)
			}
		})
	}
}

func TestFamilyKeyIgnoresAnchor(t *testing.T) {
	// §21.3：anchor（commit / dir 树 hash）不进键——由读侧核对清单。
	st := baseState()
	k1 := testKey(st)
	st.Template.Commit = "sha256/other"
	if k2 := testKey(st); k2 != k1 {
		t.Errorf("anchor 不应影响家族键：%q vs %q", k1, k2)
	}
}

func TestVarsCanonicalNilEqualsEmpty(t *testing.T) {
	// §21.3：空集（含 nil）编码 = 零字节；nil 与空 map 等价。
	if got := varsCanonical(nil); len(got) != 0 {
		t.Errorf("varsCanonical(nil) 应为零字节，得 %q", got)
	}
	if got := varsCanonical(map[string]string{}); len(got) != 0 {
		t.Errorf("varsCanonical(空 map) 应为零字节，得 %q", got)
	}
	if familyKey("e", "s", "k", nil) != familyKey("e", "s", "k", map[string]string{}) {
		t.Error("nil 与空 map 的家族键必须相同")
	}
}

func TestVarsCanonicalByteSortedKeys(t *testing.T) {
	// 键按字节序（"B" 0x42 < "a" 0x61），与插入顺序无关。
	got := varsCanonical(map[string]string{"b": "2", "a": "1"})
	if want := []byte("1:a1:11:b1:2"); !bytes.Equal(got, want) {
		t.Errorf("canonical = %q, want %q", got, want)
	}
	got = varsCanonical(map[string]string{"a": "1", "B": "2"})
	if want := []byte("1:B1:21:a1:1"); !bytes.Equal(got, want) {
		t.Errorf("键必须按字节序（非本地化）排序：canonical = %q, want %q", got, want)
	}
}

func TestVarsCanonicalLengthIsBytes(t *testing.T) {
	// netstring 的 len 是**字节数**（不是 rune 数）："中" = 3 字节 → "3:中"。
	got := varsCanonical(map[string]string{"k": "中"})
	if want := []byte("1:k3:中"); !bytes.Equal(got, want) {
		t.Errorf("多字节长度必须按字节数：canonical = %q, want %q", got, want)
	}
}

func TestVarsCanonicalNoAmbiguity(t *testing.T) {
	// netstring 消歧反例：裸拼接口径下这两对会碰撞，netstring 下不得碰撞。
	cases := []struct {
		name string
		a, b map[string]string
	}{
		{"值含分隔符", map[string]string{"a": "b|c"}, map[string]string{"a": "b", "c": ""}},
		{"值形似 netstring", map[string]string{"a": "1:b"}, map[string]string{"a": "1", "b": "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Equal(varsCanonical(tc.a), varsCanonical(tc.b)) {
				t.Errorf("canonical 碰撞：%q", varsCanonical(tc.a))
			}
			if familyKey("e", "s", "k", tc.a) == familyKey("e", "s", "k", tc.b) {
				t.Error("家族键碰撞")
			}
		})
	}
}

func TestVarsCanonicalKeepsRetiredVars(t *testing.T) {
	// §21.3：已删变量保留在 st.Vars（停盘值）里 → 必须参与编码。
	full := map[string]string{"a": "1", "old": "x"}
	without := map[string]string{"a": "1"}
	if bytes.Equal(varsCanonical(full), varsCanonical(without)) {
		t.Error("保留在 st.Vars 的旧键必须参与编码")
	}
	if familyKey("e", "s", "k", full) == familyKey("e", "s", "k", without) {
		t.Error("保留的旧键应改变家族键")
	}
}

func TestVarsCanonicalArbitraryBytes(t *testing.T) {
	// 值可为任意字节（含 \x00）——编码不得截断或转义。
	got := varsCanonical(map[string]string{"k": "a\x00b"})
	if want := []byte("1:k3:a\x00b"); !bytes.Equal(got, want) {
		t.Errorf("含 \\x00 的值编码不符：%q, want %q", got, want)
	}
}

func TestFamilyKeyGolden(t *testing.T) {
	// v1 编码契约锚：netstring / 字节序 / 域前缀任一细节改动都会让它失败——
	// 那是编码契约变更，必须升 "xinit/render-snapshot/v2" 前缀并同步换锚值
	// （§21.3）。锚值由独立实现（Python 版同一字节串）交叉核对后固化。
	const want = "10592603fe3f5a6d5c087936b936eb1d44bf5acc1df41605c887663560f62b9f"
	got := familyKey("0.1.0", "https://example.com/tpl.git", "git",
		map[string]string{"app_slug": "demo", "多": "值"})
	t.Logf("familyKey 实算 = %s", got)
	if got != want {
		t.Errorf("家族键编码契约漂移：got %s, want %s（改编码须升 v2 前缀）", got, want)
	}
}
