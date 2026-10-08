package main

import "fmt"

// Xkitora 在 **/*.go 的范围内，会被替换成 project_name；
// 这个 xkitora 不在范围内（小写规则只覆盖 go.mod 与 *.sh），保持原样。
func main() {
	fmt.Println("Xkitora")
}
