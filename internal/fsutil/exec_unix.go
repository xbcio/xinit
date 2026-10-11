//go:build unix

package fsutil

// execSupportedOS：unix 系（darwin/linux/*BSD/…）均有 x 位语义。
func execSupportedOS() bool { return true }
