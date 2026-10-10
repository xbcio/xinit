package cli

import (
	"reflect"
	"testing"

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
