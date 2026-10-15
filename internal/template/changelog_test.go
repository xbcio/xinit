package template

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// histRepo 是 mkHistory 建出的模板仓：commits 按 c1..c6 顺序，subjects 与之对应。
//
//	c1 "feat: one"    tag v1.0.0（注解）
//	c2 "fix: two"
//	c3 "feat: three"  tag v1.1.0（轻量）
//	c4 "chore: four"  tag v1.2.0-rc1（预发布，不进 changelog）
//	c5 "feat: five"   tag v2.0.0（注解）
//	c6 "wip: six"     未打 tag（HEAD）
type histRepo struct {
	dir     string
	branch  string
	commits [6]string
}

func mkHistory(t *testing.T) histRepo {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q")
	runGitT(t, dir, "config", "user.email", "t@example.com")
	runGitT(t, dir, "config", "user.name", "t")
	h := histRepo{dir: dir}
	subjects := []string{"feat: one", "fix: two", "feat: three", "chore: four", "feat: five", "wip: six"}
	for i, s := range subjects {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitT(t, dir, "add", ".")
		runGitT(t, dir, "commit", "-q", "-m", s)
		h.commits[i] = runGitT(t, dir, "rev-parse", "HEAD")
		switch i {
		case 0:
			runGitT(t, dir, "tag", "-a", "v1.0.0", "-m", "release 1.0.0")
		case 2:
			runGitT(t, dir, "tag", "v1.1.0")
		case 3:
			runGitT(t, dir, "tag", "v1.2.0-rc1")
		case 4:
			runGitT(t, dir, "tag", "-a", "v2.0.0", "-m", "release 2.0.0")
		}
	}
	h.branch = runGitT(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	return h
}

// pointsOf 把结果压成 name → subjects，方便整体断言（顺序另由 names 断言）。
func pointsOf(r *ChangelogResult) (names []string, subjects map[string][]string) {
	subjects = map[string][]string{}
	for _, p := range r.Points {
		names = append(names, p.Name)
		subjects[p.Name] = p.Subjects
	}
	return
}

func TestBuildChangelogDefaultTargetIsLatestStableTag(t *testing.T) {
	h := mkHistory(t)
	r, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Relation != RelAhead {
		t.Fatalf("Relation = %v，want RelAhead", r.Relation)
	}
	// 默认目标 = 最高稳定 tag v2.0.0，不是未打 tag 的 HEAD，也不是预发布 rc1
	if r.ToName != "v2.0.0" || r.ToCommit != h.commits[4] {
		t.Fatalf("目标 = %s@%s，want v2.0.0@%s", r.ToName, r.ToCommit, h.commits[4])
	}
	names, subj := pointsOf(r)
	if want := []string{"v1.1.0", "v2.0.0"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("分段 = %q，want %q（升序；预发布 rc1 不单列）", names, want)
	}
	// 每段 = 上一个点（不含）到本点（含）之间的提交标题，新→旧
	if want := []string{"feat: three", "fix: two"}; !reflect.DeepEqual(subj["v1.1.0"], want) {
		t.Fatalf("v1.1.0 段 = %q，want %q", subj["v1.1.0"], want)
	}
	// rc1 的提交（chore: four）并入下一个稳定段
	if want := []string{"feat: five", "chore: four"}; !reflect.DeepEqual(subj["v2.0.0"], want) {
		t.Fatalf("v2.0.0 段 = %q，want %q", subj["v2.0.0"], want)
	}
}

func TestBuildChangelogExplicitTargetStopsThere(t *testing.T) {
	h := mkHistory(t)
	r, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[0], "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	names, _ := pointsOf(r)
	if want := []string{"v1.1.0"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("分段 = %q，want %q（不得越过显式目标）", names, want)
	}
	if r.ToName != "v1.1.0" || r.ToCommit != h.commits[2] {
		t.Fatalf("目标 = %s@%s", r.ToName, r.ToCommit)
	}
}

// 显式目标不是 tag（此处 HEAD，未发布提交）：中间稳定 tag 各成一段，末段以目标原文命名。
func TestBuildChangelogUntaggedTargetGetsFinalPoint(t *testing.T) {
	h := mkHistory(t)
	r, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[0], "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	names, subj := pointsOf(r)
	if want := []string{"v1.1.0", "v2.0.0", "HEAD"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("分段 = %q，want %q", names, want)
	}
	if want := []string{"wip: six"}; !reflect.DeepEqual(subj["HEAD"], want) {
		t.Fatalf("末段 = %q，want %q", subj["HEAD"], want)
	}
	if r.ToCommit != h.commits[5] {
		t.Fatalf("ToCommit = %s，want %s", r.ToCommit, h.commits[5])
	}
}

func TestBuildChangelogRelations(t *testing.T) {
	h := mkHistory(t)
	cache := t.TempDir()

	same, err := BuildChangelog(h.dir, cache, false, h.commits[2], "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if same.Relation != RelSame || len(same.Points) != 0 {
		t.Fatalf("同一 commit：Relation=%v Points=%d，want RelSame/0", same.Relation, len(same.Points))
	}

	behind, err := BuildChangelog(h.dir, cache, false, h.commits[4], "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if behind.Relation != RelBehind || len(behind.Points) != 0 {
		t.Fatalf("目标更旧：Relation=%v Points=%d，want RelBehind/0", behind.Relation, len(behind.Points))
	}

	// 分叉：从 c2 另起一个提交，再回到主线
	runGitT(t, h.dir, "checkout", "-q", h.commits[1])
	if err := os.WriteFile(filepath.Join(h.dir, "side.txt"), []byte("side"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, h.dir, "add", ".")
	runGitT(t, h.dir, "commit", "-q", "-m", "side work")
	side := runGitT(t, h.dir, "rev-parse", "HEAD")
	runGitT(t, h.dir, "checkout", "-q", h.branch)
	div, err := BuildChangelog(h.dir, cache, false, side, "v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if div.Relation != RelDiverged || len(div.Points) != 0 {
		t.Fatalf("分叉：Relation=%v Points=%d，want RelDiverged/0", div.Relation, len(div.Points))
	}
}

// 同一 commit 上的多个稳定 tag（v1.1 与 v1.1.0 并列）合并为一段，取序上靠后的名字。
func TestBuildChangelogMergesTagsOnSameCommit(t *testing.T) {
	h := mkHistory(t)
	runGitT(t, h.dir, "tag", "v1.1", h.commits[2])
	r, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[0], "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	names, _ := pointsOf(r)
	if want := []string{"v1.1.0"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("分段 = %q，want %q", names, want)
	}
}

func TestBuildChangelogNoStableTagForDefaultTarget(t *testing.T) {
	dir, c1, _ := mkRepo(t)
	runGitT(t, dir, "tag", "v1.0.0-rc1")
	_, err := BuildChangelog(dir, t.TempDir(), false, c1, "")
	if err == nil || !strings.Contains(err.Error(), "--revision") {
		t.Fatalf("没有稳定 tag 应报错并提示 --revision，got %v", err)
	}
}

func TestBuildChangelogUnknownRecordedCommit(t *testing.T) {
	h := mkHistory(t)
	_, err := BuildChangelog(h.dir, t.TempDir(), false, strings.Repeat("a", 40), "v1.1.0")
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("记录的 commit 不在仓库里应报错，got %v", err)
	}
}

func TestBuildChangelogUnknownTarget(t *testing.T) {
	h := mkHistory(t)
	_, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[0], "no-such-ref")
	if err == nil || !strings.Contains(err.Error(), `模板中不存在版本 "no-such-ref"`) {
		t.Fatalf("目标不存在应报错，got %v", err)
	}
}

func TestBuildChangelogRejectsOptionLikeArgs(t *testing.T) {
	h := mkHistory(t)
	if _, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[0], "--upload-pack=evil"); err == nil {
		t.Fatal("以 '-' 开头的目标应被拒绝")
	}
	if _, err := BuildChangelog(h.dir, t.TempDir(), false, "--all", "v1.1.0"); err == nil {
		t.Fatal("以 '-' 开头的 from 应被拒绝")
	}
}

// 与 status 同口径：只读定位，镜像缺失返回 ErrMirrorMissing，且不创建缓存根。
func TestBuildChangelogMirrorMissingIsReadOnly(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	_, err := BuildChangelog(filepath.Join(t.TempDir(), "no-such-repo"), cacheRoot, false,
		strings.Repeat("a", 40), "")
	if !errors.Is(err, ErrMirrorMissing) {
		t.Fatalf("got %v，want ErrMirrorMissing", err)
	}
	if _, statErr := os.Stat(cacheRoot); statErr == nil {
		t.Fatal("镜像缺失时不得创建缓存根")
	}
}

// 提交标题来自模板仓（可能是第三方）：控制字符（含 ESC）不得原样进入输出。
func TestBuildChangelogStripsControlCharsFromSubjects(t *testing.T) {
	h := mkHistory(t)
	if err := os.WriteFile(filepath.Join(h.dir, "a.txt"), []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, h.dir, "add", ".")
	runGitT(t, h.dir, "commit", "-q", "-m", "ok\x1b[2J\x07done‮evil")
	r, err := BuildChangelog(h.dir, t.TempDir(), false, h.commits[4], "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	last := r.Points[len(r.Points)-1]
	if len(last.Subjects) == 0 {
		t.Fatalf("末段无标题：%+v", last)
	}
	got := last.Subjects[0]
	if strings.ContainsAny(got, "\x1b\x07‮") {
		t.Fatalf("标题仍含控制字符：%q", got)
	}
	if got != "ok[2Jdoneevil" {
		t.Fatalf("标题 = %q，want %q（只剥离控制字符，其余原样）", got, "ok[2Jdoneevil")
	}
}

func TestSanitizeLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain 中文 ✓", "plain 中文 ✓"},
		{"a\x1b[31mb", "a[31mb"},
		{"a\tb\x00c\x7fd", "abcd"},
		{"x‪y‮z⁦w⁩", "xyzw"},
		{"a\u0085b\u009bc", "abc"}, // C1（NEL / CSI）：唯一可作 CSI 引入的编码区间，必须剥
		{"bad \x9b byte", "bad � byte"},
		// 刻意保留的不可见字符（评审 O-1，注释已说明理由）：
		{"keep\u200bzwsp", "keep\u200bzwsp"},
		{"keep\ufeffbom", "keep\ufeffbom"}, // BOM（字面量在 Go 源里非法，须转义）
		{"keep\u206a\u2028\u2029", "keep\u206a\u2028\u2029"},
		{"emoji 👨‍👩", "emoji 👨‍👩"}, // ZWJ 属格式字符但不是欺骗向量，保留
	}
	for _, c := range cases {
		if got := sanitizeLine(c.in); got != c.want {
			t.Errorf("sanitizeLine(%q) = %q，want %q", c.in, got, c.want)
		}
	}
}
