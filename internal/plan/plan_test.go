package plan

import (
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/render"
)

// 元数据构造器：决策表只关心 Hash 的相等/不等，用短字符串即可。
func reg(h string) FileMeta   { return FileMeta{Kind: render.KindRegular, Hash: h} }
func execm(h string) FileMeta { m := reg(h); m.Exec = true; return m }
func genm(h string) FileMeta  { m := reg(h); m.Generated = true; return m }

// execm2gen = Regular + Exec + Generated。
func execm2gen(h string) FileMeta { m := genm(h); m.Exec = true; return m }
func binm(h string) FileMeta      { m := reg(h); m.Binary = true; return m }
func mixedm(h string) FileMeta    { m := reg(h); m.MixedEOL = true; return m }
func sym() FileMeta               { return FileMeta{Kind: render.KindSymlink} }

func ptr(m FileMeta) *FileMeta { return &m }

func metaEq(a, b *FileMeta) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func checkActions(t *testing.T, got, want []Action) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("动作数：got %d（%v），want %d（%v）", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].Path != want[i].Path || got[i].Kind != want[i].Kind {
			t.Errorf("动作 %d：got %s/%s，want %s/%s",
				i, got[i].Path, got[i].Kind, want[i].Path, want[i].Kind)
		}
		if !metaEq(got[i].NewBase, want[i].NewBase) {
			t.Errorf("%s 的 NewBase：got %+v，want %+v", got[i].Path, got[i].NewBase, want[i].NewBase)
		}
		if want[i].Note != "" && !strings.Contains(got[i].Note, want[i].Note) {
			t.Errorf("%s 的 Note 应包含 %q；got %q", got[i].Path, want[i].Note, got[i].Note)
		}
	}
}

// §8.2 的 16 行一行一夹具（验收 ① 的纯函数半边），外加 prune 与类型步。
func TestPlanRows(t *testing.T) {
	tests := []struct {
		name   string
		base   Snap
		ours   Snap
		theirs Snap
		pruned PruneSet
		hooks  bool
		want   []Action
	}{
		{
			name:   "行1 三方一致 → Noop",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h1")},
			theirs: Snap{"a.txt": reg("h1")},
			want:   []Action{{Path: "a.txt", Kind: Noop, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "行1 只改 exec 位也走 Noop（R17：== 只看内容）",
			base:   Snap{"s.sh": reg("h1")},
			ours:   Snap{"s.sh": reg("h1")},
			theirs: Snap{"s.sh": execm("h1")},
			want:   []Action{{Path: "s.sh", Kind: Noop, NewBase: ptr(execm("h1"))}},
		},
		{
			name:   "行2 本地未改、模板改了 → WriteTheirs",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h1")},
			theirs: Snap{"a.txt": reg("h2")},
			want:   []Action{{Path: "a.txt", Kind: WriteTheirs, NewBase: ptr(reg("h2"))}},
		},
		{
			name:   "行3 模板没改、本地改了 → Noop 保留本地",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": reg("h1")},
			want:   []Action{{Path: "a.txt", Kind: Noop, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "行4 本地与模板改成同一内容 → Noop",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": reg("h2")},
			want:   []Action{{Path: "a.txt", Kind: Noop, NewBase: ptr(reg("h2"))}},
		},
		{
			name:   "行5 两个 generated 位都真且钩子会跑 → WriteTheirs 覆盖",
			base:   Snap{"gen.txt": genm("h1")},
			ours:   Snap{"gen.txt": reg("h2")},
			theirs: Snap{"gen.txt": genm("h3")},
			hooks:  true,
			want:   []Action{{Path: "gen.txt", Kind: WriteTheirs, NewBase: ptr(genm("h3")), Note: "覆盖"}},
		},
		{
			name:   "行5 前提缺一（上次不是 generated）→ 落行 7 Merge",
			base:   Snap{"gen.txt": reg("h1")},
			ours:   Snap{"gen.txt": reg("h2")},
			theirs: Snap{"gen.txt": genm("h3")},
			hooks:  true,
			want:   []Action{{Path: "gen.txt", Kind: Merge, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "L7 跳过：generated + hooksRun=false → Noop 保持旧值（含位）",
			base:   Snap{"gen.txt": genm("h1")},
			ours:   Snap{"gen.txt": reg("h2")},
			theirs: Snap{"gen.txt": genm("h3")},
			want:   []Action{{Path: "gen.txt", Kind: Noop, NewBase: ptr(genm("h1")), Note: "跳过"}},
		},
		{
			name:   "行6 任一方二进制 → ConflictBinary",
			base:   Snap{"img.png": reg("h1")},
			ours:   Snap{"img.png": binm("h2")},
			theirs: Snap{"img.png": reg("h3")},
			want:   []Action{{Path: "img.png", Kind: ConflictBinary, NewBase: ptr(reg("h1")), Note: "conflicts/"}},
		},
		{
			name:   "行6 任一方混合 EOL → ConflictBinary（第二判据）",
			base:   Snap{"mix.txt": reg("h1")},
			ours:   Snap{"mix.txt": reg("h2")},
			theirs: Snap{"mix.txt": mixedm("h3")},
			want:   []Action{{Path: "mix.txt", Kind: ConflictBinary, NewBase: ptr(reg("h1")), Note: "conflicts/"}},
		},
		{
			name:   "行6 base 侧是二进制也算「任一方」（§8.2 原文）",
			base:   Snap{"a.dat": binm("h1")},
			ours:   Snap{"a.dat": reg("h2")},
			theirs: Snap{"a.dat": reg("h3")},
			want:   []Action{{Path: "a.dat", Kind: ConflictBinary, NewBase: ptr(binm("h1")), Note: "conflicts/"}},
		},
		{
			name:   "行7 三方文本 → Merge（NewBase 占位=旧值，R3）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": reg("h3")},
			want:   []Action{{Path: "a.txt", Kind: Merge, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "行8 模板删除、本地未改 → Delete",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h1")},
			theirs: Snap{},
			want:   []Action{{Path: "a.txt", Kind: Delete, NewBase: nil}},
		},
		{
			name:   "行9 模板删除、本地改过 → KeepWarned（警告含 cp 与 prune 出口）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{},
			want: []Action{{
				Path: "a.txt", Kind: KeepWarned, NewBase: ptr(reg("h1")),
				Note: "cp .xinit/base/a.txt a.txt",
			}},
		},
		{
			name:   "行10 本地删除、模板改了 → KeepLocalDelete（警告含找回命令）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{},
			theirs: Snap{"a.txt": reg("h2")},
			want: []Action{{
				Path: "a.txt", Kind: KeepLocalDelete, NewBase: ptr(reg("h2")),
				Note: "cp .xinit/base/a.txt a.txt",
			}},
		},
		{
			name:   "行11 本地删除、模板没改 → Noop 无声",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{},
			theirs: Snap{"a.txt": reg("h1")},
			want:   []Action{{Path: "a.txt", Kind: Noop, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "行12 模板与本地都删除 → 不产出 Action",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{},
			theirs: Snap{},
			want:   nil,
		},
		{
			name:   "行13 模板新增、本地没有 → WriteTheirs",
			base:   Snap{},
			ours:   Snap{},
			theirs: Snap{"new.txt": reg("h1")},
			want:   []Action{{Path: "new.txt", Kind: WriteTheirs, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "行14 本地已有同内容文件 → Noop 开始跟踪",
			base:   Snap{},
			ours:   Snap{"new.txt": reg("h1")},
			theirs: Snap{"new.txt": reg("h1")},
			want:   []Action{{Path: "new.txt", Kind: Noop, NewBase: ptr(reg("h1"))}},
		},
		{
			name:   "行15 本地与模板各自新增且内容不同 → ConflictBothAdded",
			base:   Snap{},
			ours:   Snap{"new.txt": reg("h1")},
			theirs: Snap{"new.txt": reg("h2")},
			want:   []Action{{Path: "new.txt", Kind: ConflictBothAdded, NewBase: nil, Note: "conflicts/"}},
		},
		{
			name:   "行16 纯本地新增不在并集 → 不产出 Action、不进 files（R18）",
			base:   Snap{},
			ours:   Snap{"local.txt": reg("h1")},
			theirs: Snap{},
			want:   nil,
		},
		{
			name:   "§8.0 prune 无条件（含 ours✓、theirs✓）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": reg("h2")},
			theirs: Snap{"a.txt": reg("h3")},
			pruned: PruneSet{"a.txt": true},
			want:   nil,
		},
		{
			name:   "§8.1 第一步 K1：类型不一致短路（内容相同也判冲突）",
			base:   Snap{"a.txt": reg("h1")},
			ours:   Snap{"a.txt": sym()},
			theirs: Snap{"a.txt": reg("h1")},
			want:   []Action{{Path: "a.txt", Kind: ConflictKind, NewBase: ptr(reg("h1")), Note: "prune.txt"}},
		},
		{
			name:   "§8.1 K2：模板把文件升级成目录（ours 侧文件挡路）",
			base:   Snap{},
			ours:   Snap{"a": reg("h1")},
			theirs: Snap{"a/b.txt": reg("h2")},
			want:   []Action{{Path: "a/b.txt", Kind: ConflictKind, NewBase: nil, Note: "在 ours 侧是常规文件"}},
		},
		{
			name:   "§8.1 K3：模板把目录降成文件（theirs 侧文件顶掉 base 里的子路径）",
			base:   Snap{"a/b.txt": reg("h1")},
			ours:   Snap{},
			theirs: Snap{"a": reg("h2")},
			want: []Action{
				{Path: "a", Kind: WriteTheirs, NewBase: ptr(reg("h2"))},
				{Path: "a/b.txt", Kind: ConflictKind, NewBase: ptr(reg("h1")), Note: "在 theirs 侧是常规文件"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Plan(tc.base, tc.ours, tc.theirs, tc.pruned, tc.hooks)
			checkActions(t, got, tc.want)
		})
	}
}

func TestPlanDeterministicOrder(t *testing.T) {
	base := Snap{"b.txt": reg("h1"), "a.txt": reg("h1")}
	ours := Snap{"b.txt": reg("h1"), "a.txt": reg("h1")}
	theirs := Snap{"b.txt": reg("h1"), "a.txt": reg("h1"), "c.txt": reg("h1")}
	got := Plan(base, ours, theirs, nil, false)
	if len(got) != 3 {
		t.Fatalf("动作数 %d", len(got))
	}
	for i, want := range []string{"a.txt", "b.txt", "c.txt"} {
		if got[i].Path != want {
			t.Errorf("第 %d 条应为 %s，got %s（输出必须按路径排序）", i, want, got[i].Path)
		}
	}
}

// NewBase 必须是与输入快照脱钩的副本：调用方（Task 12/6）会就地改
// NewBase.Exec / NewBase.Generated。
func TestPlanNewBaseIsDetachedCopy(t *testing.T) {
	base := Snap{"gen.txt": genm("h1")}
	ours := Snap{"gen.txt": reg("h2")}
	theirs := Snap{"gen.txt": genm("h3")}
	actions := Plan(base, ours, theirs, nil, true)
	if len(actions) != 1 || actions[0].NewBase == nil {
		t.Fatalf("夹具不对：%v", actions)
	}
	actions[0].NewBase.Exec = true
	actions[0].NewBase.Generated = false
	if theirs["gen.txt"].Exec || !theirs["gen.txt"].Generated {
		t.Fatal("Plan 的 NewBase 必须与输入快照脱钩")
	}
	if !base["gen.txt"].Generated || ours["gen.txt"].Hash != "h2" {
		t.Fatal("输入快照被修改")
	}
}
