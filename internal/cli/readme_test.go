package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestReadmeDeclarations 是 README 的**少量语义锚**（M6 F4）：不做全文比对
// （逐字锁会随文风改动假失败），只钉住三类曾被静默变异的面——优先级写反 /
// 漏「判据不变」/ diff 面误加 `--interactive`——外加退出码注记与**动态**
// 脚本计数。锚串一律从现行 README 逐字复制；断言总数 6 条（≤ 8）。
func TestReadmeDeclarations(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("读 README.md: %v", err)
	}
	readme := string(b)

	// ① 变量优先级（§7.1）：写反（交互回答 > --config）即 FAIL。
	if !strings.Contains(readme, "`--config` 给的值 > 交互回答 > `default`") {
		t.Error("README 缺变量优先级句：`--config` 给的值 > 交互回答 > `default`")
	}

	// ② 交互提问[不改判据]的两处声明（§19.2.4）：漏掉任一句即 FAIL——
	// 「只打开读入通道」是安全论证的支点，不能只留在代码注释里。
	if !strings.Contains(readme, "不改任何判据") {
		t.Error("README 缺「不改任何判据」声明（交互提问只打开读入通道）")
	}
	if !strings.Contains(readme, "信任确认仍须显式回答") {
		t.Error("README 缺「信任确认仍须显式回答」声明（--interactive 不代答信任门）")
	}

	// ③ diff 面不注册 --interactive（§19.2.2 行为矩阵）。
	if !strings.Contains(readme, "`diff` 不注册 `--interactive`") {
		t.Error("README 缺「diff 不注册 --interactive」声明")
	}

	// ④ 退出码注记（§19.5）：交互回答不扩张退出码 0–6。
	if !strings.Contains(readme, "交互回答不引入新退出码") {
		t.Error("README 缺「交互回答不引入新退出码」注记")
	}

	// ⑤ 动态计数锁：README 写的脚本数必须等于 testdata/script/*.txtar 实数
	// （同一目录口径，与 README 的「internal/cli/testdata/script/*.txtar」一致）。
	scripts, err := filepath.Glob(filepath.Join("testdata", "script", "*.txtar"))
	if err != nil {
		t.Fatalf("列举脚本: %v", err)
	}
	m := regexp.MustCompile(`（(\d+) 个脚本`).FindStringSubmatch(readme)
	if m == nil {
		t.Fatal("README 缺「（N 个脚本」计数句")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("解析 README 脚本计数 %q: %v", m[1], err)
	}
	if n != len(scripts) {
		t.Errorf("README 的脚本计数 %d 与实际 %d 个 testdata/script/*.txtar 不符", n, len(scripts))
	}
}
