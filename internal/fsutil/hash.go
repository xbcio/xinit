package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// HashAlgo 把比较 hash 的规则版本化。
//
// 改动这个常量会让所有已落盘状态的 hash 失效，必须同时改 state.json 的
// version 并写迁移 —— 不要为了"顺手优化"而改它。
const HashAlgo = "sha256/normlf-v1"

// CompareHash 计算用于三方比较的 hash。
//
// binary 为 true 时用原始字节；否则先做 CRLF→LF 归一化。归一化让
// core.autocrlf=true 的 Windows 检出不至于把所有文本文件误判成"本地改过"。
//
// 已知取舍：模板单纯把某文件从 CRLF 改成 LF 不会被同步（判定为
// theirs==base）。规范 §5.3 已声明。
func CompareHash(content []byte, binary bool) string {
	var sum [32]byte
	if binary {
		sum = sha256.Sum256(content)
	} else {
		sum = sha256.Sum256(ToLF(content))
	}
	return HashAlgo + ":" + hex.EncodeToString(sum[:])
}

// TreeEntry 是 RenderTreeHash 的输入单元。
type TreeEntry struct {
	Src  string // 模板内原路径，/ 分隔
	Kind string // "regular" | "symlink" —— 用字符串而非数字，可读且稳定
	Exec bool
	Hash string
}

// RenderTreeHash 计算一棵模板树的版本锚点，供 dir 源使用（§5.4）。
//
// 编码：按 Src 的字节序排序后，逐条写入
//
//	src \0 kind \0 exec \0 hash \n
//
// 分隔符与类型字段都是必需的：若只是把路径与内容首尾相接，"ab" + "" 与
// "a" + "b" 会得到相同的 hash，改名和 mode 变化也检测不出来。
//
// 排序用 sort.Strings（字节序），不用本地化排序 —— 排序结果会进入 hash。
func RenderTreeHash(entries []TreeEntry) string {
	sorted := make([]TreeEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Src < sorted[j].Src })

	h := sha256.New()
	for _, e := range sorted {
		exec := "0"
		if e.Exec {
			exec = "1"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", e.Src, e.Kind, exec, e.Hash)
	}
	return HashAlgo + ":" + hex.EncodeToString(h.Sum(nil))
}
