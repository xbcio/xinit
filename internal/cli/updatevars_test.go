package cli

import (
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/manifest"
)

func compileManifestT(t *testing.T, yaml string) *manifest.Compiled {
	t.Helper()
	m, err := manifest.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return c
}

const varsYAML = `
manifest_version: 1
vars:
  - name: app
    default: demo
`

func TestResolveUpdateVarsPriority(t *testing.T) {
	c := compileManifestT(t, varsYAML)

	// 冻结值 > default
	v, err := ResolveUpdateVars(c, map[string]string{"app": "old"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["app"] != "old" {
		t.Errorf("app = %q, want old（冻结值优先）", v["app"])
	}

	// --config > 冻结值
	v, err = ResolveUpdateVars(c, map[string]string{"app": "old"}, map[string]string{"app": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if v["app"] != "new" {
		t.Errorf("app = %q, want new（--config 优先）", v["app"])
	}

	// 冻结无、config 无 → default
	v, err = ResolveUpdateVars(c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["app"] != "demo" {
		t.Errorf("app = %q, want demo（default）", v["app"])
	}

	// 冻结值是空串也算「有记录」——不得被新的 default 覆盖
	v, err = ResolveUpdateVars(c, map[string]string{"app": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["app"] != "" {
		t.Errorf("app = %q, want 空串（冻结的空值不被 default 覆盖）", v["app"])
	}
}

func TestResolveUpdateVarsNewVarRequiresConfig(t *testing.T) {
	c := compileManifestT(t, `
manifest_version: 1
vars:
  - name: flag
`)
	_, err := ResolveUpdateVars(c, map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "flag") || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("新声明且三无必须报错并要求 --config：%v", err)
	}

	v, err := ResolveUpdateVars(c, nil, map[string]string{"flag": "on"})
	if err != nil {
		t.Fatal(err)
	}
	if v["flag"] != "on" {
		t.Errorf("flag = %q", v["flag"])
	}
}

// 变量在 state.vars 里**有键**（空串）就不算新声明——否则模板里非必填的
// 可选变量会在每次 update 时把用户拦住。
func TestResolveUpdateVarsEmptyFrozenCountsAsKnown(t *testing.T) {
	c := compileManifestT(t, `
manifest_version: 1
vars:
  - name: feature
`)
	v, err := ResolveUpdateVars(c, map[string]string{"feature": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["feature"] != "" {
		t.Errorf("feature = %q", v["feature"])
	}
}

func TestResolveUpdateVarsPatternCheckedOnFrozenValue(t *testing.T) {
	c := compileManifestT(t, `
manifest_version: 1
vars:
  - name: slug
    pattern: ^[a-z][a-z0-9-]*$
`)
	_, err := ResolveUpdateVars(c, map[string]string{"slug": "Bad_Slug"}, nil)
	if err == nil {
		t.Fatal("冻结值不符合新版 pattern 必须报错")
	}
	for _, want := range []string{"slug", "Bad_Slug", "pattern", "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错应点名 %q：%v", want, err)
		}
	}
}

func TestResolveUpdateVarsRequiredEmptyErrors(t *testing.T) {
	c := compileManifestT(t, `
manifest_version: 1
vars:
  - name: name
    required: true
`)
	_, err := ResolveUpdateVars(c, map[string]string{"name": ""}, nil)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("必填变量值为空必须报错：%v", err)
	}
}

func TestResolveUpdateVarsDeletedVarIgnored(t *testing.T) {
	c := compileManifestT(t, varsYAML)
	v, err := ResolveUpdateVars(c, map[string]string{"app": "x", "gone": "y"}, nil)
	if err != nil {
		t.Fatalf("state.vars 中被删除的变量不得报错（§3.4）：%v", err)
	}
	if _, ok := v["gone"]; ok {
		t.Error("已删除变量不进本次渲染的 vars（保留动作在调用方）")
	}
}

func TestResolveUpdateVarsRejectsUnknownConfigKey(t *testing.T) {
	c := compileManifestT(t, varsYAML)
	_, err := ResolveUpdateVars(c, nil, map[string]string{"typo": "1"})
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("--config 里未声明的变量必须报错（防打错字）：%v", err)
	}
}

func TestResolveUpdateVarsDefaultReferencesEarlierVar(t *testing.T) {
	c := compileManifestT(t, `
manifest_version: 1
vars:
  - name: a
    default: x
  - name: b
    default: "{{ .a }}-1"
`)
	v, err := ResolveUpdateVars(c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["b"] != "x-1" {
		t.Errorf("b = %q, want x-1（default 渲染与 EvalVars 同规则）", v["b"])
	}
}
