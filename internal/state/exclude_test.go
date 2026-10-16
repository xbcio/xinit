package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func TestEnsureGitExcludedCreatesEntry(t *testing.T) {
	dir := initRepo(t)
	if err := EnsureGitExcluded(dir); err != nil {
		t.Fatalf("EnsureGitExcluded: %v", err)
	}

	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 exclude: %v", err)
	}
	if !strings.Contains(string(data), DirName+"/") {
		t.Errorf("exclude 里应含 %s/: %q", DirName, data)
	}
}

func TestEnsureGitExcludedIsIdempotent(t *testing.T) {
	dir := initRepo(t)
	for i := 0; i < 5; i++ {
		if err := EnsureGitExcluded(dir); err != nil {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
	}

	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = dir
	out, _ := cmd.Output()
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), DirName+"/"); n != 1 {
		t.Errorf("%s/ 出现了 %d 次，应恰好 1 次:\n%s", DirName, n, data)
	}
}

func TestEnsureGitExcludedPreservesExistingContent(t *testing.T) {
	dir := initRepo(t)
	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = dir
	out, _ := cmd.Output()
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	// 用户已有的规则不能被冲掉
	if err := os.WriteFile(p, []byte("# 我的规则\n*.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureGitExcluded(dir); err != nil {
		t.Fatalf("EnsureGitExcluded: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "*.local") {
		t.Errorf("用户已有规则被冲掉: %q", data)
	}
	if !strings.Contains(string(data), "# 我的规则") {
		t.Errorf("用户已有注释被冲掉: %q", data)
	}
}

func TestEnsureGitExcludedHandlesExistingEntryWithoutNewline(t *testing.T) {
	dir := initRepo(t)
	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = dir
	out, _ := cmd.Output()
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	// 已有内容但末尾没有换行 —— 直接追加会把两行粘在一起
	if err := os.WriteFile(p, []byte("*.local"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureGitExcluded(dir); err != nil {
		t.Fatalf("EnsureGitExcluded: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "*.local"+DirName) {
		t.Errorf("追加时未补换行，规则被粘连: %q", data)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "*.local"+DirName+"/" {
			t.Errorf("规则被粘连: %q", data)
		}
	}
}

func TestEnsureGitExcludedOutsideRepoIsNoop(t *testing.T) {
	dir := t.TempDir() // 不是 git 仓
	if err := EnsureGitExcluded(dir); err != nil {
		t.Fatalf("非 git 仓不应报错（规范 §10.2 第 5 步：非 git 仓警告后继续）: %v", err)
	}
}

func TestIsGitTrackedDetectsTrackedStateDir(t *testing.T) {
	dir := initRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DirName, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracked, err := IsGitTracked(dir, DirName)
	if err != nil {
		t.Fatalf("IsGitTracked: %v", err)
	}
	if tracked {
		t.Fatal("尚未 add，不应报告为已跟踪")
	}

	for _, args := range [][]string{{"add", "-f", DirName}, {"-c", "user.email=t@e", "-c", "user.name=t", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	tracked, err = IsGitTracked(dir, DirName)
	if err != nil {
		t.Fatalf("IsGitTracked: %v", err)
	}
	if !tracked {
		t.Error("已提交的 .xinit/ 应被报告为已跟踪")
	}
}

func TestIsGitTrackedOnUntrackedPath(t *testing.T) {
	dir := initRepo(t)
	tracked, err := IsGitTracked(dir, "nope")
	if err != nil {
		t.Fatalf("IsGitTracked: %v", err)
	}
	if tracked {
		t.Error("不存在的路径不应报告为已跟踪")
	}
}

// M10 T3-M3（备案清零）：失败路径此前无行为测试（终审曾裁「结构性判据足够」，
// 本批补上读取侧行为面）。两支夹具都落在「读取」错误面：exclude 做成目录
// （EISDIR）/ info 做成文件（ENOTDIR）——os.ReadFile 在两者都稳定失败于非
// notexist（chmod 000 会被 Linux 容器 root 读穿，不可用）。写入侧失败经
// WriteFileAtomic 的既有测试面 + 结构化传播覆盖（无注入接缝，不造新缝）。
func TestEnsureGitExcludedReadErrorPropagates(t *testing.T) {
	cases := []struct {
		name   string
		damage func(t *testing.T, dir string)
	}{
		{"exclude 为目录", func(t *testing.T, dir string) {
			p, ok := GitExcludePath(dir)
			if !ok {
				t.Fatal("夹具前提：initRepo 应为 git 仓")
			}
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"info 为文件", func(t *testing.T, dir string) {
			p, ok := GitExcludePath(dir)
			if !ok {
				t.Fatal("夹具前提：initRepo 应为 git 仓")
			}
			if err := os.RemoveAll(filepath.Dir(p)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Dir(p), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			tc.damage(t, dir)
			err := EnsureGitExcluded(dir)
			if err == nil {
				t.Fatal("读不到 exclude 时应报错（调用点据此中止/回滚，不得静默）")
			}
			if !strings.Contains(err.Error(), "读取") {
				t.Errorf("错误应来自读取分支，got %v", err)
			}
		})
	}
}
