package fsutil

import (
	"strconv"
	"strings"
	"testing"
)

// TestValidRelPath 是「规范相对路径」单点谓词（§22.4）的逐项表：合法面
// 零回归，既有非法形态口径不变，新增非法形态是本次收紧（Windows 形态）。
func TestValidRelPath(t *testing.T) {
	for _, tc := range []struct {
		p    string
		want bool
	}{
		// 合法（零回归面）
		{"a", true},
		{"a/b.txt", true},
		{"sub/dir/f", true},
		{"a-b_c.9", true},
		{"好/文件.txt", true},
		// 既有非法形态（原谓词已拒，口径不变）
		{"", false},
		{"/x", false},
		{".", false},
		{"..", false},
		{"a/../b", false},
		{"a//b", false},
		{"a/./b", false},
		{"a/", false},
		{"a/..", false},
		// 新增非法形态（§22.4 收紧）：反斜杠逃逸闭合 + 盘符/ADS/UNC
		{`a\b`, false},
		{`a\..\..\evil`, false},
		{"C:/x", false},
		{"C:x", false},
		{"c:/", false},
		{`\\srv\share`, false},
		{"//srv/share", false},
		{"f.txt:stream", false},
		{"a/b:c", false},
		{`a/b\c`, false},
	} {
		if got := ValidRelPath(tc.p); got != tc.want {
			t.Errorf("ValidRelPath(%q) = %v, want %v", tc.p, got, tc.want)
		}
		err := CheckRelPath(tc.p)
		if tc.want && err != nil {
			t.Errorf("CheckRelPath(%q) = %v, want nil（ValidRelPath 与 CheckRelPath 必须同口径）", tc.p, err)
		}
		if !tc.want && err == nil {
			t.Errorf("CheckRelPath(%q) = nil, want error", tc.p)
		}
	}
}

// TestCheckRelPathForbidCharsReasonVerbatim 是 §22.4 冻结钉项的逐字回归锁：
// 逃逸闭合形态（反斜杠）与盘符形态的错误文本必须含统一理由句本体。
func TestCheckRelPathForbidCharsReasonVerbatim(t *testing.T) {
	for _, p := range []string{`a\..\..\evil`, "C:/x"} {
		err := CheckRelPath(p)
		if err == nil {
			t.Fatalf("CheckRelPath(%q) 必须拒绝", p)
		}
		if !strings.Contains(err.Error(), ForbidCharsReason) {
			t.Errorf("CheckRelPath(%q) 的错误文本必须含冻结理由句：got %q", p, err.Error())
		}
	}
	// 理由句本体逐字（钉项，不得改写）：反斜杠一个、冒号一个，措辞不动。
	if ForbidCharsReason != "含路径分隔符 \\ 或驱动器/ADS 字符 :，不受支持" {
		t.Errorf("ForbidCharsReason 逐字漂移：%q", ForbidCharsReason)
	}
}

func TestCheckForbiddenChars(t *testing.T) {
	for _, tc := range []struct {
		p     string
		forbi bool
	}{
		{"a/b.txt", false},
		{".xinit/conflicts/a.txt", false},
		{"好/文件.txt", false},
		{`a\b`, true},
		{"a/b:c", true},
		{"../a\\b", true}, // 段落判定：含 `..` 段也在检（调用方自管其余口径）
	} {
		err := CheckForbiddenChars(tc.p)
		if got := err != nil; got != tc.forbi {
			t.Errorf("CheckForbiddenChars(%q) 报错 = %v, want %v", tc.p, got, tc.forbi)
		}
		if err != nil && (!strings.Contains(err.Error(), ForbidCharsReason) ||
			!strings.Contains(err.Error(), strconv.Quote(tc.p))) {
			t.Errorf("CheckForbiddenChars(%q) 错误文本必须 = 理由句 + 原值（%%q 形态）：got %q", tc.p, err.Error())
		}
	}
}
