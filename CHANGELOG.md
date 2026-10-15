# 变更日志

本文件记录 xinit 各版本的用户可见变化，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

命令与旗标的完整说明见 [README](README.md)，已知限制见 README §11。

## [1.0.0] - 2026-10-15

首个正式版本。此前内部使用过的 v1.0.0–v1.0.2 标签已作废，本条目汇总截至目前的全部能力。

### 新增

**命令**

- `xinit init`：从模板（git 仓库或本地目录）生成并纳入管理新项目；`--revision` 直接钉到某个 `vX.Y.Z` tag，`--offline` 只读本地模板镜像缓存（镜像缺失即报错、不会克隆）；`--interactive` 按清单声明交互采集变量。
- `xinit update`：把派生项目同步到模板的新版本。每条路径给出动作（写入 / 删除 / 保持本地版本并告警 / 冲突），冲突路径的模板版本写到 `.xinit/conflicts/`；支持 `--dry-run`、`--accept`（声明冲突已手工解决）、`--no-hooks`、`--yes`。覆盖本地文件前先把原内容备份到 `.xinit/overwritten/` 与 `.xinit/backup/`。
- `xinit diff`：与 `update` 走同一条代码路径的只读预览。
- `xinit status`：只读报告；`--fetch` 检查模板是否有可用更新。
- `xinit check`：只读门禁检查（状态与 base 一致性、冲突残留、prune 与 git 约定、模板面），有 `FAIL` 项时退出码为 6。
- `xinit adopt`：为已有项目从渲染结果重建基线，把它纳入管理。
- `xinit restore <path>...`：把记账路径从 `.xinit/base/` 恢复到工作树，预检不过即零写入。
- `xinit backflow <commit>`：把派生项目共享面的改动回送给模板——三方合并翻译、夹带扫描、导出标准补丁。
- `xinit backup prune --keep <N>`：显式清理 `.xinit/backup/` 旧份（不自动清理）。
- `xinit --version`：输出版本号。`make build` / `make install` 通过 `git describe` 注入；未注入时回退到 Go 内嵌的构建信息。

**模板与渲染**

- 两种模板写法可混用：活的可运行项目（清单 `substitute` 规则改写身份字面量）和骨架项目（`{{ .var }}`，按路径 opt-in 的 Go template 渲染）。
- 清单 `xinit.yml`：`vars`、`substitute`、`render`、`generated`、`hooks`、`upstream.paths`。
- 模板改路径时自动迁移：文件物理改名、路径里的变量变化或 `rename` 规则变化导致文件位移时，本地改动随文件搬到新路径，报告里给出 `migrated` / `rename` / `skip` / `fallback` 四态。

**钩子与信任**

- `post_init` / `post_update` 钩子以 `sh -c` 单块执行；首次执行与内容变化时需要信任确认（退出码 4），钩子失败退出码为 3，并报告钩子造成的副作用。

**恢复与安全**

- 渲染快照缓存：按输入元组在用户缓存目录保存渲染快照，仅用于 `.xinit/base/` 丢失或损坏时的恢复。
- 写入 `state.json` 前保留上一份为 `state.json.prev`；载入失败时区分「缺失」与「损坏」，并给出对应指引。
- 项目级文件锁，防止并发运行。

**平台**

- macOS、Linux 和 Windows 原生（交叉编译门禁通过；Windows 真机行为尚未验证，见 README §11 第 1 条）。

### 退出码

`0` 成功；`1` 错误；`2` 有冲突、折叠拒绝或障碍物待解决；`3` 钩子失败；`4` 需要信任确认；`5` `diff` 发现非 `Noop` 动作，或 `status --fetch` 发现可用更新；`6` `check` 发现问题。`restore`、`backflow`、`backup prune` 只使用 0 / 1。

### 依赖

- 需要 `git ≥ 2.38`，启动时检查，不满足即退出。
- 不承诺回滚：冲突即停下报告，由用户手工解决后用 `--accept` 声明；失败后可从 `.xinit/backup/` 手工恢复。
