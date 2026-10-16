package template

import (
	"errors"
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

	s, err := Open(dir, "", t.TempDir(), false, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Kind != KindDir {
		t.Errorf("Kind = %v, want KindDir", s.Kind)
	}
	// dir 源的 Commit 要等清单编译完才能算（规范 §5.4 要求排除
	// exclude 命中的文件）：Open 阶段只填 DirRoot，Commit 留空，
	// 调用方须在编译清单之后调用 TreeHashOf 补上（见 cli.Init）。
	if s.Commit != "" {
		t.Errorf("Open 阶段 dir 源的 Commit 应留空，实际 %q", s.Commit)
	}
	if s.DirRoot != dir {
		t.Errorf("DirRoot = %q, want %q", s.DirRoot, dir)
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
	s, err := Open(dir, "HEAD", cache, true, true)
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

	s, err := Open(dir, "v1.0.0", t.TempDir(), true, true)
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

	if _, err := Open(dir, "v99.99.99", t.TempDir(), true, true); err == nil {
		t.Fatal("不存在的 revision 应报错，绝不能退回到默认分支")
	}
}

// 备案清零（评审 M-1）：此前只断言「有错」——删掉 openGit 的 guard 后
// resolveCommit 的 --end-of-options 也会让它报错，测试照样绿。现在点名
// checkRefArg 的报错文案，并用裸仓夹具锁住 guard 先于一切 git/镜像动作
// （删掉 guard 后会先 clone 建出缓存根，条目断言随之变红）。
func TestOpenRejectsOptionLikeRevision(t *testing.T) {
	dir, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})

	// 本地 git 仓：就地使用，不经镜像。
	_, err := Open(dir, "--upload-pack=evil", t.TempDir(), true, true)
	if err == nil {
		t.Fatal("以 - 开头的 revision 应被拒绝")
	}
	if !strings.Contains(err.Error(), "不能以 '-' 开头") {
		t.Errorf("错误应来自 checkRefArg（点名 '-' 开头），实际: %v", err)
	}

	// 裸仓：走镜像的路径。guard 若晚于 ensureMirror/clone，这里会先建出
	// 缓存根——「父目录一个条目都没有」即锁住「早于一切 git 调用」。
	bare := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", dir, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}
	parent := t.TempDir()
	cache := filepath.Join(parent, "cache-root") // 尚不存在
	_, err = Open(bare, "--upload-pack=evil", cache, true, true)
	if err == nil {
		t.Fatal("裸仓 + 以 - 开头的 revision 应被拒绝")
	}
	if !strings.Contains(err.Error(), "不能以 '-' 开头") {
		t.Errorf("错误应来自 checkRefArg，实际: %v", err)
	}
	ents, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("guard 必须发生在镜像/clone 之前，不得创建缓存根或任何条目: %v", ents)
	}
}

// TestOpenFallsBackToGitURLWhenPathMissing 覆盖「本地路径不存在 →
// 当成 git URL 处理 → clone 失败报错」这条回退路径。
//
// 注意：它**不**覆盖「存在但不是目录」的校验分支 —— 那条由
// TestOpenRejectsNonDirectoryPath 单独覆盖（评审发现旧名字会误导）。
func TestOpenFallsBackToGitURLWhenPathMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope"), "", t.TempDir(), false, true); err == nil {
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
	_, err := Open(f, "", t.TempDir(), false, true)
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

	s, err := Open(dir, "v99.9.9-does-not-exist", t.TempDir(), false, true)
	if err != nil {
		t.Fatalf("dir 源应忽略 revision，实际报错: %v", err)
	}
	if s.Kind != KindDir || s.Revision != "" {
		t.Errorf("Kind/Revision = %v/%q, want KindDir/空串", s.Kind, s.Revision)
	}

	// 备案清零（init-revision 终审 O-1）：'-' 开头的值同样忽略——选项注入
	// 校验只发生在会触达 git 的路径，dir 源不触达。对照：git 源仍拒
	//（TestOpenRejectsOptionLikeRevision）。
	s2, err := Open(dir, "--weird", t.TempDir(), false, true)
	if err != nil {
		t.Fatalf("dir 源对 '-' 开头的 revision 同样应忽略，实际报错: %v", err)
	}
	if s2.Kind != KindDir || s2.Revision != "" {
		t.Errorf("Kind/Revision = %v/%q, want KindDir/空串", s2.Kind, s2.Revision)
	}
}

func TestIsDirSource(t *testing.T) {
	// 普通目录 → true
	dir := t.TempDir()
	if !IsDirSource(dir) {
		t.Errorf("普通目录应判为 dir 源")
	}
	// git 仓库 → false
	git := t.TempDir()
	if err := os.Mkdir(filepath.Join(git, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsDirSource(git) {
		t.Errorf("含 .git/ 的目录不应判为 dir 源")
	}
	// 裸仓（objects/ + HEAD，无 .git/）→ false
	bare := t.TempDir()
	for _, p := range []string{"objects", "HEAD"} {
		if err := os.MkdirAll(filepath.Join(bare, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if IsDirSource(bare) {
		t.Errorf("裸仓不应判为 dir 源")
	}
	// 不存在的路径（URL 形态）→ false
	if IsDirSource(filepath.Join(dir, "no-such")) {
		t.Errorf("不存在的路径不应判为 dir 源")
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
		dir, err := ensureMirror(src, cache, false, true)
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

	if _, err := ensureMirror(src, cache, false, true); err != nil {
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
	s, err := Open(bare, "HEAD", cache, false, true)
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

// Open 层的真离线（init --offline 与 status/check 同口径）：allowClone=false
// 且镜像缺失 → ErrMirrorMissing 且缓存根不被创建；对照 allowClone=true 照常
// 首次 clone（update/adopt/restore/backflow 的功能性回退）。
//
// 夹具与 TestMirrorNoCloneMissingDoesNotCreateCacheRoot 同族：只有裸仓来源
// 才走镜像（普通工作副本被 repoDirFor 当本地仓直读，测不到 allowClone）。
// 判别力在「裸仓 + 空缓存」这一组合：若 Open 内部把 allowClone 硬编回 true，
// 本用例的第一条断言（ErrMirrorMissing）立刻变红。
func TestOpenOfflineMissingMirrorFailsWithoutCloning(t *testing.T) {
	work, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	bare := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	parent := t.TempDir()
	cache := filepath.Join(parent, "cache-root") // 尚不存在

	// 真离线：镜像缺失是可识别错误，不是「先 clone 一下」。
	if _, err := Open(bare, "HEAD", cache, false, false); !errors.Is(err, ErrMirrorMissing) {
		t.Fatalf("allowClone=false 且镜像缺失应报 ErrMirrorMissing: %v", err)
	}
	// 精确条目判断（比只 stat 缓存根更强）：连缓存根都不创建。
	ents, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("allowClone=false 不得创建缓存根或任何条目: %v", ents)
	}

	// 对照：allowClone=true 照常首次 clone，且打开出的源可读。
	s, err := Open(bare, "HEAD", cache, false, true)
	if err != nil {
		t.Fatalf("allowClone=true 应照常 clone: %v", err)
	}
	if s.Kind != KindGit {
		t.Errorf("Kind = %v, want KindGit", s.Kind)
	}
	if _, err := readFileAt(s.FS, "a.go"); err != nil {
		t.Errorf("clone 出的镜像里模板不可读: %v", err)
	}
}

// 评审 M-1/F2：allowClone=false（status/check 默认，§11.4「默认不联网、只读
// 本地镜像缓存」）时镜像缺失 → 可识别错误 ErrMirrorMissing，且连缓存根目录都
// 不创建；allowClone=true（status/check 之外的调用面）保持「缺失即首次
// clone」的现状。
func TestMirrorNoCloneMissingDoesNotCreateCacheRoot(t *testing.T) {
	work, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	// 裸仓来源才会走镜像（普通工作副本被 repoDirFor 当本地仓直读）
	bare := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	parent := t.TempDir()
	cache := filepath.Join(parent, "cache-root") // 尚不存在

	if _, err := ListTags(bare, cache, false, false); !errors.Is(err, ErrMirrorMissing) {
		t.Fatalf("allowClone=false 且镜像缺失应报 ErrMirrorMissing: %v", err)
	}
	// 精确条目判断：缓存根目录不得被创建。
	ents, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() == "cache-root" {
			t.Error("allowClone=false 不得创建缓存根目录（§11.4 默认不联网）")
		}
	}

	// 对照：allowClone=true 照常首次 clone（update/init 现状不变）。
	if _, err := ListTags(bare, cache, false, true); err != nil {
		t.Fatalf("allowClone=true 应照常 clone: %v", err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("allowClone=true 应创建缓存根: %v", err)
	}
}

// RepoDir 是 check 的只读定位（§18.5）：本地 git 仓（工作副本）直返源路径，
// 不经镜像、不碰缓存根。
func TestRepoDirLocalRepo(t *testing.T) {
	src, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	parent := t.TempDir()
	cache := filepath.Join(parent, "cache-root") // 尚不存在

	got, err := RepoDir(src, cache, false)
	if err != nil {
		t.Fatalf("RepoDir: %v", err)
	}
	if got != src {
		t.Errorf("RepoDir = %q, want 源路径 %q", got, src)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("本地仓源不得创建缓存根（stat err=%v）", err)
	}
}

// RepoDir 不允许首次 clone（allowClone=false，与 status 同口径）：远程式源
// （裸仓走镜像）+ 空缓存 → ErrMirrorMissing，缓存根不被创建；fetch=true 亦然
// （--fetch 只刷新已有镜像）。
func TestRepoDirMissingMirrorNoClone(t *testing.T) {
	work, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	bare := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	for _, fetch := range []bool{false, true} {
		parent := t.TempDir()
		cache := filepath.Join(parent, "cache-root") // 尚不存在
		if _, err := RepoDir(bare, cache, fetch); !errors.Is(err, ErrMirrorMissing) {
			t.Fatalf("fetch=%v：镜像缺失应报 ErrMirrorMissing: %v", fetch, err)
		}
		ents, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(ents) != 0 {
			t.Errorf("fetch=%v：RepoDir 不得创建缓存根或任何条目: %v", fetch, ents)
		}
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

	dir, err := ensureMirror(src, cache, false, true)
	if err != nil {
		t.Fatalf("陈旧锁应被接管，实际报错: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Errorf("接管后镜像应可用: %v", err)
	}
}

// TestEnsureMirrorCleansUpAfterCloneFailure 只验证「clone 失败会向上
// 报错」这一半——用不存在的源路径触发的 clone 失败发生在 git 创建
// 目标目录之前，所以下面 leftover 的 stat 断言恒为真，不具备判别力
// （清理行为由 TestEnsureMirrorCleansUpHalfClone 单独覆盖）。
func TestEnsureMirrorCleansUpAfterCloneFailure(t *testing.T) {
	cache := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	// 本地不存在的路径让 git clone 立即失败（不触网、不超时）
	if _, err := ensureMirror(missing, cache, false, true); err == nil {
		t.Fatal("clone 失败应向上报错")
	}

	// 半成品目录必须清掉：否则目录存在但没有 HEAD，下次调用会
	// 再次 clone 到已存在的非空目录、git 直接报错，永久卡死
	leftover := filepath.Join(cache, mirrorDirName(missing))
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("clone 失败后不应残留 %s（stat err=%v）", leftover, err)
	}
}

// TestEnsureMirrorCleansUpHalfClone 覆盖 I3 的清理分支，且必须真的
// 有判别力：上次进程崩溃留下的半成品目录（存在但没有 HEAD）不清理的话，
// 下次 clone 会撞上「已存在且非空」→ git 报错 → 该模板源永久卡死。
//
// 注意不能用「不存在的源路径」造 clone 失败 —— 那种失败发生在创建目标
// 目录之前，断言会恒真（这正是上一版测试空转的原因，见 task-16-fix-review.md）。
func TestEnsureMirrorCleansUpHalfClone(t *testing.T) {
	src, _ := newRepo(t, map[string]struct {
		data string
		mode os.FileMode
	}{"a.go": {"x", 0o644}})
	cache := t.TempDir()

	// 模拟崩溃残留：镜像目录存在、没有 HEAD、里面有半成品文件
	dir := filepath.Join(cache, mirrorDirName(src))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureMirror(src, cache, false, true); err == nil {
		t.Fatal("目标目录已存在且非空时 clone 应失败")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("半成品目录应被清理，否则该模板源永久卡死（stat err=%v）", err)
	}

	// 清理后重试必须成功 —— 同时证明上面的失败不是环境恒错
	got, err := ensureMirror(src, cache, false, true)
	if err != nil {
		t.Fatalf("清理后重试应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "HEAD")); err != nil {
		t.Errorf("重试后的镜像不可用: %v", err)
	}
}
