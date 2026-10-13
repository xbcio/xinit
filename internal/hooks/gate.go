package hooks

// 来源：internal/update/trust.go 块 24 内环 + internal/cli/init.go 步骤 4
//（逐字同构两副本）——I-2 M5 单点抽取。行为判据：两调用点输出/退出/写面
// 逐字节不变。

import "io"

// GateInput 是一次信任门判定的全部输入（§10.5）。两调用点的差异在此消化：
// update 传 PrepareDiff（懒取已记录版本 diff）；init 不传（无基线）。
type GateInput struct {
	Name        string // "post_init" / "post_update"
	Block       string // 钩子命令全文
	Version     string // 确认时记录的版本值（hookVersion(src)）
	Out         io.Writer
	Confirm     func() (bool, error)         // nil = 无交互面（守卫语义由调用点决定）
	PrepareDiff func() (oldVer, diff string) // nil = 无 diff 段；仅在 NeedConfirm 分支内调用一次
}

// ConfirmGate 是信任门编排的单点（§10.5）：--no-hooks 只登记 Seen；
// 需确认时打印披露（+可选 diff）与指引、经 Confirm 询问、确认后 Consent。
// 返回 declined=true = 未确认（调用方映射退出语义：init→rc4 零写；
// update→终局零写）。trusted 必须非 nil（调用点保证）。
func ConfirmGate(trusted map[string]string, in GateInput, noHooks, yes bool) (declined bool, err error) {
	h := Hash(in.Block)
	if noHooks {
		Seen(trusted, h)
		return false, nil
	}
	if !NeedConfirm(trusted, h) {
		return false, nil
	}
	var oldVer, diff string
	if in.PrepareDiff != nil {
		oldVer, diff = in.PrepareDiff()
	}
	PrintNotice(in.Out, in.Name, in.Block, oldVer, diff)
	if !yes {
		PrintAsk(in.Out) // T4-R2：--yes 预先确认时不打指引
	}
	ok := yes
	if !ok && in.Confirm != nil {
		var cerr error
		if ok, cerr = in.Confirm(); cerr != nil {
			return false, cerr
		}
	}
	if !ok {
		return true, nil
	}
	Consent(trusted, h, in.Version)
	return false, nil
}
