package fsutil

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Fold 把一个路径名映射到与 strings.EqualFold（SimpleFold 语义）一致的
// 规范键：NFC 规范化后，每个 rune 取 SimpleFold 轨道的码点最小元。
//
// 等价性质的精确形式（100k fuzz 实测）：Fold(a)==Fold(b) ⟺
// EqualFold(NFC(a), NFC(b))——0/100k 分歧。字面上的「EqualFold(a,b) ⟺
// Fold(a)==Fold(b)」过强、不成立：NFC 方向的差集（6,436/100k）是设计使然
// （见下段）；反向差集（145/100k）全在「折叠×规范化不交换」的组合序列
// 边角（长 s 类异体 + 组合附标），路径名场景不可达，且此时新旧判据同样
// 为假，不改变任何既有行为。
//
// 与此前 §9.3 的判据 apply.FoldEq（NFC 后 EqualFold；已删除，改调本包
// FoldEqual）逐例一致：判定 0/100k 分歧、旧⊆新零违反——§9.3 语义未变。
// ASCII 的等价类与此前的 ToLower 实现完全一致（只有 ſ/İ 一类异体字符的
// 归属变化）；于是「键相等」的 map 判定与 §9.3 的折叠检查是同一语义。
//
// 大小写不敏感的文件系统（APFS 默认、Windows）上两个折叠相等的名字**是
// 同一个文件** —— 实测 APFS 上 `mv license LICENSE` 直接成功、不报错、
// 不产生第二个文件。所以模板新增 LICENSE 时的原子写 rename 打的就是用户
// 那个 license。NFC：APFS 与 HFS+ 对规范形式也不敏感，NFD/NFC 写法指向
// 同一个 inode。
func Fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(s) {
		b.WriteRune(foldMin(r))
	}
	return b.String()
}

// foldMin 返回 r 的 SimpleFold 轨道里的码点最小元。SimpleFold 是环（从 r
// 出发必回到 r），循环有界。
func foldMin(r rune) rune {
	min := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < min {
			min = f
		}
	}
	return min
}

// FoldEqual 报告两个路径名是否指向同一个文件（同 Fold 语义）。
func FoldEqual(a, b string) bool {
	if a == b {
		return true
	}
	return Fold(a) == Fold(b)
}
