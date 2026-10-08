// Package state 读写 .xinit/ 下的状态。
//
// .xinit/ 是本地目录，不进版本控制。
package state

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic 原子地写一个文件。
//
// 三步：写临时文件 → chmod → rename。
//
// **chmod 不能省。** 实测 os.CreateTemp 建出的文件权限是 0600，直接
// rename 会静默丢掉目标的权限位 —— 用户本地 chmod +x 的脚本会在一次
// update 之后变成 0600，而报告说"权限未改动"。
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()

	// 失败路径统一清理
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写临时文件: %w", err)
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		tmp.Close()
		return fmt.Errorf("设置权限: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	tmpName = "" // 已改名，defer 不要再删
	return nil
}
