//go:build !unix

package apply

// currentUmask：非 unix 平台没有 umask 概念（权限位按字面应用）。
func currentUmask() uint32 { return 0 }
