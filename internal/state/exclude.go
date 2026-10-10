package state

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// excludeHeader 是写入 .git/info/exclude 时的说明。
var excludeHeader = []byte("# xinit 的本地状态，不进版本控制\n")

// EnsureGitExcluded 幂等地把 .xinit/ 写进 .git/info/exclude。
//
// 为什么不是 .gitignore：.gitignore 是模板带过来的 tracked 文件，往里加
// 一行就制造了一个永久的本地修改，反成三方合并的冲突源。
//
// 非 git 仓时静默返回 nil —— 调用点会另行给出警告。
func EnsureGitExcluded(projectDir string) error {
	p, ok := GitExcludePath(projectDir)
	if !ok {
		return nil
	}

	existing, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s: %w", p, err)
	}

	entry := DirName + "/"
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil // 已存在，什么都不做
		}
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 {
		if !bytes.HasSuffix(existing, []byte("\n")) {
			// 不补换行的话，追加的规则会和上一行粘成一条
			buf.WriteByte('\n')
		}
	} else {
		buf.Write(excludeHeader)
	}
	buf.WriteString(entry)
	buf.WriteByte('\n')

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("创建 %s: %w", filepath.Dir(p), err)
	}
	return WriteFileAtomic(p, buf.Bytes(), 0o644)
}

// GitExcludePath 返回 .git/info/exclude 的绝对路径（worktree/submodule 安全：git rev-parse --git-path）。
//
// 必须走 `git rev-parse --git-path` —— worktree 和 submodule 里
// .git 是一个**文件**而不是目录，直接拼 .git/info/exclude 会写错地方。
func GitExcludePath(projectDir string) (string, bool) {
	cmd := exec.Command("git", "-C", projectDir, "rev-parse", "--git-path", "info/exclude")
	out, err := cmd.Output()
	if err != nil {
		return "", false // 不是 git 仓
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDir, p)
	}
	return p, true
}

// IsGitTracked 报告 relPath 是否已被 git 跟踪。
//
// 用于在写 .xinit/ 之前挡住"用户误把状态目录提交了"的情况 ——
// 那会让状态进入版本历史，然后在别的机器上产生不一致的 base。
func IsGitTracked(projectDir, relPath string) (bool, error) {
	cmd := exec.Command("git", "-C", projectDir, "ls-files", "--error-unmatch", "--", relPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// ls-files 用退出码 1 表示"未匹配"，不是错误
		return false, nil
	}
	// 不是 git 仓（或 git 不可用）
	if _, ok := GitExcludePath(projectDir); !ok {
		return false, nil
	}
	return false, fmt.Errorf("git ls-files: %w\n%s", err, strings.TrimSpace(stderr.String()))
}
