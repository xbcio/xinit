package cli

import (
	"bytes"
	"strings"
	"testing"
)

const sampleChangelog = `# 变更日志

前言一段。

## [Unreleased]

- 还没发布的改动

## [1.1.0] - 2026-11-01

### 新增

- 第二版的新东西

## [1.0.0] - 2026-10-15

### 新增

- 第一版的东西
`

// runChangelog 以注入的 doc 跑 xinit changelog <args...>，返回 stdout 与错误。
func runChangelog(t *testing.T, doc string, args ...string) (string, error) {
	t.Helper()
	old := Changelog
	t.Cleanup(func() { Changelog = old })
	Changelog = doc

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"changelog"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestChangelogCmdPrintsWholeDocByDefault(t *testing.T) {
	got, err := runChangelog(t, sampleChangelog)
	if err != nil {
		t.Fatal(err)
	}
	if got != sampleChangelog {
		t.Fatalf("无参应原样输出全文；got %q", got)
	}
}

func TestChangelogCmdSingleVersion(t *testing.T) {
	for _, arg := range []string{"1.0.0", "v1.0.0", "V1.0.0"} {
		got, err := runChangelog(t, sampleChangelog, arg)
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		if !strings.HasPrefix(got, "## [1.0.0] - 2026-10-15\n") {
			t.Errorf("%s: 应从该版本的标题行开始；got %q", arg, got)
		}
		if !strings.Contains(got, "第一版的东西") {
			t.Errorf("%s: 缺少该版本正文；got %q", arg, got)
		}
		if strings.Contains(got, "第二版的新东西") || strings.Contains(got, "前言") {
			t.Errorf("%s: 不得带入其它版本或前言；got %q", arg, got)
		}
		if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
			t.Errorf("%s: 应恰好以一个换行结尾；got %q", arg, got)
		}
	}
}

// 中间的版本在下一个二级标题处截止，不吞后面的版本。
func TestChangelogCmdSectionStopsAtNextHeading(t *testing.T) {
	got, err := runChangelog(t, sampleChangelog, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "第二版的新东西") || !strings.Contains(got, "### 新增") {
		t.Fatalf("应含本版本正文及其三级标题；got %q", got)
	}
	if strings.Contains(got, "第一版的东西") || strings.Contains(got, "1.0.0") {
		t.Fatalf("不得吞入下一个版本；got %q", got)
	}
}

func TestChangelogCmdUnreleasedIsCaseInsensitive(t *testing.T) {
	got, err := runChangelog(t, sampleChangelog, "unreleased")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "还没发布的改动") {
		t.Fatalf("got %q", got)
	}
}

func TestChangelogCmdUnknownVersionListsAvailable(t *testing.T) {
	_, err := runChangelog(t, sampleChangelog, "9.9.9")
	if err == nil {
		t.Fatal("不存在的版本应报错")
	}
	for _, want := range []string{"9.9.9", "1.1.0", "1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误应点名 %q 并列出可用版本；got %v", want, err)
		}
	}
}

func TestChangelogCmdWithoutEmbeddedDoc(t *testing.T) {
	for _, args := range [][]string{nil, {"1.0.0"}} {
		_, err := runChangelog(t, "", args...)
		if err == nil || !strings.Contains(err.Error(), "未内嵌") {
			t.Errorf("args=%v: 未内嵌变更日志应明确报错；got %v", args, err)
		}
	}
}

func TestChangelogCmdRejectsExtraArgs(t *testing.T) {
	if _, err := runChangelog(t, sampleChangelog, "1.0.0", "1.1.0"); err == nil {
		t.Fatal("多于一个版本参数应报错")
	}
}

// 与真实的 CHANGELOG.md 对拍：仓库根的文件头部必须保持「## [版本]」的结构，
// 否则 changelog <版本> 找不到章节。main 的 go:embed 与此处读的是同一个文件。
func TestRepoChangelogIsSectionable(t *testing.T) {
	doc := loadRepoChangelog(t)
	got, err := runChangelog(t, doc, "1.0.0")
	if err != nil {
		t.Fatalf("真实 CHANGELOG.md 里取不到 1.0.0 章节: %v", err)
	}
	if !strings.HasPrefix(got, "## [1.0.0]") {
		t.Fatalf("章节应从标题行开始；got %.60q", got)
	}
}
