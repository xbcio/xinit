// Package backflow 实现回流（backflow）的服务层：把派生项目对共享面的改动
// 结构化地回送给模板，产物是模板作者可直接 `git am -3` 的标准补丁。
//
// 本包承接 spec §20 的全链——资格与守卫（§20.4）、域与夹带扫描（§20.5）、
// 产物生成（§20.6）、路径解析（§20.7）；命令面、退出码与输出编排归
// internal/cli（§20.8）。设计条款见 .claude/specs/2026-10-08-xinit-design.md §20。
//
// 回流是 opt-in：仅对 git 派生可用，非 git 派生与所有既有命令零行为变化（§20.1）。
package backflow
