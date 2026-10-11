// Package renderstore 实现渲染快照缓存的读、写与校验（spec §21）。
//
// 按输入元组（engine ⊕ source ⊕ kind ⊕ vars）在用户缓存目录保存当轮渲染
// 原树副本（manifest.json + tree/<Dst>），仅用作 .xinit/base/ 丢失或损坏
// 时的恢复来源——补两个真实口子：dir 源 base 丢失、git 源记录 commit 不可取。
//
// 依赖方向：renderstore → state/fsutil。**不得 import internal/cli**。
package renderstore
