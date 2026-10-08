package render

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/xbcio/xinit/internal/manifest"
)

// identityDoc 只取差分测试需要的字段；identity.yml 里其余内容（paths、
// known_derived_tokens、注释块）与本测试无关，用非严格解析忽略。
type identityDoc struct {
	Identity map[string]string `yaml:"identity"`
	Template struct {
		UpstreamTokens []struct {
			Literal        string `yaml:"literal"`
			Target         string `yaml:"target"`
			TargetCase     string `yaml:"target_case"`
			WhenPrecededBy string `yaml:"when_preceded_by"`
			WhenFollowedBy string `yaml:"when_followed_by"`
		} `yaml:"upstream_tokens"`
		ProtectedLiterals []string `yaml:"protected_literals"`
	} `yaml:"template"`
}

// derivationIdentity 是临时仓库里要落下去的派生身份。
//
// **每个字段给不同的值**，这是刻意的：xkitora 的真实表里有 7 条规则都
// 命中裸字面量 `xkitora`，靠 when_preceded_by / when_followed_by 分派到
// scheme / file_extension / data_dir / module_path。如果这些目标值都一样
// （比如都是 xdesk），谓词判对了和判错了输出完全相同 —— 差分测试就成了
// 空转。给成互不相同的值，谓词分派才真的可观测。
//
// org 同样取 xbcorp 而不是 xbcio：真实 org 与本仓库同名，那条规则恒等，
// 换一个值才能验证 org 规则，也才能让 protected_literals 的 `@xbcio/ui`
// 真的处在"会被误改"的位置（实测它确实被保护住了，见语料「保护区」）。
var derivationIdentity = map[string]string{
	"slug":           "xdesk",
	"product_name":   "Xdesk",
	"module_path":    "xmodule",
	"bundle_id":      "com.github.xdesk",
	"org":            "xbcorp",
	"scheme":         "xscheme",
	"file_extension": "xext",
	"data_dir":       "xdata",
}

// TestDifferentialAgainstRebrand 用**真实的 rebrand.sh** 做差分。
//
// 这不是「与某个本地复刻比」——测试会建一个临时 git 仓库，把 xkitora
// 检出里的 scripts/rebrand.sh 与 scripts/identity-field.py **原样拷进去**，
// identity.yml 也取自该检出（只把 identity: 块换成派生身份），然后把语料
// 当文件写进去，跑 `rebrand.sh --replay`，逐字节比对输出。
//
// 规则表不做任何手工转写：identity.yml 的 upstream_tokens 在测试里被解析
// 并生成 xinit 的清单文本。转写会有漂移，漂移会让差分测试失去意义。
//
// **只跑自动生成、且排除了五类刻意偏离的语料**：
//   - 重叠命中：本设计用重叠枚举，旧版不重叠，会静默漏改
//   - CRLF + 谓词：本设计在归一化后的文本上判谓词，旧版窗口不同
//   - 超长行：本设计有 4 KiB 谓词窗口上限，超限判不命中
//   - 非 ASCII 替换值：Go 的 strings.ToUpper 与 Python 的 .upper() 结果
//     不同（如 "straße"）
//   - **紧贴的相邻边界**：旧版把已替换的文本换成哨兵再判下一条规则的
//     边界，本实现判原文。实测 `" xX-a\n"` 配 A `xX→Zzz`、B `-a→zzz`
//     时旧版得 `" Zzzzzz\n"`、本实现得 `" Zzz-a\n"`。真实 xkitora 表碰
//     不到（字面量都以字母起止），但表一旦变化就会碰到，所以语料里不放
//     这类形态；行为由 TestApplyAdjacentBoundaryJudgedOnOriginalText 钉住。
//
// 要求全语料逐字节相同在原理上做不到；强行求同会诱导把旧 bug 搬回来。
//
// 需要 `REBRAND_SH` 指向某个 xkitora 检出的 scripts/rebrand.sh。
// 未设置、或找不到 python3/git 时跳过 —— 它属于另一个仓库。
func TestDifferentialAgainstRebrand(t *testing.T) {
	rebrand := os.Getenv("REBRAND_SH")
	if rebrand == "" {
		t.Skip("未设置 REBRAND_SH，跳过差分测试")
	}
	rebrand, err := filepath.Abs(rebrand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rebrand); err != nil {
		t.Skipf("rebrand.sh 不存在: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("无 python3，跳过差分测试")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("无 git，跳过差分测试")
	}

	// scripts/rebrand.sh -> 仓库根
	root := filepath.Dir(filepath.Dir(rebrand))
	identitySrc := filepath.Join(root, "template", "identity.yml")
	readerSrc := filepath.Join(root, "scripts", "identity-field.py")
	for _, p := range []string{identitySrc, readerSrc} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("缺 %s: %v", p, err)
		}
	}

	raw, err := os.ReadFile(identitySrc)
	if err != nil {
		t.Fatal(err)
	}
	var doc identityDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 identity.yml: %v", err)
	}
	if len(doc.Template.UpstreamTokens) == 0 {
		t.Fatal("identity.yml 的 upstream_tokens 为空，差分无从谈起")
	}

	// xinit 侧的规则表由真实表生成，不做手工转写。
	manifestText := buildDiffManifest(t, &doc)
	m, err := manifest.Parse([]byte(manifestText))
	if err != nil {
		t.Fatalf("生成的清单解析失败: %v\n---\n%s", err, manifestText)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("生成的清单编译失败: %v\n---\n%s", err, manifestText)
	}

	corpus := generateCorpus(t)
	want := runRebrandHarness(t, rebrand, readerSrc, raw, corpus)

	names := make([]string, 0, len(corpus))
	for name := range corpus {
		names = append(names, name)
	}
	// map 遍历顺序随机，报错顺序要稳定
	sort.Strings(names)

	// 规则只构建一次。注意 buildRules 返回的是带清单序号的 []scopedRule，
	// 而 Apply 吃 []Rule —— 必须显式降级（与 Content 内部同一转换，
	// 见 content.go 的 plain := make([]Rule, len(scoped))）。
	scoped, err := buildRules(c, "x.go", Vars{
		"project_name": "Xdesk",
		"app_slug":     "xdesk",
	})
	if err != nil {
		t.Fatal(err)
	}
	rules := make([]Rule, len(scoped))
	for i, s := range scoped {
		rules[i] = s.Rule
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			got, _, _ := Apply([]byte(corpus[name]), rules, c.Raw.ProtectedLiterals)

			// harness 对没改过的文件不写回；读回来的一定是文件内容，
			// 这里统一按「改后应有内容」比。
			if string(got) != want[name] {
				t.Errorf("与 rebrand.sh 不一致\n输入: %q\n本工具: %q\nrebrand: %q",
					corpus[name], got, want[name])
			}
		})
	}
}

// buildDiffManifest 把 identity.yml 的规则表翻译成 xinit 清单文本。
//
// target_case 在这里就地解析掉（对值取 upper），所以不需要清单的 case 字段；
// rebrand.sh 的 target_case 就是「对 identity 值取大写」，语义一致。
func buildDiffManifest(t *testing.T, doc *identityDoc) string {
	t.Helper()

	for _, f := range []string{"slug", "product_name", "module_path", "bundle_id",
		"org", "scheme", "file_extension", "data_dir"} {
		if derivationIdentity[f] == "" {
			t.Fatalf("derivationIdentity 缺字段 %q", f)
		}
	}

	var b strings.Builder
	b.WriteString("manifest_version: 1\n")
	// 让路径变量渲染保持空转：from/to 都是字面量，没有 {{ }}
	b.WriteString("protected_literals:\n")
	for _, p := range doc.Template.ProtectedLiterals {
		fmt.Fprintf(&b, "  - %q\n", p)
	}
	b.WriteString("substitute:\n")
	for i, e := range doc.Template.UpstreamTokens {
		val, ok := derivationIdentity[e.Target]
		if !ok {
			t.Fatalf("upstream_tokens[%d] 的 target %q 不在 derivationIdentity 里", i, e.Target)
		}
		if e.TargetCase == "upper" {
			val = strings.ToUpper(val)
		} else if e.TargetCase != "" && e.TargetCase != "lower" {
			t.Fatalf("upstream_tokens[%d] 的 target_case %q 不认识", i, e.TargetCase)
		}

		fmt.Fprintf(&b, "  - from: %q\n", e.Literal)
		fmt.Fprintf(&b, "    to: %q\n", val)
		// rebrand.sh 的规则作用于全部文件，没有 per-rule 路径过滤
		b.WriteString("    paths: [\"**\"]\n")
		if e.WhenPrecededBy != "" {
			fmt.Fprintf(&b, "    when_preceded_by: %q\n", e.WhenPrecededBy)
		}
		if e.WhenFollowedBy != "" {
			fmt.Fprintf(&b, "    when_followed_by: %q\n", e.WhenFollowedBy)
		}
	}
	return b.String()
}

// runRebrandHarness 建临时 git 仓库、落语料、跑真实的 rebrand.sh --replay，
// 返回每个语料文件被改写后的内容。
func runRebrandHarness(
	t *testing.T, rebrandPath, readerPath string, identityRaw []byte,
	corpus map[string]string,
) map[string]string {
	t.Helper()

	tmp := t.TempDir()
	for _, d := range []string{"template", "scripts", "corpus"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	mustCopy := func(src, dst string) {
		t.Helper()
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustCopy(readerPath, filepath.Join(tmp, "scripts", "identity-field.py"))
	mustCopy(rebrandPath, filepath.Join(tmp, "scripts", "rebrand.sh"))

	if err := os.WriteFile(
		filepath.Join(tmp, "template", "identity.yml"),
		spliceIdentity(t, identityRaw),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	for name, body := range corpus {
		p := filepath.Join(tmp, "corpus", name+".txt")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(name string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = tmp
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("git", "init", "-q"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// rebrand.sh 的文件清单来自 git ls-files，必须先把语料放进索引
	if out, err := run("git", "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	out, err := run("bash", "scripts/rebrand.sh", "--replay")
	if err != nil {
		// rebrand.sh 在「无改动」时 exit 3；语料全是 xkitora/Xkitora 的字面量，
		// 正常情况下应当有改动，exit 3 说明语料或表出了问题，不算通过。
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			t.Fatalf("rebrand.sh 报告 0 改动 —— 语料没命中任何规则，差分是空转的\n%s", out)
		}
		t.Fatalf("rebrand.sh 失败: %v\n%s", err, out)
	}

	got := make(map[string]string, len(corpus))
	for name := range corpus {
		data, err := os.ReadFile(filepath.Join(tmp, "corpus", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		got[name] = string(data)
	}
	return got
}

// spliceIdentity 把 identity.yml 的 top-level `identity:` 块换成派生身份。
//
// 只做这一处文本替换，其余（upstream_tokens 表、protected_literals、注释）
// 全部保持自源检出的原样 —— 表一旦被重新序列化，差分就不再是"同一个表"。
//
// 两个锚点都是列 0 的 top-level key，位置唯一。
func spliceIdentity(t *testing.T, raw []byte) []byte {
	t.Helper()

	lines := strings.Split(string(raw), "\n")
	start, end := -1, -1
	for i, ln := range lines {
		if ln == "identity:" {
			start = i
			continue
		}
		if start >= 0 && len(ln) > 0 && ln[0] != ' ' && ln[0] != '#' {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("identity.yml 里找不到 identity: 块（start=%d end=%d）", start, end)
	}

	order := []string{"slug", "product_name", "module_path", "bundle_id",
		"org", "scheme", "file_extension", "data_dir"}
	var b strings.Builder
	b.WriteString("identity:\n")
	for _, k := range order {
		fmt.Fprintf(&b, "  %s: %s\n", k, derivationIdentity[k])
	}

	out := append([]string{}, lines[:start]...)
	out = append(out, b.String()[:len(b.String())-1])
	out = append(out, lines[end:]...)
	return []byte(strings.Join(out, "\n"))
}

// generateCorpus 生成**不含**五类刻意偏离的语料。
//
// 每条语料都是单行或纯 LF 多行、行长远小于 4 KiB、替换结果为纯 ASCII。
//
// 括号里是已实测的 rebrand.sh 输出（用 derivationIdentity 那组值跑的），
// 用来说明每条语料到底在验什么；Go 侧必须逐字节复现这些输出。
func generateCorpus(t *testing.T) map[string]string {
	t.Helper()
	gen := map[string]string{}

	// 基础替换
	gen["纯字面量"] = "Xkitora 和 xkitora 混排\n"       // → Xdesk 和 xdesk
	gen["行首"] = "xkitora is here\n"                // → xdesk is here
	gen["行尾"] = "here is xkitora\n"                // → here is xdesk
	gen["字母边界_前"] = "libxkitora\n"                // → 不变
	gen["字母边界_后"] = "xkitorax\n"                  // → 不变
	gen["下划线邻接"] = "_xkitora_\n"                  // → _xdesk_（下划线不是字母）
	gen["多行"] = "Xkitora\nxkitora\nXkitora xkitora\n" // → Xdesk/xdesk/Xdesk xdesk
	gen["路径样"] = "github.com/xbcio/xkitora\n"     // → github.com/xbcorp/xdesk
	gen["多字节邻接"] = "中文xkitora中文\n"                 // → 中文xdesk中文（非 ASCII 字母不算边界）
	gen["空行"] = "\n\n\n"                          // → 不变
	gen["tab 分隔"] = "xkitora\tXkitora\n"          // → xdesk\tXdesk

	// 全大写：走 target_case: upper 那条规则
	gen["全大写"] = "XKITORA_URL 与 XKITORA_URL\n" // → XDESK_URL 与 XDESK_URL

	// bundle_id 是复合字面量，必须比裸 xkitora 规则先命中
	gen["bundle_id"] = "productIdentifier: com.github.xkitora\n" // → com.github.xdesk
	gen["bundle_id 裸串"] = "com.github.xkitora 与 xkitora 同行\n"    // → com.github.xdesk 与 xdesk

	// 谓词分派：同一个裸 token 在不同槽位落到**不同**目标。
	// 目标值刻意互不相同（见 derivationIdentity），否则谓词判对判错
	// 输出一样，这几条就是空转。
	gen["scheme 槽"] = `Scheme = "xkitora"` + "\n"             // → xscheme
	gen["scheme 槽 冒号"] = `scheme: "xkitora"` + "\n"          // → xscheme
	gen["file_ext 槽"] = `FileExtension = "xkitora"` + "\n"    // → xext
	gen["file_ext 槽 冒号"] = `ext: "xkitora"` + "\n"           // → xext（谓词要求引号）
	gen["data_dir 槽"] = `AppDirName = "xkitora"` + "\n"       // → xdata
	gen["data_dir 槽 冒号"] = `dataDir: "xkitora"` + "\n"       // → xdata
	gen["module 槽"] = "module xkitora\n"                     // → xmodule
	gen["bindings 槽"] = "frontend/bindings/xkitora/types.ts\n" // → xmodule
	gen["internal 槽"] = "xkitora/internal/app\n"             // → xmodule

	// 谓词**确实不命中**的槽位形态，必须落到最后那条裸规则（→ xdesk）。
	// 这两条的谓词形态和上面只差一点，是真正的判别用例：
	//   - module 谓词是 search('module[ \t]+\Z', 窗口)，窗口里再出现别的词就落空
	//   - ext 谓词要求冒号后跟引号，无引号时落空
	// 注意 "not module xkitora" **不是**反例：窗口 "not module " 以
	// "module " 结尾，\Z 成立，谓词照样命中（实测 → xmodule）。
	gen["谓词落空_module"] = "module foo xkitora\n" // → xdesk
	gen["谓词落空_ext"] = "ext: xkitora\n"         // → xdesk（无引号）
	gen["谓词落空_scheme"] = `schemeURL = "xkitora"` + "\n" // → xdesk

	// 边界拒绝后的推进：rebrand.sh 在拒绝处 index = end（不是 end+1），
	// 两种实现必须一致地"跳过整个字面量"，不回头找重叠起点。
	gen["边界拒绝后推进"] = "xkitorax xkitora\n" // → xkitorax xdesk

	// 刻意生成**不重叠**的多次命中：每个命中之间至少隔一个非字母字符
	gen["非重叠多命中"] = "xkitora xkitora xkitora\n" // → xdesk xdesk xdesk

	// 保护区：@xbcio/ui 必须原样留下，同文件里裸露的 xbcio 必须改掉
	gen["保护区"] = "@xbcio/ui 与 xbcio 混排\n"   // → @xbcio/ui 与 xbcorp 混排
	gen["保护区与 org 同排"] = "@xbcio/ui\nxbcio\n" // → @xbcio/ui\nxbcorp

	return gen
}
