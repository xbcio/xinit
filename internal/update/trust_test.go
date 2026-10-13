package update

// T7b/M3b C2 断言：管线门域（I8）。落点 internal/update（§4.3 规则 1）。

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// TestTrustDeclineWritesNothing 钉 I8（§10.5 / §10.2 第 6 步）：信任门拒绝
// （rc4）→ 整树零写——不建 backup/、不改 state.json、不写工作树、不记录
// 信任（Consent 未发生）。
func TestTrustDeclineWritesNothing(t *testing.T) {
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": "manifest_version: 1\nhooks:\n  post_update: |\n    echo hi\n",
		"a.txt":     "new\n",
	})
	dir := projectWithState(t,
		state.TemplateRef{Source: repo, Kind: "git", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "old\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "old\n"}) // ours 未改 → 干净 WriteTheirs
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "old\n"})
	before, err := os.ReadFile(state.Path(dir))
	if err != nil {
		t.Fatal(err)
	}

	// 无输入 → ConfirmTrust 走 EOF 分支返回 false（§19.2.3 锁存语义）。
	ps, _ := newFakeInteractive("")
	rep, err := Run(Input{ProjectDir: dir, Revision: commit, Interactive: ps})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictNeedTrust || rep.Pending {
		t.Errorf("信任拒绝终局：Verdict=%v Pending=%v，want VerdictNeedTrust/false", rep.Verdict, rep.Pending)
	}
	if _, err := os.Stat(filepath.Join(dir, state.DirName, "backup")); !os.IsNotExist(err) {
		t.Errorf("rc4 零写不得建 .xinit/backup/：stat err = %v", err)
	}
	after, err := os.ReadFile(state.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("rc4 零写不得改写 state.json")
	}
	if got := mustContent(t, dir, "a.txt"); got != "old\n" {
		t.Errorf("rc4 零写不得写工作树：a.txt = %q，want %q", got, "old\n")
	}
	st2 := mustLoadState(t, dir)
	if len(st2.TrustedHooks) != 0 {
		t.Errorf("拒绝不得记录信任：TrustedHooks = %v", st2.TrustedHooks)
	}
}

// TestTrustNoticeRoutesToInjectedOut 钉 M4 io.Out 通道（trust 提示族，
// R-M4-4 可选加强）：rc4 轮的 PrintNotice/PrintAsk 必须落在注入的 Out 缓冲
// ——与 B2 的 Err 通道合成 IO 接缝的两半（11 点替换族：Out=提示、Err=警告）。
func TestTrustNoticeRoutesToInjectedOut(t *testing.T) {
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{
		"xinit.yml": "manifest_version: 1\nhooks:\n  post_update: |\n    echo hi\n",
		"a.txt":     "new\n",
	})
	dir := projectWithState(t,
		state.TemplateRef{Source: repo, Kind: "git", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "old\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "old\n"})
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "old\n"})

	var outBuf bytes.Buffer
	ps, _ := newFakeInteractive("") // 无答复 → ConfirmTrust 走 EOF 分支
	// Err 一并注入（O-5 park 清零）：警告通道不落真 stderr，IO 接缝两半都受控。
	rep, err := Run(Input{ProjectDir: dir, Revision: commit, Interactive: ps, IO: IO{Out: &outBuf, Err: io.Discard}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict != VerdictNeedTrust {
		t.Fatalf("夹具前提：rc4 轮；Verdict = %v", rep.Verdict)
	}
	got := outBuf.String()
	if !strings.Contains(got, "需要确认") {
		t.Errorf("注入的 Out 通道应收到信任提示（PrintNotice）；got %q", got)
	}
	if !strings.Contains(got, "退出码 4") {
		t.Errorf("PrintAsk 指引应同走注入通道；got %q", got)
	}
}
