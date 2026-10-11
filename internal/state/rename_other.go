//go:build !windows

package state

// renameRetryableErr：unix 上 rename 无「杀软/索引器持句柄」类瞬态错误
// （目录权限类重试无用）——恒 false ⇒ 重试循环零成本（§22.8）。
func renameRetryableErr(error) bool { return false }
