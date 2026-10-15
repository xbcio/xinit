package cli

import "runtime/debug"

// Version 由构建时注入：
//
//	go build -ldflags "-X github.com/xbcio/xinit/internal/cli.Version=v1.0.0"
//
// （Makefile 的 build / install 目标已接好，取 git describe 的结果）。
// 为空时 versionString 回退到 Go 内嵌的构建信息。
var Version string

// versionString 返回 --version 展示的版本串，保证非空：
//  1. 注入了 Version 就用它；
//  2. 否则取内嵌构建信息：go install ...@vX.Y.Z 得到模块版本；本地构建
//     （模块版本为空或 "(devel)"）记为 "devel"，并在有 VCS 信息时附上
//     "+<7 位提交>"，工作树有改动再加 "-dirty"。
func versionString() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	v := "devel"
	if rev != "" {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		v += "+" + rev
		if dirty {
			v += "-dirty"
		}
	}
	return v
}
