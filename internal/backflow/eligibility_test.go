package backflow

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 守卫 1：非 git 目录 → 报错 + 文案点名 README 的 opt-in 说明。
func TestQualifyNonGitDir(t *testing.T) {
	dir := t.TempDir()
	q, err := Qualify(dir, "HEAD")
	if err == nil {
		t.Fatalf("非 git 目录应报错；got %+v", q)
	}
	if !strings.Contains(err.Error(), "opt-in") {
		t.Fatalf("文案应点名 opt-in；got %q", err)
	}
	if !strings.Contains(err.Error(), "README") {
		t.Fatalf("文案应点名 README 的 opt-in 说明；got %q", err)
	}
	if !strings.Contains(err.Error(), "git init") {
		t.Fatalf("文案应给出 `git init` 出口；got %q", err)
	}
}

// 守卫 1–5 全过：干净 + 单父提交 + 末段 trailer → 通过。
func TestQualifyOK(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	sha := commitAllT(t, dir, okMsg)

	q, err := Qualify(dir, "HEAD")
	if err != nil {
		t.Fatalf("合格提交应通过：%v", err)
	}
	if q.Commit != sha {
		t.Errorf("Commit 应为解析后的完整 sha；got %q, want %q", q.Commit, sha)
	}
	if len(q.Commit) != 40 {
		t.Errorf("Commit 应为 40 位；got %d 位（%q）", len(q.Commit), q.Commit)
	}
	if q.RepoDir != dir {
		t.Errorf("RepoDir 应为项目根；got %q, want %q", q.RepoDir, dir)
	}
	if !strings.Contains(q.Message, "Template-Upstream: yes") {
		t.Errorf("Message 应为提交消息全文（%%B）；got %q", q.Message)
	}
	if len(q.Untracked) != 0 {
		t.Errorf("干净工作树不该有未跟踪警告；got %v", q.Untracked)
	}
}

// 守卫 2：MERGE_HEAD 存在 → 拒绝，文案点名探测到的状态。
func TestQualifyRejectsMergeInProgress(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	gitDir := runGitT(t, dir, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(shaZero+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	q, err := Qualify(dir, "HEAD")
	if err == nil {
		t.Fatalf("merge 进行中应拒绝；got %+v", q)
	}
	for _, want := range []string{"MERGE_HEAD", "merge", "--abort"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("文案应含 %q；got %q", want, err)
		}
	}
}

// 守卫 2：rebase-merge 目录存在 → 拒绝，文案点名 rebase。
func TestQualifyRejectsRebaseInProgress(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	gitDir := runGitT(t, dir, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Qualify(dir, "HEAD")
	if err == nil {
		t.Fatal("rebase 进行中应拒绝")
	}
	if !strings.Contains(err.Error(), "rebase-merge") || !strings.Contains(err.Error(), "rebase") {
		t.Fatalf("文案应点名 rebase 状态；got %q", err)
	}
}

// 守卫 3：已跟踪文件脏 → 拒绝 + 列出脏路径（干净路径不得出现）。
func TestQualifyRejectsDirtyTracked(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n", "b.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "committed\n")
	sha := commitAllT(t, dir, okMsg)
	writeFileT(t, dir, "a.txt", "dirty\n")

	q, err := Qualify(dir, sha)
	if err == nil {
		t.Fatalf("已跟踪文件脏应拒绝；got %+v", q)
	}
	if !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("文案应列出脏路径 a.txt；got %q", err)
	}
	if strings.Contains(err.Error(), "b.txt") {
		t.Fatalf("干净路径 b.txt 不该出现在错误里；got %q", err)
	}
	if !strings.Contains(err.Error(), "stash") {
		t.Fatalf("文案应给出 commit / stash 出口；got %q", err)
	}
}

// 守卫 3：porcelain 各类非 `??` 条目都算脏；R/C 条目在 -z 里多出一段原路径，
// 必须被正确消费（否则解析直接报错，而不是给出脏路径）。
func TestQualifyDirtyEntryShapes(t *testing.T) {
	cases := []struct {
		name  string
		dirty func(t *testing.T, dir string)
		want  string // 错误文案里必须出现的脏路径
	}{
		{"工作树修改", func(t *testing.T, dir string) { writeFileT(t, dir, "a.txt", "dirty\n") }, "a.txt"},
		{"已暂存新增", func(t *testing.T, dir string) {
			writeFileT(t, dir, "s.txt", "s\n")
			runGitT(t, dir, "add", "s.txt")
		}, "s.txt"},
		{"已暂存改名（R 条目带原路径段）", func(t *testing.T, dir string) {
			runGitT(t, dir, "mv", "a.txt", "renamed.txt")
		}, "renamed.txt"},
		{"已删除", func(t *testing.T, dir string) { runGitT(t, dir, "rm", "-q", "a.txt") }, "a.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
			tc.dirty(t, dir)

			q, err := Qualify(dir, "HEAD")
			if err == nil {
				t.Fatalf("脏工作树应拒绝；got %+v", q)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("文案应列出脏路径 %q；got %q", tc.want, err)
			}
		})
	}
}

// 守卫 3：未跟踪文件只警告（通过 + Untracked 收集），不列入错误。
func TestQualifyUntrackedWarnsOnly(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "committed\n")
	sha := commitAllT(t, dir, okMsg)
	writeFileT(t, dir, "u1.txt", "x\n")
	writeFileT(t, dir, "u2.txt", "y\n")

	q, err := Qualify(dir, sha)
	if err != nil {
		t.Fatalf("未跟踪文件不该挡在门外：%v", err)
	}
	if len(q.Untracked) != 2 {
		t.Fatalf("应收集 2 条未跟踪警告；got %v", q.Untracked)
	}
	for i, want := range []string{"u1.txt", "u2.txt"} {
		if q.Untracked[i] != want {
			t.Errorf("Untracked[%d] 应为 %q；got %q", i, want, q.Untracked[i])
		}
	}
}

// 守卫 4：root 提交 → 拒绝 + 打印判据原文。
func TestQualifyRejectsRootCommit(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	root := runGitT(t, dir, "rev-list", "--max-parents=0", "HEAD")

	q, err := Qualify(dir, root)
	if err == nil {
		t.Fatalf("root 提交应拒绝；got %+v", q)
	}
	assertCriterionText(t, err)
}

// 守卫 4：merge 提交（两 parent）→ 拒绝 + 打印判据原文。
func TestQualifyRejectsMergeCommit(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	runGitT(t, dir, "checkout", "-q", "-b", "side")
	writeFileT(t, dir, "side.txt", "s\n")
	commitAllT(t, dir, "side\n\nTemplate-Upstream: yes\n")
	runGitT(t, dir, "checkout", "-q", "main")
	writeFileT(t, dir, "main.txt", "m\n")
	commitAllT(t, dir, "main\n\nTemplate-Upstream: yes\n")
	runGitT(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")

	q, err := Qualify(dir, "HEAD")
	if err == nil {
		t.Fatalf("merge 提交应拒绝；got %+v", q)
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("文案应点名 merge 成因；got %q", err)
	}
	assertCriterionText(t, err)
}

// 守卫 4：无效引用（命令失败）→ 拒绝 + 打印判据原文。
func TestQualifyRejectsUnknownCommit(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	commitAllT(t, dir, okMsg)

	q, err := Qualify(dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatalf("无效引用应拒绝；got %+v", q)
	}
	assertCriterionText(t, err)
}

// 守卫 4 加固：选项形状的位置参数 fail-closed（M8 观察-2）——`--all` 不得被
// rev-list 当选项；修复前它等价选最新提交并通过资格预检。
func TestQualifyRejectsOptionShapedCommit(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	commitAllT(t, dir, "feat: base\n\nTemplate-Upstream: yes\n") // 消息形态照抄 TestQualifyOK

	q, err := Qualify(dir, "--all")
	if err == nil {
		t.Fatalf("选项形状的 commit 应拒绝；got %+v", q)
	}
	assertContainsAll(t, err, `无法解析 "--all"`)
}

// 守卫 5：trailer 变体（§20.10 用例 8 实测定形的四种形状 + 逐字比较负例）。
func TestQualifyTrailerVariants(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		wantErr bool
	}{
		{"末段块命中", "feat: x\n\nTemplate-Upstream: yes\n", false},
		{"与其他 trailer 同块", "feat: x\n\nSigned-off-by: A <a@e>\nTemplate-Upstream: yes\n", false},
		{"正文引用该行、末段块也命中", "feat: x\n\n正文里引用 Template-Upstream: yes 这一行。\n\nTemplate-Upstream: yes\n", false},
		{"正文中间（非末段块）", "feat: x\n\nTemplate-Upstream: yes\n\nmore body\n", true},
		{"值非逐字 no", "feat: x\n\nTemplate-Upstream: no\n", true},
		{"值非逐字 Yes", "feat: x\n\nTemplate-Upstream: Yes\n", true},
		{"键非逐字（小写）", "feat: x\n\ntemplate-upstream: yes\n", true},
		{"整条消息仅一行", "Template-Upstream: yes\n", true},
		{"缺 trailer", "feat: x\n", true},
		{"末段块内出现两次（多义）", "feat: x\n\nTemplate-Upstream: yes\nTemplate-Upstream: yes\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
			writeFileT(t, dir, "a.txt", "2\n")
			sha := commitMsgT(t, dir, tc.msg)

			q, err := Qualify(dir, sha)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应拒绝；got %+v", q)
				}
				assertTrailerCriterionText(t, err)
				return
			}
			if err != nil {
				t.Fatalf("应通过：%v", err)
			}
		})
	}
}

// 守卫 5：CRLF 行尾由 parse 层归一 → 接受（§20.10 用例 8；夹具走 --cleanup=verbatim
// 才能把 CRLF 原样留在提交消息里）。
func TestQualifyTrailerCRLFAccepted(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	sha := commitMsgT(t, dir, "feat: x\r\n\r\nTemplate-Upstream: yes\r\n")

	if q, err := Qualify(dir, sha); err != nil {
		t.Fatalf("CRLF 行尾应被接受：%v（got %+v）", err, q)
	}
}

// 守卫 5：**空白整族 → 接受**（spec §20.10 用例 8 修订后的既定 parse 口径）。
// `git interpret-trailers --parse` 会把 `Key:value`、冒号后多空格、键尾空格、值尾空格
// 四种写法一并归一出逐字行——与 CRLF 归一同类。判定口径单一、全落 parse 输出上；
// 本包不自行重实现块判定与逐字差分。本用例把这条边界钉住，防后人「顺手加个
// TrimSpace 敏感化」把口径拆成两套（那必然连带重实现末段块判定）。
// 夹具须 --cleanup=verbatim：git 默认 strip 会先吃掉行尾空白，变体就到不了 git。
func TestQualifyTrailerWhitespaceNormalizedAccepted(t *testing.T) {
	cases := []struct{ name, line string }{
		{"冒号后无空格", "Template-Upstream:yes"},
		{"冒号后两空格", "Template-Upstream:  yes"},
		{"键尾空格", "Template-Upstream : yes"},
		{"值尾空格", "Template-Upstream: yes "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
			writeFileT(t, dir, "a.txt", "2\n")
			sha := commitMsgT(t, dir, "feat: x\n\n"+tc.line+"\n")

			if q, err := Qualify(dir, sha); err != nil {
				t.Fatalf("空白经 parse 归一后应通过：%v（got %+v）", err, q)
			}
		})
	}
}

// F-1：`log.showSignature=true` 时 git 会把 gpg 程序的输出（stderr）转发到自己的
// stdout 且**前置**于消息正文。`Qualified.Message` 是 T2 生成 mailbox Subject/正文的
// 唯一来源，必须逐字等于提交消息——污染是静默进交付物的，不是 fail-closed。
func TestQualifyMessageFreeOfSignatureNoise(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	commitAllT(t, dir, "base\n\nTemplate-Upstream: yes\n")
	msg := "feat: 改共享面\n\nTemplate-Upstream: yes\n"
	sha := craftSignedCommitT(t, dir, msg)
	runGitT(t, dir, "config", "log.showSignature", "true")
	runGitT(t, dir, "config", "gpg.program", fakeGpgT(t))

	q, err := Qualify(dir, sha)
	if err != nil {
		t.Fatalf("应通过：%v", err)
	}
	want := msg + "\n" // `%B` 在消息末尾再补一个 LF
	if q.Message != want {
		t.Fatalf("Message 应为逐字提交消息（不含 gpg 前言行）；got %q, want %q", q.Message, want)
	}
	if strings.Contains(q.Message, "XFAKEGPG-NOISE") {
		t.Fatalf("Message 被 gpg 输出污染：%q", q.Message)
	}
}

// F-3（M-1）：`status.showUntrackedFiles=no` 不得让未跟踪警告静默消失（spec 明确
// 「未跟踪文件只警告」的语义面）；命令显式带 --untracked-files=normal 钉住。
func TestQualifyUntrackedSurvivesShowUntrackedFilesConfig(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	sha := commitAllT(t, dir, okMsg)
	runGitT(t, dir, "config", "status.showUntrackedFiles", "no")
	writeFileT(t, dir, "u.txt", "x\n")

	q, err := Qualify(dir, sha)
	if err != nil {
		t.Fatalf("未跟踪文件不该挡在门外：%v", err)
	}
	if len(q.Untracked) != 1 || q.Untracked[0] != "u.txt" {
		t.Fatalf("Untracked 应仍收集 u.txt；got %v", q.Untracked)
	}
}

// F-4（M-3）：Stat 的非 NotExist 错误不得被当作「标记不存在」（那种读法会漏检进行中
// 操作）。存在性无法判定时 fail-closed 拒绝，文案点名该标记。
// 用自指符号链接稳定构造：`.git/MERGE_HEAD` → 自身，os.Stat 报 ELOOP，且 git 自身
// 的 rev-parse / status / rev-list 不受影响（探针实测）。
func TestQualifyFailsClosedOnStatError(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"})
	writeFileT(t, dir, "a.txt", "2\n")
	sha := commitAllT(t, dir, okMsg)
	gitDir := runGitT(t, dir, "rev-parse", "--absolute-git-dir")
	if err := os.Symlink("MERGE_HEAD", filepath.Join(gitDir, "MERGE_HEAD")); err != nil {
		t.Fatal(err)
	}

	_, err := Qualify(dir, sha)
	if err == nil {
		t.Fatal("存在性无法判定时应 fail-closed 拒绝")
	}
	if !strings.Contains(err.Error(), "MERGE_HEAD") {
		t.Fatalf("文案应点名探测失败的标记；got %q", err)
	}
}

// F-5（M-4）：守卫 1 的错误必须保留底层 git 错误（%w）——否则 git 缺失/损坏时文案
// 误报「不在 git 仓库内」，把人引向同样失败的 `git init`。
func TestQualifyNonGitDirWrapsGitError(t *testing.T) {
	dir := mkRepoT(t, map[string]string{"a.txt": "1\n"}) // 建仓必须在置空 PATH 之前
	t.Setenv("PATH", "")

	_, err := Qualify(dir, "HEAD")
	if err == nil {
		t.Fatal("git 不可用时应报错")
	}
	if !strings.Contains(err.Error(), "回流要求派生项目是 git 仓库（opt-in）") {
		t.Fatalf("要点句应保留；got %q", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("应保留底层 exec 错误链（errors.Is(err, exec.ErrNotFound)）；got %v", err)
	}
	// G-1：git 进程起不来时 stderr 为空，`runGit` 不该拼出一对空括号（用户可见文案）。
	if strings.Contains(err.Error(), "（）") {
		t.Fatalf("stderr 为空时不该出现空括号；got %q", err)
	}
}

// assertCriterionText 断言错误里打印了守卫 4 的判据原文（§20.10 文案基准表第 5 行）。
func assertCriterionText(t *testing.T, err error) {
	t.Helper()
	assertContainsAll(t, err, "判据", "`git rev-list --parents -n 1 <commit>` 词数 == 2")
}

// assertTrailerCriterionText 断言错误里打印了守卫 5 的判据原文（§20.4 行 5 修订版：
// 判定全落 parse 输出上，不再声明「逐字匹配」——空白整族属 parse 归一）。
// 首段与 spec 核句是**散文句层面的连续子串**：spec 原文该处夹着 markdown 强调符
// （`提交消息**末段 trailer 块**含 \`Template-Upstream: yes\`（判定全落 …输出上）`），
// 去掉 `**` 强调符后逐字一致，对 raw markdown 字符串则**不**连续。T6 若做端到端断言，
// 请以去掉强调符的散文句（或本断言的两段）为准，勿直接对 raw markdown 取子串。
// 末段（「——键大小写须逐字；空白与 CRLF 属 parse 归一」）是「含」字的操作口径澄清。
func assertTrailerCriterionText(t *testing.T, err error) {
	t.Helper()
	assertContainsAll(t, err, "判据",
		"提交消息末段 trailer 块含 `Template-Upstream: yes`（判定全落 `git interpret-trailers --parse` 输出上）",
		"键大小写须逐字；空白与 CRLF 属 parse 归一")
}

func assertContainsAll(t *testing.T, err error, wants ...string) {
	t.Helper()
	msg := err.Error()
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误应打印判据原文（缺 %q）；got %q", want, msg)
		}
	}
}
