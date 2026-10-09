package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 不在此重复定义（裁定 V）：apply 包内已有 Task 7 backup_test.go 的
// 同包 helper——writeFile(t, dir, rel, data string, mode os.FileMode)，
// 参数名不同但类型等价，调用形状一致。同包重复定义会编译失败。

func refusalFor(t *testing.T, refusals []Refusal, path string) (Refusal, bool) {
	t.Helper()
	for _, r := range refusals {
		if r.Path == path {
			return r, true
		}
	}
	return Refusal{}, false
}

func TestCheckWritesClean(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a/keep.txt", "x", 0o644)

	refusals, err := CheckWrites(dir, []string{"a/new.txt", "fresh/deep/file.txt", "top.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 0 {
		t.Fatalf("干净路径不该被拒：%v", refusals)
	}
	// 检查必须只读：不存在的父目录不能被创建
	if _, err := os.Lstat(filepath.Join(dir, "fresh")); !os.IsNotExist(err) {
		t.Error("CheckWrites 不得创建目录")
	}
}

// 验收 ⑤ 的一半：未跟踪的 license 挡着模板的 LICENSE。
func TestFoldRefusesUntrackedCollision(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "license", "用户的文件", 0o644)

	refusals, err := CheckWrites(dir, []string{"LICENSE"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := refusalFor(t, refusals, "LICENSE")
	if !ok {
		t.Fatal("APFS/Windows 上 rename 会打到 license 上，必须拒绝")
	}
	if !strings.Contains(r.Reason, "license") || !strings.Contains(r.Reason, "prune.txt") {
		t.Errorf("拒绝文案要点名冲突条目并给出出口；got %q", r.Reason)
	}

	// license 在本轮删除集合里 → 先删后写，放行
	refusals, err = CheckWrites(dir, []string{"LICENSE"}, map[string]bool{"license": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 0 {
		t.Fatalf("删除集合里的折叠条目应放行：%v", refusals)
	}
}

func TestFoldRefusesNFCEquivalent(t *testing.T) {
	dir := t.TempDir()
	// "é" 的 NFD 与 NFC 两种写法
	if err := os.WriteFile(filepath.Join(dir, "café.txt"), []byte("x"), 0o644); err != nil {
		t.Skipf("本机文件系统不支持该字节序列：%v", err)
	}
	refusals, err := CheckWrites(dir, []string{"café.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refusalFor(t, refusals, "café.txt"); !ok {
		t.Error("NFC/NFD 等价条目也算折叠冲突（§9.3）")
	}
}

func TestObstacles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a", "我是文件", 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}

	refusals, err := CheckWrites(dir, []string{"a/b.txt", "d"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := refusalFor(t, refusals, "a/b.txt"); !ok || !strings.Contains(r.Reason, "常规文件") {
		t.Errorf("ENOTDIR 障碍物应被拒：%v", refusals)
	} else if !strings.Contains(r.Reason, "prune.txt") {
		t.Errorf("ENOTDIR 障碍物要给出 prune 出口（AL）：%q", r.Reason)
	}
	if r, ok := refusalFor(t, refusals, "d"); !ok || !strings.Contains(r.Reason, "目录") {
		t.Errorf("EISDIR 障碍物应被拒：%v", refusals)
	} else if !strings.Contains(r.Reason, "prune.txt") {
		t.Errorf("EISDIR 障碍物要给出 prune 出口（AL）：%q", r.Reason)
	}
}

func TestSymlinkEscapeIsRefusedByPathSafety(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	refusals, err := CheckWrites(dir, []string{"escape/x.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := refusalFor(t, refusals, "escape/x.txt")
	if !ok || !strings.Contains(r.Reason, "项目根之外") {
		t.Errorf("外指链接应由 §9.5 拒绝：%v", refusals)
	}
}

// 裁定 W：悬空符号链接（EvalSymlinks 失败）不能被升格成整轮中止——§9.3 的
// 语义是「拒绝该路径、跳过、退出 2、每轮重查」，与 T7 对 ENOTDIR 的处置同构；
// T4 契约：err != nil 视为拒绝写入（fail-closed）。
func TestDanglingSymlinkRefusedNotAborted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("nowhere", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	refusals, err := CheckWrites(dir, []string{"link/x.txt", "ok.txt"}, nil)
	if err != nil {
		t.Fatalf("悬空链接只应拒绝该路径，不应中止整轮：%v", err)
	}
	r, ok := refusalFor(t, refusals, "link/x.txt")
	if !ok || !strings.Contains(r.Reason, "无法安全解析") || !strings.Contains(r.Reason, "prune.txt") {
		t.Errorf("悬空符号链接路径应被 fail-closed 拒绝并给出出口：%v", refusals)
	}
	if _, ok := refusalFor(t, refusals, "ok.txt"); ok {
		t.Errorf("无关路径不应受牵连：%v", refusals)
	}
}

// R16：指向项目**内**的符号链接同样拒绝——读盘不穿透，写盘也不穿透。
func TestSymlinkParentInsideProjectStillRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	refusals, err := CheckWrites(dir, []string{"link/x.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := refusalFor(t, refusals, "link/x.txt")
	if !ok || !strings.Contains(r.Reason, "符号链接") {
		t.Errorf("内指链接也该拒绝（R16）：%v", refusals)
	}
}

func TestSymlinkAtPathRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real.txt", "x", 0o644)
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	refusals, err := CheckWrites(dir, []string{"link.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := refusalFor(t, refusals, "link.txt"); !ok || !strings.Contains(r.Reason, "符号链接") {
		t.Errorf("末段是符号链接应拒绝：%v", refusals)
	}
}
