package fsutil

import "testing"

func TestFoldEqualCase(t *testing.T) {
	if !FoldEqual("README.md", "Readme.md") {
		t.Error("大小写不同应视为折叠相等（macOS/Windows 上它们就是同一个文件）")
	}
	if !FoldEqual("Makefile", "makefile") {
		t.Error("Makefile 与 makefile 应折叠相等")
	}
	if FoldEqual("a.md", "b.md") {
		t.Error("不同名字不应折叠相等")
	}
}

func TestFoldEqualNFC(t *testing.T) {
	// "é" 的两种编码：U+00E9（NFC，单个码点）与 U+0065 U+0301（NFD，e + 组合重音符）
	nfc := "café.md"
	nfd := "café.md"
	if nfc == nfd {
		t.Fatal("夹具写错了：两个字面量字节相同，测试没有验证 NFC 归一")
	}
	if !FoldEqual(nfc, nfd) {
		t.Error("NFC 与 NFD 应视为折叠相等（APFS 对规范形式不敏感）")
	}
}

func TestFoldIsStable(t *testing.T) {
	nfc := "café.md"
	nfd := "café.md"
	if nfc == nfd {
		t.Fatal("夹具写错了：两个字面量字节相同，测试没有验证 NFC 归一")
	}
	// 折叠必须幂等，否则没法当 map 的键做碰撞检测
	for _, s := range []string{"README.md", nfc, nfd, "Makefile"} {
		if Fold(Fold(s)) != Fold(s) {
			t.Errorf("Fold 不幂等: %q -> %q -> %q", s, Fold(s), Fold(Fold(s)))
		}
	}
}

func TestFoldMergesNonASCIICaseVariants(t *testing.T) {
	if Fold("Ä") != Fold("ä") {
		t.Error("非 ASCII 也应折叠")
	}
}

func TestFoldEqualUnifiedWithEqualFold(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"LICENSE", "license", true}, // ASCII：与旧实现一致
		{"ſ", "s", true},             // ſ（长 s）：EqualFold 语义为真（旧 ToLower 实现为假）
		{"İ", "I", false},            // İ：SimpleFold 无关联（旧 ToLower 实现为假合并）
		{"a\u0301", "\u00e1", true},  // NFD/NFC（转义写法：防字面量被 Unicode 归一化而失去判别力）
		{"src/Main.go", "src/main.go", true},
	}
	for _, c := range cases {
		if got := FoldEqual(c.a, c.b); got != c.want {
			t.Errorf("FoldEqual(%q,%q)=%v，想要 %v", c.a, c.b, got, c.want)
		}
	}
}
