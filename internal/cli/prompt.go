package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xbcio/xinit/internal/hooks"
)

// promptSession 是一次运行内的交互会话（§19.2.3）：所有提问共享同一个
// bufio.Reader——多问题各建 reader 会因预读吞掉后续回答。
type promptSession struct {
	r   *bufio.Reader // 唯一 reader：与 hooks.Confirm 共用
	out io.Writer     // 恒为 os.Stdout（§19.2.5）
	eof bool          // EOF 锁存：置位后不再打印提示，直接按 eof 返回
}

// newPromptSession 构造会话（IO 可注入：单测用 strings.NewReader/bytes.Buffer）。
func newPromptSession(in io.Reader, out io.Writer) *promptSession {
	return &promptSession{r: bufio.NewReader(in), out: out}
}

// interactiveSession 依「--interactive ∨ stdin 是终端」创建会话；否则返回 nil。
// nil 即「本次运行不可交互」：调用点一律跳过提问，不读 stdin、不打印任何交互
// 文本。创建只做一次 ioctl 判定（不读 stdin、不打印），故
// `--interactive --dry-run` 不会碰 stdin（§19.2.2）。
func interactiveSession(force bool) *promptSession {
	if !force && !hooks.Interactive(os.Stdin) {
		return nil
	}
	return newPromptSession(os.Stdin, os.Stdout)
}

// askLine 打印 q 后读一行；返回归一后的行（去行尾换行 + TrimSpace）与是否 EOF。
// err 仅为「非 EOF 的读错误」。EOF 锁存：一旦 EOF，此后不再打印提示。
func (s *promptSession) askLine(q string) (line string, eof bool, err error) {
	if s.eof {
		return "", true, nil
	}
	if _, err := fmt.Fprint(s.out, q); err != nil {
		return "", false, err
	}
	raw, err := s.r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, err
	}
	line = strings.TrimSpace(raw)
	if err == io.EOF {
		s.eof = true
		return line, true, nil // 半行内容有效（Ctrl-D 打在非空行上，§19.2.3）
	}
	return line, false, nil
}

// askYesNo 打印 q 后读一行；y/Y/yes/YES（不区分大小写）为真，其余与 EOF 为假。
func (s *promptSession) askYesNo(q string) (yes bool, eof bool, err error) {
	line, eof, err := s.askLine(q)
	if err != nil || eof {
		return false, eof, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, false, nil
	}
	return false, false, nil
}
