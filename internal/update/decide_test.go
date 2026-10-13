package update

// T7b/M3b C4 断言：hooksRun × plan 行 5（I4）。落点 internal/update（§4.3 规则 1）。

import (
	"testing"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
)

// TestHooksRunMatchesPlanLine5 钉 I4（§1.3 hooksRun / §4.1）：
// hooksRun =「未给 --no-hooks ∧ 清单声明 post_update」的四组合真值表；并把
// 该真值直接接到 plan 的行 5（§8.2）——base/theirs 双 Generated ∧ 三方内容
// 皆异时：false → Noop+SkipNote（L7 跳过、不判冲突）、true → WriteTheirs+
// OverwriteGenerated（另存后覆盖）。
func TestHooksRunMatchesPlanLine5(t *testing.T) {
	base := plan.Snap{"g.txt": {Kind: render.KindRegular, Hash: "h1", Generated: true}}
	ours := plan.Snap{"g.txt": {Kind: render.KindRegular, Hash: "h2"}}
	theirs := plan.Snap{"g.txt": {Kind: render.KindRegular, Hash: "h3", Generated: true}}

	tests := []struct {
		name    string
		noHooks bool
		post    string
		want    bool
	}{
		{"默认 × 有钩子", false, "echo hi", true},
		{"noHooks × 有钩子", true, "echo hi", false},
		{"默认 × 无钩子", false, "", false},
		{"noHooks × 无钩子", true, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// sessionT 不设 noHooks（其构造只含 P2 字段）——本用例直接构造。
			s := &session{noHooks: tc.noHooks}
			tpl := &Template{Manifest: &manifest.Manifest{
				Hooks: manifest.HooksSpec{PostUpdate: tc.post},
			}}
			if got := hooksRun(s, tpl); got != tc.want {
				t.Fatalf("hooksRun = %v，want %v", got, tc.want)
			}

			// 行 5 对齐：动作形态必须与真值一致（行 5 前提 = 双 Generated ∧
			// 三方皆异——两处修正都会在此暴露）。
			acts := plan.Plan(base, ours, theirs, plan.PruneSet{}, tc.want)
			if len(acts) != 1 {
				t.Fatalf("动作数 = %d（%v）", len(acts), acts)
			}
			a := acts[0]
			if tc.want {
				if a.Kind != plan.WriteTheirs || !a.OverwriteGenerated {
					t.Errorf("hooksRun=true 应走行 5 覆盖支（另存后覆盖）：%+v", a)
				}
			} else {
				if a.Kind != plan.Noop || a.Note != plan.SkipNote || a.OverwriteGenerated {
					t.Errorf("hooksRun=false 应走 L7 跳过（Noop+SkipNote、位保持旧值）：%+v", a)
				}
			}
		})
	}
}
