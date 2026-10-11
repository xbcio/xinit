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
		os.Setenv("HOME", home)
		os.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	}

	testscript.Main(m, map[string]func(){
		// xinit：与真实二进制的退出码一致（testscript 的 ! 只能断言非零，
		// 具体码用下面的 xinit-code）。
		"xinit": func() {
			if err := Execute(); err != nil {
				os.Exit(exitCode(err))
			}
		},
		// xinit-code：跑同一条路径并把退出码打到 stdout（`exit=N`），随后
		// 正常返回（进程退 0）——go-internal v1.16.0 没有断言具体退出码的
		// 原生语法，这是 M2 验收（退出码 2/5）唯一可靠的断言方式。
		"xinit-code": func() {
			code := 0
			if err := Execute(); err != nil {
				code = exitCode(err)
			}
			fmt.Printf("exit=%d\n", code)
		},
	})
}

func TestScripts(t *testing.T) {
	// 每条脚本统一隔离用户缓存（M10 前置）：HOME/XDG_CACHE_HOME 都指到
	// $WORK 下。注意 Env.Setenv 不做 $ 展开（实测）——必须用
	// env.Getenv("WORK") 拼绝对路径；脚本级 env HOME=… 在 Setup 之后
	// 追加，os/exec dedupEnv 后者胜，既有脚本的自设值仍生效覆盖。
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			home := filepath.Join(env.Getenv("WORK"), "home")
			env.Setenv("HOME", home)
			env.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			return nil
		},
	})
}
