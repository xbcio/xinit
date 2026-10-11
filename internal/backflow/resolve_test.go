package backflow

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
)

// ---------------- 夹具 ----------------

// mainDocT 是 §20.10 用例 11 的主夹具（brief 逐字：rename / render+strip_suffix /
// vars mod=foo）。
//
// 其中 rename 规则是**惰性**的：rename 逐段替换（render.applyRenames 对每个
// '/' 段应用 replaceToken），带 '/' 的 from 永远匹配不到任何段；带 '/' 的 to
// 一旦命中会让正向管线直接报「替换值含 '/'」。探针实测：该规则下
// `oldpkg/x.go` 的 Dst 原样。rename 形状（§20.7 规则②）由 renameDocT 的段级
// 夹具单独覆盖 —— 这里保留 brief 的逐字取值，不为测试方便改写。
const mainDocT = `
manifest_version: 1
vars:
  - name: mod
    default: "foo"
rename:
  - from: "oldpkg/"
    to: "{{ .mod }}/"
render:
  - paths: ["src/{{ .mod }}/**/*.go.tmpl"]
    strip_suffix: ".tmpl"
`

// renameDocT 是段级 rename 夹具：to 渲染为静态记号 newpkg（不含变量、不含
// '/'），规则真正参与正向管线（`oldpkg/x.go` → `newpkg/x.go`）。
const renameDocT = `
manifest_version: 1
rename:
  - from: "oldpkg"
    to: "newpkg"
`

// stripDocT 是纯 strip_suffix 夹具：render paths ["**"] 下，任何不以
// strip_suffix 结尾的路径都会让正向管线报错 —— 模板自洽的前提就是树里的文件
// 全是 .tmpl。
const stripDocT = `
manifest_version: 1
render:
  - paths: ["**"]
    strip_suffix: ".tmpl"
`

// mkResolverT 造 Resolver：清单 → Compiled → EvalVars（与 T6 传 state.Vars
// 同源），TreePaths 是记录 commit 模板树路径集的桩（只被统一闸门读到）。
// maxCands <= 0 时走默认上限 64。
func mkResolverT(t *testing.T, doc string, tree []string, maxCands int) *Resolver {
	t.Helper()
	m, err := manifest.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse 清单: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("Compile 清单: %v", err)
	}
	v, err := render.EvalVars(c, nil)
	if err != nil {
		t.Fatalf("EvalVars: %v", err)
	}
	tp := make(map[string]bool, len(tree))
	for _, p := range tree {
		tp[p] = true
	}
	return &Resolver{Compiled: c, Vars: v, TreePaths: tp, MaxCandidates: maxCands}
}

// hitsOf 断言「唯一命中」：src 取期望值、cands 为空。
func hitsOf(t *testing.T, r *Resolver, dst, wantSrc string) {
	t.Helper()
	src, cands, err := r.ResolveNew(dst)
	if err != nil {
		t.Fatalf("ResolveNew(%q): %v", dst, err)
	}
	if src != wantSrc {
		t.Errorf("ResolveNew(%q) src = %q, want %q（cands = %v）", dst, src, wantSrc, cands)
	}
	if len(cands) != 0 {
		t.Errorf("唯一命中时 cands 应为空，实际 %v", cands)
	}
}

// ---------------- ResolveNew：四种形状 ----------------

func TestResolveNewPlaceholderShapeUnique(t *testing.T) {
	// brief ①：dst `foo/x.go` 的模板态路径是占位符形态 `{{ .mod }}/x.go`
	//（§20.7 规则③：段 `foo` 精确等于 V_mod）。
	//
	// TreePaths 桩含 `foo/x.go`：模板仓已占用该路径（模板专属文件，被 exclude
	// 挡在派生项目之外）⇒ 恒等候选（规则①）相撞拒。否则恒等候选同样重放命中，
	// 构成真实二义（见 TestResolveNewMultiHitAmbiguous）。
	hitsOf(t, mkResolverT(t, mainDocT, []string{"foo/x.go"}, 0),
		"foo/x.go", "{{ .mod }}/x.go")
}

func TestResolveNewPathVariableShapeUnique(t *testing.T) {
	// brief ②：dst `foo/deep/a.go` —— 唯一能重放回它的模板态路径是
	// `{{ .mod }}/deep/a.go`（规则③）。
	//
	// brief 原文期望的 Src `src/foo/deep/a.go` 在冻结管线下不成立（它的 Dst
	// 是自身；render 判定匹配的是**含字面 `{{ .mod }}` 的原 Src**，`src/` 前缀
	// 也保留在 Dst 里）。这里按重放实值断言。
	hitsOf(t, mkResolverT(t, mainDocT, []string{"foo/deep/a.go"}, 0),
		"foo/deep/a.go", "{{ .mod }}/deep/a.go")
}

func TestResolveNewRenameShapeUnique(t *testing.T) {
	// §20.7 规则②：T_r = 渲染后的 to = `newpkg`；段 `newpkg` 内含 T_r（字母
	// 边界：整段）→ 段内替换回 from = `oldpkg`。
	//
	// 桩含 `newpkg/x.go`（模板专属文件、被 exclude 挡在派生之外）⇒ 恒等候选
	// 相撞拒 ⇒ 唯一命中 `oldpkg/x.go`（正向核对：`oldpkg/x.go` --rename-->
	// `newpkg/x.go`）。
	hitsOf(t, mkResolverT(t, renameDocT, []string{"newpkg/x.go"}, 0),
		"newpkg/x.go", "oldpkg/x.go")
}

func TestResolveNewStripSuffixShapeUnique(t *testing.T) {
	// §20.7 规则④：直接枚举补后缀候选、由重放裁决（「先判 render 命中再补
	// 后缀」会次序循环——stage 5 判定的是加后缀前的原 Src）。
	//
	// dst `docs/readme.md`：恒等候选被正向管线**淘汰**（render paths ["**"]
	// 命中它，却不以 strip_suffix `.tmpl` 结尾 ⇒ 正向报错 ⇒ 候选丢弃，不是
	// 命中）；补后缀候选 `docs/readme.md.tmpl` 重放命中（正向：命中 render →
	// 剥离 `.tmpl` → `docs/readme.md`）。
	hitsOf(t, mkResolverT(t, stripDocT, nil, 0),
		"docs/readme.md", "docs/readme.md.tmpl")
}

func TestResolveNewCompositeShapeUnique(t *testing.T) {
	// brief ④：多变换叠加 —— dst `src/foo/a.go` 的模板态路径是
	// `src/{{ .mod }}/a.go.tmpl`（规则③变量 + 规则④补后缀叠加；正向：
	// vars → `src/foo/a.go.tmpl`；render 命中原 Src → 剥离 `.tmpl`）。
	//
	// 桩含 `src/foo/a.go`（恒等候选）与 `src/{{ .mod }}/a.go`（纯变量候选）：
	// 两种候选形态已被模板仓占用 ⇒ 相撞拒。桩只承担 §20.7 的闸门输入
	//（brief：「+ TreePaths 桩」），不要求是一棵自洽模板树快照——同一 Dst
	// 的两个前像本就不能共存于自洽树中。
	hitsOf(t, mkResolverT(t, mainDocT, []string{"src/foo/a.go", "src/{{ .mod }}/a.go"}, 0),
		"src/foo/a.go", "src/{{ .mod }}/a.go.tmpl")
}

func TestResolveNewPathVariableSubstringNoBoundary(t *testing.T) {
	// §20.7 规则③的两种形态：段**精确等于** V_v（见
	// TestResolveNewPlaceholderShapeUnique）与段**内含** V_v —— 后者没有
	// 「字母边界」要求（边界只属于规则②的 rename）。dst 段 `xfoo` 内含
	// `foo`：替换回 `{{ .mod }}` 后重放成立（vars → `xfoo`）。
	hitsOf(t, mkResolverT(t, mainDocT, []string{"xfoo/a.go"}, 0),
		"xfoo/a.go", "x{{ .mod }}/a.go")
}

func TestResolveNewRenameReverseHonorsLetterBoundary(t *testing.T) {
	// §20.7 规则②：段内含 T_r 的判定「字母边界同正向判据」。
	//
	// dst 段 `new-newxnew` 里 `new` 出现三处：位置 0 两侧是串首与 '-'（边界
	// 成立）；位置 4、8 分别紧贴字母 'x'（边界不成立，正是正向 replaceToken
	// 不会替换的形态）。反向替换只回写边界成立的那一处 ⇒ 候选
	// `ol-newxnew` 重放成立（`ol` 在串首替换回 `new`，`newxnew` 保持原样——
	// 紧贴字母的 `ol` 不会被正向改写）⇒ 命中列表必须含它。
	//
	// 若反向替换退化成「替换全部出现」（丢掉边界判据），这个候选根本不会
	// 产出（只会得到 `ol-olxol`，且它重放不成立）⇒ 本用例是边界判据的钉子。
	r := mkResolverT(t, `
manifest_version: 1
rename:
  - from: "ol"
    to: "new"
`, nil, 0)
	src, cands, err := r.ResolveNew("new-newxnew")
	if err != nil {
		t.Fatalf("ResolveNew: %v", err)
	}
	if src != "" {
		t.Errorf("恒等与边界回写候选都能重放命中 ⇒ 多命中，实际 src = %q", src)
	}
	want := []string{"new-newxnew", "ol-newxnew"}
	if !reflect.DeepEqual(cands, want) {
		t.Fatalf("cands = %v, want %v", cands, want)
	}
}

func TestResolveNewExtraVarsDeterministic(t *testing.T) {
	// Vars 里出现清单未声明的键（防御面：Resolver 是公开面，不重跑 EvalVars）
	// 时既不丢候选、也不让结果随 map 迭代序抖动：声明序在前、余键按字节序。
	r := mkResolverT(t, mainDocT, nil, 0)
	r.Vars = render.Vars{"mod": "foo", "zz_extra": "zed"}
	src, cands, err := r.ResolveNew("zed/x.go")
	if err != nil {
		t.Fatalf("ResolveNew: %v", err)
	}
	if src != "" {
		t.Errorf("多命中应为空 src，实际 %q", src)
	}
	want := []string{"zed/x.go", "{{ .zz_extra }}/x.go"}
	if !reflect.DeepEqual(cands, want) {
		t.Fatalf("cands = %v, want %v", cands, want)
	}
}

// ---------------- ResolveNew：零/多命中与确定性 ----------------

func TestResolveNewMultiHitAmbiguous(t *testing.T) {
	// 真实场景（无桩）：恒等形态与占位符形态都能重放回 dst ⇒ 多命中 ⇒
	// src 空 + cands 列出全部命中（字节序排序，确定性）。出口 =
	// `--map Dst=Src` 显式给出（§20.7 规则 3）。
	r := mkResolverT(t, mainDocT, nil, 0)
	src, cands, err := r.ResolveNew("foo/x.go")
	if err != nil {
		t.Fatalf("ResolveNew: %v", err)
	}
	if src != "" {
		t.Errorf("多命中时 src 应为空，实际 %q", src)
	}
	want := []string{"foo/x.go", "{{ .mod }}/x.go"}
	if !reflect.DeepEqual(cands, want) {
		t.Fatalf("cands = %v, want %v", cands, want)
	}

	// 确定性：同输入重跑逐字同结果。
	_, again, err := r.ResolveNew("foo/x.go")
	if err != nil {
		t.Fatalf("ResolveNew(重跑): %v", err)
	}
	if !reflect.DeepEqual(again, cands) {
		t.Errorf("重跑结果不稳定: %v → %v", cands, again)
	}

	// 复合形状的多命中：恒等 + 纯变量 + 变量×补后缀三种形态（全部重放命中）。
	_, cands3, err := r.ResolveNew("src/foo/a.go")
	if err != nil {
		t.Fatalf("ResolveNew(复合): %v", err)
	}
	want3 := []string{
		"src/foo/a.go",
		"src/{{ .mod }}/a.go",
		"src/{{ .mod }}/a.go.tmpl",
	}
	if !reflect.DeepEqual(cands3, want3) {
		t.Fatalf("复合 cands = %v, want %v", cands3, want3)
	}
}

func TestResolveNewZeroHit(t *testing.T) {
	// brief ⑤ 负对照的三种真实来源 —— 恒等候选在「无 from 记号、无 render
	// 剥离」时必然重放命中，零命中只会来自这三类：
	//   (a) 正向不认同：dst 含 rename 的 from 记号（正向必然改写它，任何候选
	//       的产物都不会长这样）；
	//   (b) 隐式排除面：xinit.yml 从不进入派生项目，它的改动也无模板态可言；
	//   (c) 相撞拒：候选形态已在模板仓被占用，且没有别的候选。
	for _, tc := range []struct {
		name string
		doc  string
		tree []string
		dst  string
	}{
		{"from 记号", renameDocT, nil, "oldpkg/x.go"},
		{"隐式排除", mainDocT, nil, "xinit.yml"},
		{"相撞拒", mainDocT, []string{"docs/readme.md"}, "docs/readme.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, cands, err := mkResolverT(t, tc.doc, tc.tree, 0).ResolveNew(tc.dst)
			if err != nil {
				t.Fatalf("ResolveNew(%q): %v", tc.dst, err)
			}
			if src != "" || len(cands) != 0 {
				t.Errorf("零命中应得空 src 与空 cands，实际 src=%q cands=%v", src, cands)
			}
		})
	}
}

func TestResolveNewCandidateOverflowAbandons(t *testing.T) {
	// §20.7 规则 1 末句：候选总数受上限约束，**超出即放弃自动解析**（防御
	// 面）。放弃 = 零命中（cands 空），出口仍是 `--map`。
	//
	// dst `foo/foo/x.go`：两个 `foo` 段各有 [foo, {{ .mod }}] 两个选项，
	// 再乘两个后缀选项 = 8 个候选 > 4 ⇒ 放弃。
	src, cands, err := mkResolverT(t, mainDocT, nil, 4).ResolveNew("foo/foo/x.go")
	if err != nil {
		t.Fatalf("ResolveNew: %v", err)
	}
	if src != "" || len(cands) != 0 {
		t.Errorf("超限应放弃（零命中），实际 src=%q cands=%v", src, cands)
	}

	// 对照：同 dst 在默认上限（MaxCandidates <= 0 ⇒ 64）内不放弃 —— 多命中
	// 列出候选，而不是静默零命中。
	src2, cands2, err := mkResolverT(t, mainDocT, nil, 0).ResolveNew("foo/foo/x.go")
	if err != nil {
		t.Fatalf("ResolveNew(默认上限): %v", err)
	}
	if src2 != "" || len(cands2) < 2 {
		t.Errorf("默认上限内应为多命中（cands ≥ 2），实际 src=%q cands=%v", src2, cands2)
	}
}

func TestResolveNewDedupesCandidates(t *testing.T) {
	// from == to 的 rename 规则：反向替换的产物与恒等候选逐字相同 ⇒ 命中不得
	// 重复列出（cands 是给用户看的，重复会伪装成二义）。
	r := mkResolverT(t, `
manifest_version: 1
rename:
  - from: "x"
    to: "x"
`, nil, 0)
	hitsOf(t, r, "x/y.go", "x/y.go")
}

func TestResolveNewInternalErrors(t *testing.T) {
	// error 仅内部错误：Compiled 缺席、rename.to 引用未声明变量（与正向管线
	// 同口径，在候选枚举前就报出来——静默零命中会把清单缺陷伪装成「无候选」）。
	if _, _, err := (&Resolver{}).ResolveNew("foo/x.go"); err == nil {
		t.Error("Compiled 为空应报错")
	}
	r := mkResolverT(t, `
manifest_version: 1
rename:
  - from: "xkitora"
    to: "{{ .nope }}"
`, nil, 0)
	if _, _, err := r.ResolveNew("x.go"); err == nil {
		t.Error("rename.to 引用未声明变量应报错")
	}
}

// ---------------- 重放：合成 FS 与真实 FS 对拍 ----------------

func TestResolveReplayMatchesRealFS(t *testing.T) {
	// 合成单文件 FS 的重放必须与真实目录 FS（t.TempDir + os.DirFS）的正向产物
	// 逐字一致——否则「合成 FS」自己就成了判据的一部分（虚拟目录少一层、路径
	// 段被 path.Join 归一之类都会在这里露出来）。
	r := mkResolverT(t, mainDocT, nil, 0)
	for _, p := range []string{
		"foo/x.go",
		"{{ .mod }}/x.go",
		"foo/deep/a.go",
		"{{ .mod }}/deep/a.go",
		"src/{{ .mod }}/a.go.tmpl",
		"src/{{ .mod }}/deep/a.go.tmpl",
		"oldpkg/x.go",
	} {
		dir := t.TempDir()
		writeFileT(t, dir, p, "x\n")
		entries, _, err := render.Paths(os.DirFS(dir), r.Compiled, r.Vars)
		if err != nil {
			t.Fatalf("真实 FS 重放 %q: %v", p, err)
		}
		if len(entries) != 1 {
			t.Fatalf("真实 FS 重放 %q: 条目数 = %d, want 1", p, len(entries))
		}
		dst := entries[0].Dst
		src, cands, err := r.ResolveNew(dst)
		if err != nil {
			t.Fatalf("ResolveNew(%q): %v", dst, err)
		}
		if src == p {
			continue
		}
		found := false
		for _, c := range cands {
			if c == p {
				found = true
			}
		}
		if !found {
			t.Errorf("合成 FS 重放漏掉 %q（真实 FS 的正向 Dst = %q；src=%q cands=%v）",
				p, dst, src, cands)
		}
	}
}

func TestSynthFSContract(t *testing.T) {
	// 合成 FS 是候选重放的底座（§20.8 允许自写小实现），它自己的 io/fs 契约
	// 也要被钉住：只含指定文件、目录按段前缀虚拟、非法与不存在的名字
	// fail-closed（ErrInvalid / ErrNotExist，供正向管线按普通 IO 错误处置）。
	f := synthFS{path: "a/b/c.go"}

	root, err := f.Open(".")
	if err != nil {
		t.Fatalf("Open(.): %v", err)
	}
	if fi, err := root.Stat(); err != nil || !fi.IsDir() {
		t.Fatalf("根应为目录: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	for _, dir := range []string{"a", "a/b"} {
		df, err := f.Open(dir)
		if err != nil {
			t.Fatalf("Open(%q): %v", dir, err)
		}
		if fi, err := df.Stat(); err != nil || !fi.IsDir() {
			t.Errorf("%q 应为目录: %v", dir, err)
		}
	}

	file, err := f.Open("a/b/c.go")
	if err != nil {
		t.Fatalf("Open(文件): %v", err)
	}
	fi, err := file.Stat()
	if err != nil {
		t.Fatalf("Stat(文件): %v", err)
	}
	if fi.IsDir() || fi.Name() != "c.go" || fi.Size() != int64(len(synthFileContent)) {
		t.Errorf("文件 FileInfo 错误: %+v", fi)
	}
	b, err := io.ReadAll(file)
	if err != nil || string(b) != string(synthFileContent) {
		t.Errorf("Read = (%q, %v)", b, err)
	}

	// 目录列表：逐层唯一子项，目录/文件标志正确。
	ents, err := f.ReadDir("a")
	if err != nil || len(ents) != 1 || ents[0].Name() != "b" || !ents[0].IsDir() {
		t.Errorf("ReadDir(a) = (%v, %v)", ents, err)
	}
	ents, err = f.ReadDir("a/b")
	if err != nil || len(ents) != 1 || ents[0].Name() != "c.go" || ents[0].IsDir() {
		t.Errorf("ReadDir(a/b) = (%v, %v)", ents, err)
	}

	// fail-closed 面。
	for _, name := range []string{"a/x", "a/b/c.go/deeper"} {
		if _, err := f.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q) 应为 ErrNotExist，实际 %v", name, err)
		}
		if _, err := f.ReadDir(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadDir(%q) 应为 ErrNotExist，实际 %v", name, err)
		}
	}
	if _, err := f.Open("../x"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("非法名字应为 ErrInvalid，实际 %v", err)
	}
	if _, err := (synthFS{}).Open("."); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("空 path 的合成 FS 任何 Open 都应 ErrNotExist，实际 %v", err)
	}
}

// ---------------- ApplyMap：统一闸门 ----------------

func TestApplyMapSkipsReplay(t *testing.T) {
	// §20.7 规则 4：`--map` 跳过自动解析直接采用（自动解析不必穷尽，它是权威
	// 出口），只过统一闸门 —— 不要求重放成立。
	r := mkResolverT(t, mainDocT, nil, 0)

	// 与自动解析命中不同的路径：`src/{{ .mod }}/x.go.tmpl` 正向剥离后是
	// `src/foo/x.go`，并不重放成 `foo/x.go` —— 仍被直接采用。
	got, err := r.ApplyMap("foo/x.go", "src/{{ .mod }}/x.go.tmpl")
	if err != nil {
		t.Fatalf("ApplyMap: %v", err)
	}
	if got != "src/{{ .mod }}/x.go.tmpl" {
		t.Errorf("ApplyMap = %q, want 逐字返回 Src", got)
	}

	// 常规覆盖：解析得出的形态。
	if got, err := r.ApplyMap("foo/x.go", "{{ .mod }}/x.go"); err != nil || got != "{{ .mod }}/x.go" {
		t.Errorf("ApplyMap(常规) = (%q, %v)", got, err)
	}
}

func TestApplyMapRejectsExistingTemplatePath(t *testing.T) {
	// §20.7 规则 4 的统一闸门：P 不得已存在于记录 commit 的模板树 —— 否则产出
	// 无法 am 的 new file 补丁（模板仓中该路径已存在 → "already exists" 必败）。
	r := mkResolverT(t, mainDocT, []string{"src/{{ .mod }}/a.go.tmpl"}, 0)
	got, err := r.ApplyMap("src/foo/a.go", "src/{{ .mod }}/a.go.tmpl")
	if err == nil {
		t.Fatal("P 已在模板树中应被拒")
	}
	if got != "" {
		t.Errorf("被拒时返回值应为空，实际 %q", got)
	}
	const verbatim = "该路径在模板中已存在，不是新增文件"
	if !strings.Contains(err.Error(), verbatim) {
		t.Errorf("闸门文案缺逐字句 %q: %v", verbatim, err)
	}
	if !strings.Contains(err.Error(), "src/{{ .mod }}/a.go.tmpl") {
		t.Errorf("闸门文案应点名冲突路径: %v", err)
	}
}

func TestApplyMapRejectsIllegalShape(t *testing.T) {
	// 统一闸门的路径合法性面：`..` 穿越 / 绝对路径 / 中间 `../..` / 空路径 /
	// 不规范路径全 fail-closed（T2 评审 O-4 的探针形态组；与 T2 `ResolveDomain`
	// 对 `--map` 值的 fail-closed 口径一致）。闸门在查树**之前** —— 非法形状
	// 不得靠「恰好不在树里」蒙混。
	r := mkResolverT(t, mainDocT, nil, 0)
	for _, src := range []string{"../escape", "/etc/passwd", "a/../../x", "", "a/./b", "a//b"} {
		got, err := r.ApplyMap("foo/x.go", src)
		if err == nil {
			t.Errorf("非法模板路径 %q 应被拒", src)
			continue
		}
		if got != "" {
			t.Errorf("被拒时返回值应为空（%q），实际 %q", src, got)
		}
		if strings.Contains(err.Error(), "已存在") {
			t.Errorf("非法形状 %q 不得与「已存在」闸门混用同一文案: %v", src, err)
		}
	}
}
