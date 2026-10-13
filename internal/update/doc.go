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
//  2. 管线阶段顺序即 spec §10.2 的顺序；M2 收口后 Run 只是骨架
//     （prepare → 各阶段函数 → report），阶段拆分与边界见下表。
//
// 阶段布局（P0–P11 + 终局 report；职责为概要，权威在各文件头注释）：
//
//	P0  prepare        session.go   会话构造：目录/状态/缓存根/锁；io/now 零值回退单点
//	P1  loadTemplate   template.go  模板源获取 + 清单编译 + 渲染
//	P2  loadBase       base.go      base 快照读取（缺失/降级语义）
//	P3  snapshotOurs   ours.go      ours 工作树快照（含 prune 展开）
//	P4  rewriteAccept  accept.go    --accept 的 P 侧行 12 重写
//	P5  buildDecision  decide.go    决策表组装 + 冲突分类 + changes 预测
//	P6  refineDecision accept.go    冲突问询后的行 9/13 精化
//	P7  gate           gate.go      管线门：dry-run/pending/障碍物短路
//	P8  trust          trust.go     hooks 信任门（rc4 零写）
//	P9  deliver        deliver.go   写盘：backup/写/删/冲突文件/拒绝还原
//	P10 advance        advance.go   state 推进 + base 写回 + 渲染快照
//	P11 runHook        hook.go      post_update 钩子执行
//	终局 report         report.go   报告单点组装（三处终局共用）
package update
