package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSemverTags(t *testing.T) {
	got := SemverTags([]string{
		"v2.0.0", "1.2.3", "V1.0.0", "v1.0.0-rc1", "v1.0.0+build-5",
		"v1.2", "v1.0.0", "junk", "",
	})
	want := []string{"v1.0.0", "v1.0.0+build-5", "v1.2", "v2.0.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// 判别力：v1.0.0+build-5 含 '-' 但不是预发布版（Prerelease 为空）——必须保留
func TestSemverTagsKeepsBuildMetadata(t *testing.T) {
	got := SemverTags([]string{"v1.0.0+build-5"})
	if len(got) != 1 {
		t.Fatalf("含 build 元数据的版本不是预发布版，应保留；got %q", got)
	}
}

func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mkRepo 建一个带提交历史的临时 git 仓，返回 (目录, 两个 commit)。
func mkRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q")
	runGitT(t, dir, "config", "user.email", "t@example.com")
	runGitT(t, dir, "config", "user.name", "t")
	commit := func(name, content string) string {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitT(t, dir, "add", ".")
		runGitT(t, dir, "commit", "-q", "-m", content)
		return runGitT(t, dir, "rev-parse", "HEAD")
	}
	c1 := commit("a.txt", "one")
	c2 := commit("a.txt", "two")
	return dir, c1, c2
}

func TestListTags(t *testing.T) {
	dir, _, _ := mkRepo(t)
	runGitT(t, dir, "tag", "v1.0.0")
	runGitT(t, dir, "tag", "junk")
	got, err := ListTags(dir, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"junk", "v1.0.0"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSelectRevision(t *testing.T) {
	dir, _, c2 := mkRepo(t)
	for _, tag := range []string{"v1.0.0", "v1.0.0-rc1", "v1.2", "junk", "v2.0.0"} {
		runGitT(t, dir, "tag", tag)
	}
	cache := t.TempDir()

	// 有更高的 tag → 取最高（v1.2 < v2.0.0）
	if got, err := SelectRevision(dir, cache, false, "v1.0.0"); err != nil || got != "v2.0.0" {
		t.Fatalf("got %q, %v; want v2.0.0", got, err)
	}
	// F6：没有更高的 tag 且当前可解析 → 取当前，不报错
	if got, err := SelectRevision(dir, cache, false, "v2.0.0"); err != nil || got != "v2.0.0" {
		t.Fatalf("F6 应取当前 revision；got %q, %v", got, err)
	}
	// 与当前等值但不同写法（Compare==0）不算「更高」→ F6
	if got, err := SelectRevision(dir, cache, false, "v2.0"); err != nil || got != "v2.0" {
		t.Fatalf("等值 tag 应取当前；got %q, %v", got, err)
	}
	// 当前不可解析（sha/HEAD/分支名）→ 报错并要求显式 --revision（R8）
	if _, err := SelectRevision(dir, cache, false, c2); err == nil ||
		!strings.Contains(err.Error(), "--revision") {
		t.Fatalf("sha 当前值应报错并提示 --revision，got %v", err)
	}
	if _, err := SelectRevision(dir, cache, false, "HEAD"); err == nil {
		t.Fatal("HEAD 不是可解析 semver，应报错")
	}
}

func TestSelectRevisionNoTags(t *testing.T) {
	dir, _, _ := mkRepo(t)
	cache := t.TempDir()
	if _, err := SelectRevision(dir, cache, false, "v1.0.0"); err == nil ||
		!strings.Contains(err.Error(), "没有任何 tag") {
		t.Fatalf("无 tag 应报错，got %v", err)
	}
	runGitT(t, dir, "tag", "junk")
	if _, err := SelectRevision(dir, cache, false, "v1.0.0"); err == nil ||
		!strings.Contains(err.Error(), "vX.Y.Z") {
		t.Fatalf("无 semver tag 应报「未找到符合 vX.Y.Z 格式的 tag」，got %v", err)
	}
}

func TestIsAncestor(t *testing.T) {
	dir, c1, c2 := mkRepo(t)
	cache := t.TempDir()
	ok, err := IsAncestor(dir, cache, false, c1, c2)
	if err != nil || !ok {
		t.Fatalf("c1 是 c2 的祖先；got %v, %v", ok, err)
	}
	ok, err = IsAncestor(dir, cache, false, c2, c1)
	if err != nil || ok {
		t.Fatalf("c2 不是 c1 的祖先；got %v, %v", ok, err)
	}
}

// 回归（评审 Task 2）：分支与 tag 同名时 %(refname:short) 会把 tag 打印成
// "tags/<name>"，被 SemverTags 静默丢弃。模板仓的发布分支常与 tag 同名。
func TestListTagsBranchSharesTagName(t *testing.T) {
	dir, _, _ := mkRepo(t)
	for _, tag := range []string{"v1.0.0", "v1.2", "v2.0.0"} {
		runGitT(t, dir, "tag", tag)
	}
	runGitT(t, dir, "branch", "v2.0.0")
	cache := t.TempDir()

	tags, err := ListTags(dir, cache, false)
	if err != nil {
		t.Fatal(err)
	}
	has := false
	for _, tg := range tags {
		if strings.HasPrefix(tg, "tags/") {
			t.Fatalf("tag 短名不应带 tags/ 前缀；got %q", tags)
		}
		if tg == "v2.0.0" {
			has = true
		}
	}
	if !has {
		t.Fatalf("同名分支存在时 v2.0.0 tag 不应丢失；got %q", tags)
	}
	if got, err := SelectRevision(dir, cache, false, "v1.0.0"); err != nil || got != "v2.0.0" {
		t.Fatalf("got %q, %v; want v2.0.0", got, err)
	}
}
