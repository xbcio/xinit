package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

func TestOpenRejectsNonexistentPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope"), "", t.TempDir(), false); err == nil {
		t.Fatal("不存在的模板路径应报错")
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
