package update

// T7b/M3b C2 断言：管线门域（I7）。落点 internal/update（§4.3 规则 1）。

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// TestGateBlockedStopsBeforeDeliver 钉 I7（§2.3 Gate.Blocked / §10.2 第 4
// 步）：conflicts/ 遗留条目本轮仍判冲突 → Run 在 gate 终局提前返回——
// 不建 .xinit/backup/、不改 state.json、不写工作树，Report.Pending=true 且
// Verdict=Pending（「不执行任何写操作、不推进任何状态」）。
func TestGateBlockedStopsBeforeDeliver(t *testing.T) {
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "T\n"})
	dir := projectWithState(t,
		state.TemplateRef{Source: repo, Kind: "git", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "B\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "O\n"}) // ours 本地改过 → 三方皆异
	// base 快照（loadBase 从 .xinit/base/ 装载字节；元数据来自 state.files）。
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "B\n"})
	// 遗留未解决冲突：第 4 步检查的输入（本轮仍判冲突 → Blocked）。
	writeFilesT(t, filepath.Join(dir, state.DirName, "conflicts"), map[string]string{"a.txt": "T\n"})
	before, err := os.ReadFile(state.Path(dir))
	if err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Input{ProjectDir: dir, Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pending || rep.Verdict != VerdictPending {
		t.Errorf("blocked 终局：Pending=%v Verdict=%v，want true/VerdictPending", rep.Pending, rep.Verdict)
	}
	if !slices.Equal(rep.Blocked, []string{"a.txt"}) {
		t.Errorf("Blocked = %v，want [a.txt]", rep.Blocked)
	}
	if _, err := os.Stat(filepath.Join(dir, state.DirName, "backup")); !os.IsNotExist(err) {
		t.Errorf("blocked 提前返回不得建 .xinit/backup/（deliver 未执行）：stat err = %v", err)
	}
	after, err := os.ReadFile(state.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("blocked 提前返回不得改写 state.json")
	}
	if got := mustContent(t, dir, "a.txt"); got != "O\n" {
		t.Errorf("blocked 轮不得写工作树：a.txt = %q，want %q", got, "O\n")
	}
}
