package backflow

import (
	"errors"
	"fmt"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/merge"
)

// TranslateInput 是 §20.3 三方合并翻译的三方输入。
//
// T/B/O 都必须是**字节**（不经工作树）：T 是记录 commit 的模板 blob、B 是
// `.xinit/base/<Dst>`（或 state hash 一致的重建产物）、O 是提交 blob。
type TranslateInput struct {
	T     []byte // 模板原文（blob @ 记录 commit）
	B     []byte // .xinit/base/<Dst>（或 state hash 一致的重建产物）
	O     []byte // 提交 blob（不经工作树）
	IsNew bool   // 新增文件：F_new = O
}

// TranslateResult 是翻译结果。
//
// Conflict 为真时 FNew 恒为 nil：冲突路径不产补丁（§20.1 不变量 10 延伸），
// 冲突标记与 O 的内容都不得出现在产物里（INV-1 fail-closed）——所以这里
// 不把 merge-file 的带标记输出交出去，省得调用方误用。
type TranslateResult struct {
	FNew     []byte // EOL 已按硬规则还原（IsNew 时 = O 原样）
	Conflict bool   // fail-closed：冲突路径不产补丁
	Blocks   int    // 冲突块数
}

// TranslateFile 把 O 相对 B 的 delta 搬到 T 上（§20.3），产出模板坐标系的
// 新文件内容 F_new。
//
// 三条判据逐字实现：
//
//   - 三方合并调用 = `merge.ThreeWay(T, B, O)` —— **ours=T、base=B、theirs=O**
//     （「回流里第一个实参是 T」；update 那边第一个实参是 disk ours，照抄会写反）。
//   - **EOL 硬规则**：`F_new = FromLF(merge输出, DetectEOL(T))` —— **禁用 O 的 EOL**。
//     合并输出恒为 LF（merge 内部三方 ToLF），若按 O 的 EOL 还原，编辑器/git
//     把派生文件归一成 LF 的常见态下 CRLF 模板会变成全文件行尾改写（§20.3）。
//   - **新增文件特例**（B/T 均不存在）：不做合并，`F_new = O` 内容逐字（exec 位
//     在 T5 的 mode 层处置）——无预像即无翻译问题。
//
// 返回 error 的只有「合并跑不了」（Outcome Error：三方之一含 NUL 被 git 拒绝、
// 或进程被信号终止）；**冲突不是 error**，走 Conflict/Blocks。
//
// **适用面（INV-4 的前提）**：三方都必须是非 mixed-EOL。§20.7 的边界表把
// 「T/B/O 任一方 IsBinary ∨ DetectEOL == EOLMixed」整条挡在翻译之前（警告
// 不导出），所以 mixed-EOL 输入到不了这里 —— 本函数不重复该闸门。真要喂进来，
// merge 内部对 mixed 的 ToLF 是无操作（§9.1：mixed 按二进制处理），CR 会作为
// 内容字节留在结果里，此时 INV-4 不成立 —— 这是上游该拦的输入，不是本通道的
// 失败。
func TranslateFile(in TranslateInput) (TranslateResult, error) {
	if in.IsNew {
		return TranslateResult{FNew: in.O}, nil
	}

	res, err := merge.ThreeWay(in.T, in.B, in.O)
	if err != nil {
		return TranslateResult{}, fmt.Errorf("三方合并: %w", err)
	}
	switch res.Outcome {
	case merge.Clean:
		return TranslateResult{FNew: fsutil.FromLF(res.Bytes, fsutil.DetectEOL(in.T))}, nil
	case merge.Conflicts:
		return TranslateResult{Conflict: true, Blocks: res.Conflicts}, nil
	default: // merge.Error：冲突之外的结构性失败，调用方必须能区分（fail-closed）
		return TranslateResult{}, errors.New(
			"三方合并失败：git merge-file 拒绝该输入（三方中任一方含 NUL，如二进制文件）")
	}
}
