package update

// T7b/M3b C2 断言：管线门域（I12 收窄 / I13）。落点 internal/update
// （§4.3 规则 1）。

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// deliverEnvT 造 deliver 直调的最小环境：项目内 a.txt="old\n"，唯一动作
// WriteTheirs a.txt（NewBase=新 hash），changes 写 a.txt="new\n"。
func deliverEnvT(t *testing.T) (dir string, s *session, b *Base, o *Ours, d *Decision, g *Gate) {
	t.Helper()
	dir = t.TempDir()
	writeFilesT(t, dir, map[string]string{"a.txt": "old\n"})
	oldMeta := plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("old\n"), false)}
	newMeta := plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("new\n"), false)}
	s = sessionT(dir, &state.State{Files: map[string]state.FileState{}}, "", false)
	b = &Base{Metas: plan.Snap{"a.txt": oldMeta}, Bytes: map[string][]byte{"a.txt": []byte("old\n")}}
	o = &Ours{Snap: plan.Snap{"a.txt": oldMeta}, Bytes: map[string][]byte{"a.txt": []byte("old\n")}}
	d = &Decision{
		Actions:    []plan.Action{{Path: "a.txt", Kind: plan.WriteTheirs, NewBase: &newMeta}},
		Merged:     map[string][]byte{},
		Conflicted: map[string]bool{},
		Conflicts:  map[string][]byte{},
	}
	g = &Gate{Changes: apply.Changes{
		Writes:    map[string][]byte{"a.txt": []byte("new\n")},
		ExecBits:  map[string]bool{},
		Conflicts: map[string][]byte{},
	}}
	return
}

// TestPendingPredicate 钉 I13（§2.3 Delivery.Pending / deliver.go:95）真值表：
// {conflicted=∅, refusals=∅} → false；任一非空 → true。
// 第三组用「中间段外指符号链接的删除路径」制造 §9.5 拒绝（与 Execute 同一
// 判定序列）——拒绝不中止其余路径，但把 Pending 置真。
func TestPendingPredicate(t *testing.T) {
	t.Run("两侧皆空 → false", func(t *testing.T) {
		_, s, b, o, d, g := deliverEnvT(t)
		w, err := deliver(s, b, o, d, g)
		if err != nil {
			t.Fatal(err)
		}
		if w.Pending {
			t.Errorf("Pending = true，want false（conflicted/refusals 皆空）")
		}
		if len(w.Result.Written) != 1 || w.Result.Written[0] != "a.txt" {
			t.Errorf("夹具前置：应真实写入 a.txt；Written = %v", w.Result.Written)
		}
	})

	t.Run("conflicted 非空 → true", func(t *testing.T) {
		_, s, b, o, d, g := deliverEnvT(t)
		d.Conflicted["a.txt"] = true
		w, err := deliver(s, b, o, d, g)
		if err != nil {
			t.Fatal(err)
		}
		if !w.Pending {
			t.Errorf("Pending = false，want true（conflicted 非空）")
		}
	})

	t.Run("refusals 非空 → true", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("符号链接需要 Windows 特权")
		}
		dir, s, b, o, d, g := deliverEnvT(t)
		if err := os.Symlink(t.TempDir(), filepath.Join(dir, "escape")); err != nil {
			t.Fatal(err)
		}
		g.Changes.Deletes = []string{"escape/x.txt"}
		d.Actions = append(d.Actions, plan.Action{Path: "escape/x.txt", Kind: plan.Delete})
		w, err := deliver(s, b, o, d, g)
		if err != nil {
			t.Fatal(err)
		}
		if len(w.Result.Refusals) != 1 || w.Result.Refusals[0].Path != "escape/x.txt" {
			t.Fatalf("夹具前置：删除逃逸应被拒；Refusals = %+v", w.Result.Refusals)
		}
		if !w.Pending {
			t.Errorf("Pending = false，want true（refusals 非空）")
		}
	})
}

// TestRestoreRefusedOrder 钉 I12 收窄（R-M3b-1；§8.5 折叠拒绝行 / §10.2 第 9
// 步）：被拒路径的 NewBase 必须在推进之前还原为「本轮开始时的 base 条目」——
// deliver 返回时动作表已还原（deliver.go:93 在函数内、P10 之前），advanceState
// 消费还原后的 NewBase 判定 advanced=false ⇒ state.files 不推进、base 不写
// theirs（若还原缺席，advanceState 会推进/报「拿不到模板原路径」——本断言
// 即该顺序语义的窄锁）。cli 侧 e2e TestUpdateRefusedPathKeepsOldBase 并存
// 不替代（R-M3b-1）。
func TestRestoreRefusedOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接需要 Windows 特权")
	}
	const p = "link/x.txt"
	dir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	oldHash := fsutil.CompareHash([]byte("old\n"), false)
	newHash := fsutil.CompareHash([]byte("new\n"), false)
	oldMeta := plan.FileMeta{Kind: render.KindRegular, Hash: oldHash}
	newMeta := plan.FileMeta{Kind: render.KindRegular, Hash: newHash}

	st := state.New(engineVersionT)
	st.Files = map[string]state.FileState{p: {Src: "x.tmpl", Hash: oldHash}}
	s := sessionT(dir, st, t.TempDir(), false)
	b := &Base{Metas: plan.Snap{p: oldMeta}, Bytes: map[string][]byte{p: []byte("old\n")}}
	o := &Ours{Snap: plan.Snap{}, Bytes: map[string][]byte{}}
	d := &Decision{
		Actions:    []plan.Action{{Path: p, Kind: plan.WriteTheirs, NewBase: &newMeta}},
		Merged:     map[string][]byte{},
		Conflicted: map[string]bool{},
		Conflicts:  map[string][]byte{},
	}
	g := &Gate{Changes: apply.Changes{
		Writes:    map[string][]byte{p: []byte("new\n")},
		ExecBits:  map[string]bool{},
		Conflicts: map[string][]byte{},
	}}

	w, err := deliver(s, b, o, d, g)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Result.Refusals) != 1 || w.Result.Refusals[0].Path != p {
		t.Fatalf("夹具前置：中间段符号链接的写入应被拒；Refusals = %+v", w.Result.Refusals)
	}
	act := findAction(d.Actions, p)
	if act.NewBase == nil || *act.NewBase != oldMeta {
		t.Fatalf("deliver 返回时被拒路径 NewBase 应已还原为旧 base 条目：%+v", act.NewBase)
	}

	// 模拟 P10（Run 在 deliver 之后调 advance）：还原后的 NewBase ==
	// baseSnap.Metas ⇒ advanced=false ⇒ files 保持旧 hash、base 保持旧字节。
	if err := advanceState(dir, st, b, d.Actions, nil,
		map[string]string{p: "x.tmpl"}, render.Vars{}, &template.Source{Kind: template.KindDir},
		true, t.TempDir(), engineVersionT, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if got := st.Files[p].Hash; got != oldHash {
		t.Errorf("state.files[%s].Hash = %s，want 旧值 %s（被拒路径不推进）", p, got, oldHash)
	}
	data, err := state.ReadBase(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Errorf("base/%s = %q，want 旧字节 %q（被拒路径不写 theirs）", p, data, "old\n")
	}
}

// ── T8/M4：IO/Now 接缝断言（B1/B2）────────────────────────────────────

// TestBackupStampSingleCall 钉 I25（§1.3 :123；M4 落地）：一轮里 s.now() 恰
// 调用一次——「同轮所有备份同目录」的机制保证；且 backup 目录名就是注入时钟
// 的格式化值（消费点证明：用的不是真实时间）。
func TestBackupStampSingleCall(t *testing.T) {
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "new\n"})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "old\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "old\n"}) // ours==base → 行 2（有备份对象）
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "old\n"})

	calls := 0
	fused := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rep, err := Run(Input{ProjectDir: dir, Revision: commit, Now: func() time.Time { calls++; return fused }})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("s.now() 调用次数 = %d，want 1（I25：同轮备份同戳）", calls)
	}
	if got := filepath.Base(rep.BackupDir); got != "20260102-030405" {
		t.Errorf("BackupDir 基名 = %q，want %q（必须消费注入时钟，而非真实时间）", got, "20260102-030405")
	}
}

// TestIOInjectionRoutesWarnings 钉 M4 IO 接缝（R-M4-4：warnIfDirty 路径定死）：
// 注入 IO.Err 后，真跑的非 git 仓警告（§10.2 第 5 步）必须落在注入缓冲——
// 证明 11 个替换点中的警告族确实走 s.io.Err（warnIfDirty 经 gate 的
// gate.go 调用点传通道）。夹具即 projectWithState 于 t.TempDir 手写
// state.json，天然非 git 仓。
func TestIOInjectionRoutesWarnings(t *testing.T) {
	isolateCacheT(t)
	repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "new\n"})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "old\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "old\n"})
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "old\n"})

	var errBuf bytes.Buffer
	if _, err := Run(Input{ProjectDir: dir, Revision: commit, IO: IO{Err: &errBuf}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "当前目录不是 git 仓库") {
		t.Errorf("注入的 Err 通道应收到非 git 仓警告；got %q", errBuf.String())
	}
}

// TestRenderWarningsRouteToInjectedErr 钉 template 侧警告族的注入通道（I-2
// final-review O-4 建议落地；备案终裁 T8-O4）：11 点替换中 template 侧警告族
// （template.go:159/174/188 三处）此前无行为断言、仅 grep 机械锁——本测试用
// 「零命中替换规则」在真跑 Run 里触发 template.go:188 的按规则汇总警告，断言
// 它经末位 errW 参数落在注入的 IO.Err 缓冲（改回 os.Stderr ⇒ 本测试红）。
func TestRenderWarningsRouteToInjectedErr(t *testing.T) {
	isolateCacheT(t)
	const zeroHitYAML = "manifest_version: 1\n" +
		"substitute:\n" +
		"  - from: 'zzz-never'\n" +
		"    to: 'yyy'\n" +
		"    paths: ['a.txt']\n"
	repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": zeroHitYAML, "a.txt": "new\n"})
	dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
		map[string]state.FileState{"a.txt": textState("a.txt", "old\n")})
	writeFilesT(t, dir, map[string]string{"a.txt": "old\n"})
	writeFilesT(t, filepath.Join(dir, state.DirName, "base"), map[string]string{"a.txt": "old\n"})

	var errBuf bytes.Buffer
	if _, err := Run(Input{ProjectDir: dir, Revision: commit, IO: IO{Err: &errBuf}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "替换规则") || !strings.Contains(errBuf.String(), "命中 0 次") {
		t.Errorf("注入的 Err 通道应收到零命中替换规则警告；got %q", errBuf.String())
	}
}
