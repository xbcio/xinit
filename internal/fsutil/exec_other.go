//go:build !unix

package fsutil

// execSupportedOS：非 unix（含 Windows）无 x 位语义——Windows 上 os.Chmod
// 只影响只读位，info.Mode()&0o111 恒 false（§22.6）。
func execSupportedOS() bool { return false }
