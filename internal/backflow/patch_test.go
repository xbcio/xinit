package backflow

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 夹具口径（本文件所有用例共用）：
//
//   - 接收仓 = 模板仓替身，只要求「含 T 提交」——§20.6 可接收性① 的 pre-image blob
//     可解析性由此成立；用例直接断言补丁的 index 左值 == 接收仓 T 提交里的 blob。
//   - 所有 git 调用钉住会翻转夹具的全局配置面：`core.autocrlf`（CRLF 夹具入对象库
//     时会被归一到 LF）、`commit.gpgsign`（用户全局签名会让 am 落不了提交）、
//     `am.keepcr`（用户全局 true 会翻转「默认 am 必失败」这条 CRLF 反例）。
const (
	ds40T = "1111111111111111111111111111111111111111"
	ts40T = "2222222222222222222222222222222222222222"
)

// metaT 是「取派生提交」的确定性元数据夹具（非 ASCII 面另有用例）。
func metaT() CommitMeta {
	return CommitMeta{
		Author:  "dev",
		Email:   "dev@example.com",
		Date:    "Sun, 11 Oct 2026 10:00:00 +0800",
		Subject: "feat: share fix",
		Body:    "why\n\nTemplate-Upstream: yes",
	}
}

// gitPinT 在 dir 跑一条 git（前置钉住 autocrlf/签名两处全局配置），返回 rc 与
// stdout+stderr 合并输出。不 Fatal —— 反例断言（默认 am 必须失败）要看 rc。
func gitPinT(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	base := []string{"-c", "core.autocrlf=false", "-c", "commit.gpgsign=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if err == nil {
		return 0, buf.String()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), buf.String()
	}
	t.Fatalf("git %v: %v\n%s", args, err, buf.String())
	return -1, ""
}

// mustGitT 跑一条必须成功的 git，返回 trim 后的 stdout。
func mustGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	rc, out := gitPinT(t, dir, args...)
	if rc != 0 {
		t.Fatalf("git %v: rc=%d\n%s", args, rc, out)
	}
	return strings.TrimSpace(out)
}

// recvRepoT 建接收仓：一个含全部夹具文件的提交（= 模板仓的 T 提交）。
// execs 里的路径以 0755 提交（工作树与对象库的 mode 面）。
func recvRepoT(t *testing.T, files map[string]string, execs ...string) string {
	t.Helper()
	dir := t.TempDir()
	mustGitT(t, dir, "init", "-q", "-b", "main")
	for rel, content := range files {
		writeFileT(t, dir, rel, content)
	}
	for _, rel := range execs {
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustGitT(t, dir, "add", "-A")
	mustGitT(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// amT 在仓里跑 `git am`，返回 rc 与输出。默认口径钉住 am.keepcr=false（反例断言
// 依赖「默认 am 剥 CR」这一行为，不能让用户全局配置翻转它）。
func amT(t *testing.T, repo, patchFile string, extra ...string) (int, string) {
	t.Helper()
	args := append([]string{"am"}, extra...)
	args = append(args, patchFile)
	rc, out := gitPinT(t, repo, append([]string{"-c", "am.keepcr=false"}, args...)...)
	return rc, out
}

// patchFileT 把 mailbox 落盘，返回文件路径（am 吃文件路径）。
func patchFileT(t *testing.T, patch []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "p.patch")
	if err := os.WriteFile(p, patch, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// commitBodyT 取 HEAD 的提交消息裸字节（`cat-file commit`；`log --format=%B` 会混入
// 格式化尾巴，做不了逐字节断言）。命名避开 gittest_test.go 的 commitMsgT（造消息提交）。
func commitBodyT(t *testing.T, repo string) string {
	t.Helper()
	rc, raw := gitPinT(t, repo, "cat-file", "commit", "HEAD")
	if rc != 0 {
		t.Fatalf("cat-file commit: rc=%d\n%s", rc, raw)
	}
	i := strings.Index(raw, "\n\n")
	if i < 0 {
		t.Fatalf("commit 对象没有头/消息分隔：%q", raw)
	}
	return raw[i+2:] // 不 TrimSpace：消息的尾换行是逐字断言的一部分
}

// fileBytesT 读文件原始字节。
func fileBytesT(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// permT 返回路径的权限位。
func permT(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// mustContainT 断言 s 含全部子串（附上 s 便于定位）。
func mustContainT(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Fatalf("缺少 %q\n实际补丁：\n%s", sub, s)
		}
	}
}

// indexLineRe 匹配 --full-index 的完整 index 行（40 位双侧；mode 相等时 git 会
// 追加一组 ` <mode>` 后缀）。
var indexLineRe = regexp.MustCompile(`(?m)^index ([0-9a-f]{40})\.\.([0-9a-f]{40})`)

// TestBuildPatchModifyAmRoundTrip 是 §20.6 配方主链：修改文件的补丁 → `am -3`
// rc=0 → 落盘字节 == F_new，且 pre-image 左值 == 接收仓 T 提交的 blob（规则①）。
func TestBuildPatchModifyAmRoundTrip(t *testing.T) {
	const (
		rel = "src/patch/f.txt"
		T   = "line1\nline2\nline3\n"
		F   = "line1\nLINE2\nline3\n"
	)
	recv := recvRepoT(t, map[string]string{rel: T})
	preT := mustGitT(t, recv, "rev-parse", "HEAD:"+rel)

	res, err := BuildPatch([]PatchFile{{Src: rel, T: []byte(T), FNew: []byte(F)}}, metaT(), ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	if len(res.Noops) != 0 {
		t.Fatalf("Noops = %v, want 空", res.Noops)
	}
	if len(res.Kept) != 1 || res.Kept[0] != rel {
		t.Fatalf("Kept = %v, want [%s]", res.Kept, rel)
	}
	p := string(res.Patch)

	// 头部确定性字段（规则③）：From 行 sha = 派生 commit、author/date 取派生提交、
	// ASCII 主题逐字不编码（encodeWord 只对非 ASCII 动手）。
	mustContainT(t, p,
		"From "+ds40T+" Mon Sep 17 00:00:00 2001\n",
		"From: dev <dev@example.com>\n",
		"Date: Sun, 11 Oct 2026 10:00:00 +0800\n",
		"Subject: feat: share fix\n",
		"\nwhy\n\nTemplate-Upstream: yes\n---\n",
		"diff --git a/"+rel+" b/"+rel+"\n",
		"--- a/"+rel+"\n",
		"+++ b/"+rel+"\n",
		"@@ -1,3 +1,3 @@\n",
		"\n-line2\n+LINE2\n",
		// 尾行：裸 `From <40hex>`（无日期后缀——带日期会被 mailsplit 当新消息，
		// 实测 rc=128 且留中断态）。
		"\nFrom "+ds40T+"\n",
	)
	// 配方不泄漏临时目录（--no-prefix 的路径就是 a/、b/ 那一层）。
	if strings.Contains(p, os.TempDir()) {
		t.Fatalf("补丁泄漏了临时目录路径：\n%s", p)
	}
	// 规则②：--full-index ⇒ 40 位双侧，且左值 == 接收仓 T 的 blob（规则①）。
	m := indexLineRe.FindStringSubmatch(p)
	if m == nil {
		t.Fatalf("没有 40 位 index 行：\n%s", p)
	}
	if m[1] != preT {
		t.Fatalf("index 左值 = %s, want 接收仓 T 的 blob %s", m[1], preT)
	}

	// 接收侧（规则④）。
	rc, out := amT(t, recv, patchFileT(t, res.Patch), "-3")
	if rc != 0 {
		t.Fatalf("git am -3 rc=%d\n%s\n补丁：\n%s", rc, out, p)
	}
	if got := fileBytesT(t, filepath.Join(recv, filepath.FromSlash(rel))); string(got) != F {
		t.Fatalf("落盘 = %q, want %q", got, F)
	}
	// 落盘 blob == index 右值（后像与补丁自述一致）。
	if got := mustGitT(t, recv, "rev-parse", "HEAD:"+rel); got != m[2] {
		t.Fatalf("落盘 blob = %s, want index 右值 %s", got, m[2])
	}
	// 消息逐字保真（§20.6 步骤 3：Subject/正文逐字取派生提交 message 段）。
	wantMsg := metaT().Subject + "\n\n" + metaT().Body + "\n"
	if got := commitBodyT(t, recv); got != wantMsg {
		t.Fatalf("接收侧消息 = %q, want %q", got, wantMsg)
	}
}

// TestBuildPatchNewFiles 覆盖规则④的新增文件形态：普通、空文件、100755。
func TestBuildPatchNewFiles(t *testing.T) {
	files := []PatchFile{
		{Src: "docs/new.md", FNew: []byte("hello\n"), IsNew: true, NewExec: false},
		{Src: "src/empty.txt", FNew: nil, IsNew: true, NewExec: false},
		{Src: "scripts/run.sh", FNew: []byte("#!/bin/sh\necho hi\n"), IsNew: true, NewExec: true},
	}
	recv := recvRepoT(t, map[string]string{"keep.txt": "x\n"})

	res, err := BuildPatch(files, metaT(), ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	if len(res.Noops) != 0 {
		t.Fatalf("新增文件永不判 noop，Noops = %v", res.Noops)
	}
	wantKept := []string{"docs/new.md", "scripts/run.sh", "src/empty.txt"}
	if !equalStringsT(res.Kept, wantKept) {
		t.Fatalf("Kept = %v, want %v（按 Src 排序）", res.Kept, wantKept)
	}
	p := string(res.Patch)
	mustContainT(t, p,
		"new file mode 100644\n",
		"new file mode 100755\n",
		"--- /dev/null\n",
		"+++ b/scripts/run.sh\n",
		"index 0000000000000000000000000000000000000000..",
	)
	// 空新增文件的段只有 new file mode + index（无 ---/+++/@@）：这是配方实测形态。
	emptySeg := p[strings.Index(p, "diff --git b/src/empty.txt"):]
	if i := strings.Index(emptySeg, "\nFrom "+ds40T); i >= 0 {
		emptySeg = emptySeg[:i]
	}
	mustContainT(t, emptySeg, "new file mode 100644\n")
	if strings.Contains(emptySeg, "+++") || strings.Contains(emptySeg, "@@") {
		t.Fatalf("空新增文件不该有 +++/@@：\n%s", emptySeg)
	}

	rc, out := amT(t, recv, patchFileT(t, res.Patch), "-3")
	if rc != 0 {
		t.Fatalf("git am -3 rc=%d\n%s\n补丁：\n%s", rc, out, p)
	}
	for rel, want := range map[string]string{
		"docs/new.md":    "hello\n",
		"src/empty.txt":  "",
		"scripts/run.sh": "#!/bin/sh\necho hi\n",
	} {
		if got := fileBytesT(t, filepath.Join(recv, filepath.FromSlash(rel))); string(got) != want {
			t.Fatalf("%s 落盘 = %q, want %q", rel, got, want)
		}
	}
	if got := permT(t, filepath.Join(recv, "scripts", "run.sh")); got != 0o755 {
		t.Fatalf("run.sh 权限 = %o, want 755（exec 位是 §8.3 一等公民）", got)
	}
	if got := permT(t, filepath.Join(recv, "docs", "new.md")); got != 0o644 {
		t.Fatalf("new.md 权限 = %o, want 644", got)
	}
}

// TestBuildPatchModeOnlyChange 覆盖规则：纯 mode 变更 ⇒ 两行无 hunk、am 落盘 0755，
// 且**不得**判 noop（exec 位是 §8.3 一等公民）。
func TestBuildPatchModeOnlyChange(t *testing.T) {
	const (
		rel = "src/tool.sh"
		T   = "same\n"
	)
	recv := recvRepoT(t, map[string]string{rel: T})

	res, err := BuildPatch([]PatchFile{{Src: rel, T: []byte(T), FNew: []byte(T), OldExec: false, NewExec: true}}, metaT(), ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	if len(res.Noops) != 0 || len(res.Kept) != 1 {
		t.Fatalf("纯 mode 变更必须保留：Kept=%v Noops=%v", res.Kept, res.Noops)
	}
	p := string(res.Patch)
	mustContainT(t, p, "old mode 100644\n", "new mode 100755\n")
	if strings.Contains(p, "@@") || indexLineRe.MatchString(p) {
		t.Fatalf("纯 mode 变更不该有 hunk 或 index 行：\n%s", p)
	}

	rc, out := amT(t, recv, patchFileT(t, res.Patch), "-3")
	if rc != 0 {
		t.Fatalf("git am -3 rc=%d\n%s\n补丁：\n%s", rc, out, p)
	}
	if got := permT(t, filepath.Join(recv, filepath.FromSlash(rel))); got != 0o755 {
		t.Fatalf("落盘权限 = %o, want 755", got)
	}
	if got := fileBytesT(t, filepath.Join(recv, filepath.FromSlash(rel))); string(got) != T {
		t.Fatalf("内容 = %q, want %q（纯 mode 变更不该动内容）", got, T)
	}
}

// TestBuildPatchCRLF 覆盖规则⑥：CRLF 模板的补丁默认 `am` rc=128（剥 CR 后对不上，
// 且留 am 中断态）；`--keep-cr` rc=0 且落盘字节逐字 == T+delta。
func TestBuildPatchCRLF(t *testing.T) {
	const (
		rel = "src/win.txt"
		T   = "l1\r\nl2\r\nl3\r\n"
		F   = "l1\r\nL2\r\nl3\r\n"
	)
	res, err := BuildPatch([]PatchFile{{Src: rel, T: []byte(T), FNew: []byte(F)}}, metaT(), ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	// 补丁内容行以 CRLF 呈现（T 的 EOL；F_new 已按 §20.3 硬规则还原）。
	mustContainT(t, string(res.Patch), " l1\r\n-l2\r\n+L2\r\n l3\r\n")

	// 反例：默认 am（am.keepcr=false 钉住）必须硬失败并留中断态。
	recvA := recvRepoT(t, map[string]string{rel: T})
	rc, out := amT(t, recvA, patchFileT(t, res.Patch), "-3")
	if rc != 128 {
		t.Fatalf("默认 am 对 CRLF 模板应 rc=128，实际 rc=%d\n%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(recvA, ".git", "rebase-apply")); err != nil {
		t.Fatalf("默认 am 失败后应留 am 中断态（.git/rebase-apply）：%v", err)
	}

	// 正例：--keep-cr。
	recvB := recvRepoT(t, map[string]string{rel: T})
	rc, out = amT(t, recvB, patchFileT(t, res.Patch), "-3", "--keep-cr")
	if rc != 0 {
		t.Fatalf("git am -3 --keep-cr rc=%d\n%s", rc, out)
	}
	if got := fileBytesT(t, filepath.Join(recvB, filepath.FromSlash(rel))); string(got) != F {
		t.Fatalf("落盘 = %q, want %q（逐字节）", got, F)
	}
}

// TestBuildPatchNoopExclusion 覆盖 noop 四例：部分剔除 / 全 noop（哨兵 error）/
// mode-only 保留 / 空新增文件保留。
func TestBuildPatchNoopExclusion(t *testing.T) {
	t.Run("部分剔除", func(t *testing.T) {
		const same = "identical\n"
		files := []PatchFile{
			{Src: "a/same.txt", T: []byte(same), FNew: []byte(same)},
			{Src: "b/changed.txt", T: []byte("old\n"), FNew: []byte("new\n")},
		}
		res, err := BuildPatch(files, metaT(), ds40T, ts40T)
		if err != nil {
			t.Fatalf("BuildPatch: %v", err)
		}
		if len(res.Noops) != 1 || res.Noops[0] != "a/same.txt" {
			t.Fatalf("Noops = %v, want [a/same.txt]", res.Noops)
		}
		if len(res.Kept) != 1 || res.Kept[0] != "b/changed.txt" {
			t.Fatalf("Kept = %v, want [b/changed.txt]", res.Kept)
		}
		p := string(res.Patch)
		if strings.Contains(p, "a/same.txt") {
			t.Fatalf("noop 路径必须从补丁剔除：\n%s", p)
		}
		mustContainT(t, p, "diff --git a/b/changed.txt b/b/changed.txt\n")
	})

	t.Run("全 noop", func(t *testing.T) {
		const same = "identical\n"
		files := []PatchFile{
			{Src: "z/last.txt", T: []byte(same), FNew: []byte(same)},
			{Src: "a/first.txt", T: []byte(same), FNew: []byte(same)},
		}
		res, err := BuildPatch(files, metaT(), ds40T, ts40T)
		if !errors.Is(err, ErrAllNoop) {
			t.Fatalf("全 noop 必须返回 ErrAllNoop，实际 err=%v", err)
		}
		if !equalStringsT(res.Noops, []string{"a/first.txt", "z/last.txt"}) {
			t.Fatalf("Noops = %v, want 全量且排序", res.Noops)
		}
		if len(res.Kept) != 0 || res.Patch != nil {
			t.Fatalf("全 noop：Kept=%v Patch=%q（都应为空）", res.Kept, res.Patch)
		}
	})

	t.Run("mode-only 与空新增文件保留", func(t *testing.T) {
		files := []PatchFile{
			{Src: "mode.txt", T: []byte("x\n"), FNew: []byte("x\n"), OldExec: true, NewExec: false},
			{Src: "empty-new.txt", FNew: nil, IsNew: true},
		}
		res, err := BuildPatch(files, metaT(), ds40T, ts40T)
		if err != nil {
			t.Fatalf("BuildPatch: %v", err)
		}
		if len(res.Noops) != 0 {
			t.Fatalf("Noops = %v, want 空（两条都不得判 noop）", res.Noops)
		}
		if !equalStringsT(res.Kept, []string{"empty-new.txt", "mode.txt"}) {
			t.Fatalf("Kept = %v", res.Kept)
		}
	})
}

// TestBuildPatchDeterministicAndSorted 覆盖确定性（同输入两次导出逐字节相同）与
// 多文件排序稳定（输入乱序 ⇒ 输出同序）。
func TestBuildPatchDeterministicAndSorted(t *testing.T) {
	mk := func() []PatchFile {
		return []PatchFile{
			{Src: "c/third.txt", T: []byte("c\n"), FNew: []byte("C\n")},
			{Src: "a/first.txt", T: []byte("a\n"), FNew: []byte("A\n")},
			{Src: "b/second.txt", T: []byte("b\n"), FNew: []byte("B\n")},
		}
	}
	one, err := BuildPatch(mk(), metaT(), ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	two, err := BuildPatch(mk(), metaT(), ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	if !bytes.Equal(one.Patch, two.Patch) {
		t.Fatalf("两次导出字节不同：\n%s\n---\n%s", one.Patch, two.Patch)
	}
	if !equalStringsT(one.Kept, []string{"a/first.txt", "b/second.txt", "c/third.txt"}) {
		t.Fatalf("Kept = %v, want 按 Src 排序", one.Kept)
	}
	// 段序 == Src 序（一个补丁文件 = 一个提交，多文件拼接次序须稳定）。
	pa := strings.Index(string(one.Patch), "diff --git a/a/first.txt")
	pb := strings.Index(string(one.Patch), "diff --git a/b/second.txt")
	pc := strings.Index(string(one.Patch), "diff --git a/c/third.txt")
	if !(pa >= 0 && pa < pb && pb < pc) {
		t.Fatalf("段序不符：a=%d b=%d c=%d\n%s", pa, pb, pc, one.Patch)
	}
}

// TestBuildPatchNonASCIIHeader 固化规则⑤ 选定的实现：Subject/作者名用 RFC 2047
// （Q 编码）承载非 ASCII，正文原样 UTF-8；接收侧解码后逐字还原。
func TestBuildPatchNonASCIIHeader(t *testing.T) {
	const (
		rel     = "src/f.txt"
		T       = "l1\nl2\n"
		F       = "l1\nL2\n"
		subject = "feat: 改共享面"
		body    = "为什么要改\n\nTemplate-Upstream: yes"
	)
	meta := CommitMeta{
		Author:  "张三",
		Email:   "z@example.com",
		Date:    "Sun, 11 Oct 2026 10:00:00 +0800",
		Subject: subject,
		Body:    body,
	}
	recv := recvRepoT(t, map[string]string{rel: T})

	res, err := BuildPatch([]PatchFile{{Src: rel, T: []byte(T), FNew: []byte(F)}}, meta, ds40T, ts40T)
	if err != nil {
		t.Fatalf("BuildPatch: %v", err)
	}
	p := string(res.Patch)
	mustContainT(t, p, "Subject: =?utf-8?", "From: =?utf-8?")
	if strings.Contains(p, "Subject: "+subject) {
		t.Fatalf("非 ASCII 主题应当编码而不是原样进头部：\n%s", p)
	}
	// 正文原样 UTF-8（不参与头部编码）。
	mustContainT(t, p, "\n"+body+"\n---\n")

	rc, out := amT(t, recv, patchFileT(t, res.Patch), "-3")
	if rc != 0 {
		t.Fatalf("git am -3 rc=%d\n%s\n补丁：\n%s", rc, out, p)
	}
	if got := commitBodyT(t, recv); got != subject+"\n\n"+body+"\n" {
		t.Fatalf("消息 = %q, want 逐字还原", got)
	}
	if got := mustGitT(t, recv, "log", "-1", "--format=%an <%ae>"); got != "张三 <z@example.com>" {
		t.Fatalf("作者 = %q, want 张三 <z@example.com>", got)
	}
}

// TestBuildArtifactNameStable 是 §20.6 的产物名判据（<ds7>-<ts7>-<slug>.patch）：
// 含模板短 sha（模板发新版后同一派生 sha 的重新导出是不同产物）。
func TestBuildArtifactNameStable(t *testing.T) {
	got := ArtifactName(ds40T, ts40T, "feat: share fix")
	if got != "1111111-2222222-feat-share-fix.patch" {
		t.Fatalf("ArtifactName = %q", got)
	}
	if again := ArtifactName(ds40T, ts40T, "feat: share fix"); again != got {
		t.Fatalf("ArtifactName 不稳定：%q vs %q", got, again)
	}
	if other := ArtifactName(ds40T, "3333333333333333333333333333333333333333", "feat: share fix"); other == got {
		t.Fatalf("模板 commit 变了产物名必须变（不得静默覆盖已送出的补丁）：%q", other)
	}
}

// TestBuildPatchFailClosed 覆盖输入面 fail-closed：空输入 / 空 Date / 非法 sha /
// 重复 Src / 非法 Src 一律报错，且绝不退化成「全 noop」（那是另一条出口）。
func TestBuildPatchFailClosed(t *testing.T) {
	ok := func() PatchFile { return PatchFile{Src: "a.txt", T: []byte("a\n"), FNew: []byte("b\n")} }

	cases := []struct {
		name  string
		files []PatchFile
		meta  CommitMeta
		ds    string
		ts    string
		want  string // 错误文案要点
	}{
		{name: "空输入", files: nil, meta: metaT(), ds: ds40T, ts: ts40T, want: "没有"},
		{name: "空 Date", files: []PatchFile{ok()}, meta: func() CommitMeta { m := metaT(); m.Date = ""; return m }(), ds: ds40T, ts: ts40T, want: "Date"},
		{name: "空 Subject", files: []PatchFile{ok()}, meta: func() CommitMeta { m := metaT(); m.Subject = ""; return m }(), ds: ds40T, ts: ts40T, want: "Subject"},
		{name: "派生 sha 非 40 位", files: []PatchFile{ok()}, meta: metaT(), ds: "abc123", ts: ts40T, want: "derivedSha"},
		{name: "模板 sha 非十六进制", files: []PatchFile{ok()}, meta: metaT(), ds: ds40T, ts: strings.Repeat("z", 40), want: "tmplSha"},
		{name: "重复 Src", files: []PatchFile{ok(), {Src: "a.txt", T: []byte("x\n"), FNew: []byte("y\n")}}, meta: metaT(), ds: ds40T, ts: ts40T, want: "重复"},
		{name: "Src 穿越", files: []PatchFile{{Src: "../out.txt", FNew: []byte("x\n"), IsNew: true}}, meta: metaT(), ds: ds40T, ts: ts40T, want: "Src"},
		{name: "Src 绝对", files: []PatchFile{{Src: "/etc/x", FNew: []byte("x\n"), IsNew: true}}, meta: metaT(), ds: ds40T, ts: ts40T, want: "Src"},
		{name: "Src 非规范", files: []PatchFile{{Src: "a/../b.txt", FNew: []byte("x\n"), IsNew: true}}, meta: metaT(), ds: ds40T, ts: ts40T, want: "Src"},
		{name: "Src 空", files: []PatchFile{{Src: "", FNew: []byte("x\n"), IsNew: true}}, meta: metaT(), ds: ds40T, ts: ts40T, want: "Src"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := BuildPatch(c.files, c.meta, c.ds, c.ts)
			if err == nil {
				t.Fatalf("应当报错，实际 res=%+v", res)
			}
			if errors.Is(err, ErrAllNoop) {
				t.Fatalf("输入面错误不得退化成 ErrAllNoop：%v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误文案 = %q, want 含 %q", err.Error(), c.want)
			}
			if res.Patch != nil {
				t.Fatalf("错误路径不得产出补丁：%q", res.Patch)
			}
		})
	}
}

// equalStringsT 比对两个字符串切片（顺序敏感）。
func equalStringsT(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
