package update

// 来源：cli 侧夹具的自足副本（§4.3 规则 2/3 + §5-R10：定义一律留 cli，update
// 侧复制一份，使 `go test ./internal/update/...` 不依赖 cli 夹具；I-2 实施批
// T2/M1）。逐字复制自：restore_test.go（runGitT/writeFilesT/mkTemplateRepoT/
// tmplYAML/projectWithState/mustLoadState）、update_test.go（mustContent）、
// snapshot_test.go（renderEntryT）、check_test.go（isolateCacheT）；
// seedSnapshotT/textState 随快照用例一并迁入（原 restore_snapshot_test.go）。
// commitT/captureStdoutT/readTreeT 未复制：其消费方全在留 cli 的用例里。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/renderstore"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// runGitT 在指定目录跑一条 git 命令（固定作者身份，避免依赖用户全局配置）。
// 包内通用 helper，Task 12 的夹具继续复用。
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFilesT 往目录里写一批文件（建父目录）。
func writeFilesT(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// mkTemplateRepoT 建一个真 git 模板仓（工作副本当模板源），返回 (source, commit)。
func mkTemplateRepoT(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q", "-b", "main")
	writeFilesT(t, dir, files)
	runGitT(t, dir, "add", "-A")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir, runGitT(t, dir, "rev-parse", "HEAD")
}

// 模板：xinit.yml 清单（无 vars）+ a.txt。
const tmplYAML = "manifest_version: 1\n"

func projectWithState(t *testing.T, tmpl state.TemplateRef, files map[string]state.FileState) string {
	t.Helper()
	dir := t.TempDir()
	st := state.New("test")
	st.Template = tmpl
	st.Files = files
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustLoadState(t *testing.T, dir string) *state.State {
	t.Helper()
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// mustContent 读文件内容。
func mustContent(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// renderEntryT 返回缓存里唯一的渲染快照条目目录（render/<familyKey>）。
func renderEntryT(t *testing.T, cacheRoot string) string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(cacheRoot, "render"))
	if err != nil {
		t.Fatalf("读快照 render/ 目录: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("render/ 下条目 = %d 个，want 1（%v）", len(ents), ents)
	}
	return filepath.Join(cacheRoot, "render", ents[0].Name())
}

// isolateCacheT 把用户缓存目录指向空临时目录（darwin 走 HOME、Linux 走
// XDG_CACHE_HOME），返回此时的 DefaultCacheRoot（尚不存在）。
func isolateCacheT(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg"))
	root, err := template.DefaultCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("缓存根应尚不存在: %v", err)
	}
	return root
}

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

// sessionT 构造只含 P2 所需字段的 session（测试专用）。loadBase 阶段化
// （M2a/P2，签名收 s *session）后测试调用点的机械连带——各站点原四实参
// (dir, st, cacheRoot, readOnly) 原样透传，断言零改动。
func sessionT(dir string, st *state.State, cacheRoot string, dryRun bool) *session {
	return &session{dir: dir, st: st, cacheRoot: cacheRoot, dryRun: dryRun}
}
