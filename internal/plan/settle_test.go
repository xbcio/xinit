package plan

import (
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/render"
)

// §8.5 的每行一个断言（验收 ②）：夹具刻意让「朴素拷贝 theirs」与「按位规则/
// mode 规则修整后」的值不同，断言才有判别力。
func TestSection85NewBasePerRow(t *testing.T) {
	tests := []struct {
		row    string
		base   Snap
		ours   Snap
		theirs Snap
		hooks  bool
		settle bool // 行 7 干净合并：分类后由调用方调 SettleMergeClean（R19）
		want   Action
	}{
		{
			row:    "行1 三方一致 → theirs[P]（exec 取模板）",
			base:   Snap{"s.sh": reg("h1")},
			ours:   Snap{"s.sh": reg("h1")},
			theirs: Snap{"s.sh": execm("h1")},
			want:   Action{Path: "s.sh", Kind: Noop, NewBase: ptr(execm("h1"))},
		},
		{
			row:    "行2 ours==base → theirs[P]",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h1")},
			theirs: Snap{"a.txt": execm("h2")},
			want:   Action{Path: "a.txt", Kind: WriteTheirs, NewBase: ptr(execm("h2"))},
		},
		{
			row:    "行3 保留本地改动 → theirs[P]（exec 仍按模板对齐）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": execm("h1")},
			want:   Action{Path: "a.txt", Kind: Noop, NewBase: ptr(execm("h1"))},
		},
		{
			row:    "行4 ours==theirs → theirs[P]",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": execm("h2")},
			want:   Action{Path: "a.txt", Kind: Noop, NewBase: ptr(execm("h2"))},
		},
		{
			row:    "行5 覆盖 → theirs[P]",
			base:   Snap{"gen.txt": genm("h1")},
			ours:   Snap{"gen.txt": reg("h2")},
			theirs: Snap{"gen.txt": genm("h3")},
			hooks:  true,
			want:   Action{Path: "gen.txt", Kind: WriteTheirs, NewBase: ptr(genm("h3"))},
		},
		{
			row:  "行6 冲突 → 保持旧值（Exec/Binary/Generated 原样）",
			base: Snap{"img.png": {Kind: render.KindRegular, Hash: "h1", Exec: true, Binary: true, Generated: true}},
			ours: Snap{"img.png": reg("h2")}, theirs: Snap{"img.png": reg("h3")},
			want: Action{Path: "img.png", Kind: ConflictBinary,
				NewBase: ptr(FileMeta{Kind: render.KindRegular, Hash: "h1", Exec: true, Binary: true, Generated: true})},
		},
		{
			row:    "行7 有冲突 → 保持旧值（不调 SettleMergeClean）",
			base:   Snap{"a.txt": {Kind: render.KindRegular, Hash: "h1", Exec: true}},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": reg("h3")},
			want:   Action{Path: "a.txt", Kind: Merge, NewBase: ptr(FileMeta{Kind: render.KindRegular, Hash: "h1", Exec: true})},
		},
		{
			// 夹具修正（偏离 brief 的逐字夹具，理由见报告）：原文 base 用 genm
			// 使 base/theirs 两位皆真，在 hooks=true 下 Plan 落行 5（WriteTheirs）
			// 而非行 7；且行 7 不可能被 promoteAllowed 放行（放行需要
			// sameContent(o,b)，那是行 2），真值必然落回 false。故：base 去位，
			// 断言 = theirs 拷贝再按位规则修整（Generated 保持 false）。
			row:    "行7 干净合并 → SettleMergeClean 落定为 theirs[P]（含 mode 与位）",
			base:   Snap{"gen.txt": reg("h1")},
			ours:   Snap{"gen.txt": reg("h2")},
			theirs: Snap{"gen.txt": execm2gen("h3")}, // generated 且 exec
			hooks:  true,
			settle: true,
			want:   Action{Path: "gen.txt", Kind: Merge, NewBase: ptr(execm("h3"))},
		},
		{
			row:    "行8 → 移除（nil）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h1")},
			theirs: Snap{},
			want:   Action{Path: "a.txt", Kind: Delete, NewBase: nil},
		},
		{
			row:    "行9 → 保持旧值（含位 true 保持）",
			base:   Snap{"gen.txt": execm2gen("h1")},
			ours:   Snap{"gen.txt": reg("h2")},
			theirs: Snap{},
			want:   Action{Path: "gen.txt", Kind: KeepWarned, NewBase: ptr(execm2gen("h1"))},
		},
		{
			row:    "行10 → theirs[P]，true→false 不受限（位落回 false）",
			base:   Snap{"gen.txt": genm("h1")},
			ours:   Snap{},
			theirs: Snap{"gen.txt": execm("h2")},
			want:   Action{Path: "gen.txt", Kind: KeepLocalDelete, NewBase: ptr(execm("h2"))},
		},
		{
			row:    "行11 → theirs[P]，false→true 不放行（A5 保守，位保持 false）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{},
			theirs: Snap{"a.txt": execm2gen("h1")},
			want:   Action{Path: "a.txt", Kind: Noop, NewBase: ptr(execm("h1"))},
		},
		{
			row:    "行13 → theirs[P]，false→true 放行（模板新增时落盘）",
			base:   Snap{},
			ours:   Snap{},
			theirs: Snap{"gen.txt": genm("h1")},
			want:   Action{Path: "gen.txt", Kind: WriteTheirs, NewBase: ptr(genm("h1"))},
		},
		{
			row:    "行14 → theirs[P]，false→true 放行（内容与模板逐字节相同）",
			base:   Snap{},
			ours:   Snap{"gen.txt": reg("h1")},
			theirs: Snap{"gen.txt": genm("h1")},
			want:   Action{Path: "gen.txt", Kind: Noop, NewBase: ptr(genm("h1"))},
		},
		{
			row:    "行15 → 保持旧值，base✗ 时为 nil",
			base:   Snap{},
			ours:   Snap{"new.txt": reg("h1")},
			theirs: Snap{"new.txt": genm("h2")},
			want:   Action{Path: "new.txt", Kind: ConflictBothAdded, NewBase: nil},
		},
	}

	for _, tc := range tests {
		t.Run(tc.row, func(t *testing.T) {
			got := Plan(tc.base, tc.ours, tc.theirs, nil, tc.hooks)
			if len(got) != 1 {
				t.Fatalf("动作数 %d：%v", len(got), got)
			}
			if tc.settle {
				SettleMergeClean(&got[0], tc.base, tc.ours, tc.theirs)
			}
			checkActions(t, got, []Action{tc.want})
		})
	}
}

func TestExecDivergenceWarnsInsteadOfChmod(t *testing.T) {
	// 行 1：模板改了 exec，但本地 exec 与 base 不一致（用户自己 chmod 过）
	base := Snap{"s.sh": reg("h1")}     // exec=false
	ours := Snap{"s.sh": execm("h1")}   // 用户 +x
	theirs := Snap{"s.sh": execm("h1")} // 模板也要 +x
	got := Plan(base, ours, theirs, nil, false)
	if len(got) != 1 || got[0].Kind != Noop {
		t.Fatalf("%v", got)
	}
	if !got[0].NewBase.Exec {
		t.Error("「不动」= NewBase.Exec 不变（从 theirs 拷来的值）；apply 侧靠 R7 前件不成立来拒绝 chmod")
	}
	if !strings.Contains(got[0].Note, "执行位") {
		t.Errorf("应打印 exec 不一致的警告；got %q", got[0].Note)
	}
}

// L7 跳过必须保持旧值（含 Exec）：若 mode 步不限制在「推进了 base」的动作上，
// 这里会把旧值的 Exec 改成模板的 false。
func TestL7SkipKeepsOldValueIncludingExec(t *testing.T) {
	base := Snap{"gen.txt": execm2gen("h1")} // Exec=true, Generated=true
	ours := Snap{"gen.txt": execm("h2")}     // Exec=true——与 base 相同，正好落进 mode 步的第一分支
	theirs := Snap{"gen.txt": genm("h3")}    // 模板去掉了 exec 位
	got := Plan(base, ours, theirs, nil, false)
	if len(got) != 1 || got[0].Kind != Noop {
		t.Fatalf("%v", got)
	}
	if !metaEq(got[0].NewBase, ptr(execm2gen("h1"))) {
		t.Fatalf("跳过必须原样保持旧值；got %+v", got[0].NewBase)
	}
}

// D3 两步发布攻击的完整走查：位规则必须挡住第一步。
func TestGeneratedBitBlocksTwoStepRelease(t *testing.T) {
	ours := Snap{"a.txt": reg("h2")} // 用户改过

	// v2：只把 a.txt 加进 generated，内容不动（h1）
	v1base := Snap{"a.txt": reg("h1")}
	v2theirs := Snap{"a.txt": genm("h1")}
	step1 := Plan(v1base, ours, v2theirs, nil, true)
	if len(step1) != 1 || step1[0].Kind != Noop {
		t.Fatalf("第一步应落行 3 Noop：%v", step1)
	}
	if step1[0].NewBase.Generated {
		t.Fatal("行 3 是 ours!=base 的轮次，false→true 必须被挡住")
	}

	// v3：generated 且内容变了。此时 base 的位是 false → 行 5 前提不成立
	step2 := Plan(Snap{"a.txt": *step1[0].NewBase}, ours, Snap{"a.txt": genm("h3")}, nil, true)
	if len(step2) != 1 || step2[0].Kind == WriteTheirs {
		t.Fatalf("两步发布攻击成功——用户改动会被覆盖：%v", step2)
	}
	if step2[0].Kind != Merge {
		t.Fatalf("应落行 7 Merge；got %v", step2[0].Kind)
	}
}
