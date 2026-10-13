package update

// T7b/M3b C2 断言：管线门域（I7）。落点 internal/update（§4.3 规则 1）。

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/plan"
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

// TestChmodPredictionMatchesConclusion 钉 I23（§2.3 Chmodded / §22.6）：dry-run
// 的 rep.Chmodded（chmodPredictions，gate.go）与真跑 res.Chmodded
// （apply.Execute）集合相等——两径同源 execConclusion（decide.go），预测不
// 漂移；夹具为纯 exec 变更（R7 通道），两径都必须产生同一记账。
func TestChmodPredictionMatchesConclusion(t *testing.T) {
	dir := t.TempDir()
	writeFilesT(t, dir, map[string]string{"a.txt": "same\n"})
	changes := apply.Changes{
		Writes:    map[string][]byte{}, // 纯 exec 变更
		ExecBits:  map[string]bool{"a.txt": true},
		Conflicts: map[string][]byte{},
	}
	// 径 1：dry-run——经 report() 终局① 取 rep.Chmodded（预测径）。
	refusals, err := predictRefusals(dir, changes)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gate{Changes: changes, Forecast: refusals}
	rep := report(reportSessionT(t, true), reportTplT(), &Base{}, &Ours{},
		&Decision{Actions: []plan.Action{}, Merged: map[string][]byte{}, Conflicted: map[string]bool{}, Conflicts: map[string][]byte{}},
		g, nil, VerdictChanged, nil)
	// 径 2：真跑——apply.Execute 的 res.Chmodded。
	res, err := apply.Execute(dir, changes)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Chmodded, []string{"a.txt"}) {
		t.Fatalf("夹具前置：纯 exec 变更应产生 chmod 记账；got %v", res.Chmodded)
	}
	if !slices.Equal(rep.Chmodded, res.Chmodded) {
		t.Errorf("dry-run 预测 %v ≠ 真跑 %v（同源 execConclusion 不得漂移）", rep.Chmodded, res.Chmodded)
	}
	info, err := os.Stat(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("a.txt 权限 = %v，want 0755", info.Mode().Perm())
	}
}
