package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
)

// contentsT 是整棵树的「相对路径 → 内容」快照（Noop 路径的字节不变断言用）。
func contentsT(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for rel := range readTreeT(t, root) {
		if rel == "" {
			continue
		}
		out[rel] = mustContent(t, root, rel)
	}
	return out
}

// TestDiffMatchesRealRun 是 §13 M2 ③：diff 的每条 Action 与实跑后的
// 磁盘树 + state.json 逐条对应（比较两次 Plan 的返回值没有意义）。
func TestDiffMatchesRealRun(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
		"b.txt":     "bee\n",
		"c.txt":     "see\n",
		"e.txt":     "x\ny\nz\n",
	})
	// 用户改 a 与 e（e 与模板改动重叠 → 冲突）；模板改 b、删 c、加 d、改 e
	writeFilesT(t, proj, map[string]string{
		"a.txt": "one local\n",
		"e.txt": "x\nY_ours\nz\n",
	})
	if err := os.Remove(filepath.Join(repo, "c.txt")); err != nil {
		t.Fatal(err)
	}
	commitT(t, repo, map[string]string{
		"b.txt": "bee v2\n",
		"d.txt": "dee\n",
		"e.txt": "x\nY_theirs\nz\n",
	}, "v1.1.0")

	// 1) diff：预测、不写盘
	beforeTree := readTreeT(t, proj)
	beforeC := contentsT(t, proj)
	stBefore := mustLoadState(t, proj)
	rep, err := runUpdate(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExitCode != CodeChanged {
		t.Fatalf("有非 Noop → 5: %+v", rep)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(beforeTree, after) {
		t.Fatal("diff 不得写任何文件")
	}

	// 2) 实跑
	real, err := runUpdate(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if real.ExitCode != CodePending {
		t.Fatalf("含冲突的实跑应退出 2: %+v", real)
	}

	// 3) 逐条对应
	st := mustLoadState(t, proj)
	byPath := map[string]plan.Action{}
	for _, a := range rep.Actions {
		byPath[a.Path] = a
	}
	for _, p := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		a, ok := byPath[p]
		if !ok {
			t.Fatalf("diff 报告里缺 %s 的 Action", p)
		}
		switch a.Kind {
		case plan.Noop:
			if got := mustContent(t, proj, p); got != beforeC[p] {
				t.Errorf("diff 判 Noop 但实跑后字节变了: %s", p)
			}
			if st.Files[p].Hash != a.NewBase.Hash {
				t.Errorf("Noop 路径的 state hash 应与 NewBase 一致: %s", p)
			}
		case plan.WriteTheirs:
			if a.NewBase == nil {
				t.Fatalf("WriteTheirs 行必须有 NewBase: %s", p)
			}
			got := fsutil.ContentHash([]byte(mustContent(t, proj, p)))
			if got != a.NewBase.Hash {
				t.Errorf("WriteTheirs 实跑后磁盘 hash ≠ NewBase.Hash: %s", p)
			}
			if st.Files[p].Hash != a.NewBase.Hash {
				t.Errorf("WriteTheirs 的 state hash ≠ NewBase.Hash: %s", p)
			}
		case plan.Delete:
			if _, err := os.Lstat(filepath.Join(proj, filepath.FromSlash(p))); !os.IsNotExist(err) {
				t.Errorf("diff 判 Delete 但文件还在: %s", p)
			}
			if _, ok := st.Files[p]; ok {
				t.Errorf("Delete 的路径应移出 state.files: %s", p)
			}
		case plan.Merge:
			// 本夹具里 e.txt 应被判冲突（merge-conflict）
			if !slices.Contains(rep.Conflicted, p) {
				t.Errorf("e.txt 应在 diff 的冲突清单里: %v", rep.Conflicted)
			}
			if got := mustContent(t, proj, p); got != beforeC[p] {
				t.Errorf("冲突路径磁盘字节必须保持 ours: %s", p)
			}
			if st.Files[p].Hash != stBefore.Files[p].Hash {
				t.Errorf("冲突路径 base 不得推进（files hash 应保持 diff 前的值）: %s", p)
			}
			if got := mustContent(t, proj, ".xinit/conflicts/"+p); got != "x\nY_theirs\nz\n" {
				t.Errorf("conflicts/%s = %q", p, got)
			}
		default:
			t.Fatalf("未预期的动作: %s %s", a.Kind, p)
		}
	}
}

// §17（取代 §7.5 的「只报告」）：模板把 old.txt 改名成 new.txt（内容不变），
// 本地改过 old.txt——dry-run 与实跑同产出 migrated 行；动作改写为 Delete+Merge，
// 实跑后 new.txt = 本地字节、old.txt 删除（§17.8 端到端，含 base.Bytes 接线）。
func TestDiffReportsSuspectedRename(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"old.txt":   "shared\ncontent\n",
	})
	writeFilesT(t, proj, map[string]string{"old.txt": "shared\ncontent\nlocal\n"})
	if err := os.Remove(filepath.Join(repo, "old.txt")); err != nil {
		t.Fatal(err)
	}
	commitT(t, repo, map[string]string{"new.txt": "shared\ncontent\n"}, "v1.1.0")

	want := []MigrationNote{{Old: "old.txt", New: "new.txt", Status: "migrated"}}
	rep, err := runUpdate(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Migrations, want) {
		t.Fatalf("dry-run Migrations = %+v", rep.Migrations)
	}
	byPath := map[string]plan.Action{}
	for _, a := range rep.Actions {
		byPath[a.Path] = a
	}
	if byPath["old.txt"].Kind != plan.Delete || byPath["new.txt"].Kind != plan.Merge || byPath["new.txt"].MigratedFrom != "old.txt" {
		t.Errorf("动作未按 §17.3 改写：old=%+v new=%+v", byPath["old.txt"], byPath["new.txt"])
	}

	real, err := runUpdate(UpdateOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(real.Migrations, want) {
		t.Errorf("实跑 Migrations = %+v", real.Migrations)
	}
	if got := mustContent(t, proj, "new.txt"); got != "shared\ncontent\nlocal\n" {
		t.Errorf("new.txt = %q，应为迁移后的本地字节", got)
	}
	if _, err := os.Stat(filepath.Join(proj, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("old.txt 应被删除：%v", err)
	}
}

// §7.5 的多对多歧义（两个同名内容的旧路径 ← 一个新增路径）不报。
func TestDiffRenameAmbiguityNotReported(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "same\n",
		"b.txt":     "same\n",
	})
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal(err)
	}
	commitT(t, repo, map[string]string{"c.txt": "same\n"}, "v1.1.0")

	rep, err := runUpdate(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Renames) != 0 {
		t.Errorf("一对多歧义必须不报（§7.5）: %+v", rep.Renames)
	}
}

func TestDiffNoChangesExitsZero(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")
	if _, err := runUpdate(UpdateOptions{ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}

	before := readTreeT(t, proj)
	if err := Diff(UpdateOptions{ProjectDir: proj}); err != nil {
		t.Fatalf("全 Noop 应退出 0: %v", err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("diff 不得写任何文件")
	}
}

func TestDiffBlockedShowsUnresolvedAndKeepsGoing(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "a\nb\nc\n",
	})
	writeFilesT(t, proj, map[string]string{"a.txt": "a\nB_ours\nc\n"})
	commitT(t, repo, map[string]string{"a.txt": "a\nB_theirs\nc\n"}, "v1.1.0")
	if _, err := runUpdate(UpdateOptions{ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}

	before := readTreeT(t, proj)
	rep, err := runUpdate(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Blocked, []string{"a.txt"}) {
		t.Errorf("Blocked = %v", rep.Blocked)
	}
	if rep.ExitCode != CodeChanged {
		t.Errorf("仍应给出 5（有非 Noop）: %+v", rep)
	}
	if len(rep.Actions) == 0 {
		t.Error("dry-run 下 Blocked 不阻止打全预测动作")
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("被阻塞的 diff 也不得写盘")
	}
}
