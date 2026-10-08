package fsutil

import "bytes"

// EOL 是一个文件的换行风格。EOLMixed 表示同一文件里 CRLF 与 LF 并存。
type EOL uint8

const (
	EOLLF EOL = iota
	EOLCRLF
	EOLMixed
)

func (e EOL) String() string {
	switch e {
	case EOLLF:
		return "lf"
	case EOLCRLF:
		return "crlf"
	case EOLMixed:
		return "mixed"
	}
	return "unknown"
}

// DetectEOL 判定换行风格。只有 \r\n 计入 CRLF；单独的 \r 不是换行符
// （规范 §7.4：整文件按字节处理，不按行切分）。
func DetectEOL(b []byte) EOL {
	var crlf, lf int
	for i := 0; i < len(b); i++ {
		if b[i] != '\n' {
			continue
		}
		if i > 0 && b[i-1] == '\r' {
			crlf++
		} else {
			lf++
		}
	}
	switch {
	case crlf > 0 && lf > 0:
		return EOLMixed
	case crlf > 0:
		return EOLCRLF
	default:
		return EOLLF
	}
}

// ToLF 把 CRLF 归一化为 LF。
//
// 混合 EOL 的文件原样返回、不做任何改动 —— 规范 §9.1 规定这类文件按二进制
// 处理。若归一化再还原，未被替换的字节也会被改写（实测 'a\r\nb\n' 会变成
// 'a\r\nb\r\n'）。
func ToLF(b []byte) []byte {
	if DetectEOL(b) == EOLMixed {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// FromLF 是 ToLF 的逆操作。混合 EOL 原样返回。
func FromLF(b []byte, e EOL) []byte {
	if e != EOLCRLF {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}
