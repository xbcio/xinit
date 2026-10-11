package backflow

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/state"
)

// ---------------- 夹具 ----------------

// upstreamDocT 造一份只声明 upstream.paths 的清单（paths 为 nil 时整体缺键）。
func upstreamDocT(paths []string) string {
	if paths == nil {
		return "manifest_version: 1\n"
	}
	items := make([]string, len(paths))
	for i, p := range paths {
		items[i] = "'" + p + "'"
	}
	return "manifest_version: 1\nupstream:\n  paths: [" + strings.Join(items, ", ") + "]\n"
}

// mkCompiledT 解析一份清单并返回编译产物（域判定的输入面）。
func mkCompiledT(t *testing.T, doc string) *manifest.Compiled {
	t.Helper()
	m, err := manifest.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse 清单: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("Compile 清单: %v", err)
	}
	return c
}

// mkTreeT 造一个模板树（只被 §20.5 句 2 的存在性判据读到）。
func mkTreeT(t *testing.T, files ...string) fs.FS {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		writeFileT(t, dir, f, "x\n")
	}
	return os.DirFS(dir)
}

// stWithSrcT 造一个带 Dst→Src 映射的 state。
func stWithSrcT(t *testing.T, srcs map[string]string) *state.State {
	t.Helper()
	st := state.New("test")
	for dst, src := range srcs {
		st.Files[dst] = state.FileState{Src: src, Hash: "sha256/normlf-v1:0"}
	}
	return st
}

// ---------------- BuildItems ----------------

func TestBuildItemsAssemblesCandidates(t *testing.T) {
	st := stWithSrcT(t, map[string]string{"internal/a.go": "internal/a.go"})
	chs := []Change{
		{Status: "M", OldMode: "100644", NewMode: "100644", Path: "internal/a.go"},
		{Status: "A", OldMode: "000000", NewMode: "100644", Path: "internal/b.go"},
		{Status: "R100", OldMode: "100644", NewMode: "100644", Path: "internal/c.go", OldPath: "internal/d.go"},
	}
	prune, err := fsutil.CompileGlob([]string{"internal/c.go"})
	if err != nil {
		t.Fatal(err)
	}
	items := BuildItems(st, chs, prune)
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}

	// 受管：有 state 映射 ⇒ Src 取记录值、保持候选态。
	got := items[0]
	if got.Dst != "internal/a.go" || got.Src != "internal/a.go" ||
		got.Class != ClassManaged || got.Exit != ExitExport || got.ExitNote != "" {
		t.Errorf("受管项装配错误: %+v", got)
	}
	if got.Change.Status != "M" {
		t.Errorf("Change 应保留原始枚举记录: %+v", got.Change)
	}

	// 新增：无映射 ⇒ Src 为空、ClassNewFile（等 T3 路径解析）。
	got = items[1]
	if got.Dst != "internal/b.go" || got.Src != "" ||
		got.Class != ClassNewFile || got.Exit != ExitExport {
		t.Errorf("新增项装配错误: %+v", got)
	}

	// prune 命中（判据 = .xinit/prune.txt 展开出的 matcher）⇒ 装配即定出口。
	got = items[2]
	if got.Dst != "internal/c.go" || got.Exit != ExitPruned {
		t.Errorf("prune 项装配错误: %+v", got)
	}
	if !strings.Contains(got.ExitNote, "该路径已脱离管理，无法确定模板路径") {
		t.Errorf("prune 文案缺失: %q", got.ExitNote)
	}
	if !strings.Contains(got.ExitNote, "人工判断") {
		t.Errorf("prune 出口缺失: %q", got.ExitNote)
	}
}

func TestBuildItemsPruneBeatsManagedMapping(t *testing.T) {
	// prune.txt 是权威记录（§20.5 句 2）：在册路径命中 prune 也按「已脱离管理」
	// 处置 —— 用户把它写进 prune.txt 后（update 前）不该被回流导出。
	st := stWithSrcT(t, map[string]string{"docs/a.md": "docs/a.md"})
	prune, err := fsutil.CompileGlob([]string{"docs/**"})
	if err != nil {
		t.Fatal(err)
	}
	items := BuildItems(st, []Change{{Status: "M", Path: "docs/a.md"}}, prune)
	if items[0].Class != ClassManaged {
		t.Errorf("在册路径应仍是 ClassManaged: %+v", items[0])
	}
	if items[0].Exit != ExitPruned {
		t.Errorf("prune 命中应压倒受管候选态: %+v", items[0])
	}
}

// ---------------- ResolveDomain ----------------

func TestResolveDomainInDomain(t *testing.T) {
	c := mkCompiledT(t, upstreamDocT([]string{"internal/util/**"}))
	items := []Item{{
		Dst: "internal/util/a.go", Src: "internal/util/a.go",
		Class: ClassManaged, Exit: ExitExport,
	}}
	missing, err := ResolveDomain(items, mkTreeT(t, "internal/util/a.go"), c, nil, nil)
	if err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if missing {
		t.Fatal("清单声明了 upstream.paths，不应报缺键")
	}
	if items[0].Exit != ExitExport {
		t.Errorf("域内条目应保持候选态，got %v", items[0].Exit)
	}
	if items[0].ExitNote != "来源：清单 upstream.paths" {
		t.Errorf("来源标注 = %q", items[0].ExitNote)
	}
}

func TestResolveDomainOutOfDomain(t *testing.T) {
	c := mkCompiledT(t, upstreamDocT([]string{"internal/util/**"}))
	items := []Item{{
		Dst: "docs/adr/1.md", Src: "docs/adr/1.md",
		Class: ClassManaged, Exit: ExitExport,
	}}
	missing, err := ResolveDomain(items, mkTreeT(t, "docs/adr/1.md"), c, nil, nil)
	if err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if missing {
		t.Fatal("不应报缺键")
	}
	if items[0].Exit != ExitOutOfDomain {
		t.Fatalf("域外条目应定 ExitOutOfDomain，got %v", items[0].Exit)
	}
	for _, want := range []string{"不属于共享面（upstream.paths）", "重拆提交", "`--paths` 显式覆盖"} {
		if !strings.Contains(items[0].ExitNote, want) {
			t.Errorf("域外文案缺 %q: %q", want, items[0].ExitNote)
		}
	}
}

func TestResolveDomainUnionAndProvenance(t *testing.T) {
	c := mkCompiledT(t, upstreamDocT([]string{"a/**"}))
	filesys := mkTreeT(t, "a/x.go", "b/y.go")

	// 并集：清单（a/**）∪ 旗标（b/**），逐行注明来源。
	items := []Item{
		{Dst: "a/x.go", Src: "a/x.go", Class: ClassManaged, Exit: ExitExport},
		{Dst: "b/y.go", Src: "b/y.go", Class: ClassManaged, Exit: ExitExport},
	}
	if _, err := ResolveDomain(items, filesys, c, []string{"b/**"}, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Exit != ExitExport || items[0].ExitNote != "来源：清单 upstream.paths" {
		t.Errorf("清单来源标注错误: %+v", items[0])
	}
	if items[1].Exit != ExitExport || items[1].ExitNote != "来源：--paths" {
		t.Errorf("旗标来源标注错误: %+v", items[1])
	}

	// 两侧同时命中：并集不产生重复条目，来源标注合并。
	both := []Item{{Dst: "a/x.go", Src: "a/x.go", Class: ClassManaged, Exit: ExitExport}}
	if _, err := ResolveDomain(both, filesys, c, []string{"a/**"}, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if both[0].ExitNote != "来源：清单 upstream.paths、--paths" {
		t.Errorf("并集来源标注错误: %q", both[0].ExitNote)
	}
}

func TestResolveDomainMissingKey(t *testing.T) {
	c := mkCompiledT(t, upstreamDocT(nil))
	items := []Item{{
		Dst: "a/x.go", Src: "a/x.go",
		Class: ClassManaged, Exit: ExitExport,
	}}
	missing, err := ResolveDomain(items, mkTreeT(t, "a/x.go"), c, nil, nil)
	if err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if !missing {
		t.Fatal("清单缺 upstream.paths 键且未给 --paths ⇒ missingKey=true（调用方 rc=1）")
	}
	if items[0].Exit != ExitExport || items[0].ExitNote != "" {
		t.Errorf("缺键时不应改写条目（不静默判域外）: %+v", items[0])
	}
}

func TestResolveDomainPathsFlagCoversMissingKey(t *testing.T) {
	// 缺键的出口：「`--paths` 或升级模板」——给了 --paths 就不报缺键，
	// 域 = 旗标模式。
	c := mkCompiledT(t, upstreamDocT(nil))
	filesys := mkTreeT(t, "a/x.go", "b/y.go")
	items := []Item{
		{Dst: "a/x.go", Src: "a/x.go", Class: ClassManaged, Exit: ExitExport},
		{Dst: "b/y.go", Src: "b/y.go", Class: ClassManaged, Exit: ExitExport},
	}
	missing, err := ResolveDomain(items, filesys, c, []string{"a/**"}, nil)
	if err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if missing {
		t.Fatal("给了 --paths 不应报缺键")
	}
	if items[0].Exit != ExitExport || items[0].ExitNote != "来源：--paths" {
		t.Errorf("旗标域内条目错误: %+v", items[0])
	}
	if items[1].Exit != ExitOutOfDomain {
		t.Errorf("旗标域外条目错误: %+v", items[1])
	}
}

func TestResolveDomainPrunedItemUntouched(t *testing.T) {
	// prune 在 BuildItems 已定出口；ResolveDomain 只处置候选态（ExitExport）。
	st := stWithSrcT(t, map[string]string{"a/x.go": "a/x.go"})
	prune, err := fsutil.CompileGlob([]string{"a/x.go"})
	if err != nil {
		t.Fatal(err)
	}
	items := BuildItems(st, []Change{{Status: "M", Path: "a/x.go"}}, prune)
	c := mkCompiledT(t, upstreamDocT([]string{"a/**"}))
	if _, err := ResolveDomain(items, mkTreeT(t, "a/x.go"), c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Exit != ExitPruned || !strings.Contains(items[0].ExitNote, "该路径已脱离管理") {
		t.Errorf("已定出口的条目不得被二次改写: %+v", items[0])
	}
}

func TestResolveDomainSourceGoneForManaged(t *testing.T) {
	// 域模式给 `**`（能匹配该 Src）：来源消失判据仍须先行命中 —— 它比域判定
	// 更根本（没有模板原文就没有可翻译的 pre-image）。
	c := mkCompiledT(t, upstreamDocT([]string{"**"}))
	items := []Item{{
		Dst: "a/gone.go", Src: "a/gone.go",
		Class: ClassManaged, Exit: ExitExport,
	}}
	if _, err := ResolveDomain(items, mkTreeT(t, "a/kept.go"), c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Exit != ExitSourceGone {
		t.Fatalf("受管路径 Src 不在记录 commit 模板树 ⇒ ExitSourceGone，got %v", items[0].Exit)
	}
	for _, want := range []string{"该路径的模板来源已不存在", "模板已移除或改名", "`--map` 显式给出模板路径"} {
		if !strings.Contains(items[0].ExitNote, want) {
			t.Errorf("来源消失文案缺 %q: %q", want, items[0].ExitNote)
		}
	}
}

func TestResolveDomainNewFileNotBlockedBySourceGone(t *testing.T) {
	// §20.5 句 2：来源消失判据**仅作用于受管路径** —— 新增文件恒不在模板树，
	// 不得被该闸门拦截（解得 Src 后照常入域判定）。
	c := mkCompiledT(t, upstreamDocT([]string{"internal/**"}))
	items := []Item{{
		Dst: "internal/new.go", Src: "internal/new.go",
		Class: ClassNewFile, Exit: ExitExport,
	}}
	if _, err := ResolveDomain(items, mkTreeT(t, "internal/kept.go"), c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Exit != ExitExport {
		t.Fatalf("新增文件不应被来源消失闸门拦截，got %v", items[0].Exit)
	}
	if items[0].ExitNote != "来源：清单 upstream.paths" {
		t.Errorf("新增文件应照常入域判定: %q", items[0].ExitNote)
	}
}

func TestResolveDomainNewFileUnresolvedLeftToResolver(t *testing.T) {
	// ClassNewFile 且 Src 为空 = 待 §20.7 路径解析（T3）；ResolveDomain 不动它，
	// 便于调用方在 T3 解析后再调一次本函数完成域判定（本函数幂等）。
	c := mkCompiledT(t, upstreamDocT([]string{"**"}))
	items := []Item{{Dst: "brand/new.go", Class: ClassNewFile, Exit: ExitExport}}
	if _, err := ResolveDomain(items, mkTreeT(t), c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Exit != ExitExport || items[0].ExitNote != "" || items[0].Src != "" {
		t.Errorf("未解析的新增文件应原样留给 T3: %+v", items[0])
	}
}

func TestResolveDomainMapRescuesManagedPath(t *testing.T) {
	// §20.5 句 2 的文案点名 `--map`：受管路径的来源消失可由显式映射救回，
	// 覆盖后的 Src 仍过存在性判据与域判定。
	c := mkCompiledT(t, upstreamDocT([]string{"internal/**"}))
	items := []Item{{
		Dst: "internal/x.go", Src: "internal/gone.go",
		Class: ClassManaged, Exit: ExitExport,
	}}
	mp := map[string]string{"internal/x.go": "internal/real.go"}
	if _, err := ResolveDomain(items, mkTreeT(t, "internal/real.go"), c, nil, mp); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Src != "internal/real.go" {
		t.Errorf("--map 覆盖应写回 Item.Src: %q", items[0].Src)
	}
	if items[0].Exit != ExitExport || items[0].ExitNote != "来源：清单 upstream.paths" {
		t.Errorf("救援后的条目应入域判定: %+v", items[0])
	}
}

func TestResolveDomainMapOnNewFileNotApplied(t *testing.T) {
	// 新增文件的 `--map` 归 §20.7 规则 4（T3 的 ApplyMap，带「P 不得已存在于
	// 模板树」统一闸门）；本函数不越俎 —— 避免绕过闸门产出无法 am 的补丁。
	c := mkCompiledT(t, upstreamDocT([]string{"internal/**"}))
	items := []Item{{Dst: "internal/new.go", Class: ClassNewFile, Exit: ExitExport}}
	mp := map[string]string{"internal/new.go": "internal/new.go"}
	if _, err := ResolveDomain(items, mkTreeT(t, "internal/kept.go"), c, nil, mp); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Src != "" || items[0].Exit != ExitExport || items[0].ExitNote != "" {
		t.Errorf("新增文件的 --map 不应在本函数生效: %+v", items[0])
	}
}

func TestResolveDomainManagedWithoutSrc(t *testing.T) {
	// 受管却没有 Src 映射（state 损坏面）：按来源已消失 fail-closed 不导出。
	c := mkCompiledT(t, upstreamDocT([]string{"**"}))
	items := []Item{{Dst: "a.go", Class: ClassManaged, Exit: ExitExport}}
	if _, err := ResolveDomain(items, mkTreeT(t, "a.go"), c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain: %v", err)
	}
	if items[0].Exit != ExitSourceGone {
		t.Errorf("受管无 Src 应 fail-closed 为 ExitSourceGone: %+v", items[0])
	}
}

func TestResolveDomainIdempotent(t *testing.T) {
	// 建议的编排可两次调用本函数（T3 解析完新增文件后再判一次）；同参数重跑
	// 不得改写已定出口的条目。
	c := mkCompiledT(t, upstreamDocT([]string{"a/**"}))
	items := []Item{
		{Dst: "a/x.go", Src: "a/x.go", Class: ClassManaged, Exit: ExitExport},
		{Dst: "b/y.go", Src: "b/y.go", Class: ClassManaged, Exit: ExitExport},
	}
	filesys := mkTreeT(t, "a/x.go", "b/y.go")
	if _, err := ResolveDomain(items, filesys, c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain(1): %v", err)
	}
	first := items[1]
	if _, err := ResolveDomain(items, filesys, c, nil, nil); err != nil {
		t.Fatalf("ResolveDomain(2): %v", err)
	}
	if items[1] != first {
		t.Errorf("二次调用改写了条目: %+v → %+v", first, items[1])
	}
}

func TestResolveDomainBadPatternsError(t *testing.T) {
	// 清单侧模式非法（manifest.Compile 只做非空校验、不编译 glob）：消费点报错。
	c := mkCompiledT(t, upstreamDocT([]string{"a/["}))
	items := []Item{{Dst: "a/x.go", Src: "a/x.go", Class: ClassManaged, Exit: ExitExport}}
	if _, err := ResolveDomain(items, mkTreeT(t, "a/x.go"), c, nil, nil); err == nil {
		t.Fatal("清单 upstream.paths 里的非法 glob 应报错")
	} else if !strings.Contains(err.Error(), "upstream.paths") {
		t.Errorf("错误信息应点名 upstream.paths: %v", err)
	}

	c2 := mkCompiledT(t, upstreamDocT([]string{"a/**"}))
	if _, err := ResolveDomain(items, mkTreeT(t, "a/x.go"), c2, []string{"["}, nil); err == nil {
		t.Fatal("--paths 里的非法 glob 应报错")
	} else if !strings.Contains(err.Error(), "--paths") {
		t.Errorf("错误信息应点名 --paths: %v", err)
	}
}
