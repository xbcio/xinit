// Package state 读写 .xinit/ 下的状态。
//
// .xinit/ 是本地目录，不进版本控制。
package state

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// 两个注入接缝（§22.18 #8）：单测覆写以模拟 Windows 瞬态/只读目标语义
// （defer 恢复纪律）。
var (
	renameFile      = os.Rename
	renameRetryable = renameRetryableErr
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

	if err := renameWithRetry(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	tmpName = "" // 已改名，defer 不要再删
	return nil
}

// 瞬态 rename 失败的有界重试（§22.8）：Windows 上杀软/索引器会持句柄
// 数十 ms；最坏 ~270ms 后放弃。unix 分类器恒 false ⇒ 零重试。
const (
	renameAttempts = 10
	renameBackoff  = 30 * time.Millisecond
)

// renameWithRetry：瞬态错误有界重试；ACCESS_DENIED 且目标只读 → 清目标
// 只读位 → 重试一次；仍失败 → fail-closed 专用文案（§22.8）。
func renameWithRetry(tmp, target string) error {
	var lastErr error
	for attempt := 0; attempt < renameAttempts; attempt++ {
		lastErr = renameFile(tmp, target)
		if lastErr == nil {
			return nil
		}
		if !renameRetryable(lastErr) {
			break
		}
		if attempt < renameAttempts-1 {
			time.Sleep(renameBackoff)
		}
	}
	if os.IsPermission(lastErr) {
		if fi, err := os.Lstat(target); err == nil && fi.Mode().IsRegular() &&
			fi.Mode().Perm()&0o200 == 0 {
			if cerr := os.Chmod(target, fi.Mode().Perm()|0o200); cerr == nil {
				// 诊断取**清位后这次** rename 的 err：只读位已清除仍失败，原因
				// 必须来自最后一次尝试（循环的 lastErr 可能已被清位修复、不再
				// 是真实成因）。
				rerr := renameFile(tmp, target)
				if rerr == nil {
					return nil
				}
				return fmt.Errorf("%w（目标只读位已清除、写入仍失败）", rerr)
			}
		}
	}
	return lastErr
}
