package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// exitCode 把 Execute 的错误映射为进程退出码；Silent 的错误不补打印
// （与 main.go 同语义：报告已经写全，入口不再补行）。
func exitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		if !ee.Silent {
			os.Stderr.WriteString("xinit: " + err.Error() + "\n")
		}
		return ee.Code
	}
	os.Stderr.WriteString("xinit: " + err.Error() + "\n")
	return 1
}

// cacheIsolationEnv 返回用户缓存隔离的三变量接线（§22.13）：darwin/Linux 据
// HOME/XDG_CACHE_HOME、Windows 据 %LocalAppData%（os.UserCacheDir 口径；darwin
// 上该变量无读者，零影响）。**两处调用点基不同，不得混用**：
// TestMain → os.TempDir()/xinit-test-home；Setup → $WORK/home。
func cacheIsolationEnv(home string) [][2]string {
	return [][2]string{
		{"HOME", home},
		{"XDG_CACHE_HOME", filepath.Join(home, ".cache")},
		{"LocalAppData", filepath.Join(home, "AppData", "Local")},
	}
}

// repoChangelog 读仓库根的 CHANGELOG.md。路径取自本源文件的编译期位置
// （runtime.Caller）：脚本里 xinit 子进程的 cwd 是脚本工作目录，相对路径不可用。
func repoChangelog() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("无法定位测试源文件")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "CHANGELOG.md"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// loadRepoChangelog 是 repoChangelog 的测试封装。
func loadRepoChangelog(t *testing.T) string {
	t.Helper()
	doc, err := repoChangelog()
	if err != nil {
		t.Fatalf("读取仓库 CHANGELOG.md: %v", err)
	}
	return doc
}

// injectChangelog 模拟 main.go 的 go:embed 注入，让脚本里的 xinit 子进程
// 也带着真实的变更日志（读不到则保持为空，只有依赖它的脚本会红）。
func injectChangelog() {
	if doc, err := repoChangelog(); err == nil {
		Changelog = doc
	}
}

func TestMain(m *testing.M) {
	// 测试隔离（M10 前置）：把用户缓存目录（os.UserCacheDir 据 HOME/
	// XDG_CACHE_HOME 推导）引到一次性暂存目录，Go 单测不再脏写真实缓存。
	//
	// 分派判据与 testscript.Main 的派发同源，含 Windows .exe 后缀裁剪：
	// 脚本跑 xinit/xinit-code 时，testscript 会把本测试二进制复制为
	// …/bin/xinit 并 exec，本 TestMain 在该子进程里先于分派执行——此时
	// env 归脚本管（含 Params.Setup），必须跳过，否则会击穿脚本级隔离。
	name := filepath.Base(os.Args[0])
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".exe")
	}
	switch name {
	case "xinit", "xinit-code":
		// 子进程（复制为 …/bin/xinit 后 exec）：env 归脚本管，跳过。
	default:
		home := filepath.Join(os.TempDir(), "xinit-test-home")
		if err := os.MkdirAll(home, 0o755); err != nil { // 幂等；失败即退
			fmt.Fprintf(os.Stderr, "xinit-test: 创建隔离 HOME %s: %v\n", home, err)
			os.Exit(1)
		}
		for _, kv := range cacheIsolationEnv(home) {
			os.Setenv(kv[0], kv[1])
		}
	}

	testscript.Main(m, map[string]func(){
		// xinit：与真实二进制的退出码一致（testscript 的 ! 只能断言非零，
		// 具体码用下面的 xinit-code）。
		"xinit": func() {
			injectChangelog()
			if err := Execute(); err != nil {
				os.Exit(exitCode(err))
			}
		},
		// xinit-code：跑同一条路径并把退出码打到 stdout（`exit=N`），随后
		// 正常返回（进程退 0）——go-internal v1.16.0 没有断言具体退出码的
		// 原生语法，这是 M2 验收（退出码 2/5）唯一可靠的断言方式。
		"xinit-code": func() {
			injectChangelog()
			code := 0
			if err := Execute(); err != nil {
				code = exitCode(err)
			}
			fmt.Printf("exit=%d\n", code)
		},
	})
}

func TestScripts(t *testing.T) {
	// 每条脚本统一隔离用户缓存（M10 前置）：HOME/XDG_CACHE_HOME/LocalAppData
	// 都指到 $WORK 下（三变量，§22.13）。注意 Env.Setenv 不做 $ 展开（实测）
	// ——必须用 env.Getenv("WORK") 拼绝对路径；脚本级 env HOME=… 在 Setup
	// 之后追加，os/exec dedupEnv 后者胜，既有脚本的自设值仍生效覆盖。
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			// Env.Setenv 不做 $ 展开（实测）——必须 env.Getenv("WORK") 拼绝对路径。
			for _, kv := range cacheIsolationEnv(filepath.Join(env.Getenv("WORK"), "home")) {
				env.Setenv(kv[0], kv[1])
			}
			return nil
		},
		Condition: func(cond string) (bool, error) {
			// casefold：大小写不敏感文件系统（折叠语义）平台 = darwin || windows
			// （§22.13）。内置条件从不走到这里（testscript 先判）。
			if cond == "casefold" {
				return runtime.GOOS == "darwin" || runtime.GOOS == "windows", nil
			}
			return false, fmt.Errorf("unknown condition %q", cond)
		},
	})
}

func TestCacheIsolationEnv(t *testing.T) {
	// 期望值以 POSIX 字面表达原意，再归一为平台形态（helper 用 filepath.Join
	// 构造：Windows 输出反斜杠，FromSlash 在 unix 恒等 ⇒ darwin 零变化）。
	want := map[string]string{
		"HOME":           "/base",
		"XDG_CACHE_HOME": filepath.FromSlash("/base/.cache"),
		"LocalAppData":   filepath.FromSlash("/base/AppData/Local"),
	}
	got := map[string]string{}
	for _, kv := range cacheIsolationEnv("/base") {
		got[kv[0]] = kv[1]
	}
	if len(got) != len(want) {
		t.Fatalf("cacheIsolationEnv 变量数 = %d，want %d（三变量缺一即红）", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("cacheIsolationEnv[%s] = %q，want %q", k, got[k], v)
		}
	}
}
