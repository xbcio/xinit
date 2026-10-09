package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

func TestStatusLocalChangesAndOrphans(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
		"b.txt":     "bee\n",
	})
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")
	writeFilesT(t, proj, map[string]string{"a.txt": "one local\n"})
	if err := os.Remove(filepath.Join(proj, "b.txt")); err != nil {
		t.Fatal(err)
	}

	rep, err := runStatus(StatusOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Modified, []string{"a.txt"}) {
		t.Errorf("Modified = %v", rep.Modified)
	}
	if !slices.Equal(rep.Missing, []string{"b.txt"}) {
		t.Errorf("Missing = %v", rep.Missing)
	}
	if len(rep.Generated) != 0 {
		t.Errorf("Generated = %v", rep.Generated)
	}
	if rep.EngineRecorded != rep.EngineCurrent {
		t.Errorf("engine_version 记录 %q ≠ 当前 %q", rep.EngineRecorded, rep.EngineCurrent)
	}
	if rep.PruneCount != 0 || len(rep.MixedEOL) != 0 {
		t.Errorf("prune=%d mixedEOL=%v", rep.PruneCount, rep.MixedEOL)
	}
	// 记录版本 v1.0.0、模板已到 v1.1.0：可用更新列表应含它（本地仓，无需联网）
	if !slices.Contains(rep.NewerTags, "v1.1.0") {
		t.Errorf("NewerTags = %v", rep.NewerTags)
	}
}

func TestStatusGeneratedSeparate(t *testing.T) {
	_, proj := initProjectT(t, map[string]string{
		"xinit.yml": genYAML,
		"gen.txt":   "g1\n",
	})
	writeFilesT(t, proj, map[string]string{"gen.txt": "g_hook\n"})

	rep, err := runStatus(StatusOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Generated, []string{"gen.txt"}) {
		t.Errorf("Generated = %v（generated 路径必须单列）", rep.Generated)
	}
	if len(rep.Modified) != 0 {
		t.Errorf("Modified 不得包含 generated 路径: %v", rep.Modified)
	}
}

func TestStatusFetchExitCode(t *testing.T) {
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	// 无更新：--fetch 也退 0
	if err := Status(StatusOptions{ProjectDir: proj, Fetch: true}); err != nil {
		t.Fatalf("无更新应退 0: %v", err)
	}

	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0")
	var ee *ExitError
	if err := Status(StatusOptions{ProjectDir: proj, Fetch: true}); !errors.As(err, &ee) || ee.Code != CodeChanged {
		t.Fatalf("--fetch 有更新应 ExitError{5}: %v", err)
	}
	// 不带 --fetch：恒 0
	if err := Status(StatusOptions{ProjectDir: proj}); err != nil {
		t.Fatalf("不带 --fetch 恒 0: %v", err)
	}
}

func TestStatusWritesNothing(t *testing.T) {
	_, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	before := readTreeT(t, proj)
	if _, err := runStatus(StatusOptions{ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("status 不得写任何文件（含 .git/info/exclude 与 .xinit/lock）")
	}
}

func TestStatusMissingState(t *testing.T) {
	if _, err := runStatus(StatusOptions{ProjectDir: t.TempDir()}); err == nil {
		t.Fatal("状态缺失应报错")
	}
}

func TestStatusPruneAndMixedEOL(t *testing.T) {
	_, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"m.txt":     "a\nb\r\nc\n", // 混合 EOL
	})
	// T1 只有 ReadPrune（prune.txt 由用户手工维护、xinit 只读）——测试直接写文件
	prunePath := filepath.Join(proj, state.DirName, "prune.txt")
	if err := os.WriteFile(prunePath, []byte("a.txt\nb/**.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := runStatus(StatusOptions{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if rep.PruneCount != 2 {
		t.Errorf("PruneCount = %d，want 2", rep.PruneCount)
	}
	if !slices.Equal(rep.MixedEOL, []string{"m.txt"}) {
		t.Errorf("MixedEOL = %v", rep.MixedEOL)
	}
}
