package fsutil

import (
	"fmt"
	"path"
	"strings"
)

// ForbidCharsReason 是「路径任一段含 \ 或 :」的统一理由句（§22.4 冻结钉项：
// 实现期逐字、不得改写）。
const ForbidCharsReason = "含路径分隔符 \\ 或驱动器/ADS 字符 :，不受支持"

// CheckForbiddenChars 报告 p 的任一段是否含 `\` 或 `:`（§22.4 的收紧项：
// Windows 上 `\` 亦为分隔符、`:` 承载盘符/ADS 语义）。判据单点——所有落点的
// 字符级判定都归这里，调用方只决定消息前缀。
func CheckForbiddenChars(p string) error {
	for _, seg := range strings.Split(p, "/") {
		if strings.ContainsAny(seg, "\\:") {
			return fmt.Errorf("%s: %q", ForbidCharsReason, p)
		}
	}
	return nil
}

// CheckRelPath 校验 p 是否「规范相对路径」（§4.3、§22.4 单点谓词）：
// 非空；非绝对（盘符 X: / UNC \\ / \\?\ 由 IsAbs 与禁 `:`/`\` 共同否决）；
// 按 / 分段无空段、无 "."、无 ".."；path.Clean 不变；任一段不含 `\` 与 `:`。
func CheckRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("为空")
	}
	if path.IsAbs(p) {
		return fmt.Errorf("是绝对路径: %q", p)
	}
	if err := CheckForbiddenChars(p); err != nil {
		return err
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("含空路径段: %q", p)
		case ".", "..":
			return fmt.Errorf("含 %q 段: %q", seg, p)
		}
	}
	if cleaned := path.Clean(p); cleaned != p {
		return fmt.Errorf("不规范: %q（Clean 后为 %q）", p, cleaned)
	}
	return nil
}

// ValidRelPath 是 CheckRelPath 的布尔形态。
func ValidRelPath(p string) bool { return CheckRelPath(p) == nil }
