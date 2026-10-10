package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/merge"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
)

// migMeta 是常规文本文件的快照元数据（配对与静态决策只看 Kind 与 Hash）。
func migMeta(hash string) plan.FileMeta {
	return plan.FileMeta{Kind: render.KindRegular, Hash: hash}
}

// migName 是迁移决策的可读名（仅测试失败信息用；类型本身不带 String）。
func migName(d migrationDecision) string {
	switch d {
	case migRename:
		return "migRename"
	case migSkip:
		return "migSkip"
	case migDirect:
		return "migDirect"
	case migMerge:
		return "migMerge"
	}
	return "unknown"
}

// TestDetectMigrationPairs 逐条对应 §17.1 的配对判据（M4 计划 Task 1 Step 1）。
func TestDetectMigrationPairs(t *testing.T) {
	tests := []struct {
		name    string
		actions []plan.Action
		base    plan.Snap
		theirs  plan.Snap
		files   map[string]state.FileState
		newSrc  map[string]string
		want    []migrationPair
	}{
		{
			// 1) Part A：路径变量变化导致目录位移（§3.4）；hash 不等 → 只有 Part A 能配。
			name: "PartA 同模板源配对（Dst 位移）",
			actions: []plan.Action{
				{Path: "old/a.txt", Kind: plan.Delete},
				{Path: "new/a.txt", Kind: plan.WriteTheirs},
			},
			base:   plan.Snap{"old/a.txt": migMeta("h-old")},
			theirs: plan.Snap{"new/a.txt": migMeta("h-new")},
			files:  map[string]state.FileState{"old/a.txt": {Src: "a.txt.tmpl", Hash: "h-old"}},
			newSrc: map[string]string{"new/a.txt": "a.txt.tmpl"},
			want:   []migrationPair{{Old: "old/a.txt", New: "new/a.txt", PartA: true}},
		},
		{
			// 2) Part B：模板物理改名、内容不变；Src 也变了 → 只有 Part B 能配。
			name: "PartB 同内容 hash 配对（旧候选 KeepWarned）",
			actions: []plan.Action{
				{Path: "old.go", Kind: plan.KeepWarned},
				{Path: "new.go", Kind: plan.WriteTheirs},
			},
			base:   plan.Snap{"old.go": migMeta("h1")},
			theirs: plan.Snap{"new.go": migMeta("h1")},
			files:  map[string]state.FileState{"old.go": {Src: "old.go", Hash: "h1"}},
			newSrc: map[string]string{"new.go": "new.go"},
			want:   []migrationPair{{Old: "old.go", New: "new.go"}},
		},
		{
			// 3) 多对多：两个同 hash 旧候选 ← 一个同 hash 新候选 → 全部不配（§17.1）。
			name: "PartB 两个同 hash 旧候选不配对",
			actions: []plan.Action{
				{Path: "a.txt", Kind: plan.Delete},
				{Path: "b.txt", Kind: plan.Delete},
				{Path: "c.txt", Kind: plan.WriteTheirs},
			},
			base: plan.Snap{
				"a.txt": migMeta("same"),
				"b.txt": migMeta("same"),
			},
			theirs: plan.Snap{"c.txt": migMeta("same")},
			files: map[string]state.FileState{
				"a.txt": {Src: "a.txt", Hash: "same"},
				"b.txt": {Src: "b.txt", Hash: "same"},
			},
			newSrc: map[string]string{"c.txt": "c.txt"},
			want:   nil,
		},
		{
			// 3) 多对多：一个旧候选 ← 两个同 hash 新候选 → 全部不配（§17.1）。
			name: "PartB 两个同 hash 新候选不配对",
			actions: []plan.Action{
				{Path: "a.txt", Kind: plan.Delete},
				{Path: "b.txt", Kind: plan.WriteTheirs},
				{Path: "c.txt", Kind: plan.WriteTheirs},
			},
			base:   plan.Snap{"a.txt": migMeta("same")},
			theirs: plan.Snap{"b.txt": migMeta("same"), "c.txt": migMeta("same")},
			files:  map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: "same"}},
			newSrc: map[string]string{"b.txt": "b.txt", "c.txt": "c.txt"},
			want:   nil,
		},
		{
			// 4) 同一 P/Q 同时满足 A/B：Part A 先配并占用，Part B 不重复输出。
			name: "同源且同内容：Part A 优先且只输出一条",
			actions: []plan.Action{
				{Path: "old.go", Kind: plan.Delete},
				{Path: "new.go", Kind: plan.WriteTheirs},
			},
			base:   plan.Snap{"old.go": migMeta("h1")},
			theirs: plan.Snap{"new.go": migMeta("h1")},
			files:  map[string]state.FileState{"old.go": {Src: "src/go.tmpl", Hash: "h1"}},
			newSrc: map[string]string{"new.go": "src/go.tmpl"},
			want:   []migrationPair{{Old: "old.go", New: "new.go", PartA: true}},
		},
		{
			// 5) 防御 1:1：同一 Src 出现在两个旧候选 → 不配对（§17.1 多对多口径）。
			name: "同一 Src 两个旧候选不配对",
			actions: []plan.Action{
				{Path: "x/old.go", Kind: plan.Delete},
				{Path: "y/old.go", Kind: plan.KeepWarned},
				{Path: "x/new.go", Kind: plan.WriteTheirs},
			},
			base: plan.Snap{
				"x/old.go": migMeta("h1"),
				"y/old.go": migMeta("h2"),
			},
			theirs: plan.Snap{"x/new.go": migMeta("h3")},
			files: map[string]state.FileState{
				"x/old.go": {Src: "shared.go.tmpl", Hash: "h1"},
				"y/old.go": {Src: "shared.go.tmpl", Hash: "h2"},
			},
			newSrc: map[string]string{"x/new.go": "shared.go.tmpl"},
			want:   nil,
		},
		{
			// 6) 防御：Q ∈ files（state 有记录）→ 不配（行 13 前件就要求 files✗）。
			name: "新候选已在 state 记录不配对",
			actions: []plan.Action{
				{Path: "old.go", Kind: plan.Delete},
				{Path: "new.go", Kind: plan.WriteTheirs},
			},
			base:   plan.Snap{"old.go": migMeta("h1")},
			theirs: plan.Snap{"new.go": migMeta("h1")},
			files: map[string]state.FileState{
				"old.go": {Src: "src.tmpl", Hash: "h1"},
				"new.go": {Src: "src.tmpl", Hash: "h1"},
			},
			newSrc: map[string]string{"new.go": "src.tmpl"},
			want:   nil,
		},
		{
			// 7) 输出按 Old 字节序排序；动作表故意乱序。
			name: "输出按 Old 字节序排序",
			actions: []plan.Action{
				{Path: "b_new.go", Kind: plan.WriteTheirs},
				{Path: "b_old.go", Kind: plan.Delete},
				{Path: "a_new.go", Kind: plan.WriteTheirs},
				{Path: "a_old.go", Kind: plan.KeepWarned},
			},
			base: plan.Snap{
				"b_old.go": migMeta("hb"),
				"a_old.go": migMeta("ha"),
			},
			theirs: plan.Snap{
				"b_new.go": migMeta("hb"),
				"a_new.go": migMeta("ha"),
			},
			files: map[string]state.FileState{
				"b_old.go": {Src: "b_old.go", Hash: "hb"},
				"a_old.go": {Src: "a_old.go", Hash: "ha"},
			},
			newSrc: map[string]string{"b_new.go": "b_new.go", "a_new.go": "a_new.go"},
			want: []migrationPair{
				{Old: "a_old.go", New: "a_new.go"},
				{Old: "b_old.go", New: "b_new.go"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// ours 不参与配对判据（§17.7：配对只用模板空间信息）——传 nil。
			got := detectMigrationPairs(tc.actions, tc.base, nil, tc.theirs, tc.files, tc.newSrc)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("配对 = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestDecideMigrationStatic 表驱动，逐行对应 §17.2 第 0/1/2 步的静态决策
// （理由文案与 §17.6 口径一致；fallback 不在本枚举——由合并阶段产生）。
func TestDecideMigrationStatic(t *testing.T) {
	tests := []struct {
		name       string
		pair       migrationPair
		base       plan.Snap
		ours       plan.Snap
		theirs     plan.Snap
		baseBytes  map[string][]byte
		want       migrationDecision
		wantReason string
	}{
		{
			name:       "第 0 步：ours 内容未改 → rename",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": migMeta("h1")},
			ours:       plan.Snap{"old.go": migMeta("h1")},
			theirs:     plan.Snap{"new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migRename,
			wantReason: "",
		},
		{
			name:       "第 0 步：仅 exec 位差异同样判 rename（R6，v1 语义保留）",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": migMeta("h1")},
			ours:       plan.Snap{"old.go": {Kind: render.KindRegular, Hash: "h1", Exec: true}},
			theirs:     plan.Snap{"new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migRename,
			wantReason: "",
		},
		{
			name:       "第 1 步：base 侧 generated → skip",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": {Kind: render.KindRegular, Hash: "h1", Generated: true}},
			ours:       plan.Snap{"old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migSkip,
			wantReason: "生成物路径不参与迁移",
		},
		{
			name:       "第 1 步：theirs 侧 generated → skip",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": migMeta("h1")},
			ours:       plan.Snap{"old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"new.go": {Kind: render.KindRegular, Hash: "h2", Generated: true}},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migSkip,
			wantReason: "生成物路径不参与迁移",
		},
		{
			name:       "第 1 步：PartB 空内容配对 → skip",
			pair:       migrationPair{Old: "old.go", New: "new.go"},
			base:       plan.Snap{"old.go": migMeta("h-empty")},
			ours:       plan.Snap{"old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"new.go": migMeta("h-empty")},
			baseBytes:  map[string][]byte{"old.go": {}},
			want:       migSkip,
			wantReason: "空内容配对不自动迁移",
		},
		{
			// N2：Part A 靠同 Src 判定，空占位被本地填写后位移正是旗舰场景。
			name:       "第 1 步：PartA 空内容不受限 → 直搬",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": migMeta("h-empty")},
			ours:       plan.Snap{"old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"new.go": migMeta("h-empty")},
			baseBytes:  map[string][]byte{"old.go": {}},
			want:       migDirect,
			wantReason: "",
		},
		{
			name:       "第 1 步：ours 侧类型非 regular → skip",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": migMeta("h1")},
			ours:       plan.Snap{"old.go": {Kind: render.KindSymlink, Hash: "h-ours"}},
			theirs:     plan.Snap{"new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migSkip,
			wantReason: "类型不符",
		},
		{
			name:       "第 1 步：base 侧类型非 regular → skip",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": {Kind: render.KindSymlink, Hash: "h1"}},
			ours:       plan.Snap{"old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migSkip,
			wantReason: "类型不符",
		},
		{
			// 名字与目录都变（且 hash 相等）→ 置信度 skip 必须排在直搬分流之前。
			name:       "第 1 步：PartB 名字与目录都变 → skip",
			pair:       migrationPair{Old: "a/old.go", New: "b/new.go"},
			base:       plan.Snap{"a/old.go": migMeta("h1")},
			ours:       plan.Snap{"a/old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"b/new.go": migMeta("h1")},
			baseBytes:  map[string][]byte{"a/old.go": []byte("x\n")},
			want:       migSkip,
			wantReason: "名字与目录都变，配对置信度不足",
		},
		{
			name:       "第 1 步：PartB 同 basename 跨目录放行（旗舰场景）",
			pair:       migrationPair{Old: "a/old.go", New: "b/old.go"},
			base:       plan.Snap{"a/old.go": migMeta("h1")},
			ours:       plan.Snap{"a/old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"b/old.go": migMeta("h1")},
			baseBytes:  map[string][]byte{"a/old.go": []byte("x\n")},
			want:       migDirect,
			wantReason: "",
		},
		{
			name:       "第 1 步：PartB 同目录改名放行（旗舰场景）",
			pair:       migrationPair{Old: "d/old.go", New: "d/new.go"},
			base:       plan.Snap{"d/old.go": migMeta("h1")},
			ours:       plan.Snap{"d/old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"d/new.go": migMeta("h1")},
			baseBytes:  map[string][]byte{"d/old.go": []byte("x\n")},
			want:       migDirect,
			wantReason: "",
		},
		{
			name:       "第 1 步：PartA 不受置信度约束 → 合并",
			pair:       migrationPair{Old: "a/old.go", New: "b/new.go", PartA: true},
			base:       plan.Snap{"a/old.go": migMeta("h1")},
			ours:       plan.Snap{"a/old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"b/new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"a/old.go": []byte("x\n")},
			want:       migMerge,
			wantReason: "",
		},
		{
			name:       "第 2 步：hash 相等 → 直搬（二进制同样适用）",
			pair:       migrationPair{Old: "old.bin", New: "new.bin"},
			base:       plan.Snap{"old.bin": {Kind: render.KindRegular, Hash: "hb", Binary: true}},
			ours:       plan.Snap{"old.bin": {Kind: render.KindRegular, Hash: "h-ours", Binary: true}},
			theirs:     plan.Snap{"new.bin": {Kind: render.KindRegular, Hash: "hb", Binary: true}},
			baseBytes:  map[string][]byte{"old.bin": {0x00, 0x01, 0x02}},
			want:       migDirect,
			wantReason: "",
		},
		{
			name:       "第 2 步：hash 不等 → 三方合并",
			pair:       migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base:       plan.Snap{"old.go": migMeta("h1")},
			ours:       plan.Snap{"old.go": migMeta("h-ours")},
			theirs:     plan.Snap{"new.go": migMeta("h2")},
			baseBytes:  map[string][]byte{"old.go": []byte("x\n")},
			want:       migMerge,
			wantReason: "",
		},
		{
			// Binary/MixedEOL 不进静态决策：前置过滤在合并阶段（§17.2 第 2 步）。
			name: "第 2 步：Binary/MixedEOL 位不影响分流（仍判合并）",
			pair: migrationPair{Old: "old.go", New: "new.go", PartA: true},
			base: plan.Snap{"old.go": {Kind: render.KindRegular, Hash: "h1", Binary: true, MixedEOL: true}},
			ours: plan.Snap{"old.go": {Kind: render.KindRegular, Hash: "h-ours", Binary: true, MixedEOL: true}},
			theirs: plan.Snap{
				"new.go": {Kind: render.KindRegular, Hash: "h2", Binary: true, MixedEOL: true},
			},
			baseBytes:  map[string][]byte{"old.go": {0x00, 0x01}},
			want:       migMerge,
			wantReason: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := decideMigration(tc.pair, tc.base, tc.ours, tc.theirs, tc.baseBytes)
			if got != tc.want {
				t.Errorf("决策 = %s, want %s", migName(got), migName(tc.want))
			}
			if reason != tc.wantReason {
				t.Errorf("理由 = %q, want %q", reason, tc.wantReason)
			}
			// Reason 口径（§17.6）：仅 fallback/skip 非空——本函数只产 skip。
			if got != migSkip && reason != "" {
				t.Errorf("非 skip 决策不得给理由，得到 %q", reason)
			}
		})
	}
}

// migFixture 是编排层单测的最小场景：P=old/a.txt（行 9 KeepWarned）、
// Q=new/a.txt（行 13 WriteTheirs），Part A 同 Src 配对。
type migFixture struct {
	actions        []plan.Action
	base           *baseSnapshot
	ours, theirs   plan.Snap
	oursBytes      map[string][]byte
	theirsRendered map[string][]byte
	newSrc         map[string]string
	st             *state.State
}

func newMigFixture(baseB, oursB, theirsB []byte) *migFixture {
	const p, q = "old/a.txt", "new/a.txt"
	meta := func(b []byte) plan.FileMeta {
		bin := fsutil.IsBinary(b)
		return plan.FileMeta{Kind: render.KindRegular, Binary: bin,
			MixedEOL: fsutil.DetectEOL(b) == fsutil.EOLMixed, Hash: fsutil.CompareHash(b, bin)}
	}
	tq := meta(theirsB)
	return &migFixture{
		actions: []plan.Action{
			{Path: q, Kind: plan.WriteTheirs, NewBase: &tq},
			{Path: p, Kind: plan.KeepWarned, Note: "v1 keep"},
		},
		base:           &baseSnapshot{Metas: map[string]plan.FileMeta{p: meta(baseB)}, Bytes: map[string][]byte{p: baseB}},
		ours:           plan.Snap{p: meta(oursB)},
		theirs:         plan.Snap{q: tq},
		oursBytes:      map[string][]byte{p: oursB},
		theirsRendered: map[string][]byte{q: theirsB},
		newSrc:         map[string]string{q: "a.txt.tmpl"},
		st:             &state.State{Files: map[string]state.FileState{p: {Src: "a.txt.tmpl"}}},
	}
}

func (f *migFixture) run(t *testing.T, dir string) ([]MigrationNote, map[string][]byte) {
	t.Helper()
	merged := map[string][]byte{}
	notes, err := classifyMigrations(dir, f.actions, f.base, f.ours, f.oursBytes, f.theirs,
		f.theirsRendered, f.newSrc, f.st, merged)
	if err != nil {
		t.Fatal(err)
	}
	return notes, merged
}

// assertMigrated 钉 §17.3 的 P/Q 改写形态与 merged[Q] 字节。
func assertMigrated(t *testing.T, f *migFixture, notes []MigrationNote, merged map[string][]byte, want []byte) {
	t.Helper()
	if len(notes) != 1 || notes[0] != (MigrationNote{Old: "old/a.txt", New: "new/a.txt", Status: "migrated"}) {
		t.Fatalf("notes = %+v", notes)
	}
	if got, ok := merged["new/a.txt"]; !ok || !bytes.Equal(got, want) {
		t.Fatalf("merged[Q] = %q, want %q", got, want)
	}
	q, p := f.actions[0], f.actions[1]
	if q.Kind != plan.Merge || q.MigratedFrom != "old/a.txt" || q.NewBase == nil ||
		*q.NewBase != f.theirs["new/a.txt"] || !strings.HasPrefix(q.Note, "路径迁移自 old/a.txt：本地改动已保留") {
		t.Fatalf("Q 动作 = %+v", q)
	}
	if p.Kind != plan.Delete || p.NewBase != nil || p.Note != "路径迁移：本地改动已迁移到 new/a.txt" {
		t.Fatalf("P 动作 = %+v", p)
	}
}

// assertUntouched：fallback/skip 不改写动作、不注入 merged。
func assertUntouched(t *testing.T, f *migFixture, notes []MigrationNote, merged map[string][]byte, status, reason string) {
	t.Helper()
	if len(notes) != 1 || notes[0].Status != status || notes[0].Reason != reason {
		t.Fatalf("notes = %+v, want %s/%q", notes, status, reason)
	}
	if len(merged) != 0 {
		t.Fatalf("merged 不应注入：%v", merged)
	}
	q, p := f.actions[0], f.actions[1]
	if q.Kind != plan.WriteTheirs || q.MigratedFrom != "" || q.Note != "" || q.NewBase == nil {
		t.Fatalf("Q 动作被改写：%+v", q)
	}
	if p.Kind != plan.KeepWarned || p.Note != "v1 keep" {
		t.Fatalf("P 动作被改写：%+v", p)
	}
}

func TestClassifyMigrationsDirectCRLF(t *testing.T) {
	base := []byte("a\r\nb\r\n")
	ours := []byte("a\r\nb\r\nlocal\r\n")
	f := newMigFixture(base, ours, base)
	notes, merged := f.run(t, t.TempDir())
	assertMigrated(t, f, notes, merged, ours) // 逐字节，CRLF 不归一
}

func TestClassifyMigrationsDirectBinary(t *testing.T) {
	base := []byte{0x00, 0x01, 0x02}
	ours := []byte{0x00, 0x01, 0x02, 0xff, 0x00}
	f := newMigFixture(base, ours, base)
	notes, merged := f.run(t, t.TempDir())
	assertMigrated(t, f, notes, merged, ours)
}

func TestClassifyMigrationsCleanMerge(t *testing.T) {
	base := []byte("1\r\n2\r\n3\r\n4\r\n5\r\n")
	ours := []byte("1 local\r\n2\r\n3\r\n4\r\n5\r\n")
	theirs := []byte("1\n2\n3\n4\n5 tpl\n")
	f := newMigFixture(base, ours, theirs)
	res, err := merge.ThreeWay(ours, base, theirs)
	if err != nil || res.Outcome != merge.Clean {
		t.Fatalf("前提：应干净合并，got %v %v", res.Outcome, err)
	}
	want := fsutil.FromLF(res.Bytes, fsutil.DetectEOL(ours))
	notes, merged := f.run(t, t.TempDir())
	assertMigrated(t, f, notes, merged, want)
	if !bytes.Equal(want, []byte("1 local\r\n2\r\n3\r\n4\r\n5 tpl\r\n")) {
		t.Fatalf("EOL 还原口径不符：%q", want)
	}
}

func TestClassifyMigrationsConflictFallback(t *testing.T) {
	f := newMigFixture([]byte("x\n"), []byte("ours\n"), []byte("theirs\n"))
	notes, merged := f.run(t, t.TempDir())
	assertUntouched(t, f, notes, merged, "fallback", "模板与本地都在改且相冲突")
}

func TestClassifyMigrationsMixedEOLFallback(t *testing.T) {
	f := newMigFixture([]byte("x\n"), []byte("a\r\nb\n"), []byte("y\n"))
	notes, merged := f.run(t, t.TempDir())
	assertUntouched(t, f, notes, merged, "fallback", "二进制或混合行尾不参与三方合并")
}

// 预检 skip（§17.4）：Q 的父目录里预造折叠等价的未跟踪文件 → 写 Q 会被拒。
func TestClassifyMigrationsPrecheckSkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new", "A.txt"), []byte("u"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := []byte("x\n")
	f := newMigFixture(base, []byte("local\n"), base)
	want, err := predictRefusals(dir, apply.Changes{Writes: map[string][]byte{"new/a.txt": nil}})
	if err != nil || len(want) != 1 {
		t.Fatalf("前提：Q 应被折叠拒绝，got %v %v", want, err)
	}
	notes, merged := f.run(t, dir)
	assertUntouched(t, f, notes, merged, "skip", want[0].Reason)
}

func TestClassifyMigrationsExec(t *testing.T) {
	base := []byte("x\n")
	for _, tc := range []struct {
		name                  string
		baseX, oursX, theirsX bool
		wantExec, wantWarn    bool
	}{
		{"本地 exec 差异 → 取 ours[P].Exec + Note", false, true, false, true, true},
		{"本地无差异 → 取 theirs[Q].Exec", false, false, true, true, false},
		{"本地无差异且 theirs 非 exec → 仍给结论 false（N3）", true, true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMigFixture(base, []byte("local\n"), base)
			setExec := func(s map[string]plan.FileMeta, k string, x bool) { m := s[k]; m.Exec = x; s[k] = m }
			setExec(f.base.Metas, "old/a.txt", tc.baseX)
			setExec(f.ours, "old/a.txt", tc.oursX)
			setExec(f.theirs, "new/a.txt", tc.theirsX)
			nb := f.theirs["new/a.txt"]
			f.actions[0].NewBase = &nb
			f.run(t, t.TempDir())
			q := f.actions[0]
			if got := strings.Contains(q.Note, "新路径保留本地 exec 位"); got != tc.wantWarn {
				t.Errorf("Note = %q, 警告 want %v", q.Note, tc.wantWarn)
			}
			if q.NewBase.Exec != tc.theirsX {
				t.Errorf("NewBase.Exec = %v, want theirs %v", q.NewBase.Exec, tc.theirsX)
			}
			got, ok := execConclusion(q, f.base.Metas, f.ours, f.theirs)
			if !ok || got != tc.wantExec {
				t.Errorf("execConclusion = %v,%v want %v,true", got, ok, tc.wantExec)
			}
		})
	}
}

// R9-M4①：Part A 与 Part B 混合，按 Old 全局排序。
func TestDetectMigrationPairsMixedOrder(t *testing.T) {
	actions := []plan.Action{
		{Path: "z/a.txt", Kind: plan.Delete},
		{Path: "y/a.txt", Kind: plan.WriteTheirs},
		{Path: "b.txt", Kind: plan.Delete},
		{Path: "c.txt", Kind: plan.WriteTheirs},
	}
	base := plan.Snap{"z/a.txt": migMeta("h-z"), "b.txt": migMeta("h-b")}
	theirs := plan.Snap{"y/a.txt": migMeta("h-y"), "c.txt": migMeta("h-b")}
	files := map[string]state.FileState{"z/a.txt": {Src: "a.tmpl"}, "b.txt": {Src: "b.tmpl"}}
	newSrc := map[string]string{"y/a.txt": "a.tmpl", "c.txt": "c.tmpl"}
	got := detectMigrationPairs(actions, base, nil, theirs, files, newSrc)
	want := []migrationPair{{Old: "b.txt", New: "c.txt"}, {Old: "z/a.txt", New: "y/a.txt", PartA: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// R9-M4②：baseBytes 缺键（区别于显式空切片）同样按空内容 skip。
func TestDecideMigrationBaseBytesMissingKey(t *testing.T) {
	p := migrationPair{Old: "old.go", New: "new.go"}
	got, reason := decideMigration(p, plan.Snap{"old.go": migMeta("h1")}, plan.Snap{"old.go": migMeta("h-o")},
		plan.Snap{"new.go": migMeta("h1")}, map[string][]byte{})
	if got != migSkip || reason != "空内容配对不自动迁移" {
		t.Fatalf("got %s %q", migName(got), reason)
	}
}

// R9-M4③：ours 非 nil、与 theirs 同 hash 但 base 不同 → 照常 Part B 配对
// （§17.7：配对准据与 ours 内容无关）。
func TestDetectMigrationPairsIgnoresOurs(t *testing.T) {
	actions := []plan.Action{{Path: "d/old.go", Kind: plan.KeepWarned}, {Path: "d/new.go", Kind: plan.WriteTheirs}}
	base := plan.Snap{"d/old.go": migMeta("h1")}
	ours := plan.Snap{"d/old.go": migMeta("h2")}
	theirs := plan.Snap{"d/new.go": migMeta("h1")}
	files := map[string]state.FileState{"d/old.go": {Src: "old.go"}}
	got := detectMigrationPairs(actions, base, ours, theirs, files, map[string]string{"d/new.go": "new.go"})
	want := []migrationPair{{Old: "d/old.go", New: "d/new.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	// 反证：ours 与 theirs 同 hash 而 base 不同 → 不配
	got = detectMigrationPairs(actions, plan.Snap{"d/old.go": migMeta("h0")}, plan.Snap{"d/old.go": migMeta("h1")},
		theirs, files, map[string]string{"d/new.go": "new.go"})
	if len(got) != 0 {
		t.Fatalf("ours==theirs≠base 不应配对，got %+v", got)
	}
}

// R10-M2：fallback 对的 P（KeepWarned，执行期不删）不得进后续对的预检删除集。
// 对 1：X.txt → z/X.txt 冲突 fallback；对 2：Y.txt → x.txt 直搬，但磁盘上的
// X.txt 与 x.txt 折叠等价且不会被删 → 对 2 必须 skip。
func TestClassifyMigrationsFallbackPNotInPrecheckSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "X.txt"), []byte("ours1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := func(h string) plan.FileMeta { return migMeta(h) }
	q1, q2 := m("h-t1"), m("h-b2")
	actions := []plan.Action{
		{Path: "X.txt", Kind: plan.KeepWarned},
		{Path: "Y.txt", Kind: plan.KeepWarned},
		{Path: "x.txt", Kind: plan.WriteTheirs, NewBase: &q2},
		{Path: "z/X.txt", Kind: plan.WriteTheirs, NewBase: &q1},
	}
	base := &baseSnapshot{
		Metas: map[string]plan.FileMeta{"X.txt": m("h-b1"), "Y.txt": m("h-b2")},
		Bytes: map[string][]byte{"X.txt": []byte("b1\n"), "Y.txt": []byte("b2\n")},
	}
	ours := plan.Snap{"X.txt": m("h-o1"), "Y.txt": m("h-o2")}
	theirs := plan.Snap{"z/X.txt": q1, "x.txt": q2}
	st := &state.State{Files: map[string]state.FileState{"X.txt": {Src: "s1"}, "Y.txt": {Src: "s2"}}}
	merged := map[string][]byte{}
	notes, err := classifyMigrations(dir, actions, base, ours,
		map[string][]byte{"X.txt": []byte("ours1\n"), "Y.txt": []byte("ours2\n")}, theirs,
		map[string][]byte{"z/X.txt": []byte("theirs1\n"), "x.txt": []byte("b2\n")},
		map[string]string{"z/X.txt": "s1", "x.txt": "s2"}, st, merged)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Status != "fallback" || notes[1].Status != "skip" {
		t.Fatalf("notes = %+v，want [fallback, skip]", notes)
	}
	if len(merged) != 0 || actions[2].Kind != plan.WriteTheirs {
		t.Fatalf("对 2 不应改写：merged=%v x.txt=%+v", merged, actions[2])
	}
}

// R10-M3：localExecDiff 判据（缺键 → false）。
func TestLocalExecDiff(t *testing.T) {
	x := plan.FileMeta{Kind: render.KindRegular, Exec: true}
	n := plan.FileMeta{Kind: render.KindRegular}
	for _, tc := range []struct {
		name       string
		base, ours plan.Snap
		want       bool
	}{
		{"有差异", plan.Snap{"p": n}, plan.Snap{"p": x}, true},
		{"无差异", plan.Snap{"p": x}, plan.Snap{"p": x}, false},
		{"ours 缺键", plan.Snap{"p": n}, plan.Snap{}, false},
		{"base 缺键", plan.Snap{}, plan.Snap{"p": x}, false},
	} {
		if got := localExecDiff("p", tc.base, tc.ours); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
