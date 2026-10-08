package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// setupTemplate 建一个最小的模板目录。
func setupTemplate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"xinit.yml": `manifest_version: 1
vars:
  - name: project_name
    default: "MyApp"
  - name: app_slug
    default: "{{ .project_name | lower }}"
    pattern: '^[a-z][a-z0-9-]*$'
substitute:
  - from: "Xkitora"
    to: "{{ .project_name }}"
    paths: ["**/*.go"]
  - from: "xkitora"
    to: "{{ .app_slug }}"
    paths: ["go.mod"]
rename:
  - from: "xkitora"
    to: "{{ .app_slug }}"
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
`,
		"xkitora/main.go": `package main

// Xkitora 是模板里的一个产品名
func main() {}
`,
		"go.mod":          "module xkitora\n\ngo 1.24\n",
		"README.md.tmpl": "# {{ .project_name }}\n",
		"run.sh":          "#!/bin/sh\necho Xkitora\n",
	}
	for rel, data := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if rel == "run.sh" {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInitGeneratesTree(t *testing.T) {
	tmpl := setupTemplate(t)
	dest := t.TempDir()

	err := Init(dest, tmpl, "", "", false, true, false, true)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// 目录改名
	if _, err := os.Stat(filepath.Join(dest, "myapp", "main.go")); err != nil {
		t.Errorf("目录未按 app_slug 改名: %v", err)
	}
	// 内容替换
	data, err := os.ReadFile(filepath.Join(dest, "myapp", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "MyApp") {
		t.Errorf("Xkitora 未替换: %q", data)
	}
	if strings.Contains(string(data), "Xkitora") {
		t.Errorf("替换不彻底: %q", data)
	}
	// go.mod 只有小写规则
	gomod, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gomod) != "module myapp\n\ngo 1.24\n" {
		t.Errorf("go.mod = %q", gomod)
	}
	// render + strip_suffix
	readme, err := os.ReadFile(filepath.Join(dest, "README.md"))
	if err != nil {
		t.Fatalf("README.md 未生成: %v", err)
	}
	if string(readme) != "# MyApp\n" {
		t.Errorf("README.md = %q", readme)
	}
	// exec 位
	info, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh 权限 = %v, want 0755", info.Mode().Perm())
	}
	// xinit.yml 本身不输出
	if _, err := os.Stat(filepath.Join(dest, "xinit.yml")); err == nil {
		t.Error("xinit.yml 不应输出到派生项目")
	}
}

func TestInitWritesStateAndBase(t *testing.T) {
	tmpl := setupTemplate(t)
	dest := t.TempDir()

	if err := Init(dest, tmpl, "", "", false, true, false, true); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, ".xinit", "state.json")); err != nil {
		t.Errorf("state.json 未写入: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".xinit", "base", "go.mod")); err != nil {
		t.Errorf("base 树未写入: %v", err)
	}
	// base 必须与派生树一致（init 时二者相等）
	want, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, ".xinit", "base", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("init 后 base 应等于派生内容:\n%q\n%q", got, want)
	}
}

func TestInitRefusesNonEmptyDir(t *testing.T) {
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "existing.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Init(dest, tmpl, "", "", false, true, false, true)
	if err == nil {
		t.Fatal("非空目录应报错")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("错误信息应提示 --force: %v", err)
	}
	// 预存内容必须完好
	data, _ := os.ReadFile(filepath.Join(dest, "existing.txt"))
	if string(data) != "mine" {
		t.Error("失败时不应改动预先存在的文件")
	}
}

func TestInitTreatsGitOnlyDirAsEmpty(t *testing.T) {
	// git clone <空仓> && xinit init . —— 目录里只有 .git，
	// 按字面算非空，但不该要求 --force
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Init(dest, tmpl, "", "", false, true, false, true); err != nil {
		t.Fatalf("只含 .git 的目录应视为空目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Error(".git 必须保留")
	}
}

func TestInitForceSkipsExistingFilesWithoutOverwriting(t *testing.T) {
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "go.mod"), []byte("MINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dest, tmpl, "", "", false, true, true, true); err != nil {
		t.Fatalf("--force 应继续执行: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "MINE\n" {
		t.Errorf("--force 不应覆盖已有文件，实际 %q", got)
	}
}

func TestInitForceSkipExcludesFromStateAndBase(t *testing.T) {
	// 被 --force 跳过的路径既不该进 state.files，也不该进 base/。
	// 只排除前者会留下一个孤儿 base 文件，而它描述的内容并不在工作树里，
	// 下一轮 update 会把这份不存在的内容当成 legitimate 的基线去比较。
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "go.mod"), []byte("MINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dest, tmpl, "", "", false, true, true, true); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, ".xinit", "base", "go.mod")); err == nil {
		t.Error("被跳过的路径不应出现在 base/")
	}

	st, err := state.Load(dest)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := st.Files["go.mod"]; ok {
		t.Error("被跳过的路径不应记入 state.files")
	}
}

func TestInitConfigOverrides(t *testing.T) {
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(cfg, []byte("project_name: Xdesk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dest, tmpl, cfg, "", false, true, false, true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "xdesk", "main.go")); err != nil {
		t.Errorf("配置覆盖未生效: %v", err)
	}
}

func TestInitRejectsUnknownConfigKey(t *testing.T) {
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(cfg, []byte("nope: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Init(dest, tmpl, cfg, "", false, true, false, true)
	if err == nil {
		t.Fatal("配置里出现清单未声明的变量应报错，而不是静默忽略")
	}
}

func TestInitFailureLeavesNoPartialTree(t *testing.T) {
	// 让写入在**中途**失败：模板要建目录 myapp/，而目标里已有一个同名
	// 普通文件。按 Dst 的字节序，README.md 与 go.mod 会先写成功，然后
	// myapp/main.go 失败 —— 已写入的那两个必须被回删。
	//
	// 用一个在 EvalVars 阶段就失败的清单是测不到回删的：那时还什么都没写。
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "myapp"), []byte("I am a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dest, tmpl, "", "", false, true, true, true); err == nil {
		t.Fatal("myapp 是普通文件、无法在其下建目录，写入应失败")
	}

	// 预先存在的内容必须完好
	data, readErr := os.ReadFile(filepath.Join(dest, "myapp"))
	if readErr != nil {
		t.Fatalf("预先存在的文件不应被删除: %v", readErr)
	}
	if string(data) != "I am a file\n" {
		t.Errorf("预先存在的文件被改动: %q", data)
	}

	// 本次创建的路径必须全部回删，dest 里只剩那一个预存文件
	ents, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "myapp" {
		t.Errorf("失败后 dest 应只剩预先存在的 myapp，实际 %v", names)
	}
}

func TestRollbackRemovesXinitOnlyIfCreated(t *testing.T) {
	// 本次创建的 .xinit/ 必须回删 —— 残留的 state.json 会宣称一棵已被
	// 回删的树；预先存在的 .xinit/ 属于用户，不得删除。
	d1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d1, state.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d1, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollback(d1, []string{"a.txt"}, false)
	if _, err := os.Stat(filepath.Join(d1, state.DirName)); err == nil {
		t.Error("本次创建的 .xinit/ 必须回删")
	}
	if _, err := os.Stat(filepath.Join(d1, "a.txt")); err == nil {
		t.Error("created 里的文件必须回删")
	}

	d2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d2, state.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d2, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollback(d2, []string{"a.txt"}, true)
	if _, err := os.Stat(filepath.Join(d2, state.DirName)); err != nil {
		t.Errorf("预先存在的 .xinit/ 不得被删除: %v", err)
	}
}

func TestRollbackKeepsPreexistingEmptyDir(t *testing.T) {
	// --force 场景：dest 里有用户预先存在的空目录，回删不得扫描 dest
	// 把「看起来空」的目录一并删掉
	dest := t.TempDir()
	mine := filepath.Join(dest, "keepme")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dest, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "sub", "created.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rollback(dest, []string{"sub/created.txt"}, true)

	if _, err := os.Stat(mine); err != nil {
		t.Error("预先存在的空目录不得被回删")
	}
	if _, err := os.Stat(filepath.Join(dest, "sub")); err == nil {
		t.Error("本次创建的空父目录应被清理")
	}
}

func TestInitFailureKeepsPreexistingXinitFile(t *testing.T) {
	// .xinit 预先存在（普通文件）⇒ state.Save 的 MkdirAll 必失败：
	// 端到端验证「失败时工作树回删、用户的 .xinit 不动」
	tmpl := setupTemplate(t)
	dest := t.TempDir()
	xinitPath := filepath.Join(dest, state.DirName)
	if err := os.WriteFile(xinitPath, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dest, tmpl, "", "", false, true, true, true); err == nil {
		t.Fatal(".xinit 是普通文件时写 state 必失败")
	}

	data, readErr := os.ReadFile(xinitPath)
	if readErr != nil || string(data) != "mine" {
		t.Errorf("预先存在的 .xinit 不得被删除或改动: %v %q", readErr, data)
	}
	ents, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != state.DirName {
			t.Errorf("失败后 dest 里应只剩预先存在的 .xinit，实际还有 %s", e.Name())
		}
	}
}

func TestLoadConfigRejectsNonStringValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(p, []byte("feature: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("非字符串的配置值应报错")
	}
	if !strings.Contains(err.Error(), "字符串") {
		t.Errorf("错误信息应说明需要字符串: %v", err)
	}
}

func TestLoadConfigFlatMap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(p, []byte("project_name: Xdesk\napp_slug: xdesk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got["project_name"] != "Xdesk" || got["app_slug"] != "xdesk" {
		t.Errorf("= %+v", got)
	}
}
