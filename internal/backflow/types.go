package backflow

// Item 是全链的中间表示：一个待导出条目或带出口的警告项。
type Item struct {
	Dst      string         // 派生侧路径（提交内路径）
	Src      string         // 模板侧路径（映射后；新增文件经 T3 解析）
	Change   Change         // 枚举原始记录
	Class    Classification // Managed | NewFile
	Exit     Exit           // 终态出口
	ExitNote string         // 出口文案（稳定标识行由 T6 统一加前缀）
}

// Classification 区分候选条目的来源面：受管路径（state.Files 有条目，Src
// 映射来自记录状态）与新增文件（无映射，Src 待 §20.7 的路径解析产出）。
type Classification int

const (
	ClassManaged Classification = iota
	ClassNewFile
)

// Exit 是候选条目的终态出口。零值 ExitExport 兼作「尚未定出口」的候选态：
// 各阶段（域判定 / 收敛 / 边界分类 / 翻译 / noop）只处置 Exit == ExitExport
// 的条目，一经定出口即不再改写 —— 编排方因此可以分阶段多次推进同一批 Items。
//
// 取值面按 spec §20 一次列全（含后续阶段才用到的出口）：
// ExitExport 导出（§20.6 成功面）；ExitOutOfDomain / ExitPruned /
// ExitSourceGone 见 §20.5；ExitRender / ExitBinary / ExitSymlink /
// ExitTypechange / ExitDeleted / ExitRenamed / ExitCopied / ExitGenerated
// 见 §20.7 边界总表；ExitConflict 见 §20.3（翻译冲突 fail-closed）；
// ExitNoop 见 §20.6（内容 ∧ exec 位与 T 无差异）；ExitResolveAmbiguous
// 见 §20.7 新增文件路径解析（零/多命中）。
type Exit int

const (
	ExitExport Exit = iota
	ExitOutOfDomain
	ExitPruned
	ExitSourceGone
	ExitRender
	ExitBinary
	ExitSymlink
	ExitTypechange
	ExitDeleted
	ExitRenamed
	ExitCopied
	ExitGenerated
	ExitConflict
	ExitNoop
	ExitResolveAmbiguous
)
