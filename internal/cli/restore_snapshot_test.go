package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/renderstore"
	"github.com/xbcio/xinit/internal/state"
)

// ── 读侧恢复链（§21.6）测试夹具 ──────────────────────────────────────────
//
// 快照夹具统一用 renderstore.Write 直造（写侧挂点已由 snapshot_test.go 覆盖）；
// cacheRoot 一律显式传给 loadBase（不经 env），只有 initProjectT 这类真跑
// Init 的用例才需要 isolateCacheT——init 挂点（§21.5）会把快照写进
// DefaultCacheRoot。

// seedSnapshotT 用项目的持久化 state 造一份渲染快照（Dst → 渲染原文）。
// rendered 的键集就是清单键集（st.Files ∩ rendered，§21.4）——只传子集即可
// 制造「部分命中」。
func seedSnapshotT(t *testing.T, proj, cacheRoot string, rendered map[string][]byte) {
	t.Helper()
	if err := renderstore.Write(mustLoadState(t, proj), cacheRoot, rendered); err != nil {
		t.Fatalf("造渲染快照夹具: %v", err)
	}
}

// textState 是文本文件（非 binary）的 FileState（hash = 归一化 hash）。
func textState(src, content string) state.FileState {
	return state.FileState{Src: src, Hash: fsutil.CompareHash([]byte(content), false)}
}

// ① dir 源全命中：base 删净后由快照恢复——不依赖模板源（Source 不存在也能
// 跑通）、磁盘逐路径写回、Restored = need 序（= 排序后的路径集）。
func TestRestoreBaseFromSnapshotDir(t *testing.T) {
	cacheRoot := t.TempDir()
	dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
		map[string]state.FileState{
			"a.txt":     textState("a.txt", "hello\n"),
			"sub/b.txt": textState("sub/b.txt", "bee\n"),
		})
	seedSnapshotT(t, dir, cacheRoot, map[string][]byte{
		"a.txt":     []byte("hello\n"),
		"sub/b.txt": []byte("bee\n"),
	})

	snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
	if err != nil {
		t.Fatalf("快照全命中不得报错（也不得打开模板源）：%v", err)
	}
	if !slices.Equal(snap.Restored, []string{"a.txt", "sub/b.txt"}) {
		t.Errorf("Restored = %v，want 排序后路径集 [a.txt sub/b.txt]", snap.Restored)
	}
	if len(snap.Degraded) != 0 {
		t.Errorf("Degraded = %v，want 空", snap.Degraded)
	}
	for dst, want := range map[string]string{"a.txt": "hello\n", "sub/b.txt": "bee\n"} {
		if _, ok := snap.Metas[dst]; !ok {
			t.Errorf("Metas[%s] 缺失：命中路径须进内存快照（Plan 的 base 参数）", dst)
		}
		if got := string(snap.Bytes[dst]); got != want {
			t.Errorf("Bytes[%s] = %q，want %q", dst, got, want)
		}
		if got := mustContent(t, state.BaseDir(dir), dst); got != want {
			t.Errorf("base/%s = %q，want 写回 %q", dst, got, want)
		}
	}
}

// ② git 源全命中 + 旧 commit 真不可取（删 .git 重建为新历史）：无错 +
// 字节级对照——快照是唯一恢复来源（§21.6-1、判据 2）。
func TestRestoreBaseFromSnapshotCommitGone(t *testing.T) {
	cacheRoot := isolateCacheT(t) // init 挂点会把快照写到这里（§21.5）
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
	})
	if err := os.RemoveAll(state.BaseDir(proj)); err != nil {
		t.Fatal(err)
	}
	// 旧 commit 真不可取：删 .git 重建为新历史（新历史里没有旧 commit）。
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, repo, "init", "-q", "-b", "main")
	writeFilesT(t, repo, map[string]string{"xinit.yml": tmplYAML, "a.txt": "rewritten\n"})
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", "rewritten")

	snap, err := loadBase(proj, mustLoadState(t, proj), cacheRoot, false)
	if err != nil {
		t.Fatalf("快照全命中不得触碰模板源（旧 commit 已不可取）：%v", err)
	}
	if !slices.Equal(snap.Restored, []string{"a.txt"}) {
		t.Errorf("Restored = %v，want [a.txt]", snap.Restored)
	}
	if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
		t.Errorf("Bytes[a.txt] = %q，want %q", got, "hello\n")
	}
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "hello\n" {
		t.Errorf("base/a.txt = %q，want 写回旧快照字节 %q", got, "hello\n")
	}
}

// ③ dir 部分恢复：快照只含 2/3（第三个从未写）→ Restored 2 条、余项进
// Degraded、不再硬错（§21.6-3 dir 混合态；现状为硬错 + adopt，属明示行为修正）。
func TestRestoreBasePartialRecoveryDir(t *testing.T) {
	cacheRoot := t.TempDir()
	dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
		map[string]state.FileState{
			"a.txt": textState("a.txt", "A\n"),
			"b.txt": textState("b.txt", "B\n"),
			"c.txt": textState("c.txt", "C\n"),
		})
	seedSnapshotT(t, dir, cacheRoot, map[string][]byte{
		"a.txt": []byte("A\n"),
		"b.txt": []byte("B\n"),
	})

	snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
	if err != nil {
		t.Fatalf("部分恢复不得硬错（§21.6-3 dir 混合态）：%v", err)
	}
	if !slices.Equal(snap.Restored, []string{"a.txt", "b.txt"}) {
		t.Errorf("Restored = %v，want [a.txt b.txt]", snap.Restored)
	}
	if !slices.Equal(snap.Degraded, []string{"c.txt"}) {
		t.Errorf("Degraded = %v，want [c.txt]（余项走 §5.2 降级语义）", snap.Degraded)
	}
	if got := mustContent(t, state.BaseDir(dir), "a.txt"); got != "A\n" {
		t.Errorf("base/a.txt = %q，want 命中路径已写回", got)
	}
	if _, err := os.Lstat(filepath.Join(state.BaseDir(dir), "c.txt")); !os.IsNotExist(err) {
		t.Errorf("降级路径不得写回 base: %v", err)
	}
}

// ④ readOnly（dry-run/diff）：命中照进内存快照，但零写——base/ 目录整体
// 不存在（连目录都不建，评审 I-1 的延伸）。
func TestRestoreBaseReadOnlyNoWrite(t *testing.T) {
	cacheRoot := t.TempDir()
	dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
		map[string]state.FileState{"a.txt": textState("a.txt", "hello\n")})
	seedSnapshotT(t, dir, cacheRoot, map[string][]byte{"a.txt": []byte("hello\n")})

	snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(snap.Restored, []string{"a.txt"}) {
		t.Errorf("Restored = %v，want [a.txt]（只读同样进内存）", snap.Restored)
	}
	if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
		t.Errorf("Bytes[a.txt] = %q，want 内存恢复照常", got)
	}
	if _, err := os.Stat(state.BaseDir(dir)); !os.IsNotExist(err) {
		t.Errorf("readOnly 恢复不得落盘（base/ 应整体不存在）: %v", err)
	}
}

// ⑤ CRLF 孪生负例（§21.4 raw 门）：tree 文件 EOL 翻转 → raw sha256 变、
// 归一化 hash 不变——必须 miss（否则会把 CRLF 字节写回 base）。git 源下该
// 路径走既有重渲染链，字节仍正确。
func TestRestoreBaseSnapshotCRLFTwinMisses(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	_, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
	})
	if err := os.RemoveAll(state.BaseDir(proj)); err != nil {
		t.Fatal(err)
	}
	// 快照 tree/a.txt 改写成 EOL 孪生：raw 变、归一化 hash 不变——只有
	// raw 门能拦下（记录门 CompareHash 归一化后仍相等）。
	entry := renderEntryT(t, cacheRoot)
	if err := os.WriteFile(filepath.Join(entry, "tree", "a.txt"), []byte("hello\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loadBase(proj, mustLoadState(t, proj), cacheRoot, false)
	if err != nil {
		t.Fatalf("git 源余项应走既有重渲染链：%v", err)
	}
	if len(snap.Restored) != 0 {
		t.Errorf("CRLF 孪生必须 miss（raw 门）：Restored = %v", snap.Restored)
	}
	if len(snap.Degraded) != 0 {
		t.Errorf("重渲染字节正确，不得降级：Degraded = %v", snap.Degraded)
	}
	if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
		t.Errorf("重渲染后 Bytes[a.txt] = %q，want %q", got, "hello\n")
	}
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "hello\n" {
		t.Errorf("base/a.txt = %q，want 重渲染写回 %q（不得写回 CRLF 孪生）", got, "hello\n")
	}
}

// ⑥ 读侧零建根（§21.6-4）：Fetch 只 os.ReadFile/os.Open，绝不创建缓存目录。
// 三个变体（dir 硬错 / git 照旧 / 健康态）跑完 cacheRoot 都必须仍不存在。
func TestRestoreBaseNoCacheRootCreated(t *testing.T) {
	assertNoRoot := func(t *testing.T, cacheRoot string) {
		t.Helper()
		if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
			t.Errorf("读侧零建根：cacheRoot %s 必须仍不存在（stat err = %v）", cacheRoot, err)
		}
	}

	t.Run("dir源零命中硬错", func(t *testing.T) {
		cacheRoot := filepath.Join(t.TempDir(), "never-created")
		dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
			map[string]state.FileState{"a.txt": textState("a.txt", "hello\n")})

		_, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
		if err == nil || !strings.Contains(err.Error(), "adopt") {
			t.Fatalf("dir 零命中应保持现行硬错（含 adopt 指引）：%v", err)
		}
		assertNoRoot(t, cacheRoot)
	})

	t.Run("git源重渲染照旧", func(t *testing.T) {
		cacheRoot := filepath.Join(t.TempDir(), "never-created")
		repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "hello\n"})
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{"a.txt": textState("a.txt", "hello\n")})

		snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
		if err != nil {
			t.Fatalf("git 源零命中应走现行重渲染链：%v", err)
		}
		if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
			t.Errorf("重渲染 Bytes[a.txt] = %q，want %q", got, "hello\n")
		}
		if len(snap.Restored) != 0 {
			t.Errorf("无快照可命中：Restored = %v，want 空", snap.Restored)
		}
		assertNoRoot(t, cacheRoot)
	})

	t.Run("健康态不触缓存", func(t *testing.T) {
		cacheRoot := filepath.Join(t.TempDir(), "never-created")
		dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
			map[string]state.FileState{"a.txt": textState("a.txt", "hello\n")})
		if err := os.MkdirAll(state.BaseDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state.BaseDir(dir), "a.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
		if err != nil {
			t.Fatalf("健康 base 不应触发恢复：%v", err)
		}
		if _, ok := snap.Metas["a.txt"]; !ok {
			t.Error("健康路径应进 Metas")
		}
		assertNoRoot(t, cacheRoot)
	})
}

// ⑦ 缓存不可读 → 静默 miss，行为与现状逐字同（判据 8/16）。
// 注意权限值：brief 原文的 0500 目录**可遍历**（r-x），读取照常成功——去 x
// 后的 0400 才真正不可遍历（T4-0 裁定，与 T2-M1 同构；守卫同 T2 的
// renderstore/fetch_test.go:194-200）。
func TestRestoreBaseUnreadableCacheMisses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视权限位，无法制造不可读缓存")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 权限位不生效（R9：Windows 运行期 park）")
	}
	// sealEntryUnreadableT 把 <cacheRoot>/render 去 r+x（0400）并注册恢复，
	// 让 t.TempDir 的清理仍能删除。
	sealEntryUnreadableT := func(t *testing.T, cacheRoot string) {
		t.Helper()
		renderDir := filepath.Join(cacheRoot, "render")
		if err := os.Chmod(renderDir, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(renderDir, 0o755) })
	}

	t.Run("dir源硬错文案逐字不变", func(t *testing.T) {
		cacheRoot := t.TempDir()
		dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
			map[string]state.FileState{"a.txt": textState("a.txt", "hello\n")})
		seedSnapshotT(t, dir, cacheRoot, map[string][]byte{"a.txt": []byte("hello\n")})
		sealEntryUnreadableT(t, cacheRoot)

		// 对照组：无快照（cacheRoot 不存在）跑同一 state——两者错误文本必须逐字同。
		ctrl := filepath.Join(t.TempDir(), "no-cache")
		_, ctrlErr := loadBase(dir, mustLoadState(t, dir), ctrl, false)
		_, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
		if err == nil || ctrlErr == nil {
			t.Fatalf("dir 零命中应硬错：err=%v ctrlErr=%v", err, ctrlErr)
		}
		if err.Error() != ctrlErr.Error() {
			t.Errorf("不可读缓存必须与无快照逐字同：\n got %q\nwant %q", err.Error(), ctrlErr.Error())
		}
		if !strings.Contains(err.Error(), "adopt") {
			t.Errorf("硬错文案应含 adopt 指引：%v", err)
		}
	})

	t.Run("git源重渲染照旧", func(t *testing.T) {
		cacheRoot := t.TempDir()
		repo, commit := mkTemplateRepoT(t, map[string]string{"xinit.yml": tmplYAML, "a.txt": "hello\n"})
		dir := projectWithState(t, state.TemplateRef{Source: repo, Kind: "git", Revision: "main", Commit: commit},
			map[string]state.FileState{"a.txt": textState("a.txt", "hello\n")})
		seedSnapshotT(t, dir, cacheRoot, map[string][]byte{"a.txt": []byte("hello\n")})
		sealEntryUnreadableT(t, cacheRoot)

		snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
		if err != nil {
			t.Fatalf("缓存不可读应静默 miss、走既有重渲染链：%v", err)
		}
		if len(snap.Restored) != 0 {
			t.Errorf("不可读缓存不得命中：Restored = %v", snap.Restored)
		}
		if got := string(snap.Bytes["a.txt"]); got != "hello\n" {
			t.Errorf("重渲染 Bytes[a.txt] = %q，want %q", got, "hello\n")
		}
	})
}

// ⑧ 判据 10：git 混合态 + 旧 commit 不可取 → 硬错（rc=1 半写回）——错误
// 文本尾附恢复行（「现行硬错文案 + 恢复行」）；已命中路径已写回磁盘。
func TestRestoreBaseGitMixedErrorCarriesRecoveryLine(t *testing.T) {
	cacheRoot := isolateCacheT(t)
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "hello\n",
		"b.txt":     "world\n",
	})
	// 快照只留 a.txt（b.txt 从未写入 → 整条覆写轮转成单条，§21.5）
	seedSnapshotT(t, proj, cacheRoot, map[string][]byte{"a.txt": []byte("hello\n")})
	if err := os.RemoveAll(state.BaseDir(proj)); err != nil {
		t.Fatal(err)
	}
	// 旧 commit 真不可取（同 ② 手法）
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	runGitT(t, repo, "init", "-q", "-b", "main")
	writeFilesT(t, repo, map[string]string{"xinit.yml": tmplYAML, "a.txt": "rewritten\n", "b.txt": "rewritten\n"})
	runGitT(t, repo, "add", "-A")
	runGitT(t, repo, "commit", "-q", "-m", "rewritten")

	_, err := loadBase(proj, mustLoadState(t, proj), cacheRoot, false)
	if err == nil {
		t.Fatal("余项走重渲染链且旧 commit 不可取 → 应硬错（rc=1 半写回）")
	}
	if !strings.Contains(err.Error(), "已不可取到") {
		t.Errorf("错误应保持现行「已不可取到」逐字句：%v", err)
	}
	if !strings.Contains(err.Error(), "adopt") {
		t.Errorf("错误应保持 adopt 指引：%v", err)
	}
	want := "base 恢复（来自渲染快照，不依赖模板源，§5.2、§21）：1 条"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("错误应尾附恢复行（判据 10）：\n got %q\nwant 含 %q", err.Error(), want)
	}
	if !strings.HasSuffix(err.Error(), want) {
		t.Errorf("恢复行应在硬错误文本尾部：%v", err)
	}
	// 半写回：已命中路径已写回磁盘。
	if got := mustContent(t, state.BaseDir(proj), "a.txt"); got != "hello\n" {
		t.Errorf("base/a.txt = %q，want 命中路径已写回 %q", got, "hello\n")
	}
	if _, err := os.Lstat(filepath.Join(state.BaseDir(proj), "b.txt")); !os.IsNotExist(err) {
		t.Errorf("未命中路径不得写回 base: %v", err)
	}
}

// ⑨ 判据 13：dir「base✅ state✗」自愈——state 保存失败轮的残局（磁盘 base =
// 新渲染、state 仍旧），下轮快照（anchor == 旧 commit）命中 → 写回旧字节 →
// 对齐（Degraded 空、Restored 全量、磁盘回到旧字节）；现状为硬错 + adopt。
func TestRestoreBaseDirSelfHealsBaseNewerThanState(t *testing.T) {
	cacheRoot := t.TempDir()
	dir := t.TempDir()
	st := state.New("test")
	st.Template = state.TemplateRef{Source: t.TempDir(), Kind: "dir", Commit: "treehash-old"}
	old := []byte("old\n")
	st.Files = map[string]state.FileState{"a.txt": {Src: "a.txt", Hash: fsutil.CompareHash(old, false)}}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := renderstore.Write(st, cacheRoot, map[string][]byte{"a.txt": old}); err != nil {
		t.Fatalf("造快照夹具: %v", err)
	}
	// state.Save 失败轮的残局：磁盘 base = 新渲染字节，state 记录仍旧。
	if err := os.MkdirAll(state.BaseDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.BaseDir(dir), "a.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
	if err != nil {
		t.Fatalf("快照自愈不得硬错（判据 13）：%v", err)
	}
	if !slices.Equal(snap.Restored, []string{"a.txt"}) {
		t.Errorf("Restored = %v，want 全量 [a.txt]", snap.Restored)
	}
	if len(snap.Degraded) != 0 {
		t.Errorf("Degraded = %v，want 空（对齐后正常推进）", snap.Degraded)
	}
	if got := string(snap.Bytes["a.txt"]); got != "old\n" {
		t.Errorf("Bytes[a.txt] = %q，want 快照旧字节 %q", got, "old\n")
	}
	if got := mustContent(t, state.BaseDir(dir), "a.txt"); got != "old\n" {
		t.Errorf("base/a.txt = %q，want 写回旧字节对齐 state", got)
	}
}

// ⑩ 恢复写回 mode 由 fst.Exec 推导（0755/0644，复用既有通道）。
func TestRestoreBaseRecoveredExecMode(t *testing.T) {
	cacheRoot := t.TempDir()
	dir := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir", Commit: "treehash-v1"},
		map[string]state.FileState{
			"run.sh": {Src: "run.sh", Hash: fsutil.CompareHash([]byte("#!/bin/sh\necho hi\n"), false), Exec: true},
		})
	seedSnapshotT(t, dir, cacheRoot, map[string][]byte{"run.sh": []byte("#!/bin/sh\necho hi\n")})

	snap, err := loadBase(dir, mustLoadState(t, dir), cacheRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Metas["run.sh"].Exec {
		t.Error("Metas[run.sh].Exec 应为 true（state 权威记录）")
	}
	info, err := os.Stat(filepath.Join(state.BaseDir(dir), "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("恢复的 base/run.sh 权限 = %v，应为 0755", info.Mode().Perm())
	}
}
