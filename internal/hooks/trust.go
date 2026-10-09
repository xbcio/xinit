// Package hooks 实现模板钩子的信任确认、执行与副作用检测（规范 §10.5/§10.6）。
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash 是钩子块的信任标识："sha256:" + 内容 hex（§4.3 trusted_hooks 的键形状）。
func Hash(block string) string {
	sum := sha256.Sum256([]byte(block))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NeedConfirm 报告该内容是否必须经用户确认才能执行（规范 §10.5）。
// trusted[h] 非空 = 已确认；缺席或空串（--no-hooks 只记录过「已见」）= 需确认。
func NeedConfirm(trusted map[string]string, h string) bool {
	v, ok := trusted[h]
	return !ok || v == ""
}

// Consent 记录用户对 h 的确认，version 是确认时的模板版本
// （git 源 = revision，dir 源 = 树 hash；不得为空——空串是 Seen 的 marker）。
func Consent(trusted map[string]string, h, version string) {
	trusted[h] = version
}

// Seen 记录「本次跳过执行、只记录内容」（--no-hooks，§10.5）。
// 已有记录（任何值）不动：内容没变时既有确认仍然有效（M3-R1）。
func Seen(trusted map[string]string, h string) {
	if _, ok := trusted[h]; !ok {
		trusted[h] = ""
	}
}
