# xinit

按「模板项目 + 声明式清单」生成新项目，并在模板演进后把变更**增量同步**进派生项目的命令行工具。

同步走三方比较（模板基线 `base` / 模板新版本 `theirs` / 你的磁盘 `ours`）：模板的改动会合进来，你的本地改动原样保留；两边改了同一处就**停下报告冲突**，由你手工解决后再用一条命令声明收敛——不会静默覆盖你的文件。

典型场景：一个团队/内网模板仓库（服务骨架、CI 配置、部署清单），对应许多派生项目。模板改一次，各派生项目在本地 `xinit update` 就能拿到更新。

- 派生项目**不必是 git 仓库**（有 git 时体验更好：备份之外多一层版本控制兜底）。
- 不需要修改被模板跟踪的 `.gitignore`：`init` / `update` 会幂等把 `.xinit/` 写进 `.git/info/exclude`。
- 依赖：**git ≥ 2.38**（硬依赖，启动时检查，不满足即退出）；钩子另需 PATH 中有 POSIX `sh`（Git for Windows 自带）。

## 目录

1. [简介](#1-简介)
2. [构建与安装](#2-构建与安装)
3. [快速开始](#3-快速开始)
4. [命令面](#4-命令面)
5. [模板清单 xinit.yml 参考](#5-模板清单-xinityml-参考)
6. [冲突与收敛](#6-冲突与收敛)
7. [`.xinit/` 目录说明](#7-xinit-目录说明)
8. [退出码表](#8-退出码表)
9. [回流](#9-回流)
10. [钩子与信任](#10-钩子与信任)
11. [已知限制](#11-已知限制)
12. [开发](#12-开发)

## 1. 简介

模板有两种写法，可以混用：

- **活的可运行项目**：文件里没有占位符，靠清单里的 `substitute` 规则改写「身份字面量」（如模板自带的品牌名、模块路径）。
- **骨架项目**：文件里写 `{{ .var }}`，按路径 opt-in 走 Go template 渲染（清单里的 `render` 段）。

`xinit` 把你的项目**纳入管理**后（`.xinit/` 状态目录），每条受管路径都记录着「上次同步时的模板内容」。之后：

```
改模板 → 打 tag → 在派生项目里 xinit diff（预览）→ xinit update（落地）
```

`update` 会给出每条路径的动作（写入 / 删除 / 保持本地版本并告警 / 冲突）与汇总，冲突路径的模板版本写到 `.xinit/conflicts/`。

## 2. 构建与安装

需要 **Go 1.25 或更高**（以 `go.mod` 的 `go` 声明为准）。

**运行时依赖**：`git ≥ 2.38`（启动时硬检查，不满足即退出）与 POSIX `sh`（钩子执行；Windows 用 Git for Windows 自带 sh）。详见[已知限制](#11-已知限制)。

```bash
# 从源码构建到当前目录
go build -o xinit .

# 安装到 $GOBIN（或 $GOPATH/bin）
go install .

# 或者用 Makefile
make build      # → bin/xinit
make install    # → go install .
```

`make` 目标一览见[开发](#12-开发)。

## 3. 快速开始

下面每一步的命令与输出都来自一次真实实操（macOS + git 2.50.1），可以直接照做。

### 3.1 准备模板仓库

模板仓库 = 普通 git 仓库，根目录放一个 `xinit.yml`。最小可用清单（`xkitora` 是模板自带的身份字面量，会被替换掉）：

```yaml
manifest_version: 1

vars:
  - name: app_slug
    default: "demo-service"
    pattern: '^[a-z][a-z0-9-]*$'

substitute:
  - from: "xkitora"
    to: "{{ .app_slug }}"
    paths: ["**/*"]

render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
```

配上一个 `go.mod`、一个 `main.go`（内容里含字面量 `xkitora`）和一个 `README.md.tmpl`（内容含 `{{ .app_slug }}`）：

```
# go.mod
module example.com/xkitora

go 1.25
```

```
# main.go
// Package main 是 xkitora 服务的入口。
package main

import "fmt"

func main() {
	fmt.Println("xkitora 服务启动")
}
```

```
# README.md.tmpl
# {{ .app_slug }}

由最小模板生成。
```

提交并**打 tag**：

```bash
git init -q -b main
git add -A
git commit -m "feat: 最小模板 v1.0.0"
git tag v1.0.0        # 一定要打 tag，理由见 3.3
```

### 3.2 生成派生项目

**建议先把目标目录建成 git 仓库**（这样 `.xinit/` 会自动进 `.git/info/exclude`，不会被 `git add -A` 误收进版本控制）：

**注意这仍然只是「建议」**：派生项目**不必是 git 仓库**（`init` / `update` 都不要求）；但**回流（`backflow`）是 opt-in、只在 git 派生上可用**——它要用 git 枚举提交、读提交内容（不经工作树），非 git 派生用不了这一条命令。想回流就先 `git init`，细节见[回流](#9-回流)。

```bash
mkdir order-api && cd order-api
git init -q -b main

xinit init --template $WORK/minimal-template --config $WORK/order-api.vars.yml .
```

`order-api.vars.yml` 是变量文件（扁平 key-value YAML，可只写要覆盖的变量）：

```yaml
app_slug: order-api
```

输出与结果（这一段来自一次**建了 git 仓**的实操）：

```
已生成 3 个文件到 /path/to/order-api
```

```
$ cat go.mod
module example.com/order-api

go 1.25

$ cat README.md
# order-api

由最小模板生成。

$ tail -2 .git/info/exclude
# *~
.xinit/

$ git status --porcelain --ignored
?? README.md
?? go.mod
?? main.go
!! .xinit/
```

**注意**：`init` **没有 `--revision` 旗标**，它取的永远是模板仓库**默认分支的 HEAD**，并在状态里记为 `"HEAD"`。所以要把新项目钉在某个 tag 上，靠的是 3.3 的第一条 `update`。

### 3.3 日常循环（改模板 → `diff` → `update`）

`init` 之后，先做一次**显式的版本对齐**（这是 `"HEAD"` 记录的收口动作）。下面这一段输出来自另一次**没有建 git 仓**的实操，所以多一条 `当前目录不是 git 仓库` 的警告——按 3.2 的建议建了仓就不会有：

```bash
cd order-api
xinit update --revision v1.0.0
```

```
xinit: 警告: 当前目录不是 git 仓库：出问题只能靠 .xinit/backup/ 还原
xinit update: /path/to/minimal-template
版本: HEAD → v1.0.0
汇总: 写入 0、删除 0、chmod 0、冲突 0
```

这一轮不需要模板有任何改动（全 `Noop`），作用是把状态里的版本从 `"HEAD"` 换成 `v1.0.0`。**此后默认模式（不带 `--revision`）即可正常工作。**

> 如果你按 3.2 建了 git 仓、但**还没提交** `init` 生成的那几个文件，这一轮还会多一条警告：
> `工作树有 3 处未提交改动；update 会直接改写工作树，建议先提交（或用 --force 跳过检查）`
> —— 3 就是未跟踪的那三个新文件（`.xinit/` 已在 `.git/info/exclude` 里，不计入）。先 `git add -A && git commit` 再跑 `update`，这条警告就没有了。

接着就是常规循环。模板侧改一处并打新 tag：

```bash
cd $WORK/minimal-template
printf '\n// 版本：1.1.0\n' >> main.go
git add -A && git commit -m "feat: 打印版本注释（v1.1.0）" && git tag v1.1.0
```

派生项目侧先预览、再落地（本地同时改了 `go.mod`，与模板改的不是同一个文件）：

```bash
cd $WORK/order-api
printf '\n// 本地：构建标签\n' >> go.mod

xinit diff          # 只预览：退出码 5 = 有动作，0 = 全是 Noop
```

```
xinit diff: /path/to/minimal-template
版本: v1.0.0 → v1.1.0
  write                main.go
汇总: 写入 1、删除 0、chmod 0、冲突 0
（diff 只读预览：以上为预测，未写任何文件、未改 state）
```

```bash
xinit update
```

```
xinit: 警告: 当前目录不是 git 仓库：出问题只能靠 .xinit/backup/ 还原
xinit update: /path/to/minimal-template
版本: v1.0.0 → v1.1.0
  write                main.go
汇总: 写入 1、删除 0、chmod 0、冲突 0
备份: .xinit/backup/20261010-094906
```

模板的改动落到了 `main.go`，本地对 `go.mod` 的改动原样保留。

### 3.4 把已有的项目纳入管理（`adopt`）

如果项目已经存在、只是没有 `.xinit/`（例如手工拷贝了一份，或状态目录丢了），用 `adopt` 重建基线：

```bash
xinit adopt --template $WORK/minimal-template --config $WORK/order-api.vars.yml
```

```
xinit adopt: /path/to/minimal-template@v1.1.0
基线已重建：3 个文件（.xinit/base/ 与 .xinit/state.json）
本地改动 1 个文件（磁盘与模板渲染结果不同）：update 起按「本地改过」保留——内容改动的（磁盘侧为常规文件），模板之后也改到它时走合并/冲突（行 6/7；本地版本与模板新内容已一致时行 4 静默收敛）；删除的，模板之后也改到它时保留删除并告警（行 10）
  local-diff           main.go
警告：--revision 选错版本时，两版之间模板的改动在磁盘上不存在，会被当成
「本地把模板改回去了」而永久保留——那些模板更新从此不会再被应用（§5.3）。
```

`--template` 与 `--config` 必填：`vars` 是重建基线的三要素之一（模板 + vars + 版本），缺了就无法渲染出可比对的基线。

**`--revision` 不必填**——省略时取模板最新的 semver tag（上面那次实操就没写它，直接 rc=0，基线落在 `v1.1.0`）。一般也**不建议**填：省略时拿到的就是「最新发布版本」，而显式指定一旦选错版本，两版之间模板的改动在磁盘上并不存在，会被当成「本地把模板改回去了」而永久保留（就是上面那条警告的后果）。只有确实要把基线建在某个特定版本上时才显式写。

如果模板声明了钩子，`adopt` 不执行钩子、也不写信任记录——下一次 `update` 会照常要求确认。

## 4. 命令面

`init` 与 `backflow <commit>` 以外，所有命令都作用于**当前目录**、没有位置参数（`restore <path>...` 除外——它的位置参数是项目内的记账相对路径）。

> 下表是旗标速查；**权威说明以 `xinit <命令> --help` 为准**。为便于阅读，表格里省略了 `--help` 原文中的内部章节号（如 `（§10.3）`，本 README 后面引用到的输出里也保留了这类编号——它们只是工具内部的文档索引，不影响使用）。

### `xinit init [目标目录]` — 从模板生成新项目

| 旗标 | 语义 |
| --- | --- |
| `--template <source>` | 模板来源（本地路径或 git URL），**必填** |
| `--config <file>` | 变量文件（扁平 YAML） |
| `--offline` | 不联网，只读本地模板镜像缓存；镜像缺失即报错（不会克隆） |
| `--no-hooks` | 跳过 `post_init` 执行（仍记录本次内容，下次带钩子运行仍需确认） |
| `--force` | 允许在非空目录执行（仍不覆盖同名文件） |
| `--yes` | 跳过钩子信任确认（等同于在交互终端回答 y） |
| `--interactive` | 即使 stdin 不是终端也按可交互处理：交互提问（含信任确认）从 stdin 逐行读取（供脚本与 CI 驱动） |

目标目录不存在时会创建；已存在且非空时需 `--force`；**只含 `.git` 的目录视为空目录**。`init` 不会在已初始化的项目上重跑（要同步更新请用 `update`）。

**`--template` 指向什么，决定了模板源的类型**（自动判定，不需要额外旗标）：

- **git 源**：本地 git 仓库，或一个 git URL。有版本概念（tag / commit），`update` 的选版、钉版本都靠它。
- **dir 源**：本地目录**且不是** git 仓库。**没有版本概念**——状态里 `kind` 记为 `dir`、`revision` 是空串，版本锚点用目录内容的**树 hash**（改一个文件就是「新版本」）。因此 dir 源不需要 `--revision`（写了也无意义），也不需要 3.3 那一步版本对齐：实测 dir 源 `xinit init` 之后直接 `xinit update` 即 rc=0 全 `Noop`。想干净地跟踪版本，把模板放进 git 仓并打 tag。

### `xinit update` — 把派生项目同步到模板的新版本

| 旗标 | 语义 |
| --- | --- |
| `--revision <ref>` | 目标版本（默认取模板最新的 semver tag） |
| `--config <file>` | 变量文件（扁平 YAML；补齐新增或失效的 vars） |
| `--accept <路径>...` | 声明这些冲突路径已手工解决：把 `.xinit/conflicts/<路径>` 的内容记为 base |
| `--dry-run` | 只输出动作列表，不落盘、不改状态 |
| `--no-hooks` | 跳过 `post_update` 执行（仍记录本次内容，下次带钩子运行仍需确认） |
| `--yes` | 跳过钩子信任确认（等同于在交互终端回答 y） |
| `--force` | 跳过脏工作树检查 |
| `--fetch` | 联网更新模板镜像缓存 |
| `--interactive` | 即使 stdin 不是终端也按可交互处理：交互提问（含信任确认）从 stdin 逐行读取（供脚本与 CI 驱动） |

### `xinit diff` — 预览 update 会做什么

走与 `update` 完全相同的代码路径，但**不写任何文件、不推进任何状态**。退出码：有非 `Noop` 动作（含冲突、折叠拒绝/障碍物的预测）→ 5；全部 `Noop` → 0。

| 旗标 | 语义 |
| --- | --- |
| `--revision <ref>` | 目标版本（默认取模板最新的 semver tag） |
| `--config <file>` | 变量文件（扁平 YAML） |
| `--fetch` | 联网更新模板镜像缓存 |

### `xinit status` — 只读报告

四段：**模板侧**（当前记录的版本、是否有更新）、**本地改动**（相对 base 改过的路径，`generated` 路径单列）、**状态健康**（prune 名单条数、混合 EOL 文件数、引擎版本一致性、孤儿路径）、**状态缺失**（没有 `.xinit/` 时直接报错并指引 `adopt`）。

| 旗标 | 语义 |
| --- | --- |
| `--fetch` | 联网更新模板镜像缓存，并检查可用更新 |

默认**不联网**、**不写任何文件**（包括 `.git/info/exclude`）。不带 `--fetch` 时恒为退出码 0（除非出错）。

### `xinit check` — 只读门禁检查

断言「`.xinit/` 内部自洽 + git 契约成立 + 模板可继续更新」，供 CI 与本地门禁消费。跑完全部检查再汇总（不在首个失败处中止），分四组输出：**A 状态与 base 树**（`files` 键合法性、折叠碰撞、base 与记账 hash 对应、残片/非普通文件、binary/mixed_eol/exec 位、template 字段）、**B 冲突残留**（`.xinit/conflicts/` 非空）、**C 配置与 git 契约**（`prune.txt` glob 可编译、`.xinit/` 未被 git 跟踪、exclude 含 `.xinit/`）、**D 模板面**（本地镜像有更高 tag、引擎版本漂移、记录的 commit 可取）。每行以 `FAIL` / `warn` / `skip` 开头，末尾一行汇总。

| 旗标 | 语义 |
| --- | --- |
| `--fetch` | 联网刷新模板镜像缓存后再检查模板面 |

**只读**：不加项目锁、不写项目内任何文件（包括 `.git/info/exclude` 与 `conflicts/`）、不修复任何东西；不 clone 镜像，默认不联网（镜像缺失时模板面检查降级为 `skip`）。退出码：有 `FAIL` → **6**；只有 `warn` / `skip` → 0（含 `--fetch` 下的「有更高 tag」——落后不算不健康，这一点与 `status --fetch` 的 5 不同）；不是 xinit 项目或 `state.json` 损坏 → 1。

### `xinit adopt` — 重建基线

把模板在指定版本的**渲染结果**记为 base（不是磁盘内容），磁盘与渲染结果的差异全部视为本地改动。

| 旗标 | 语义 |
| --- | --- |
| `--template <source>` | 模板来源（本地路径或 git URL），**必填** |
| `--revision <ref>` | 目标版本（默认取模板最新的 semver tag） |
| `--config <file>` | 变量文件（扁平 YAML），**必填** |

### `xinit restore <path>...` — 把记账路径恢复到工作树

把 1–n 个记账路径（`.xinit/state.json` 里 `files` 的键，即模板已记录的 Dst）从 `.xinit/base/` 恢复到项目工作树——即 `update` 报告里「找回模板已记录的版本：cp .xinit/base/<路径> <路径>」提示的命令化。`update` 负责**推进到新版本**，`restore` 只负责**把 base 记录的版本拿回来**（两者互不调用）；文件被误删、或本地改动想整体退回模板版时用它。

三态语义（每个 `<path>` 都必须是记账路径）：

- **目标不存在** → 从 base 恢复（写入字节与 exec 位）；
- **目标与 base 逐字节一致** → 无变化（幂等成功，输出「无变化：<路径>」）；
- **目标存在且内容不同** → **默认拒绝**（退 1、一个字节都不写，报告这些路径并提示 `--force`）；加 `--force` 才覆盖——把该路径重置回模板已记录的版本。

| 旗标 | 语义 |
| --- | --- |
| `--force` | 覆盖内容不同的现有文件（默认拒绝覆盖这些路径，退 1、不改动磁盘） |

**预检全过才写（all-or-nothing）**：任一路径非法（绝对路径、`..` 段等）/ 未记账 / base 里缺该文件 / 拒绝覆盖，都会在写任何字节之前以退 1 结束。base 里缺该文件时**不重建 base**（重建需要模板源与 vars）：先 `xinit update` 重建 base 后再 `restore`。

**零联网、零状态写入**：不联网、不写 `.xinit/state.json`、不写 `.xinit/base/`（只会多出项目锁 `.xinit/lock`）——恢复后该路径与 base 一致，下一轮 `update` 自然不再告警。退出码 0 / 1（见第 8 节）。

**已 prune 的路径不在记账里**：`update` 生效轮会把该路径从 `.xinit/base/` 与 `state.json` 记账中一并移除，`restore` 不适用——拿回管理见 §7。

### `xinit backflow <commit>` — 把共享面的改动回送给模板

把派生项目某个提交对**共享面**（清单 `upstream.paths` ∪ `--paths`）的改动翻译回模板坐标系，产出模板作者可直接 `git am` 的补丁；**要求派生项目是 git 仓库**（opt-in），全流程见[回流](#9-回流)。

| 旗标 | 语义 |
| --- | --- |
| `--paths <glob>` | 共享面 glob（可重复；与清单 `upstream.paths` 取并集） |
| `--map <Dst>=<Src>` | 显式给出模板态路径（可重复；新增文件自动解析零/多命中、以及「模板来源已不存在」路径的救援出口） |
| `--out <dir>` | 产物目录（默认 `.xinit/backflow/`；项目内相对路径允许，项目外必须是绝对路径） |
| `--dry-run` | 全部分类/翻译/扫描照跑，不写产物、不加锁 |

退出码 0 / 1（见第 8 节）；目标提交的**末段 trailer 块**须含逐字行 `Template-Upstream: yes`。

> 根命令还带有 cobra 自带的 `completion` 与 `help` 两个辅助命令，与上面的业务命令无关。

### `xinit backup prune --keep <N>` — 清理 `.xinit/backup/` 旧份

删除 `.xinit/backup/` 里**较旧**的备份目录、只保留最近 `N` 份——`update` 每次改写前都先备份（见第 7 节），长期运行会让 `backup/` 无界增长。清理因此是**显式的手工动作**：`update` / `init` / `adopt` / `restore` / `backflow` 的行为都不受影响，**绝不自动清理**。

- **「份」= 一个备份目录**：只认 `update` 的备份命名（`<yyyyMMdd-HHmmss>`，同一秒的第二次起追加 `-2`、`-3`……，各自算一份），按时间升序保留最新 `N` 份、删除其余；别的名字、普通文件、符号链接一律不动（匹配名字但不是目录的也忽略）。
- **默认执行删除**（删除不可恢复——确认这些份无需回滚后再删）；`backup/` 不存在或份数不足 `N` 时不删任何东西（rc=0），**也不会新建 `backup/`**。

| 旗标 | 语义 |
| --- | --- |
| `--keep <N>` | 保留最近 N 份（**必填**；`N ≥ 1`，`N < 1` 报错退 1） |
| `--dry-run` | 只列出将删除的份，不实际删除 |

**零联网、零状态写入**：不联网、不写 `.xinit/state.json`、不写工作树（只会多出项目锁 `.xinit/lock`）；不是 xinit 项目（没有 `.xinit/`）时报错退 1、不会替你建出 `.xinit/`。退出码 0 / 1（见第 8 节）。要清空全部备份就手工 `rm -rf .xinit/backup`（删除范围由 `--keep` 显式指定，没有 `--force`）。

### 交互提问

`init` 与 `update` 在**真终端**上会向你提问；`--interactive` 让 stdin 不是终端时也按可交互处理——**所有提问（含信任确认）都从 stdin 逐行读取**，供脚本与 CI 驱动。它只打开读入通道、不改任何判据：信任确认仍须显式回答 `y` / `yes`（或用 `--yes`），钩子全文照旧**先打印**、不跳过、也不代答；`--yes` 只跳过信任确认，**不代答** vars 与冲突提问。反方向的逃生口也不变：不带 `--interactive` 且 stdin 不是终端时信任门不提问——`xinit update < /dev/null` 只报告（需要确认时即以退 4、零写结束），不打印提问行。

**提问顺序就是回答流的顺序**：一次运行内的所有提问共用同一条 stdin，回答按行依序被消费，脚本据此构造输入文件。

- `init`：① vars 逐条提问（清单**声明顺序**；`--config` 已提供该变量时不问）→ ② 钩子信任确认。
- `update`：① 冲突逐路径提问（路径按**字节序**）→ ② 钩子信任确认。

**不可交互时零提问**：stdin 不是终端、又没有 `--interactive` 时，工具不读 stdin、不打印任何交互文本——管道里预填的回答一律被忽略（此时信任门也不提问，直接按拒绝处理）。想在管道 / CI 里驱动提问，就加上 `--interactive`。

**默认回答恒等于现状**（工具永不在默认路径上做不可逆的事）：

- **冲突**：逐条提问 `<路径>（<冲突类型>）改用模板版？[y/N] `。回答 `y` / `yes` = **改用模板版**——模板版写入工作树（本地原内容先备份到 `.xinit/backup/`），该路径本轮即收敛；**直接回车 = 保留本地**，即第 6 节的现状报告与退 2。（`.xinit/conflicts/` 里的条目在本轮仍判冲突时，该轮会先早退报告、不再提问。）
- **vars**：提问形如 `<提问词> [<默认值>]: `（无默认值时是 `<提问词>（必填）: ` 或 `<提问词>（可留空）: `）。直接回车 = 取默认值；取值不满足 `pattern`（空答按默认值判定）、或必填变量答空时，打印理由并重新提问；**输入结束（EOF）则中止**——退 1、不生成任何文件，错误信息点名 `--config` 补救。
- **信任确认**：直接回车 / 输入结束 = 拒绝（退 4、零写）。

**EOF 语义两侧刻意不同**：`init` 的 vars 采集读到输入结束就**中止**——静默采用半套回答会产出一个你从未确认过的变量集；`update` 的冲突提问读到输入结束则把**其余全部按「保留本地」**处理（已答的按回答处理），永不丢数据。

**Ctrl-C**：等待输入期间按 Ctrl-C 按终端默认语义终止（unix 下 shell 视角退出码 130；Windows 由宿主决定），不写入任何项目内容，也没有专属退出码——所有提问点都在本轮第一次**内容**写盘之前。只读路径不提问：`--dry-run` 恒不提问；`diff` 不注册 `--interactive`（传它是 unknown flag），本身也不提问。

## 5. 模板清单 `xinit.yml` 参考

清单放在模板仓库根目录。解析用严格模式：**未声明的顶层键会直接报错**，所有正则（含 `pattern` 与谓词）在读取清单时编译，写错立刻失败而不是等到渲染。

### `manifest_version`（必填）

整数，当前为 `1`。

### `vars`

序列（不是映射），每项：

| 字段 | 语义 |
| --- | --- |
| `name` | 变量名，必须是合法 Go 标识符（`[A-Za-z_][A-Za-z0-9_]*`）——它要作为模板字段使用 |
| `prompt` | 交互采集的提问词：`init` 对「声明了它、且 `--config` 未提供」的变量逐条提问（非交互运行时被完全忽略） |
| `default` | 默认值，本身按 Go template 渲染，可引用已求值的变量 |
| `pattern` | Go RE2 正则，用户提供的值必须匹配 |
| `required` | 为真时值不能为空 |

- **变量值只能是字符串**；YAML 里写 `default: false` 或 `default: 42` 会被拒绝。
- `default` 可用模板函数：`lower`、`upper`、`title`、`replace`、`trim`、`trimSuffix`、`trimPrefix`、`default`。
- 应用顺序：`--config` 给的值 > 交互回答 > `default`（交互回答只可能来自 `init` 的[交互提问](#交互提问)）；随后逐个跑 `pattern` 校验，不匹配时报错并指出变量名、实际值与 pattern：

  ```
  xinit: 变量 "app_slug" 的值 "Order-API" 不匹配 pattern "^[a-z][a-z0-9-]*$"
  ```

### `substitute`

声明式字面量替换，**按序求值**（下面是本文演示模板里真实使用的两条规则）：

```yaml
substitute:
  - from: "SvcKit"
    to: "{{ .project_name }}"
    paths: ["**/*.go", "**/*.yaml"]
  - from: "svckit"
    to: "{{ .app_slug }}"
    paths: ["go.mod", "Taskfile.yml", "deploy/**"]
```

| 字段 | 语义 |
| --- | --- |
| `from` | 要替换的字面量（**空串会被 schema 拒绝**） |
| `to` | 替换成的文本，按 Go template 渲染 |
| `paths` | 只在命中这些 glob 的模板文件里生效 |
| `when_preceded_by` | 谓词：从**本行开头**到命中位置必须匹配该正则，才替换（典型写法 `'module[ \t]+'`——只改 `module` 后面那一处） |
| `when_followed_by` | 谓词：从命中结束到**本行结尾**必须匹配该正则，才替换 |
| `target_case` | 大小写变换，默认不变换（`from: "Xkitora"` 不会把 `Xdesk` 小写成 `xdesk`） |

匹配细节：`from` 前后一个字节不得是 `[A-Za-z]`（字母边界，避免 `xkitora` 命中 `xkitora2`）；谓词窗口限制在**当前行内**，不跨行。规则按声明顺序生效，先命中的规则在该位置获胜。

### `render`

命中 `paths` 的文件按 Go `text/template` 渲染（根数据是变量表，写 `{{ .app_slug }}`）：

```yaml
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
```

`strip_suffix` 会在目标路径里去掉该后缀（`README.md.tmpl` → `README.md`）。命中 `render` 的路径**不再参与 `substitute`**。

### `generated`

声明「由钩子生成、不能与本地改动合并」的路径（匹配的是**派生侧的目标路径**）：

```yaml
generated:
  paths: ["deploy/bindings.gen.txt"]
```

语义：这些路径上**内容改动的**（磁盘侧为**常规文件**）模板版本会**覆盖**你的磁盘内容（你的版本另存到 `.xinit/overwritten/`；本地版本与模板新内容已一致时行 4 静默收敛），因此**必须由钩子在每次 `init`/`update` 后重新生成**——两者只能同时写，不能只写一边。钩子不会运行的轮次（`--no-hooks`，或模板未声明 `post_update`）这些路径整条跳过（不判冲突、磁盘不动、保留本地内容）；被本地删除的按删除分支处理（行 10）。

### `hooks`

```yaml
hooks:
  post_init: |
    set -e
    echo "post_init: 生成 deploy/bindings.gen.txt"
    printf 'generated-by: template-v1.3.0\n' > deploy/bindings.gen.txt
  post_update: |
    set -e
    echo "post_update: 重新生成 bindings（v1.3.0）"
    printf 'generated-by: template-v1.3.0\n' > deploy/bindings.gen.txt
    printf 'build %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > deploy/.buildinfo
```

（上面这段是本文演示模板 v1.3.0 里真实使用的钩子；其中 `deploy/.buildinfo` 故意写了一个不在计划内的文件，用来演示「计划外副作用」是怎么被报告的。）

块标量，**整块交给一个 `sh -c` 在项目根执行**（不是逐行执行——`cd`、heredoc、`set -e` 都依赖这一点）。钩子是上游模板提供的任意 shell 命令，执行前需要你信任确认，详见[钩子与信任](#10-钩子与信任)。

### `upstream.paths`

声明**共享面**：只有命中这些 glob 的模板路径，才允许派生项目通过 `backflow` 回送改动（匹配的是**模板内原路径**，与 `exclude` 同口径；`!` 开头是排除）：

```yaml
upstream:
  paths: ["internal/util/**", "docs/adr/**"]
```

- 域判定看的是**映射回模板后的路径**（`rename` / 路径变量的逆映射），`--paths` 旗标给出的模式与这里**取并集**，报告逐行注明来源。
- 清单没有这个键、又没给 `--paths` 时，`backflow` 直接报错退 1（不静默判「全都在共享面之外」——那会让人误以为是提交写错）。
- **兼容性代价**：模板一旦声明 `upstream`，所有派生都要升级到支持该键的 xinit 才能 `init` / `update` / `adopt`（清单解析是严格模式，未声明的顶层键直接报错）；只读命令（`check` / `status`）不受影响。

### 其余键

下面几个键本文的演示模板没有用到，是清单 schema 的组成部分：

| 键 | 语义 |
| --- | --- |
| `exclude.paths` | 命中即整树跳过（匹配**模板内原路径**），如 `["dist/**", "node_modules/**"]` |
| `upstream.paths` | 共享面 glob：只允许这些路径回来（见上），如 `["internal/util/**"]` |
| `rename[]` | `{from, to}`，对路径的每一段做子串替换（匹配规则同 `substitute` 的字母边界，子串替换而非整段相等） |
| `protected_literals` | `[]string`，全文作用域的字面量保护区：落在其中的内容不会被 `substitute` 改写 |
| `definitions` | 仅为托管 YAML 锚点而存在，解析后忽略（严格模式要求顶层键必须声明，锚点变量写在这里） |

### 完整示例（本文演示模板的 v1.0.0 清单）

```yaml
manifest_version: 1

vars:
  - name: project_name
    prompt: "项目显示名（Go 标识符风格）"
    default: "SvcKit"
  - name: app_slug
    default: "{{ .project_name | lower }}"
    pattern: '^[a-z][a-z0-9-]*$'

substitute:
  - from: "SvcKit"
    to: "{{ .project_name }}"
    paths: ["**/*.go", "**/*.yaml"]
  - from: "svckit"
    to: "{{ .app_slug }}"
    paths: ["go.mod", "Taskfile.yml", "deploy/**"]

render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"

generated:
  paths: ["deploy/bindings.gen.txt"]
```

### 路径 glob 规则

- 模式**不含 `/`** 时按 gitignore 语义匹配任意深度（`*.go` 能命中 `a/b/x.go`）；模式**含 `/`** 时锚定在模板根（`build/**` 不会命中 `frontend/build/x`）。
- `!` 开头是排除，按序生效；目录一旦被排除，其下的 `!` 无法把它重新包含进来。
- 匹配**大小写敏感**、按字节序比较。
- 无论清单怎么写，`.git/`、`.xinit/`、`xinit.yml` 永远不进派生项目，也不参与版本锚点。

## 6. 冲突与收敛

**什么时候会冲突**：模板和你在同一个文件上都做了改动，且落在同一处（同一行附近）。不同文件的改动、或同一文件不相邻的改动，`update` 会直接合并。

冲突时的行为（退出码 **2**）。下面是一次真实运行的输出（模板仓库 `$WORK/my-service-template`，冲突文件 `docs/notes.md`）：

```
$ xinit update
xinit update: /path/to/my-service-template
版本: v1.1.0 → v1.2.0
（本轮仍待解决：state 里的版本记录保持 v1.1.0，收敛后重跑再推进；§10.2 第 9 步）
  merge-conflict       docs/notes.md
汇总: 写入 0、删除 0、chmod 0、冲突 1
冲突（1 条）需要你处理：
  conflict             docs/notes.md
  accept?              xinit update --accept 'docs/notes.md'
出路一：手工解决冲突，使内容与模板一致（或等模板回退），重跑 update 即收敛；
        若解决结果与模板不同，用上面的 xinit update --accept <路径> 声明已解决（把你看到并认可的版本记为 base，§10.3）。
出路二：把路径写进 .xinit/prune.txt，使其脱离管理（模板对它的改动不再同步）。
参考：模板新内容已写到 .xinit/conflicts/<路径>。
```

类型冲突（symlink / 目录障碍）不可 `--accept`（§10.3）——该节全为类型冲突时，出路一替换为「类型冲突无 `--accept` 出口——写进 `.xinit/prune.txt`（脱离管理），或移走障碍物」。

要点：

- **磁盘不会被改**；冲突路径的模板版本写到 `.xinit/conflicts/<路径>`，供你手工合并。
- **版本记录不推进**（仍是旧版本），所以收敛后重跑 `update` 会自动再次选中引起冲突的那个新 tag。
- `.xinit/conflicts/` 的存在与否**就是**「有没有未解决的冲突」：可解决、已不在冲突的条目会被清理，全部解决后目录消失。
- **真终端上会逐条询问**（带 `--interactive` 时管道下同）：`<路径>（<冲突类型>）改用模板版？[y/N] `——回答 `y` = 用模板版覆盖本地（本地原内容先备份到 `.xinit/backup/`，该路径本轮即收敛）；**直接回车 = 保留本地**，就是上面这套现状行为（单键退出）。详见[交互提问](#交互提问)。

**两条出路：**

1. **手工合并**，然后二选一：
   - 解决结果恰好与模板一致 → 直接重跑 `xinit update` 即收敛；
   - 解决结果是「你的版本 + 模板意图」的合并版 → 重跑时必须加 `--accept`：

     ```bash
     xinit update --accept docs/notes.md      # 可多个路径
     ```

     语义：**把你上次看到并认可的那份模板版本记为新的 base**（即 `conflicts/<路径>` 的内容，不是本次运行的模板），然后照常走合并。这不是「盲信模板」——它只声明「我确实看过并处理了这个冲突」。收敛后该路径如果仍与模板不同，`status` 会把它列为本地改动，这是正常的。
     `--accept` 不能与 `--dry-run` 组合；路径必须真的在 `.xinit/conflicts/` 里；对已是 `generated` 而被跳过的路径、或已写进 `prune.txt` 的路径，会报错拒绝。
2. **写进 `.xinit/prune.txt`**（一行一个路径）：让该路径**脱离管理**——从此它是你的普通文件，模板对它的改动不再同步，也不再产生冲突。

收敛之后重跑一次，应该是全 `Noop`：

```
$ xinit update
版本: v1.2.0 → v1.2.0
汇总: 写入 0、删除 0、chmod 0、冲突 0
```

## 7. `.xinit/` 目录说明

派生项目根目录下的 `.xinit/` 是状态目录，**不进版本控制**（`init`/`update` 幂等写入 `.git/info/exclude`；`status` 与 `check` 只报告不写入）。

| 条目 | 职责 |
| --- | --- |
| `state.json` | 受管路径清单（每条含 `src`、内容 hash、exec 位、二进制/混合 EOL 标记、`generated` 标记）、模板来源与版本、变量值、已信任的钩子 |
| `state.json.prev` | 上一版 `state.json` 的备份（每次保存前保留旧字节；首次写入不产生）。`state.json` 损坏时按报错指引人工恢复——把它复制回 `state.json` 后重试；**不自动回退**（`Load` 不读它） |
| `base/` | 渲染后的**基线树**（普通目录副本），三方比较的 base 侧 |
| `conflicts/` | 未解决的冲突路径 → 模板版本；目录存在即「有冲突待处理」 |
| `overwritten/` | 被模板覆盖掉的你的内容（`generated` 路径），持久保留 |
| `backup/` | 每次 `update` 改写前的原内容 + exec 位，按运行开始时刻分目录（同名时追加 `-2`、`-3`），**不覆盖、不自动清理**（清理旧份：`xinit backup prune --keep N`） |
| `backflow/` | 回流产物（`backflow` 生成的 `.patch`；文件名含派生与模板两个短 sha）。有它在 `.xinit/` 里就已进 `.git/info/exclude` 的整目录条目，不用单独登记 |
| `lock` | 项目锁，防止并发 `update`（`diff` / `status` / `check` 只读不加锁）；unix 用 flock、Windows 用 LockFileEx；锁文件常驻不删；Windows 上持锁期间被共享模式占用，外部工具删不掉（§22.3） |
| `prune.txt` | 一行一个路径：这些路径脱离管理、不再同步（单向，见下） |

`prune.txt` 是**单向**的：写入的路径在下一轮 `update`（生效轮）从 `.xinit/base/` 与 `state.json` 记账中一并移除——该轮不会单独提示这些路径已脱离管理（汇总计数里也看不到这一变化）。此后 `xinit restore` 对它们不适用（已不在记账路径里），模板对它们的改动也完全无声。要拿回管理：从 `prune.txt` 删掉该行、再跑一次 `update`——该路径按「模板新增」重新纳入管理：本地无该文件时写回模板记录的版本；本地已有同名文件时，与模板一致则直接纳入管理，不同则报 `conflict-both-added` 冲突（退 2，模板版写到 `.xinit/conflicts/`，本地文件不动）——手工处理到与模板一致后重跑，或按 §6 用 `--accept` 声明已解决（把 `conflicts/` 里的模板版记为 base，本地内容此后按本地改动处理）。

### 用户缓存目录与渲染快照缓存

`init` / `adopt` 成功、`update` **收敛轮**（无未解决冲突、折叠拒绝或障碍物）成功后（均在项目状态成功落盘之后），会把本轮**渲染原文**顺手写一份到用户级缓存目录（best-effort：写失败只在 stderr 警告，命令照常成功、项目状态零影响；§21）。它**只用于**在项目内 `.xinit/base/`（基线树）丢失或损坏时作恢复来源——覆盖「dir 源 base 丢失」与「git 源记录的 commit 在模板源中取不到」这两个原本只能 `adopt`（或硬错）的口子；**恢复链**在 base 健康时不读快照（零命中也不创建缓存根）。

- **位置**：`<UserCacheDir>/xinit/render/<familyKey>/`——darwin 为 `~/Library/Caches/xinit/render/`，Linux 为 `$XDG_CACHE_HOME/xinit/render/`，Windows 为 `%LocalAppData%\xinit\render\`；条目内是 `manifest.json`（清单）与 `tree/<Dst>`（逐路径的渲染原文）。**Windows 注**：`%LocalAppData%` 未定义（服务账户/精简 CI）时用户缓存目录推导失败——快照写侧按 best-effort 降级为一行警告，读侧（快照/镜像）按其既有错误面处理（§22.14、§22.17 R14-12）。
- **可整体删除**：`rm -rf <cacheRoot>/render`（`<cacheRoot>` 即 `<UserCacheDir>/xinit`）——删掉后行为与没有快照一致，这也是快照损坏时的维修指引。
- **无 GC**：每族（= 一个「输入元组」：引擎版本、模板来源、种类、变量）至多一份条目，写入即整条覆写轮转；总量 = 去重后的输入元组数，随使用缓慢增长、**无自动清理**。
- **已知限制（跨项目同族边界）**：同 (engine, source, kind, vars) 的两个项目钉在不同 commit 时，后写者覆写前者的条目（last-manifest-wins）——前者此后 base 丢失只能走 git 重渲染回退 / dir 硬错。
- **引擎升级后**：旧条目自然失效（校验不过即静默 miss），可手工删除。

## 8. 退出码表

| 码 | 含义 |
| --- | --- |
| 0 | 成功，无待处理项 |
| 1 | 错误 |
| 2 | 有冲突、折叠拒绝或障碍物待解决 |
| 3 | 钩子失败（文件与状态已正确落盘） |
| 4 | 需要信任确认 |
| 5 | `diff` 发现有非 `Noop` 的动作；或 `status --fetch` 发现有可用更新 |
| 6 | 检查发现问题（`xinit check` 有 `FAIL` 项） |

`diff` 无动作时返回 0；`status` 不带 `--fetch` 时恒为 0（除非出错）；`check` 只有 `warn` / `skip` 时返回 0；`--no-hooks` 下被跳过的 `generated` 路径不需要你处理，**不影响退出码**。

`backflow` **只用 0 / 1（不新增码）**：0 = 导出成功（**含警告**）；1 = 错误（资格、未收敛、全部路径被出口过滤、全 noop、IO）。「有警告」不是 2——2 是 `update` 的「有待处理冲突」语义，回流没有它（冲突一律 fail-closed 退 1）。

`restore` **只用 0 / 1（不新增码）**：0 = 全部恢复或无变化；1 = 错误（含拒绝覆盖——预检不过即零写入）。

`backup prune` **只用 0 / 1（不新增码）**：0 = 完成（含无删除项）；1 = 错误（`--keep < 1`、不是 xinit 项目——这两者都零磁盘动作；以及删除失败）。

交互回答不引入新退出码：vars 采集中止（EOF）= 1、冲突默认回答 = 现状（2）、改用模板版 = 正常写入通道（收敛轮 0）、信任拒绝 = 4。

## 9. 回流

`backflow` 是生命周期的第三步——**实例化（`init`）→ 更新（`update`）→ 回流（`backflow`）**：把派生项目对**共享面**（清单 `upstream.paths` ∪ `--paths`）的一次改动**结构化地**翻译回模板坐标系，产出一份模板作者可以直接 `git am` 的补丁。

这是 **opt-in** 能力：`init` / `update` 都不要求派生项目是 git 仓库，`backflow` 要求（它用 git 枚举提交、读提交内容，见 3.2）。不用这条命令时，其余命令与路径的行为与从前完全一样。

```
派生侧   xinit backflow <commit>          → .xinit/backflow/<派生sha7>-<模板sha7>-<slug>.patch
传递     人工/任意通道把补丁交给模板作者
接收     git am -3 [--keep-cr] <patch> + 模板侧 check/测试 + CHANGELOG + tag   ← xinit 不参与接收侧
广播     全体派生 xinit update 收到回流成果（往返闭合 = 下次 update 零冲突）
```

目标提交要过这些前置（任一不过即报告并退 1）：派生是 git 仓库；没有进行中的操作（merge / rebase / cherry-pick / revert）；**已跟踪文件干净**（未跟踪文件只警告、不挡）；提交存在且非 root / 非 merge；提交消息的**末段 trailer 块**含逐字行 `Template-Upstream: yes`；提交有改动；`.xinit/base` 与记录的模板 commit **同源**（上一轮 `pending` 推进过 base 的混合态要先跑 `xinit update` 收敛）；改动落在共享面内。

**翻译与夹带扫描**：预像区（模板原文与 base 都存在的那部分）的改动，由三方合并结构性还原成**模板措辞**；**新增行 / 新增文件不做翻译**（没有预像就没有翻译）——它们由**夹带扫描**逐条点名（`backflow-warn <文件>:<行>: <token> — <内容>`，新增文件的路径段命中则打 `path: token`），需要模板作者人工改写。改动落在替换点附近（同一 hunk 邻域）会**翻译冲突**：此时整体 fail-closed、不产补丁，报告的冲突块三侧是——`ours` 段 = **模板措辞**（基准侧）、`base` 段 = 渲染基线（`.xinit/base` 的重建）、`theirs` 段 = 你的提交内容；把冲突块里的行改回模板措辞后重导出。

**接收指引**（`backflow` 成功时报告会原样打印这套话；模板作者**不需要**安装 xinit）：

1. **接收仓必须含记录的模板 commit 的历史**——补丁的 pre-image 就是它；强推 / 换史会让 `-3` 的索引回退失效。建议先 `git checkout -b backflow/<派生sha7>`。
2. **CRLF 模板必须 `git am -3 --keep-cr`**（或接收仓 `git config am.keepcr true`）：默认 `am` 会剥掉补丁内容行的 CR，rc=128 硬失败并留下中断态。
3. 接收仓工作树干净（`am` 直接改写工作树）。
4. 补丁由 `--full-index` 生成（40 位 blob），接收仓 git 需 ≥ 2.38（同 xinit 依赖）。

应用：`git am -3 <patch>`（CRLF 模板加 `--keep-cr`）。中断态：`git am --abort` 放弃；解决冲突后 `git am --continue`。模板 HEAD 已经前进也没关系——`-3` 用 blob 索引重建基线、提交落在当前 HEAD 之上；只有目标 commit 已不在历史里时 `am` 会失败并留中断态，`--abort` 后用 `git am -3` 重试或人工移植。

**已知限制**（M8）：

- **冲突半径是替换点的 hunk 邻域**（token 行 ±1、紧邻 token 行的插入删除），比「同一行」的直观印象大——替换点在文件里常成簇（标题 / 版权头 / import / 配置名）。
- `render` 模式文件、删除 / 重命名 / 拷贝、二进制与混合行尾、symlink、`generated` 位文件、已 prune 或模板来源已消失的路径：一律**警告不导出**。
- 多个派生回送同名新文件时不测重：接收时第二个派生的补丁会冲突，由模板作者裁定合并。
- 夹带扫描**未经硬闸门**（xinit 没有模板侧检查器）：它是警告级 + 模板作者人审，报告自陈这一点。
- 产物文件名含派生与模板两个短 sha：模板发新版后的重新导出是**新产物**、不覆盖旧的；`backflow` 不自动清理 `.xinit/backflow/`（与 `backup/` 同规）。
- 与 git 的 mailbox 边界同病：提交消息里的独立 `---` 行、以及形如 `From … Mon Sep 17 00:00:00 2001` 的行（尤其作尾行）会被 mailinfo 静默截断。
- 边界：sha256 对象格式的模板仓不支持 `-3` 回退；纯非 ASCII 的提交主题会让文件名 slug 退化为 `patch`（文件名仍靠两个短 sha 唯一）。

## 10. 钩子与信任

模板清单里的 `hooks` 是**上游仓库里的任意 shell 命令**，因此执行前必须由你确认。

- **何时确认**：首次遇到某组命令、或内容相对上次有变化时。工具打印**钩子全文**（内容有变化时还打印行级 diff），然后要求 `--yes` 或交互回答 `y`。diff 的基准是**当前记录的模板版本**，所以刚在 `--no-hooks` 下跳过过一轮时，紧接着的确认可能没有 diff 段（版本已经被推进到含新钩子的那一版了）。
- **拒绝的代价是零**：不加 `--yes` 直接以退出码 **4** 结束，本轮**不做任何修改**（不写文件、不写备份、不动状态）。确认时机在任何写入之前。
- **`--yes` 一次性越过信任门**：自动化（CI）中启用它时，**模板源本身即信任根**——必须仅来自可信渠道。
- **`--no-hooks`**：跳过执行，但仍记录本次内容——所以下一次不带 `--no-hooks` 的运行**仍然需要确认**（跳过不等于信任）。此时：
  - `generated` 路径**整条跳过**（不判冲突、磁盘不动、base 保持旧值），带钩子重跑时再按「模板生成物」正常处理；这不影响退出码。
- **钩子失败**：退出码 **3**。文件与状态**都已正确落盘并保留**（渲染产物本身没错），工具打印失败的命令与手工重跑方式。
- **副作用报告**：钩子执行前后各做一次目录快照，报告中列出
  - 「计划外副作用」——钩子触碰了不在计划内的路径（如 `npm install` 写出的文件）；
  - 「计划内但被钩子改写」——这些路径下一轮可能被判定为「本地改过」，报告会点名它们。
- **执行时机**：`post_init` 在 `init` 落盘后；`post_update` 在**每一轮** `update` 落盘后执行——**包括本轮没有任何模板改动的 `Noop` 轮**。所以钩子应当幂等，并且要意识到它每轮都会跑。
- **不承诺回滚**：钩子是任意命令，`npm install` 会改写依赖目录、生成器可能删掉不在计划内的旧文件，「工作树与运行前逐字节一致」物理上做不到。工具能保证的是：**本项目改写过的计划内路径**的原文都在 `.xinit/backup/<时间戳>/` 里，钩子写坏的东西需要你用自己的方式恢复（模板侧还有 git）。
- **平台**：钩子以 `sh -c` 执行（§22.12 维持唯一形态）——PATH 中没有 POSIX `sh` 的平台不支持钩子并会明确报错（Windows 由 Git for Windows 自带 sh 满足）。

## 11. 已知限制

1. **平台**：macOS / Linux / **Windows 原生**（§22 设计；Windows 真机验证清单见 §22.17——本批含交叉编译门禁与 darwin 可测的代理语义，真机行为未验证）。钩子以 `sh -c` 执行（Windows 用 Git for Windows 自带 sh）；Windows 无「可执行位」语义：exec 记账照常、磁盘比较按平台跳过（§22.6），`check` 的 A9 按平台跳过；`.xinit/lock` 持锁期间（Windows 句柄共享模式）外部工具删不掉；reparse/junction 中间段显式拒绝、完整语义未支持（§22.5、§22.16 P6）。
2. **git 模板的选版（重要）**：`init` **没有** `--revision` 旗标，永远取模板默认分支的 **HEAD**，并在状态里记为 `"HEAD"`——而 `"HEAD"` 无法参与 semver 比较，所以 **`init` 之后第一次 `update` 必须显式写 `--revision <tag>`**（否则退出码 1，报错信息里会提示）。一次对齐之后默认模式即可继续工作。（dir 源没有这个问题：它没有版本概念，首个 `update` 直接跑即可——见第 4 节。）
   建议：模板仓库要发版本就**先打 tag 再让新项目 init**。如果模板的 HEAD 领先于最新 tag，`init` 会把未发布的改动一起带进新项目；随后若按上面的办法对齐到最新 tag，这些改动会被**回退**（工具会打印「目标版本不是当前版本的后代（降级/回退）」的警告，但不会阻止你）。
3. **不承诺回滚**：见上一节的「不承诺回滚」。
4. **模板改路径时自动迁移，但有回退**：模板把 `a.go` 物理改名为 `b.go`（内容不变），或改掉出现在路径里的变量（如 `app_slug`）、`rename` 规则变化导致文件位移时，`update` 会把旧路径上的**本地改动随文件搬到新路径**，旧路径按删除处理（原内容照常进 `.xinit/backup/`）。报告的「路径迁移」节对每一对给出四态之一（`diff` / `--dry-run` 与实跑同判定、同文案）：
   - `migrated`：本地改动已随迁移保留，旧路径按删除处理；
   - `rename`：本地无改动，按普通的删除 + 新增完成改名；
   - `skip`：跳过迁移、保持不迁移时的行为（旧路径保留本地版并告警，新路径按模板新增处理），括号里给出理由（生成物路径、空内容配对、类型不符、名字与目录都变而配对置信度不足、新路径的写入或旧路径的删除会被拒绝）；
   - `fallback`：迁移未完成（模板与本地都在改且相冲突 / 二进制或混合行尾不参与三方合并 / 三方合并失败）——旧路径**保留本地版**、新路径写入模板版，**本地改动需要你手工合并到新路径，或把旧路径写进 `.xinit/prune.txt`**。

   **重试语义**：`fallback` 与非拒绝类的 `skip` 之后该对**不再自动重试**；拒绝类 `skip` 看拒绝落在哪一侧——落在**新路径**（如磁盘上已有同名目录、大小写折叠冲突）时，**本轮新路径不写入（退出码 2）**，移走障碍后重跑会再次配对并完成迁移；落在**旧路径**（删除路径解析逃逸）时，新路径照常写入模板版、该对不再重试，按 `fallback` 的人工去向处理。认不出的情况仍按删除 + 新增处理（本地改过的旧路径保留并告警，报告里没有迁移行）：模板**物理改名的同时改了内容**，以及多个旧路径与新路径无法一一对应的歧义配对。
5. **`git ≥ 2.38` 是硬依赖**：启动时检查，不满足即退出。
6. **M9 之前 init 的存量项目**：若当时在项目目录外以相对路径给出模板源（该相对路径在项目目录内解析不到），`state.template.source` 记录的是相对路径——在项目目录内 `update` 会落入 git URL 分支而失败（历史行为）。把 `.xinit/state.json` 里的 `template.source` 改成绝对路径即可恢复；M9 起 `init`/`adopt` 对本地路径源一律记绝对路径。
7. **模板源目录迁移/改名**：`state.template.source` 记的是绝对路径——模板目录移动后，把 `.xinit/state.json` 里的 `template.source` 改成新绝对路径即可恢复。
8. **路径字符收紧（M11 起，§22.4）**：受管路径任一段不得含 `\` 或 `:`（Windows 上分别有分隔符 / 盘符与 ADS 语义）。此前可渲染的 dir 源文件（如 `a\b.txt`、`a:b.txt`）M11 起明确拒绝——请改名，或改用 git 模板源；git 源中不存在这类文件名，不受影响。同时，路径校验的错误文案已随单点谓词统一（例如「不能是绝对路径」→「是绝对路径: …」），判据不变——如有脚本比对这些消息请注意。
9. **dir 源的版本锚点含 exec 位（§22.7 R4c）**：dir 源树 hash 含可执行位信息——模板目录跨平台复制（如 macOS → Windows）后 exec 记账可能翻转，`update` 可能报告「模板版本变化」类输出而模板内容实际未变（忽略即可，或改用 git 源）。
10. **写失败路径上的权限放宽（§22.8 明示取舍）**：unix 上，当目标文件只读、且其所在目录不可写时，原子写（`update` 落盘）会在 rename 失败前先清掉该文件的只读位、失败后**不还原**——即失败路径上该文件可能变为可写（成功路径不受影响：「清位清的是目标的位」是 §22.8 的取舍，非缺陷）。若在此类失败后发现权限变化，按需重新 `chmod` 即可。

## 12. 开发

```bash
make check     # fmt + vet + test，提交前跑这个
```

| 目标 | 作用 |
| --- | --- |
| `make build` | `go build -o bin/xinit .` |
| `make install` | `go install .` |
| `make test` | `go test ./... -count=1` |
| `make vet` | `go vet ./...` |
| `make fmt` | `gofmt -l .` 输出非空即失败 |
| `make check` | `fmt` + `vet` + `test` |
| `make clean` | 删除 `bin/` |

测试结构：

- **单元测试**在各 `internal/*` 包里（渲染、清单、决策表、合并、状态、模板获取等）。
- **端到端测试**在 `internal/cli/testdata/script/*.txtar`（68 个脚本，`go test ./internal/cli` 时执行）。每个脚本是一个真实的命令序列，在临时目录里跑 `init`/`update`/`diff`/`status`/`check`/`adopt`/`restore`/`backflow`/`backup prune` 的全流程（含冲突、钩子信任、生成物、备份、路径迁移、回流往返、退出码断言），脚本内用 `xinit` 与 `xinit-code` 两个命令调用同一入口 `Execute()`；`xinit-code` 会把退出码打成 `exit=N` 供断言。
