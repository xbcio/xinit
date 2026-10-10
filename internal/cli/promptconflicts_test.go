package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
)

// cfMeta 是冲突夹具的常规文本文件元数据（Kind 恒 Regular；Generated 位按参数）。
func cfMeta(hash string, generated bool) plan.FileMeta {
	return plan.FileMeta{Kind: render.KindRegular, Hash: hash, Generated: generated}
}

// cfPtr 取元数据的指针（动作的 NewBase 是「改写前」旧值）。
func cfPtr(m plan.FileMeta) *plan.FileMeta { return &m }

// TestAskableConflict 锁住 §19.4.1 的可问闭集：Merge 且本轮判冲突、
// ConflictBinary、ConflictBothAdded 可问；ConflictKind、Merge 未判冲突、
// theirs 内容缺失一律不问。闭集（而非「排除 ConflictKind」）意味着将来新增
// Kind 默认不问，需显式加入。
func TestAskableConflict(t *testing.T) {
	theirs := map[string][]byte{"a.txt": []byte("t\n")}
	for _, tc := range []struct {
		name       string
		kind       plan.ActionKind
		conflicted map[string]bool
		want       bool
	}{
		{"Merge 且判冲突", plan.Merge, map[string]bool{"a.txt": true}, true},
		{"ConflictBinary", plan.ConflictBinary, map[string]bool{"a.txt": true}, true},
		{"ConflictBothAdded", plan.ConflictBothAdded, map[string]bool{"a.txt": true}, true},
		{"ConflictKind（无可写通道）", plan.ConflictKind, map[string]bool{"a.txt": true}, false},
		{"Merge 未判冲突", plan.Merge, map[string]bool{}, false},
		{"非冲突动作", plan.WriteTheirs, map[string]bool{"a.txt": true}, false},
	} {
		if got := askableConflict("a.txt", tc.kind, tc.conflicted, theirs); got != tc.want {
			t.Errorf("%s: askableConflict = %v，期望 %v", tc.name, got, tc.want)
		}
	}

	// theirs 缺失（模板已删该路径）：没有正常写入通道 ⇒ 不问（出口是 --accept）。
	conflicted := map[string]bool{"a.txt": true}
	for _, kind := range []plan.ActionKind{plan.Merge, plan.ConflictBinary, plan.ConflictBothAdded} {
		if askableConflict("a.txt", kind, conflicted, map[string][]byte{}) {
			t.Errorf("%v：theirs 缺失时不得提问", kind)
		}
		if askableConflict("a.txt", kind, conflicted, nil) {
			t.Errorf("%v：theirs 表为 nil 时不得提问", kind)
		}
	}
}

// TestTakeTheirsRewrite 锁住 §19.4.2 的动作改写：Kind → WriteTheirs、
// Note = C4、OverwriteGenerated 保持 false、NewBase = theirs[P]（含 §8.5 的
// Generated 位修整），以及 settle 补注追加在 C4 之后。
func TestTakeTheirsRewrite(t *testing.T) {
	// 三方：base 未改、ours 本地改过、theirs 模板改过且模板侧带 generated 位。
	base := plan.Snap{"a.txt": cfMeta("h1", false)}
	ours := plan.Snap{"a.txt": cfMeta("h2", false)}
	theirs := plan.Snap{"a.txt": cfMeta("h3", true)}
	a := plan.Action{Path: "a.txt", Kind: plan.Merge, NewBase: cfPtr(cfMeta("h1", false))}

	takeTheirs(&a, base, ours, theirs)

	if a.Kind != plan.WriteTheirs {
		t.Errorf("Kind = %v，期望 write（走正常写入通道）", a.Kind)
	}
	if a.Note != takeTheirsNote {
		t.Errorf("Note = %q，期望 C4 逐字", a.Note)
	}
	if a.OverwriteGenerated {
		t.Error("OverwriteGenerated 必须保持 false：改写不引入行 5 的另存语义")
	}
	// ours ≠ base ⇒ 不在 false→true 允许集合：新 base 取 theirs 的内容，
	// 但 Generated 位被 §8.5 修整回 false。
	want := cfMeta("h3", false)
	if a.NewBase == nil || *a.NewBase != want {
		t.Errorf("NewBase = %+v，期望 %+v", a.NewBase, want)
	}

	// settle 的 exec 补注必须追加在 C4 之后（noteJoin 语义）：ours 改过权限
	// ⇒ §8.3 不给结论、报告行是「C4；执行位…」。
	ours2 := plan.Snap{"a.txt": plan.FileMeta{Kind: render.KindRegular, Hash: "h2", Exec: true}}
	a2 := plan.Action{Path: "a.txt", Kind: plan.Merge, NewBase: cfPtr(cfMeta("h1", false))}
	takeTheirs(&a2, base, ours2, theirs)
	const settleNote = "执行位与 base 不一致（本地改过权限）：本轮不调整 exec"
	if want := takeTheirsNote + "；" + settleNote; a2.Note != want {
		t.Errorf("Note = %q，期望 %q", a2.Note, want)
	}
	if a2.NewBase == nil || a2.NewBase.Exec {
		t.Errorf("exec 分歧时本轮不调整 exec（新 base 保持 theirs 的位）：%+v", a2.NewBase)
	}
}

// TestTakeTheirsNoFalseGeneratedFlip 是 §19.4.2 的保守性锚：takeTheirs 用
// **改写前**的三方快照跑 settle，任何冲突形态下 generated false→true 都不
// 发生（promoteAllowed 看不到改写后的磁盘状态）。
func TestTakeTheirsNoFalseGeneratedFlip(t *testing.T) {
	for _, tc := range []struct {
		name         string
		base         plan.Snap
		ours         plan.Snap
		theirs       plan.Snap
		wantNewValue string
	}{
		{
			name:         "行 7 冲突（三方都在、ours ≠ base）",
			base:         plan.Snap{"a.txt": cfMeta("h1", false)},
			ours:         plan.Snap{"a.txt": cfMeta("h2", false)},
			theirs:       plan.Snap{"a.txt": cfMeta("h3", true)},
			wantNewValue: "h3",
		},
		{
			name:         "行 15 冲突（各自新增且不同）",
			base:         plan.Snap{},
			ours:         plan.Snap{"a.txt": cfMeta("h4", false)},
			theirs:       plan.Snap{"a.txt": cfMeta("h5", true)},
			wantNewValue: "h5",
		},
	} {
		a := plan.Action{Path: "a.txt", Kind: plan.Merge, Note: takeTheirsNote}
		takeTheirs(&a, tc.base, tc.ours, tc.theirs)
		if a.NewBase == nil {
			t.Fatalf("%s: NewBase 不得为 nil", tc.name)
		}
		if a.NewBase.Hash != tc.wantNewValue {
			t.Errorf("%s: NewBase.Hash = %q，期望 %q", tc.name, a.NewBase.Hash, tc.wantNewValue)
		}
		if a.NewBase.Generated {
			t.Errorf("%s: generated 位不得 false→true（改写前快照下 promoteAllowed=false）", tc.name)
		}
	}

	// 非空转对照：手工等价路径（把 conflicts/P 拷成 ours 后重跑）在行 15 场景
	// 落行 14，**允许**翻位 —— 两条路的差异真实存在，本断言防止上面两条退化成
	// 「无论如何都是 false」的空转。
	base := plan.Snap{}
	ours := plan.Snap{"a.txt": cfMeta("h5", false)} // = theirs 的内容（cp 之后）
	theirs := plan.Snap{"a.txt": cfMeta("h5", true)}
	acts := plan.Plan(base, ours, theirs, nil, false)
	if len(acts) != 1 || acts[0].NewBase == nil {
		t.Fatalf("对照路径应产出单个带 NewBase 的动作：%+v", acts)
	}
	if !acts[0].NewBase.Generated {
		t.Errorf("对照路径（行 14）应允许 false→true，否则本测试为空转：%+v", acts[0])
	}
}

// TestAcceptableConflictsExtraction 锁住从 update.go 内联循环抽出的零行为变化：
// 冲突全集里「动作表里有 ∧ 非 ConflictKind」的路径按原序返回；ConflictKind 与
// 动作表里没有的路径排除。
func TestAcceptableConflictsExtraction(t *testing.T) {
	conflicted := []string{"m.txt", "b.bin", "c.txt", "gone.txt"}
	actions := []plan.Action{
		{Path: "b.bin", Kind: plan.ConflictBinary},
		{Path: "c.txt", Kind: plan.ConflictKind},
		{Path: "m.txt", Kind: plan.Merge},
	}
	want := []string{"m.txt", "b.bin"} // 输入顺序（生产侧输入本就有序）
	if got := acceptableConflicts(conflicted, actions); !slices.Equal(got, want) {
		t.Errorf("acceptableConflicts = %v，期望 %v", got, want)
	}
	if got := acceptableConflicts(nil, actions); got != nil {
		t.Errorf("空冲突集应返回 nil：%v", got)
	}
}

// cfFixture 是 promptConflicts 的夹具：四类冲突路径各一。可问者三条
// （a.txt = Merge 且判冲突、b.bin = ConflictBinary、d.txt = ConflictBothAdded），
// c.txt = ConflictKind（闭集外，永不提问）。
type cfFixture struct {
	actions        []plan.Action
	conflicted     map[string]bool
	conflicts      map[string][]byte
	base           plan.Snap
	ours           plan.Snap
	theirs         plan.Snap
	theirsRendered map[string][]byte
}

func newCFFixture() *cfFixture {
	return &cfFixture{
		actions: []plan.Action{
			{Path: "a.txt", Kind: plan.Merge, NewBase: cfPtr(cfMeta("h-base-a", false))},
			{Path: "b.bin", Kind: plan.ConflictBinary, NewBase: cfPtr(cfMeta("h-base-b", true))},
			{Path: "c.txt", Kind: plan.ConflictKind, NewBase: cfPtr(cfMeta("h-base-c", false))},
			{Path: "d.txt", Kind: plan.ConflictBothAdded},
		},
		conflicted: map[string]bool{"a.txt": true, "b.bin": true, "c.txt": true, "d.txt": true},
		conflicts: map[string][]byte{
			"a.txt": []byte("theirs-a\n"), "b.bin": []byte{0x00},
			"c.txt": []byte("theirs-c\n"), "d.txt": []byte("theirs-d\n"),
		},
		base: plan.Snap{
			"a.txt": cfMeta("h-base-a", false),
			"b.bin": cfMeta("h-base-b", true),
			"c.txt": cfMeta("h-base-c", false),
		},
		ours: plan.Snap{
			"a.txt": cfMeta("h-ours-a", false),
			"b.bin": cfMeta("h-ours-b", true),
			"c.txt": cfMeta("h-ours-c", false),
			"d.txt": cfMeta("h-ours-d", false),
		},
		theirs: plan.Snap{
			"a.txt": cfMeta("h-theirs-a", false),
			"b.bin": cfMeta("h-theirs-b", true),
			"c.txt": cfMeta("h-theirs-c", false),
			"d.txt": cfMeta("h-theirs-d", false),
		},
		theirsRendered: map[string][]byte{
			"a.txt": []byte("theirs-a\n"), "b.bin": []byte{0x00},
			"c.txt": []byte("theirs-c\n"), "d.txt": []byte("theirs-d\n"),
		},
	}
}

func (f *cfFixture) run(ps *promptSession) (int, error) {
	return promptConflicts(ps, f.actions, f.conflicted, f.conflicts, f.base, f.ours, f.theirs, f.theirsRendered)
}

// TestPromptConflictsFlow 覆盖 §19.4.4 的问答循环：y / n / 回车的组合、EOF 时
// C3 打印一次且剩余保持、C1 仅在可问数 > 0 时整块打印一次（空集合零输出）。
func TestPromptConflictsFlow(t *testing.T) {
	t.Run("y/n/回车组合", func(t *testing.T) {
		f := newCFFixture()
		ps, out := newTestSession("y\nn\n\n")
		taken, err := f.run(ps)
		if err != nil || taken != 1 {
			t.Fatalf("taken=%d err=%v，期望 1/nil（只 a.txt 回答 y）", taken, err)
		}
		// 改写集合：a.txt 变 WriteTheirs + C4；b.bin/d.txt 的「保留本地」= 零改写。
		if f.actions[0].Kind != plan.WriteTheirs || f.actions[0].Note != takeTheirsNote {
			t.Errorf("a.txt 应改写为 write + C4：%+v", f.actions[0])
		}
		if got, want := *f.actions[0].NewBase, cfMeta("h-theirs-a", false); got != want {
			t.Errorf("a.txt 的新 base = %+v，期望 %+v", got, want)
		}
		before := newCFFixture()
		for i, a := range f.actions[1:] {
			b := before.actions[i+1]
			// NewBase 逐值比较（nil 是行 15 的正当取值：d.txt 本就没有旧 base）。
			sameBase := (a.NewBase == nil) == (b.NewBase == nil) &&
				(a.NewBase == nil || *a.NewBase == *b.NewBase)
			if a.Kind != b.Kind || a.Note != b.Note || a.OverwriteGenerated != b.OverwriteGenerated || !sameBase {
				t.Errorf("%s 应零改写：%+v（原 %+v）", a.Path, a, b)
			}
		}
		// 集合同步：回答 y 的路径退出两个集合，其余原样。
		if f.conflicted["a.txt"] || f.conflicts["a.txt"] != nil {
			t.Error("a.txt 应退出 conflicted/conflicts")
		}
		for _, p := range []string{"b.bin", "c.txt", "d.txt"} {
			if !f.conflicted[p] || f.conflicts[p] == nil {
				t.Errorf("%s 保留本地时应留在两个集合里", p)
			}
		}
		// 输出：C1 整块一次（%d = 可问条数 3）、提问顺序 = 字节序、无 C3。
		got := out.String()
		if n := strings.Count(got, fmt.Sprintf(conflictHeader, 3)); n != 1 {
			t.Errorf("C1 应整块打印一次：%d 次\n%s", n, got)
		}
		wantPrompts := "a.txt（merge-conflict）改用模板版？[y/N] " +
			"b.bin（conflict-binary）改用模板版？[y/N] " +
			"d.txt（conflict-both-added）改用模板版？[y/N] "
		if !strings.HasSuffix(got, wantPrompts) {
			t.Errorf("提问序列（含 kindLabel 拼写与字节序）：\n得到 %q\n期望后缀 %q", got, wantPrompts)
		}
		if strings.Contains(got, "输入已结束（EOF）") {
			t.Errorf("无 EOF 时不得打印 C3：%q", got)
		}
	})

	t.Run("EOF", func(t *testing.T) {
		// 只给一行 y：a.txt 改写后输入耗尽 ⇒ C3 打印一次（%d = 剩余可问条数）、
		// 其余保持「保留本地」；EOF 锁存使第三条的提示不再打印。
		f := newCFFixture()
		ps, out := newTestSession("y\n")
		taken, err := f.run(ps)
		if err != nil || taken != 1 {
			t.Fatalf("taken=%d err=%v，期望 1/nil", taken, err)
		}
		got := out.String()
		const wantC3 = "输入已结束（EOF）：其余 2 条冲突按「保留本地」处理（§19.4）。\n"
		if !strings.HasSuffix(got, wantC3) {
			t.Errorf("C3 应逐字收尾（%d 含当前这条）：\n%s", 2, got)
		}
		if n := strings.Count(got, "输入已结束（EOF）"); n != 1 {
			t.Errorf("C3 只打印一次，实际 %d 次：%q", n, got)
		}
		if n := strings.Count(got, "改用模板版？[y/N] "); n != 2 {
			t.Errorf("提示只应打两次（第三次在锁存后），实际 %d 次：%q", n, got)
		}
		if f.conflicted["a.txt"] || f.conflicts["a.txt"] != nil {
			t.Error("EOF 前已答 y 的 a.txt 应保持改写结果（退出集合）")
		}
		for _, p := range []string{"b.bin", "c.txt", "d.txt"} {
			if !f.conflicted[p] || f.conflicts[p] == nil {
				t.Errorf("%s 应按保留本地保持原样", p)
			}
		}
	})

	t.Run("空可问集合零输出", func(t *testing.T) {
		// 唯一的冲突路径是 ConflictKind（闭集外）⇒ 不打印任何文本（C1 亦然）。
		// 备用输入行是探测器：实现若误问，这一行会被消费并让断言所见输出非空。
		f := newCFFixture()
		f.actions = []plan.Action{{Path: "c.txt", Kind: plan.ConflictKind}}
		f.conflicted = map[string]bool{"c.txt": true}
		f.conflicts = map[string][]byte{"c.txt": []byte("theirs-c\n")}
		ps, out := newTestSession("y\n")
		taken, err := f.run(ps)
		if err != nil || taken != 0 {
			t.Fatalf("taken=%d err=%v，期望 0/nil", taken, err)
		}
		if out.Len() != 0 {
			t.Errorf("可问集合为空时不得有任何输出：%q", out.String())
		}
		if f.actions[0].Kind != plan.ConflictKind || !f.conflicted["c.txt"] || f.conflicts["c.txt"] == nil {
			t.Errorf("不可问路径必须零改写、零集合变动：%+v", f)
		}

		// 对照：同一路径改判 ConflictBinary（可问）⇒ 必有 C1 与提示行 ——
		// 空输出来自闭集判定，而非夹具本身不产生输出。
		f2 := newCFFixture()
		f2.actions = []plan.Action{{Path: "c.txt", Kind: plan.ConflictBinary}}
		f2.conflicted = map[string]bool{"c.txt": true}
		f2.conflicts = map[string][]byte{"c.txt": []byte("theirs-c\n")}
		ps2, out2 := newTestSession("\n")
		if _, err := f2.run(ps2); err != nil {
			t.Fatalf("对照夹具不应出错：%v", err)
		}
		if !strings.Contains(out2.String(), "改用模板版？[y/N] ") {
			t.Fatalf("对照夹具应产生提问行：%q", out2.String())
		}
	})

	t.Run("读错误上抛", func(t *testing.T) {
		// 非 EOF 的读错误是普通错误（退 1）：带前缀上抛、保留根因，且不打印 C3。
		f := newCFFixture()
		ps := newPromptSession(errReader{}, &strings.Builder{})
		taken, err := f.run(ps)
		if taken != 0 || err == nil {
			t.Fatalf("taken=%d err=%v，期望 0/非 nil", taken, err)
		}
		if !strings.Contains(err.Error(), "冲突交互读取失败: ") {
			t.Errorf("错误应带前缀：%v", err)
		}
		if !errors.Is(err, errBoom) {
			t.Errorf("应保留根因（%%w）：%v", err)
		}
		var ee *ExitError
		if errors.As(err, &ee) {
			t.Errorf("读错误上抛是普通错误（退 1），不应是 ExitError：%v", err)
		}
	})
}
