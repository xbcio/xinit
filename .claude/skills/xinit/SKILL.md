---
name: xinit
description: 把一个现有项目/模板库快速改造成 xinit 模板（编写 xinit.yml：vars / substitute / protected_literals / rename / generated / upstream / hooks），并以「渲染等价性对拍 + 异身份冒烟」双门禁验证。Use when：将某个库接入 xinit 作为模板、为仓库编写或审阅 xinit.yml、把旧机制模板（template/identity.yml）迁移到 xinit、为模板打版本 tag。
---

# xinit 模板化改造（templateize）

把一个普通 git 仓库改造为 **xinit 模板库**：根目录加一个 `xinit.yml`，使 `xinit init` 能从中渲染出派生项目、`xinit update` 能对其做增量同步、`xinit backflow` 能回流。

## 权威依据（写清单前必读，不得凭记忆）

| 材料 | 路径 |
|---|---|
| **清单 schema 权威** | `~/Desktop/caffe/xbcio/xinit/.claude/specs/2026-10-08-xinit-design.md` §3（~46-189 行：字段语义 / glob 语义 / 隐式排除 / 演进规则） |
| **完整范本**（Wails 桌面模板，216 行带注释） | `~/Desktop/caffe/xbcio/xkitora/xinit.yml` |
| **最小示例** | `~/Desktop/caffe/xbcio/xinit/README.md` §3.1 |
| xinit 二进制 | `cd ~/Desktop/caffe/xbcio/xinit && make build` → `bin/xinit`（PATH 可能没有，用绝对路径） |

## 心智模型（5 条）

1. **模板 = git 仓 + 根目录 `xinit.yml`**。`.git/`、`.xinit/`、`xinit.yml` 永远不进派生（隐式排除，spec §3.3）。
2. **渲染 = 身份字面量替换**：`substitute[]` 把模板自己的拼写（`xkitora` / `Xkitora` / `XKITORA`）替换为 `{{ .var }}`。**模板清单里的 default 值 = 模板自己的身份值**——这是「渲染等价性对拍」的基石。
3. **求值序 = 列表序、命中即胜**：整串（`com.github.xkitora`）必须排在裸 token（`xkitora`）之前，否则被拆走。
4. **`generated` 与 `hooks` 是绑定对**：generated 路径冲突时被 theirs 覆盖（spec:104），**必须**由钩子每轮重生成；只写一边 = 坏模板。
5. **tag = 版本锚**：派生 `update` 默认取最新 semver tag；不打 tag 只能显式 `--revision <ref>`。

## 复杂度分诊（决定走哪条通道）

| 目标库特征 | 通道 | 增量动作 |
|---|---|---|
| 纯文本身份（无生成物、无路径中的身份名） | **快速通道**：vars + substitute + tag，3 步 | 阶段 0-2 精简版 + 门禁 |
| 有构建生成物（bindings / plist / lockfile 类） | 完整通道 + **阶段 3**（generated + hooks） | 全流程 |
| 路径段含身份名（如 `bindings/<name>/`） | 完整通道 + **rename 段** | 全流程 |
| 含旧机制面（`template/identity.yml`、`scripts/`、`.githooks/`） | 完整通道 + **阶段 4**（迁移清理） | 参照 xkitora ADR-0009 案例 |

## 工作流

### 阶段 0 · 侦察（产出：身份族清单 + 出现面归类）

```bash
cd <目标库> && git status --short --branch   # 确认 git 仓；没有则 git init -b main + 首次提交
# 身份族穷举（大小写三态 + 各字段），逐族统计出现面：
grep -rIn -e '<name>' -e '<Name>' -e '<NAME>' . --exclude-dir=.git | wc -l
grep -rIn '<org>' <bundle-id 前缀> '<scheme 槽位>' '<data dir 槽位>' . --exclude-dir=.git
```

把每个命中归入四桶：

| 桶 | 去向 | 判据 |
|---|---|---|
| ① 参数化 | `substitute` 替换 | 身份展示/机器名，派生必须换 |
| ② 保护 | `protected_literals` | 含身份字面量但**产品中立**（共享包名如 `@xbcio/ui`）；成对符号（C 符号/桥接声明，单侧改名即链坏）；测试夹具（是断言输入） |
| ③ 排除 | `substitute` 的 `!` 排除 或 `exclude.paths` | 由工具重生成的锁文件；模板叙事文档（changelog/ADR——按设计携带模板身份） |
| ④ 自动跳过 | 无需声明 | 二进制美术（png/icns/ico）；但**含文本的伪二进制**（如 `.icon/` 里的 svg）要手动保护 |

再识别两类结构面：**生成物**（谁由构建链产出 → 阶段 3）与**路径中的身份段**（`ls -d` 目录名 → rename 段）。

### 阶段 1 · vars 设计

从标准 8 变量按库裁剪（xkitora 版，可直接抄改）：

| name | required/default | pattern | 说明 |
|---|---|---|---|
| `slug` | **required** | `^[a-z][a-z0-9-]*$` | 机器名；go.mod 与 bindings 目录默认值 |
| `product_name` | `'{{ title .slug }}'` | — | 显示名，永不翻译 |
| `module_path` | `'{{ .slug }}'` | — | **单段**（含 `/` 会 fail-closed，见坑 6） |
| `bundle_id` | `'com.github.{{ .slug }}'` | — | 各平台唯一 ID 基名 |
| `org` | `'xbcio'` | — | companyName/copyright/LICENSE vendor |
| `scheme` | `'{{ .slug }}'` | — | URL scheme（与代码常量互钉） |
| `file_extension` | `'{{ .slug }}'` | — | 文件关联扩展名 |
| `data_dir` | `'{{ .slug }}'` | — | 用户配置目录名 |

约束（spec §3.1）：变量名是 Go 标识符；**值只能是字符串**（YAML 非字符串标量是陷阱）；`default` 可引用**前序**变量；`pattern` 是 Go RE2；`prompt` 供交互提问。

### 阶段 2 · 写 xinit.yml

按 spec §3 的字段语义逐段写。要点：

- `definitions`：只为托管 YAML 锚点（`Strict()` 拒未声明顶层键）；多条 substitute 共享作用域时用锚点定义一次（见 xkitora `rewrite_scope`）。
- `substitute`（**列表序 = 求值序，命中即胜**）：
  - 排序：整串先（`com.github.xkitora` → bundle_id）→ 槽位谓词（scheme/ext/data_dir/module）→ 显示名（`Xkitora`）→ 大写形态（`XKITORA` + `target_case: upper`）→ 裸 token 兜底。
  - 谓词是**行内紧邻**匹配：`when_preceded_by` = 命中位之前的本行文本以该模式结尾；`when_followed_by` = 之后的本行文本以该模式开头。
  - `from` 不得为空（schema 拒）。
- `protected_literals`：全文作用域，整串保护。
- `exclude.paths`：匹配**模板内原路径**（`Src`），命中即整树不进派生。
- `render`：`.tmpl` 后缀类（命中走 `text/template` 并去后缀，从 substitute 作用域排除）——只在库本来就带 `.tmpl` 时用。
- `rename`：对**路径的每一段**做子串替换（字母边界），用于 `bindings/<name>/` 这类目录名。
- glob 语义（spec §3.2，gitignore 式）：不含 `/` 的模式自动前加 `**/`（任意深度）；含 `/` 锚定在根；`!` 按序排除、目录一旦排除不可重含；模式含 `{{ }}` 时整条按字面量转义（**模板作者照直觉写 `{{ .slug }}/**` 即可，别混用 `{a,b}`**）。

### 阶段 3 · generated + hooks（有生成物时必做）

```yaml
generated:            # 匹配派生侧 Dst；这些路径冲突时被 theirs 覆盖
  paths:
    - 'frontend/bindings/**'
    - 'build/**/Info.plist'        # …列出全部钩子产物
hooks:
  post_init: |
    set -e
    <构建/生成命令链>                 # 例：wails3 generate bindings && npm install
  post_update: |
    set -e
    <同上>
```

- **绑定约束**：generated 里的每条路径都必须由钩子重生成，反之钩子改写的计划内路径都应声明 generated；只写一边 = 坏模板（spec:104 与防呆：`--no-hooks` 时行 5 覆盖不生效）。
- 钩子是**整块交一个 `sh -c`**（不是逐行）；cwd = 项目根；`set -e` 保任一环失败即中止；失败 = init/update 退出码 3。
- 信任门：带钩子的模板首次 `init`/`update` 会要求信任确认（`--yes` 跳过 / `--interactive` 脚本驱动 / `--no-hooks` 跳过执行但下次仍需确认）。

### 阶段 4 · 清理旧机制面（仅旧机制模板）

旧机制（`template/identity.yml`、`scripts/`、`.githooks/` 等）整体移除，语义逐节搬进 `xinit.yml`（对照表见 xkitora `xinit.yml` 头部注释；决策见其 `docs/adr/0009`）：`vars` ← identity 块；`substitute` ← upstream_tokens；`protected_literals` ← protected_literals；`generated.paths` ← generated_globs；`upstream.paths` ← shared_globs；hooks ← 重生成链。

### 阶段 5 · 验证门禁（**双门禁互补，必须全过**）

**① 渲染等价性对拍**（验「无过冲」——替换只把字面量换回自值）：

```bash
XINIT=~/Desktop/caffe/xbcio/xinit/bin/xinit
TPL=<目标库绝对路径>
printf 'slug: <模板自身名>\n' > /tmp/self.yml          # 其余变量走 default（= 自值）
"$XINIT" init --template "$TPL" --config /tmp/self.yml --no-hooks --yes /tmp/verify-self
diff -r -x .git -x .xinit -x xinit.yml \
  -x node_modules -x .task -x bin -x test-results -x .omc -x dist -x .DS_Store \
  "$TPL" /tmp/verify-self
# 判据：diff 中**不得出现 "differ" 行**（共有文件内容必须逐字节同）。
# "Only in <模板侧>" 行来自两类可解释面：
#   ① 模板工作树的非跟踪产物（node_modules / .task / bin / test-results / .omc /
#      .bindings-tmp-* 等——尽量用 -x 排除；模板是 git 源时渲染只取跟踪面）；
#   ② exclude 面与 render 的 .tmpl 面（派生侧去后缀，文件名天然不同）。
# 互证：init 输出「已生成 N 个文件」应 = 模板跟踪文件数 − 1（xinit.yml）。
# 实测（xkitora，2026-10-14）：430 = 431 − 1；按上方 -x 集 diff 仅 6 行 Only-in
# （4 个 git-ignored + 1 个构建临时目录 + 1 个空目录——git 不跟踪空目录）、**零 differ**。
```

**② 异身份冒烟**（验「无漏网」——该替换的都替换了）：

```bash
printf 'slug: foo-bar\nproduct_name: FooBar\n' > /tmp/other.yml   # 换整套身份值
"$XINIT" init --template "$TPL" --config /tmp/other.yml --yes /tmp/verify-other
cd /tmp/verify-other && "$XINIT" check                            # 0 失败 / 0 警告
grep -rIn -e '<name>' -e '<Name>' -e '<NAME>' . --exclude-dir=.git --exclude-dir=.xinit \
  | sed 's|^\./||' | cut -d: -f1 | sort | uniq -c
# 判据：**每个命中文件都必须能归入清单声明的保留族**（②保护桶 / ③排除桶的 ! 排除
# 面 / protected_literals）。任何「意料之外」的命中 = 漏网，回头补 substitute 规则。
# 实测（xkitora 冒烟，2026-10-14）：22 个命中文件全部落在其 rewrite_scope 排除面
# 的族①-⑥（成对符号 4 / 夹具 2 / lockfile / changelog / docs 快照 14）——零意外。
# 注：非 git 目录会有 `.xinit` exclude 提示，仅验证用可忽略（真实派生先 git init）。
"$XINIT" update --revision <同 ref> --no-hooks                    # 幂等：应全 Noop
```

**③ 构建链**（若库可测）：`go test ./...` / `npm run build` / 原库自带门禁照跑一遍。

**④ 提交 + 打 tag**（annotated）：

```bash
git add -A && git commit -m 'feat: 接入 xinit 模板（xinit.yml）'
git tag -a v0.1.0 -m 'xinit 模板首版'
```

> 双门禁为何互补：对拍只证明「被替换的都对」（自值替换回自值也通过）；异身份 grep 才证明「该替换的都替了」。两者都过才算模板可信。

### 阶段 6 · 文档

- `TEMPLATE_CHANGELOG.md`：模板自身演进记录（携带模板身份，不进 substitute 改写面）。
- README 增「从此模板派生」节：`git init` → `xinit init --template <url/路径> --config vars.yml --yes` → 常规循环；提醒派生 `update` 默认跟随最新 tag。

## 常见坑（全部有实案）

1. **顺序即语义**：`com.github.xkitora` 不先于 `xkitora`，整串会被裸 token 规则拆走。
2. **漏大写形态**：`XKITORA`（env var / 常量）漏了会静默保留——异身份冒烟 grep 专抓这类。
3. **空 `from` 被 schema 拒**（会匹配每个位置）。
4. **glob 锚定**：含 `/` 的模式锚根（`build/**` 不会改写 `frontend/build/**`）；不含 `/` 任意深度；目录被 `!` 排除后不可重含。
5. **模板路径 glob**：模式含 `{{ }}` 整条转义，不能与 `{a,b}` 混用；拆两条即可。
6. **`rename` 替换值含 `/` → fail-closed**：`module_path=github.com/acme/app` 会让路径段变多级 → rc=1、零半成品（这是防呆，不是 bug）。所以 module_path 保持单段。
7. **generated ↔ hooks 绑定**：只声明 generated 不写钩子 = 每轮 update 都被覆盖且不会重生。
8. **lockfile 两难**：npm 会按 `package.json` 重写 `name`——要么进 `generated` + 钩子 `npm install`，要么排除出 substitute 并把 status 常驻 1 条本地改动当已知代价（xkitora 选后者）；**别两头都不做**。
9. **测试夹具/成对符号**：改名会反转断言或链接失败——进 ②桶保护，别参数化（xkitora 族①/②）。
10. **dir 源 vs git 源**：目录（非 git）模板没有版本概念（树 hash 为锚，`--revision` 无意义）；要版本跟踪就 `git init` + tag。
11. **钩子环境**：整块 `sh -c`、cwd=项目根、`set -e`；平台要有 POSIX `sh`（Windows 用 Git 自带）。
12. **信任门旗标语义**：`--yes` 只跳过信任确认（不代答 vars 与冲突提问）；脚本驱动交互用 `--interactive` 而不是裸管道。

## 验证清单（逐项打勾才可收工）

- [ ] 对拍 diff **零 differ**（only-in 面逐条可解释：非跟踪产物 / exclude / .tmpl / 空目录）
- [ ] 异身份冒烟：旧字面量 grep 命中**全落清单声明的保留族**（零意外命中）
- [ ] `xinit check` 0 失败 / 0 警告
- [ ] `xinit update --revision <同 ref>` 全 Noop（幂等）
- [ ] 构建链测试通过（若适用）
- [ ] generated 每条路径都在钩子重生成面内（逐条核对）
- [ ] annotated tag 已打 + `TEMPLATE_CHANGELOG.md` 已记
