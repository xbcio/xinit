package fsutil

import (
	"bytes"
	"testing"
)

func TestDetectEOL(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want EOL
	}{
		{"全 LF", []byte("a\nb\n"), EOLLF},
		{"全 CRLF", []byte("a\r\nb\r\n"), EOLCRLF},
		{"无换行的单行", []byte("abc"), EOLLF},
		{"空文件", []byte{}, EOLLF},
		{"末尾混入一个 LF", []byte("a\r\nb\n"), EOLMixed},
		{"末尾混入一个 CRLF", []byte("a\nb\r\n"), EOLMixed},
		{"只有 CR 不算 CRLF", []byte("a\rb"), EOLLF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectEOL(tt.in); got != tt.want {
				t.Errorf("DetectEOL(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestToLFLeavesMixedAlone(t *testing.T) {
	// 混合 EOL 必须原样返回：归一化再还原会改坏未被替换的字节
	mixed := []byte("a\r\nb\n")
	if got := ToLF(mixed); !bytes.Equal(got, mixed) {
		t.Errorf("ToLF(混合) = %q, want 原样 %q", got, mixed)
	}
}

func TestToLFConvertsCRLF(t *testing.T) {
	if got := ToLF([]byte("a\r\nb\r\n")); !bytes.Equal(got, []byte("a\nb\n")) {
		t.Errorf("ToLF = %q, want %q", got, "a\nb\n")
	}
}

func TestFromLFRoundTrip(t *testing.T) {
	orig := []byte("a\r\nb\r\n")
	if got := FromLF(ToLF(orig), EOLCRLF); !bytes.Equal(got, orig) {
		t.Errorf("往返 = %q, want %q", got, orig)
	}
	lf := []byte("a\nb\n")
	if got := FromLF(ToLF(lf), EOLLF); !bytes.Equal(got, lf) {
		t.Errorf("往返 = %q, want %q", got, lf)
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{"纯 ASCII", []byte("hello world\n"), false},
		{"中文 UTF-8", []byte("你好\n"), false},
		{"含 NUL", []byte("abc\x00def"), true},
		{"非法 UTF-8（GBK 中文）", []byte{0xc4, 0xe3, 0xba, 0xc3}, true},
		{"空文件", []byte{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBinary(tt.in); got != tt.want {
				t.Errorf("IsBinary(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsBinaryOnlyLooksAtFirst8192Bytes(t *testing.T) {
	// NUL 出现在 8192 字节之后不算二进制（与 git 的启发式一致）
	in := append(bytes.Repeat([]byte("a"), 9000), 0x00)
	if IsBinary(in) {
		t.Error("NUL 在第 9000 字节，不应判为二进制")
	}
}

func TestIsBinaryRejectsInvalidUTF8BeyondWindow(t *testing.T) {
	// 但 UTF-8 合法性检查覆盖全文——坏字节会让正则与行切分全部错位
	in := append(bytes.Repeat([]byte("a"), 9000), 0xff)
	if !IsBinary(in) {
		t.Error("第 9000 字节有非法 UTF-8，应判为二进制")
	}
}
