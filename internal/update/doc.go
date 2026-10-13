// Package update 是 `xinit update` / `xinit diff` 的编排管线（spec §10.2 的
// 十二步本体）。它由 internal/cli 逐文件 1:1 下沉而来（I-2 实施批 T2/M1），
// 是继 cli 之后第二个聚合点，但自身是叶子消费者。
//
// 两条边界（评审 D1 裁定，写死于此）：
//
//  1. update 不 import cli——依赖方向只有 cli → update。cli 只做两件事：
//     flag → Input（inputOf）与 Verdict → §11.5 退出码（codeOf）的单点映射。
//     反向引用会构成 import 环，且被发布判据
//     `go list -deps ./internal/update | grep -c internal/cli` = 0 兜底。
//  2. 管线阶段顺序即 spec §10.2 的顺序。M1 形态仍是单个大函数 Run（十九个
//     编号步骤原样搬运）；12 阶段（P0–P11）的拆分、阶段函数与阶段表在 M2
//     补齐，M1 不做任何按阶段重组。
//
// 目录内文件与来源（M1）：
//
//	update.go     cli/update.go 的迁移集（runUpdate→Run；+Input/Mode/Verdict/
//	              Interactive 接缝声明）
//	report.go     cli/report.go（UpdateReport→Report；退出码常量与 ExitError 留 cli）
//	migrate.go    cli/migrate.go（整文件）
//	base.go       cli/restore.go 行 1-234（base 机制）
//	conflicts.go  cli/promptconflicts.go（ps *promptSession → ps Interactive）
//	vars.go       cli/updatevars.go（整文件）
package update
