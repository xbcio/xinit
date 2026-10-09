package cli

import (
	"errors"
	"fmt"
	"os"
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
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
	})
}
