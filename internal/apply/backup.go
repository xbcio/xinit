// Package apply 执行 §10.2 第 8 步：备份（§10.1）、写前检查（§9.3/§9.5）、
// 先删后写与 conflicts/ 重建。
package apply

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xbcio/xinit/internal/state"
)

// Backup 把计划内将被写入/删除的路径的原内容与权限复制到
// .xinit/backup/<stamp>/，返回**相对项目根**的目录（无目标 → ""）。
//
// §10.1：每次 update 一个新时间戳目录、不覆盖、不自动清理；同名（同一秒内
// 第二次运行）追加 -2/-3……绝不覆盖——固定单目录会让「第 8 步中途失败后
// 重跑」用已被改写的磁盘做备份，第一次运行前的原文就永远丢了（D5）。
//
// stamp 由调用方在本轮 update 开始时算一次（所有备份落在同一目录）。
func Backup(projectDir, stamp string, targets []string) (string, error) {
	if len(targets) == 0 {
		return "", nil
	}
	dir, err := uniqueBackupDir(projectDir, stamp)
	if err != nil {
		return "", err
	}
	for _, rel := range targets {
		src := filepath.Join(projectDir, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("备份 %s: %w", rel, err)
		}
		if !fi.Mode().IsRegular() {
			continue // 目录/符号链接：本轮不会被改动，不备份（§10.1 只谈文件）
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return "", fmt.Errorf("备份读取 %s: %w", rel, err)
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		// 原子写 + 原样权限：备份自己也要抗中途失败
		if err := state.WriteFileAtomic(dst, data, fi.Mode().Perm()); err != nil {
			return "", fmt.Errorf("备份写入 %s: %w", rel, err)
		}
	}
	return relToProject(projectDir, dir), nil
}

// uniqueBackupDir 取 .xinit/backup/<stamp>，已存在则依次试 <stamp>-2、-3……
func uniqueBackupDir(projectDir, stamp string) (string, error) {
	base := filepath.Join(projectDir, state.DirName, "backup")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("创建备份目录 %s: %w", base, err)
	}
	for i := 1; ; i++ {
		name := stamp
		if i > 1 {
			name = fmt.Sprintf("%s-%d", stamp, i)
		}
		dir := filepath.Join(base, name)
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("创建备份目录 %s: %w", dir, err)
		}
	}
}

// relToProject 把 dir 表述为项目根下的 slash 相对路径（报告用）。
func relToProject(projectDir, dir string) string {
	rel, err := filepath.Rel(projectDir, dir)
	if err != nil {
		return dir
	}
	return filepath.ToSlash(rel)
}
