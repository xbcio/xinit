// Package merge 封装三方合并（规范 §8.6）。
//
// 只做一件事：把三方内容喂给 `git merge-file` 并按其退出码分类。
// 谁在什么时候跑（§10.2 第 3 步）、合并结果要不要写回、EOL 怎么还原，
// 都是调用方的事——这里保持无状态、无磁盘树知识。
package merge

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xbcio/xinit/internal/fsutil"
)

// Outcome 是合并的分类结果。
type Outcome int

const (
	Clean     Outcome = iota // rc 0：干净合并
	Conflicts                // rc 1–127：冲突，值即冲突块数
	Error                    // rc ≥128（如二进制 255）或进程被信号终止
)

type Result struct {
	Bytes     []byte // LF 归一化的合并输出（Conflicts 时含冲突标记；Error 时为空）
	Outcome   Outcome
	Conflicts int // = git 退出码，即冲突块数（仅 Conflicts 时非零）
}

// ThreeWay 以 ours 为基准方合并 ours/base/theirs。
//
// 命令与判据（§8.6，已在 git 2.50.1 实测）：
//   - `git merge-file --diff3 -p --marker-size=7 -L ours -L base -L theirs`；
//   - 三方先经 fsutil.ToLF 归一再算（§9.1 的三处「按二进制处理」之一；
//     混合 EOL 的 ToLF 是无操作，但那种路径根本到不了这里——§8.2 行 6）；
//   - rc 0 → Clean；1–127 → Conflicts；≥128 或信号（ExitCode()==-1）→ Error。
//     （实测：两处冲突 rc=2；含 NUL 的文件 git 直接拒绝，rc=255、stdout 空。）
//
// 命令在新建的临时目录里执行（cmd.Dir）：`git merge-file` 会读环境仓库的
// 属性/合并策略，若继承派生项目的 .gitattributes（如 `*.md merge=ours`），
// 同一个模板在「在项目里跑」与「在别处跑」会合并出不同结果。临时目录不是
// 仓库，行为只由输入字节决定。
func ThreeWay(ours, base, theirs []byte) (Result, error) {
	dir, err := os.MkdirTemp("", "xinit-merge-")
	if err != nil {
		return Result{}, fmt.Errorf("创建合并临时目录: %w", err)
	}
	defer os.RemoveAll(dir)

	paths := make([]string, 3)
	for i, in := range [][]byte{ours, base, theirs} {
		paths[i] = filepath.Join(dir, []string{"ours", "base", "theirs"}[i])
		if err := os.WriteFile(paths[i], fsutil.ToLF(in), 0o600); err != nil {
			return Result{}, fmt.Errorf("写合并输入 %s: %w", paths[i], err)
		}
	}

	cmd := exec.Command("git", "merge-file", "--diff3", "-p", "--marker-size=7",
		"-L", "ours", "-L", "base", "-L", "theirs",
		paths[0], paths[1], paths[2])
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if cmd.ProcessState == nil {
		return Result{}, fmt.Errorf("启动 git merge-file: %w\n%s", runErr, strings.TrimSpace(stderr.String()))
	}
	switch code := cmd.ProcessState.ExitCode(); {
	case code == 0:
		return Result{Bytes: stdout.Bytes(), Outcome: Clean}, nil
	case code >= 1 && code <= 127:
		return Result{Bytes: stdout.Bytes(), Outcome: Conflicts, Conflicts: code}, nil
	default: // 信号（-1）或 ≥128
		return Result{Outcome: Error}, nil
	}
}
