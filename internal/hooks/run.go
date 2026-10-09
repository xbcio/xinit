package hooks

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Result 是一次钩子执行的结果。
type Result struct {
	Output   []byte // stdout 与 stderr 的合并输出
	ExitCode int
}

// Run 把钩子块整块交给一个 sh -c（§3.1：不逐行），工作目录 = 项目根。
//
// 返回的 error 只表示「命令起不来」——sh 缺失（§3.1 要求明确报错，不静默
// 跳过）、fork 失败等；命令本身非零退出是 Result.ExitCode ≠ 0、error == nil。
func Run(projectDir, block string) (Result, error) {
	cmd := exec.Command("sh", "-c", block)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Result{Output: out, ExitCode: ee.ExitCode()}, nil
		}
		return Result{Output: out}, fmt.Errorf(
			"执行钩子失败（需要 sh，但无法启动 %q）：%w；v1 在无 sh 的平台不支持钩子（§3.1）", "sh", err)
	}
	return Result{Output: out, ExitCode: 0}, nil
}

// Interactive 报告 f 是否连着一个字符设备（TTY）。非 TTY（管道、CI）下
// 不提示交互，直接走 --yes / 退出码 4 的路径。
func Interactive(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Confirm 向 out 打印提示并从 in 读一行；y/Y/yes（不区分大小写）为真。
// EOF 与其他输入一律为假（不出错）。
func Confirm(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "信任并执行？[y/N] ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
