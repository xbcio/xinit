package fsutil

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Fold 把一个路径名映射到用于碰撞检测的规范键。
//
// 两步：NFC 规范化，然后小写。
//
// 大小写不敏感的文件系统（APFS 默认、Windows）上两个折叠相等的名字**是
// 同一个文件** —— 实测 APFS 上 `mv license LICENSE` 直接成功、不报错、
// 不产生第二个文件。所以模板新增 LICENSE 时的原子写 rename 打的就是用户
// 那个 license。
//
// NFC：APFS 与 HFS+ 对规范形式也不敏感，NFD/NFC 写法指向同一个 inode。
func Fold(s string) string {
	return strings.ToLower(norm.NFC.String(s))
}

// FoldEqual 报告两个路径名是否指向同一个文件。
func FoldEqual(a, b string) bool {
	if a == b {
		return true
	}
	return Fold(a) == Fold(b)
}
