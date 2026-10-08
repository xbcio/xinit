package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadPrune 读取 prune.txt（规范 §8.0）。
//
// 格式：每行一个 Dst glob（§3.2 语义）或精确路径；`#` 开头为注释；空行忽略。
// 返回原始条目（去首尾空白），glob 的编译与展开由调用方做 —— 这里保持
// 「读什么是什么」，让调用方能在报错时带上「来自 prune.txt」的上下文。
func ReadPrune(projectDir string) ([]string, error) {
	p := filepath.Join(projectDir, DirName, "prune.txt")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", p, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}
