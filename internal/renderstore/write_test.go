package renderstore

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbcio/xinit/internal/state"
)

// writeFixture 造最小 state：engine "e1" / dir 源 /tpl / 给定 commit 与 vars，
// Files 的键 = dsts（Write 只用键，Hash 不参与）。
func writeFixture(commit string, vars map[string]string, dsts ...string) *state.State {
	st := state.New("e1")
	st.Template = state.TemplateRef{Source: "/tpl", Kind: "dir", Commit: commit}
	if vars != nil {
		st.Vars = vars
	}
	files := make(map[string]state.FileState, len(dsts))
	for _, d := range dsts {
		files[d] = state.FileState{Src: d}
	}
	st.Files = files
	return st
}

func readFileT(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s: %v", p, err)
	}
	return data
}

// treeSnap 是快照对比用的单文件记录。
type treeSnap struct {
	ModTime time.Time
	Data    []byte
}

// snapshotTree 递归记录 dir 下全部普通文件的 mtime 与字节（键 = 相对路径）。
func snapshotTree(t *testing.T, dir string) map[string]treeSnap {
	t.Helper()
	snap := map[string]treeSnap{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snap[filepath.ToSlash(rel)] = treeSnap{ModTime: info.ModTime(), Data: readFileT(t, p)}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s: %v", dir, err)
	}
	return snap
}

// manifestT 读取条目目录下的清单，失败即 Fatal。
func manifestT(t *testing.T, dir string) *manifest {
	t.Helper()
	m, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatalf("读清单 %s: %v", dir, err)
	}
	return m
}

func TestWriteFirstTime(t *testing.T) {
	root := t.TempDir()
	st := writeFixture("sha256/c1", map[string]string{"app": "demo"}, "a.txt", "sub/b.txt")
	rendered := map[string][]byte{
		"a.txt":     []byte("alpha\n"),
		"sub/b.txt": []byte("beta\r\n"), // 原文（含 CRLF）逐字节落盘
	}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write: %v", err)
	}

	dir := entryDir(root, testKey(st))
	m := manifestT(t, dir)
	if m.Version != manifestVersion || m.Engine != "e1" || m.Source != "/tpl" ||
		m.Kind != "dir" || m.Anchor != "sha256/c1" {
		t.Errorf("清单头部不符：%+v", m)
	}
	if m.VarsSHA256 != varsHash(st.Vars) {
		t.Errorf("vars_sha256 = %q, want %q", m.VarsSHA256, varsHash(st.Vars))
	}
	if len(m.Files) != 2 || m.Files[0].Dst != "a.txt" || m.Files[1].Dst != "sub/b.txt" {
		t.Fatalf("清单键集/排序不符：%+v", m.Files)
	}
	for _, f := range m.Files {
		raw := rendered[f.Dst]
		if f.RawSHA256 != sha256Hex(raw) || f.Size != len(raw) {
			t.Errorf("%s 的 raw_sha256/size 不符：%+v", f.Dst, f)
		}
	}
	for dst, raw := range rendered {
		got := readFileT(t, filepath.Join(dir, treeDirName, filepath.FromSlash(dst)))
		if !bytes.Equal(got, raw) {
			t.Errorf("tree/%s 必须逐字节等于渲染原文：got %q", dst, got)
		}
	}
}

func TestWritePermissions(t *testing.T) {
	// 目录 0755、文件 0644（R7；循 state/base_test.go:77 权限断言先例）。
	root := t.TempDir()
	st := writeFixture("sha256/c1", nil, "a.txt", "sub/b.txt")
	rendered := map[string][]byte{"a.txt": []byte("a\n"), "sub/b.txt": []byte("b\n")}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := entryDir(root, testKey(st))

	checkPerm := func(p string, want fs.FileMode) {
		t.Helper()
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s 权限 = %v, want %v", p, got, want)
		}
	}
	checkPerm(dir, 0o755)
	checkPerm(filepath.Join(dir, treeDirName), 0o755)
	checkPerm(filepath.Join(dir, manifestName), 0o644)
	for dst := range rendered {
		checkPerm(filepath.Join(dir, treeDirName, filepath.FromSlash(dst)), 0o644)
	}
}

func TestWriteSkipsWhenEntryMatches(t *testing.T) {
	// 全匹配（同键同 anchor + tree 完整）→ 不重写：字节与 mtime 都不变。
	root := t.TempDir()
	st := writeFixture("sha256/c1", map[string]string{"a": "1"}, "a.txt", "sub/b.txt")
	rendered := map[string][]byte{"a.txt": []byte("a\n"), "sub/b.txt": []byte("b\n")}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write#1: %v", err)
	}
	dir := entryDir(root, testKey(st))

	before := snapshotTree(t, dir)
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write#2: %v", err)
	}
	after := snapshotTree(t, dir)

	if len(before) != len(after) {
		t.Fatalf("跳过不应增删文件：%d → %d", len(before), len(after))
	}
	for p, b := range before {
		a, ok := after[p]
		if !ok {
			t.Errorf("%s 消失了", p)
			continue
		}
		if !b.ModTime.Equal(a.ModTime) {
			t.Errorf("%s 的 mtime 变了（全匹配必须零重写）", p)
		}
		if !bytes.Equal(b.Data, a.Data) {
			t.Errorf("%s 的字节变了", p)
		}
	}
}

func TestWriteRotatesOnAnchorChange(t *testing.T) {
	// 异 anchor → 覆写轮转：manifest 更新、旧清单外文件被清。
	root := t.TempDir()
	st := writeFixture("sha256/c1", map[string]string{"a": "1"}, "x", "y")
	rendered := map[string][]byte{"x": []byte("x1\n"), "y": []byte("y1\n")}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write#1: %v", err)
	}
	dir := entryDir(root, testKey(st))

	st2 := writeFixture("sha256/c2", map[string]string{"a": "1"}, "x")
	if err := Write(st2, root, map[string][]byte{"x": []byte("x2\n")}); err != nil {
		t.Fatalf("Write#2: %v", err)
	}

	m := manifestT(t, dir)
	if m.Anchor != "sha256/c2" {
		t.Errorf("anchor 未更新：%q", m.Anchor)
	}
	if len(m.Files) != 1 || m.Files[0].Dst != "x" {
		t.Errorf("新清单应按新交集：%+v", m.Files)
	}
	if got := readFileT(t, filepath.Join(dir, treeDirName, "x")); !bytes.Equal(got, []byte("x2\n")) {
		t.Errorf("tree/x 应为新原文：%q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, treeDirName, "y")); !os.IsNotExist(err) {
		t.Errorf("旧清单外的 tree/y 应被清：%v", err)
	}
}

func TestWriteRewritesWhenTreeFileMissing(t *testing.T) {
	// 跳过判据含 tree/ 存在性核对：清单内文件被删 → 再写必须补回。
	root := t.TempDir()
	st := writeFixture("sha256/c1", nil, "a.txt", "b.txt")
	rendered := map[string][]byte{"a.txt": []byte("a\n"), "b.txt": []byte("b\n")}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write#1: %v", err)
	}
	dir := entryDir(root, testKey(st))
	if err := os.Remove(filepath.Join(dir, treeDirName, "a.txt")); err != nil {
		t.Fatal(err)
	}

	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("Write#2: %v", err)
	}
	if got := readFileT(t, filepath.Join(dir, treeDirName, "a.txt")); !bytes.Equal(got, rendered["a.txt"]) {
		t.Errorf("缺失文件应被重写补回：%q", got)
	}
}

func TestWriteManifestKeySetIsIntersection(t *testing.T) {
	// 清单键集 = st.Files ∩ 渲染源：rendered 多出的（--force 跳过类）不进清单；
	// st.Files 多出的（异常缺项）静默略过且不报错（宁缺勿错）。
	root := t.TempDir()
	st := writeFixture("sha256/c1", nil, "a.txt", "missing.txt")
	rendered := map[string][]byte{"a.txt": []byte("a\n"), "extra.txt": []byte("x\n")}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("缺项应静默略过，不报错：%v", err)
	}
	dir := entryDir(root, testKey(st))

	m := manifestT(t, dir)
	if len(m.Files) != 1 || m.Files[0].Dst != "a.txt" {
		t.Errorf("清单键集必须是交集：%+v", m.Files)
	}
	for _, dst := range []string{"extra.txt", "missing.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, treeDirName, dst)); !os.IsNotExist(err) {
			t.Errorf("交集外的 %s 不得进 tree/：%v", dst, err)
		}
	}
}

func TestWriteCleansRotatedTreeFiles(t *testing.T) {
	// 覆写轮转：新清单放不下的旧文件 best-effort 清理。
	root := t.TempDir()
	st := writeFixture("sha256/c1", nil, "x", "y")
	if err := Write(st, root, map[string][]byte{"x": []byte("x1\n"), "y": []byte("y1\n")}); err != nil {
		t.Fatalf("Write#1: %v", err)
	}
	dir := entryDir(root, testKey(st))

	// 同键同 anchor，仅清单集变小 → 跳过判据的「清单 dst 集 == 现算交集」不成立 → 轮转。
	st2 := writeFixture("sha256/c1", nil, "x")
	if err := Write(st2, root, map[string][]byte{"x": []byte("x2\n")}); err != nil {
		t.Fatalf("Write#2: %v", err)
	}

	m := manifestT(t, dir)
	if len(m.Files) != 1 || m.Files[0].Dst != "x" {
		t.Errorf("新清单应只有 x：%+v", m.Files)
	}
	if _, err := os.Lstat(filepath.Join(dir, treeDirName, "y")); !os.IsNotExist(err) {
		t.Errorf("旧清单外的 tree/y 应被清：%v", err)
	}
	if got := readFileT(t, filepath.Join(dir, treeDirName, "x")); !bytes.Equal(got, []byte("x2\n")) {
		t.Errorf("tree/x 应为新原文：%q", got)
	}
}

func TestWriteToleratesCleanupFailure(t *testing.T) {
	// R9：清理删除失败必须被吞。把待清路径做成非空目录占位——os.Remove
	// 对非空目录失败（被忽略），Write 仍返回 nil。
	root := t.TempDir()
	st := writeFixture("sha256/c1", nil, "old")
	if err := Write(st, root, map[string][]byte{"old": []byte("old\n")}); err != nil {
		t.Fatalf("Write#1: %v", err)
	}
	dir := entryDir(root, testKey(st))
	treeDir := filepath.Join(dir, treeDirName)

	if err := os.Remove(filepath.Join(treeDir, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(treeDir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(treeDir, "old", "inner"), []byte("i\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st2 := writeFixture("sha256/c1", nil, "x")
	if err := Write(st2, root, map[string][]byte{"x": []byte("x\n")}); err != nil {
		t.Fatalf("清理失败必须被吞（Write 仍返回 nil）：%v", err)
	}
	if _, err := os.Lstat(filepath.Join(treeDir, "old", "inner")); !os.IsNotExist(err) {
		t.Errorf("占位目录内的旧文件仍应被清：%v", err)
	}
}

func TestWriteSkipsTraversalDst(t *testing.T) {
	// 安全门：坏 Dst（.. 段 / 绝对路径）不得写出条目目录——与读侧同一
	// CheckWithinBase（base.go 写路径同款判据，§21.9）。
	root := t.TempDir()
	st := writeFixture("sha256/c1", nil, "a.txt", "../evil.txt", "/abs/evil.txt")
	rendered := map[string][]byte{
		"a.txt":         []byte("ok\n"),
		"../evil.txt":   []byte("evil\n"),
		"/abs/evil.txt": []byte("evil\n"),
	}
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("坏 Dst 应按略过处理，不硬错：%v", err)
	}
	dir := entryDir(root, testKey(st))

	m := manifestT(t, dir)
	if len(m.Files) != 1 || m.Files[0].Dst != "a.txt" {
		t.Errorf("坏 Dst 不得进清单：%+v", m.Files)
	}
	// "../evil.txt" 若未拦住会落在条目目录内（Join(treeDir,"../evil.txt") = dir/evil.txt）。
	if _, err := os.Lstat(filepath.Join(dir, "evil.txt")); !os.IsNotExist(err) {
		t.Errorf("坏 Dst 写出了 tree/ 之外：%v", err)
	}
}

func TestWriteEmptyIntersectionIsNoop(t *testing.T) {
	// 空交集 → 直接返回 nil，不创建条目（甚至不建缓存根）。
	root := filepath.Join(t.TempDir(), "cache")
	st := writeFixture("sha256/c1", nil, "a.txt")
	if err := Write(st, root, map[string][]byte{"b.txt": []byte("b\n")}); err != nil {
		t.Fatalf("空交集不应报错：%v", err)
	}
	if _, err := os.Stat(entryDir(root, testKey(st))); !os.IsNotExist(err) {
		t.Errorf("空交集不得创建条目：%v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("空交集不得创建缓存根：%v", err)
	}
}

func TestWriteUnwritableRootErrors(t *testing.T) {
	// cacheRoot 指向普通文件 → 返回 error（不 panic）。
	parent := t.TempDir()
	notDir := filepath.Join(parent, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := writeFixture("sha256/c1", nil, "a.txt")
	if err := Write(st, notDir, map[string][]byte{"a.txt": []byte("a\n")}); err == nil {
		t.Fatal("cacheRoot 不可用时必须返回 error")
	}
}
