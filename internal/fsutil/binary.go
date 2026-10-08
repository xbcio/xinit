package fsutil

import (
	"bytes"
	"unicode/utf8"
)

// binarySniffLen 与 git 的启发式一致：只看前 8192 字节找 NUL。
const binarySniffLen = 8192

// IsBinary 判定内容是否应跳过文本处理。
//
// 两个判据：
//   - 前 8192 字节含 NUL 字节；
//   - 全文不是合法 UTF-8。
//
// 全文校验是必需的：坏字节会让正则匹配与行切分全部错位。代价是 GBK /
// Latin-1 / UTF-16 编码的文本会被判为二进制从而跳过替换 —— 安全但会产生
// 额外冲突（规范 §7.4 已声明这个取舍）。
func IsBinary(b []byte) bool {
	head := b
	if len(head) > binarySniffLen {
		head = head[:binarySniffLen]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	return !utf8.Valid(b)
}
