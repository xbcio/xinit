// Package plan 实现更新决策表（规范 §4.2 冻结契约、§8 五步）。
//
// 本文件是冻结契约本身（类型/枚举逐字对应 §4.2）；决策函数在 plan.go。
package plan

import "github.com/xbcio/xinit/internal/render"

type Snap map[string]FileMeta // key = 派生侧路径 Dst

type FileMeta struct {
	Kind      render.Kind
	Exec      bool
	Binary    bool   // §7.4 的探测：前 8192 字节含 NUL，或整体非合法 UTF-8
	MixedEOL  bool   // DetectEOL == EOLMixed（§9.1）；行 6 的第二判据，与 Binary 并列不合并
	Generated bool   // 上次更新时该路径是否属于 generated（§8 行 5）
	Hash      string // 文本：EOL 归一化后 sha256；二进制/混合 EOL：原始字节 sha256
}

type ActionKind uint8

const (
	Noop ActionKind = iota
	WriteTheirs
	Delete
	Merge
	ConflictBinary
	ConflictBothAdded
	ConflictKind
	KeepWarned
	KeepLocalDelete
)

// String 是小写稳定标识（diff 行前缀与报告用）；已有拼写不可改
// （测试夹具与用户脚本按它断言）。
func (k ActionKind) String() string {
	switch k {
	case Noop:
		return "noop"
	case WriteTheirs:
		return "write"
	case Delete:
		return "delete"
	case Merge:
		return "merge"
	case ConflictBinary:
		return "conflict-binary"
	case ConflictBothAdded:
		return "conflict-both-added"
	case ConflictKind:
		return "conflict-kind"
	case KeepWarned:
		return "keep-warned"
	case KeepLocalDelete:
		return "keep-local-delete"
	}
	return "unknown"
}

type Action struct {
	Path    string
	Kind    ActionKind
	NewBase *FileMeta // 该路径更新后的 base；nil = 从 state.files 移除
	Note    string
}

// PruneSet 是 prune.txt 展开后的精确 Dst 集合。
//
// 规范只在 Plan 签名里引用了类型名、未声明它（正文仅 §8.0 给出语义：
// 命中即移出并集、从 files 移除、静默）。M2 裁定 R4：类型定为
// map[string]bool，glob 的编译（§3.2）与对路径并集的展开由调用方完成——
// Plan 保持纯函数，不碰 prune.txt、不跑 glob。
type PruneSet map[string]bool
