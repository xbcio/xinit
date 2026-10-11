package backflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xbcio/xinit/internal/state"
)

// defaultOutDir 是 `--out` 的默认值（相对项目根；§20.8）。
const defaultOutDir = ".xinit/backflow"

// ResolveOutDir 解析 `--out`（§20.8）：默认 `.xinit/backflow/`；项目内相对路径
// 允许；**项目外必须绝对路径**（path.Clean 校验、拒绝 `..` 穿越——§9.5 精神：
// 产物是交付物、不是项目内容，故不强制项目内，但相对形态只许落在项目里，
// 免得产物被静默丢到项目外）。
func ResolveOutDir(projectDir, out string) (string, error) {
	if out == "" {
		return filepath.Join(projectDir, filepath.FromSlash(defaultOutDir)), nil
	}
	if filepath.IsAbs(out) {
		// 绝对路径自带「写在项目外」的显式授权；Clean 折掉 `..` 段。
		return filepath.Clean(out), nil
	}
	clean := filepath.Clean(out)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("`--out %s` 越出项目（拒绝 `..` 穿越）：项目外的产物目录必须显式写成绝对路径（如 `--out /tmp/patches`）", out)
	}
	return filepath.Join(projectDir, clean), nil
}

// WriteArtifact 写产物并返回其绝对路径（§20.6）。name 是产物文件路径
// （`filepath.Join(ResolveOutDir(...), ArtifactName(...))`）；相对形态按 projectDir
// 解析，越出项目即拒（`..` 穿越；纵深防御，挡住调用方拼错路径）。
//
// **`dryRun` 全链零写**：只返回路径，不建目录、不加锁（对齐 `update --dry-run`
// 的只读惯例，§20.8）。真写时先 `state.LockProject`（同项目一次只跑一个 xinit，
// 拿不到锁报错退出、不排队），再走 `state.WriteFileAtomic`（原子替换）。
// 不自动清理旧补丁（与 `backup/` 的「不覆盖、不自动清理」一致）——文件名的
// 模板短 sha 已保证不同版本是不同产物。
func WriteArtifact(projectDir string, dryRun bool, name string, patch []byte) (path string, err error) {
	abs := name
	if !filepath.IsAbs(abs) {
		clean := filepath.Clean(name)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("产物路径 %q 相对 %s 越出项目（拒绝 `..` 穿越）", name, projectDir)
		}
		abs = filepath.Join(projectDir, clean)
	}
	if dryRun {
		return abs, nil
	}
	lock, err := state.LockProject(projectDir)
	if err != nil {
		return "", err
	}
	defer lock.Unlock()
	if err := state.WriteFileAtomic(abs, patch, 0o644); err != nil {
		return "", err
	}
	return abs, nil
}
