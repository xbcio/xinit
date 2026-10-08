package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenDirSource(t *testing.T) {
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	s, err := Open(dir, "", t.TempDir(), false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Kind != KindDir {
		t.Errorf("Kind = %v, want KindDir", s.Kind)
	}
	if s.Commit == "" {
		t.Error("dir 源的 Commit 应是树 hash，不应为空")
	}
	if _, err := readFileAt(s.FS, "a.go"); err != nil {
		t.Errorf("FS 不可用: %v", err)
	}
}

func TestOpenGitSource(t *testing.T) {
	dir, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	cache := t.TempDir()
	s, err := Open(dir, "HEAD", cache, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Kind != KindGit {
		t.Errorf("Kind = %v, want KindGit", s.Kind)
	}
	if s.Commit == "" || s.Revision != "HEAD" {
		t.Errorf("Revision/Commit = %q/%q", s.Revision, s.Commit)
	}
	if _, err := readFileAt(s.FS, "a.go"); err != nil {
		t.Errorf("FS 不可用: %v", err)
	}
}

func TestOpenGitSourceResolvesTagToCommit(t *testing.T) {
	dir, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	cmd := exec.Command("git", "-C", dir, "tag", "v1.0.0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tag: %v\n%s", err, out)
	}

	s, err := Open(dir, "v1.0.0", t.TempDir(), true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Revision != "v1.0.0" {
		t.Errorf("Revision 应保留用户给的名字，实际 %q", s.Revision)
	}
	if len(s.Commit) != 40 {
		t.Errorf("Commit 应是解析出的 sha，实际 %q", s.Commit)
	}
}

func TestOpenRejectsNonexistentRevision(t *testing.T) {
	dir, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	if _, err := Open(dir, "v99.99.99", t.TempDir(), true); err == nil {
		t.Fatal("不存在的 revision 应报错，绝不能退回到默认分支")
	}
}

func TestOpenRejectsOptionLikeRevision(t *testing.T) {
	dir, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	if _, err := Open(dir, "--upload-pack=evil", t.TempDir(), true); err == nil {
		t.Fatal("以 - 开头的 revision 应被拒绝")
	}
}

// TestOpenFallsBackToGitURLWhenPathMissing 覆盖「本地路径不存在 →
// 当成 git URL 处理 → clone 失败报错」这条回退路径。
//
// 注意：它**不**覆盖「存在但不是目录」的校验分支 —— 那条由
// TestOpenRejectsNonDirectoryPath 单独覆盖（评审发现旧名字会误导）。
func TestOpenFallsBackToGitURLWhenPathMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope"), "", t.TempDir(), false); err == nil {
		t.Fatal("不存在的模板路径应报错")
	}
}

// TestOpenRejectsNonDirectoryPath 覆盖 source.go 中「路径存在但不是
// 目录」的校验分支 —— 这条分支此前从未被任何测试走到。
func TestOpenRejectsNonDirectoryPath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "plainfile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(f, "", t.TempDir(), false)
	if err == nil {
		t.Fatal("路径存在但不是目录时应报错")
	}
	if !strings.Contains(err.Error(), "不是目录") {
		t.Errorf("错误应说明不是目录，实际: %v", err)
	}
}

// TestOpenDirSourceIgnoresRevision 覆盖 dir 源忽略 revision 的分支：
// dir 源没有版本概念，给了 revision 也应忽略，而不是去 resolve 一个
// 不存在的 ref（那会把所有本地目录模板卡死）。
func TestOpenDirSourceIgnoresRevision(t *testing.T) {
	dir := writeTree(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	s, err := Open(dir, "v99.9.9-does-not-exist", t.TempDir(), false)
	if err != nil {
		t.Fatalf("dir 源应忽略 revision，实际报错: %v", err)
	}
	if s.Kind != KindDir {
		t.Errorf("Kind = %v, want KindDir", s.Kind)
	}
}

func TestDefaultCacheRootIsStable(t *testing.T) {
	root, err := DefaultCacheRoot()
	if err != nil {
		t.Fatalf("DefaultCacheRoot: %v", err)
	}
	if root == "" {
		t.Error("缓存根不应为空")
	}
	// 两次调用必须一致 —— 否则缓存永远不命中
	again, err := DefaultCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != again {
		t.Errorf("缓存根不稳定: %q != %q", root, again)
	}
}

func TestMirrorLockIsReentrantSafe(t *testing.T) {
	// 同一进程内连续多次镜像同一来源不应死锁（锁必须在 defer 里释放）。
	//
	// 注意这里直接调 ensureMirror，不走 Open —— 本地 git 仓会被 Open 当成
	// 「就地使用」而绕过镜像，那样这个测试就什么都没测到。
	src, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	cache := t.TempDir()

	for i := 0; i < 3; i++ {
		dir, err := ensureMirror(src, cache, false)
		if err != nil {
			t.Fatalf("第 %d 次 ensureMirror: %v", i+1, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
			t.Fatalf("镜像不可用: %v", err)
		}
	}
}

func TestMirrorDirNameIsStableForSameSource(t *testing.T) {
	a := mirrorDirName("/tmp/repo")
	b := mirrorDirName("/tmp/repo")
	c := mirrorDirName("/tmp/other")
	if a != b {
		t.Error("同一来源的镜像目录必须稳定")
	}
	if a == c {
		t.Error("不同来源应映射到不同目录")
	}
	if len(a) != 64 {
		t.Errorf("目录名应是 sha256 的 hex（64 字符），实际 %d", len(a))
	}
}

func TestMirrorDirIsCreatedUnderCacheRoot(t *testing.T) {
	src, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	cache := t.TempDir()

	if _, err := ensureMirror(src, cache, false); err != nil {
		t.Fatalf("ensureMirror: %v", err)
	}
	ents, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(ents) == 0 {
		t.Error("镜像缓存目录应被创建")
	}
}

// TestOpenBareRepoGoesThroughMirror 覆盖「来源不是本地工作副本」这条路径。
//
// 普通目录会被 Open 当成 dir 源、带 .git 的目录会被就地使用 —— 只有
// 裸仓（有 HEAD 但没有 .git 子目录）会真正走到镜像逻辑。
func TestOpenBareRepoGoesThroughMirror(t *testing.T) {
	work, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	bare := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	cache := t.TempDir()
	s, err := Open(bare, "HEAD", cache, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Kind != KindGit {
		t.Errorf("Kind = %v, want KindGit", s.Kind)
	}
	if _, err := readFileAt(s.FS, "a.go"); err != nil {
		t.Errorf("镜像里的模板不可读: %v", err)
	}
	if ents, err := os.ReadDir(cache); err != nil || len(ents) == 0 {
		t.Errorf("应创建镜像目录 (ents=%d, err=%v)", len(ents), err)
	}
}

// TestMirrorLockContentionFailsFast 覆盖锁被占用且未陈旧时的报错分支
// （以确定性锁原语覆盖代替真并发 goroutine 测试：第二个调用者到达锁的
// 时刻不确定，真并发测试可能偶发全绿，是 flake 而非判别力）。
func TestMirrorLockContentionFailsFast(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "mirror.lock")

	unlock, err := acquireMirrorLock(lockPath)
	if err != nil {
		t.Fatalf("首次获取锁: %v", err)
	}
	defer unlock()

	// 锁被占用且未陈旧：第二个 xinit 必须立即报错，
	// 不能等待、不能直接放行（两条都会导致同一镜像目录双写）
	_, err = acquireMirrorLock(lockPath)
	if err == nil {
		t.Fatal("锁已被占用时第二次获取应报错")
	}
	if !strings.Contains(err.Error(), "正被另一个") {
		t.Errorf("错误应说明锁被占用，实际: %v", err)
	}

	// 释放后必须能重新获取（锁不是一次性的；也证明上面的报错
	// 不是"锁文件永远删不掉"这类恒错夹具）
	unlock()
	unlock2, err := acquireMirrorLock(lockPath)
	if err != nil {
		t.Fatalf("释放后重新获取: %v", err)
	}
	unlock2()
}

// TestMirrorLockStaleTakeover 覆盖陈旧锁接管分支（评审点名「特别检查」
// 却零覆盖的分支）。
func TestMirrorLockStaleTakeover(t *testing.T) {
	src, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	cache := t.TempDir()

	// 制造陈旧锁：进程被 kill 后遗留的锁文件必须能被接管，
	// 否则崩一次就永久卡死这个模板源（O_EXCL 唯一的残留风险，
	// 这条分支正是为它准备的）
	lockPath := filepath.Join(cache, mirrorDirName(src)+".lock")
	if err := os.WriteFile(lockPath, []byte("99999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	dir, err := ensureMirror(src, cache, false)
	if err != nil {
		t.Fatalf("陈旧锁应被接管，实际报错: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Errorf("接管后镜像应可用: %v", err)
	}
}

// TestEnsureMirrorCleansUpAfterCloneFailure 覆盖 clone 失败后清理半成品
// 目录的分支。
func TestEnsureMirrorCleansUpAfterCloneFailure(t *testing.T) {
	cache := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	// 本地不存在的路径让 git clone 立即失败（不触网、不超时）
	if _, err := ensureMirror(missing, cache, false); err == nil {
		t.Fatal("clone 失败应向上报错")
	}

	// 半成品目录必须清掉：否则目录存在但没有 HEAD，下次调用会
	// 再次 clone 到已存在的非空目录、git 直接报错，永久卡死
	leftover := filepath.Join(cache, mirrorDirName(missing))
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("clone 失败后不应残留 %s（stat err=%v）", leftover, err)
	}
}
