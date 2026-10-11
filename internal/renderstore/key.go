package renderstore

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strconv"
)

// 缓存布局（§21.3–21.4）：<cacheRoot>/render/<familyKey>/{manifest.json, tree/<Dst>}。
const (
	renderDirName = "render"
	treeDirName   = "tree"
	manifestName  = "manifest.json"
)

// keyDomain 是家族键的域前缀：做编码版本化——键/编码变更须升 "v2"，
// 旧条目自然 miss（§21.3）。
const keyDomain = "xinit/render-snapshot/v1"

// familyKey 是输入元组（engine/source/kind/vars）的家族键（§21.3）：
// hex(sha256(域前缀 ‖ 0x00 ‖ N(engine) ‖ N(source) ‖ N(kind) ‖ N(varsCanonical)))。
//
// anchor（commit / dir 树 hash）不进键——由读侧核对清单，每族至多一份
// 条目（覆写轮转）。
func familyKey(engine, source, kind string, vars map[string]string) string {
	h := sha256.New()
	h.Write([]byte(keyDomain))
	h.Write([]byte{0x00})
	h.Write(netstring([]byte(engine)))
	h.Write(netstring([]byte(source)))
	h.Write(netstring([]byte(kind)))
	h.Write(netstring(varsCanonical(vars)))
	return hex.EncodeToString(h.Sum(nil))
}

// varsCanonical 把 st.Vars 编码为确定性字节串（§21.3）。
//
// 键按**字节序**排序，逐项 N(k) ++ N(v) 拼接；值可为任意字节（含 \x00）；
// 空集（含 nil）编码 = 零字节——nil ≡ 空 map。
func varsCanonical(vars map[string]string) []byte {
	if len(vars) == 0 {
		return nil
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 字节序（非本地化）——排序结果进编码

	out := make([]byte, 0, 32*len(keys))
	for _, k := range keys {
		out = append(out, netstring([]byte(k))...)
		out = append(out, netstring([]byte(vars[k]))...)
	}
	return out
}

// netstring 把字节串编码为 decimal(len(b)) + ":" + b；len 是**字节数**。
func netstring(b []byte) []byte {
	out := strconv.AppendInt(nil, int64(len(b)), 10)
	out = append(out, ':')
	return append(out, b...)
}

// varsHash 是清单的 vars_sha256 字段：hex(sha256(varsCanonical(vars)))。
func varsHash(vars map[string]string) string {
	return sha256Hex(varsCanonical(vars))
}

// sha256Hex 是内容/分量的 raw 摘要（写侧记账、读侧自校验，§21.4）。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// entryDir 返回某家族键的条目目录：<cacheRoot>/render/<key>。
func entryDir(cacheRoot, key string) string {
	return filepath.Join(cacheRoot, renderDirName, key)
}
