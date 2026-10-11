package backflow

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xbcio/xinit/internal/fsutil"
)

// ErrAllNoop 是「该提交的改动在模板坐标系下为空」的哨兵（§20.6）：判 noop 的
// 路径从补丁剔除后一条不剩。调用方用 errors.Is 把它与其它错误区分开——文案与
// rc=1 的打印出口归 CLI 层。注意它只表示「有候选、但全部与 T 无差异」，没有
// 候选（全部路径被出口过滤）是另一条出口（守卫 8）。
var ErrAllNoop = errors.New("该提交的改动在模板坐标系下为空")

// PatchFile 是一个待导出条目在模板坐标系下的三方物：Src 是模板态路径（补丁内
// a/、b/ 路径），T 是模板原文（IsNew 时为 nil），FNew 是翻译后的新内容
// （EOL 已按 §20.3 硬规则还原为 T 的 EOL）。
type PatchFile struct {
	Src     string
	T       []byte
	FNew    []byte
	OldExec bool
	NewExec bool
	IsNew   bool
}

// CommitMeta 是 mailbox 的提交身份，**逐字取派生提交**（§20.6 步骤 3：出处留痕
// 随 message 进模板仓，与 xkitora 的 `cherry-pick -x` 机制不同）。
//
// Date 必须是派生提交的作者时间（`git log -1 --format=%aD` 的 RFC 2822 与
// `%aI` 的 ISO 8601 都实测可被 `git am` 解析）。空 Date 会让 `git am` 用
// 「当前时刻」落提交（实测），破坏「同输入同产物」的确定性——故 fail-closed。
// Body 是提交消息去掉首行标题后的余部（首部空行由 BuildPatch 剥净）。
type CommitMeta struct {
	Author  string
	Email   string
	Date    string
	Subject string
	Body    string
}

// PatchResult 是 BuildPatch 的产出。
type PatchResult struct {
	Patch []byte   // 完整 mailbox（无导出项时 nil）
	Kept  []string // 保留进补丁的 Src（排序）
	Noops []string // noop 剔除的 Src（排序）
}

// BuildPatch 把待导出条目拼成一份可被接收侧 `git am -3` 消费的 mailbox
// （§20.6：一个补丁文件 = 一个提交；多文件按 Src 排序拼接）。
//
// 判据落点：
//   - noop = **内容 ∧ exec 位**均与 T 无差异 ⇒ 剔除 + 记进 Noops（纯 mode 变更
//     不得判 noop——exec 位是 §8.3 一等公民；新增文件永不判 noop，空的新增文件
//     同样保留）；全部路径 noop ⇒ 返回 ErrAllNoop（error 与 PatchResult 同时
//     返回：Kept 空、Noops 完整）。
//   - 配方 = §20.6 步骤 1–4：T/F_new 写进临时目录的 a/、b/ 两侧（**按各自 exec
//     位显式 chmod**——mode 行来自临时文件权限位，不 chmod 则纯 mode 变更空输出、
//     exec 位静默丢失）、逐文件 `git diff --no-index --full-index --no-prefix`、
//     手工拼 mailbox 头 + diff + 尾。
//   - 确定性：author/date/Subject/正文取派生提交；`From <sha>` 行固定；
//     Subject/作者名非 ASCII 时按规则⑤ 用 RFC 2047（Q 编码）承载，正文原样
//     UTF-8；不产 Message-Id/version 行。
func BuildPatch(files []PatchFile, meta CommitMeta, derivedSha, tmplSha string) (PatchResult, error) {
	// 两个 sha 是溯源与命名的锚（规则③ 的 From 行 / 文件名的模板短 sha）：
	// 空值或非 40 位会让两者静默退化成不可回溯的形态，故 fail-closed。
	if err := checkShaArg("derivedSha", derivedSha); err != nil {
		return PatchResult{}, err
	}
	if err := checkShaArg("tmplSha", tmplSha); err != nil {
		return PatchResult{}, err
	}
	if strings.TrimSpace(meta.Date) == "" {
		return PatchResult{}, errors.New("CommitMeta.Date 为空：author/date 必须取派生提交（§20.6 规则③；如 `git log -1 --format=%aD`）——缺 Date 的 mailbox 会让 `git am` 用「当前时刻」落提交，破坏「同输入同产物」")
	}
	if strings.TrimSpace(meta.Subject) == "" {
		// 实测：`Subject:` 为空时 mailinfo 会把正文首行提升成主题并留下前导空行
		// （消息被静默改写），故 fail-closed。派生提交的首行即主题，正常不会为空。
		return PatchResult{}, errors.New("CommitMeta.Subject 为空：mailbox 的 Subject 头必须有值（取派生提交消息的首行）")
	}
	if len(files) == 0 {
		return PatchResult{}, errors.New("没有可导出的文件：BuildPatch 需要至少一个候选（「全部路径被出口过滤」是调用方的另一条出口）")
	}

	sorted := make([]PatchFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Src < sorted[j].Src })
	for i, f := range sorted {
		if err := checkSrcPath(f.Src); err != nil {
			return PatchResult{}, err
		}
		if i > 0 && sorted[i-1].Src == f.Src {
			return PatchResult{}, fmt.Errorf("待导出条目里 Src %q 重复：同一模板路径只能有一条改动记录（一个补丁文件 = 一个提交）", f.Src)
		}
	}

	var res PatchResult
	var kept []PatchFile
	for _, f := range sorted {
		if isNoop(f) {
			res.Noops = append(res.Noops, f.Src)
			continue
		}
		kept = append(kept, f)
		res.Kept = append(res.Kept, f.Src)
	}
	if len(kept) == 0 {
		return res, fmt.Errorf("%w：%d 条候选路径全部与模板无差异（noop 剔除）", ErrAllNoop, len(res.Noops))
	}

	tmp, err := os.MkdirTemp("", "xinit-backflow-")
	if err != nil {
		return PatchResult{}, fmt.Errorf("创建配方临时目录：%w", err)
	}
	defer os.RemoveAll(tmp)

	segs := make([][]byte, 0, len(kept))
	for _, f := range kept {
		seg, err := patchSegment(tmp, f)
		if err != nil {
			return PatchResult{}, err
		}
		segs = append(segs, seg)
	}

	var b bytes.Buffer
	// mbox From 行：sha 取派生提交；`Mon Sep 17 00:00:00 2001` 是 git 自己的固定
	// 历史值（format-patch 同此），不是日期来源——日期在 Date: 头。
	fmt.Fprintf(&b, "From %s Mon Sep 17 00:00:00 2001\n", derivedSha)
	fmt.Fprintf(&b, "From: %s\n", formatAddress(meta))
	fmt.Fprintf(&b, "Date: %s\n", meta.Date)
	fmt.Fprintf(&b, "Subject: %s\n\n", encodeWord(meta.Subject))
	if body := strings.Trim(meta.Body, "\n"); body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	for _, seg := range segs {
		b.Write(seg)
	}
	// 尾行：**裸** `From <40hex>`（无日期后缀）。带日期的 From 行会被 mailsplit
	// 认成新消息边界 → `Patch is empty` rc=128 且留 am 中断态（实测）；裸行不含
	// 其启发式要求的时分秒形状，不会被切分。
	fmt.Fprintf(&b, "\nFrom %s\n", derivedSha)

	res.Patch = b.Bytes()
	return res, nil
}

// isNoop 是 §20.6 的 noop 判据：内容 ∧ exec 位均与 T 无差异。
//
// 新增文件永不判 noop：无 T 可比（B/T 均不存在），空的新增文件也一样——「与模板
// 无差异」对新文件不成立，其内容本身就是模板侧的新增。
func isNoop(f PatchFile) bool {
	if f.IsNew {
		return false
	}
	return f.OldExec == f.NewExec && bytes.Equal(f.T, f.FNew)
}

// patchSegment 产出一个文件的 diff 段（§20.6 步骤 1/2/2'）。
//
// 传给 git 的路径必须是**相对**形态（`a/<Src>`、`b/<Src>`，配 `-C <tmp>`）：配方
// 靠 `--no-prefix` 把这两个已有前缀的路径原样打进补丁头（`diff --git a/<Src> b/<Src>`）；
// 传绝对路径会把临时目录名泄进补丁（不可确定、am 也认不出目标路径）。
func patchSegment(tmp string, f PatchFile) ([]byte, error) {
	if err := writeRecipeFile(filepath.Join(tmp, "b", filepath.FromSlash(f.Src)), f.FNew, f.NewExec); err != nil {
		return nil, err
	}
	left := os.DevNull // 新增文件：无 T，左侧走 /dev/null（步骤 2'）
	if !f.IsNew {
		if err := writeRecipeFile(filepath.Join(tmp, "a", filepath.FromSlash(f.Src)), f.T, f.OldExec); err != nil {
			return nil, err
		}
		left = "a/" + f.Src
	}
	seg, err := diffGit(tmp, left, "b/"+f.Src)
	if err != nil {
		return nil, fmt.Errorf("生成 %s 的补丁段：%w", f.Src, err)
	}
	if err := verifySegment(seg, f); err != nil {
		return nil, err
	}
	return seg, nil
}

// writeRecipeFile 写一个配方临时文件并按 exec 位**显式 chmod**：mode 行来自临时
// 文件的权限位，不 chmod 则纯 mode 变更空输出、exec 位静默丢失（§20.6 步骤 1
// 实测）。先按 0600 写再 chmod——别依赖 `os.WriteFile` 的 mode 参数（会被 umask 削）。
func writeRecipeFile(p string, content []byte, exec bool) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("创建配方目录 %s：%w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, content, 0o600); err != nil {
		return fmt.Errorf("写配方临时文件 %s：%w", p, err)
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.Chmod(p, mode); err != nil {
		return fmt.Errorf("设置配方临时文件权限 %s：%w", p, err)
	}
	return nil
}

// diffGit 跑配方的 `git diff --no-index --full-index --no-prefix <left> <right>`
// （§20.6 步骤 2/2'）。
//
// `--no-index` 两侧有差异时 rc=1（同 diff(1)），故不能走 runGit 的「rc≠0 即错」
// 口径。环境面显式钉死（各自都有实测/机制成因）：
//   - `-c core.autocrlf=false`：用户全局 autocrlf=true 时 git 会把 CRLF 内容归一后
//     入 blob（实测：index 左值变成 LF blob），接收仓解析不到 pre-image ⇒ 违反
//     可接收性①；也让 CRLF 模板的补丁内容被静默改写成 LF。
//   - `--no-color` / `--no-ext-diff`：全局 `color.ui=always` 会把 ANSI 序列灌进
//     补丁、`diff.external` 会顶掉 diff 产出（两处都会静默产出不可消费的补丁）。
//   - `diff.algorithm=myers` / `diff.indentHeuristic=true`：显式钉成 git 的默认值，
//     让 hunk 形态不受用户配置影响（「同输入同产物」的字节确定性）。
func diffGit(dir string, left, right string) ([]byte, error) {
	args := []string{
		"-C", dir,
		"-c", "core.autocrlf=false",
		"-c", "diff.algorithm=myers",
		"-c", "diff.indentHeuristic=true",
		"diff", "--no-index", "--full-index", "--no-prefix", "--no-color", "--no-ext-diff",
		left, right,
	}
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 { // rc=1 = 有差异
			suffix := ""
			if detail := firstLine(stderr.String()); detail != "" {
				suffix = "（" + detail + "）"
			}
			return nil, fmt.Errorf("跑 git diff --no-index 失败：%w%s", err, suffix)
		}
	}
	return stdout.Bytes(), nil
}

// fullIndexLineRe 匹配 --full-index 的 index 行（40 位双侧；mode 相等时 git 会
// 追加 ` <mode>` 后缀，纯 mode 变更则整行不出现）。
var fullIndexLineRe = regexp.MustCompile(`(?m)^index ([0-9a-f]{40})\.\.([0-9a-f]{40})`)

// verifySegment 在拼装前钉住配方产出形态（fail-closed，全部可在本层判定）：
//
//   - 段非空：Kept 的文件必与 T 有差异（noop 已剔除），空段 = 配方没产出，绝不能
//     静默拼进 mailbox；
//   - index 行必须 40 位双侧（规则② --full-index：缩写 hash 在 `-3` 回退里不保证
//     可解析）；
//   - 非新增：index 左值 == T 的 blob sha —— 这是可接收性①（pre-image blob 须在
//     接收仓可解析）的本地代理：接收仓里 T 就是同一个对象；纯 mode 变更没有 index
//     行，须有 `old mode`/`new mode` 两行（§20.6 步骤 2 实测形态）；
//   - 新增：index 左值全 0（/dev/null 侧）且带 `new file mode`（步骤 2'）。
func verifySegment(seg []byte, f PatchFile) error {
	if len(seg) == 0 {
		return fmt.Errorf("配方未产出 %s 的 diff 段：该文件与模板没有 git 认得的差异（内容与 exec 位都已比过，故属形态异常）", f.Src)
	}
	s := string(seg)
	m := fullIndexLineRe.FindStringSubmatch(s)
	if f.IsNew {
		if !strings.Contains(s, "new file mode ") {
			return fmt.Errorf("新增文件 %s 的补丁段缺 `new file mode`：\n%s", f.Src, s)
		}
		if m == nil {
			return fmt.Errorf("新增文件 %s 的补丁段缺 40 位 index 行（--full-index）：\n%s", f.Src, s)
		}
		if m[1] != strings.Repeat("0", 40) {
			return fmt.Errorf("新增文件 %s 的 index 左值应为全 0（/dev/null 侧），实际 %s", f.Src, m[1])
		}
		return nil
	}
	if m == nil {
		if !strings.Contains(s, "old mode ") || !strings.Contains(s, "new mode ") {
			return fmt.Errorf("非新增的 %s 段既无 index 行也无 `old mode`/`new mode`（配方产出形态异常）：\n%s", f.Src, s)
		}
		return nil
	}
	if want := gitBlobHash(f.T); m[1] != want {
		return fmt.Errorf("补丁的 pre-image（%s）与模板原文的 blob（%s）不一致：%s——该补丁在接收仓无法用 `am -3` 的索引信息重建基线（§20.6 可接收性①）；常见成因是 git 配置对临时文件内容做了转换", m[1], want, f.Src)
	}
	return nil
}

// gitBlobHash 复算 git 的 blob 对象名 `sha1("blob <len>\0" + content)`。
// 这是 git 的对象命名算法（sha1 仓口径 = git 默认，也是 §20.6 实测基线），
// 不是密码学用途。
func gitBlobHash(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// checkShaArg 校验调用方给的是 `git rev-parse` 的 40 位 sha。
func checkShaArg(what, sha string) error {
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return fmt.Errorf("%s 必须是 40 位小写十六进制（`git rev-parse` 的输出），实际 %q", what, sha)
	}
	return nil
}

// checkSrcPath 是写盘前的路径闸门（§20.7 规则 4 已在解析层闸过合法性，这里是
// 纵深防御：`filepath.Join(<tmp>, "b", Src)` 的前缀逃逸面 + 非规范形态一律 fail-closed）。
func checkSrcPath(src string) error {
	if !fsutil.ValidRelPath(src) {
		return fmt.Errorf("待导出条目的 Src %q 非法：必须是模板树里的规范相对路径（不得为空/绝对/含 `..`/含 `\\` 或 `:`/非规范）", src)
	}
	return nil
}

// encodeWord 按规则⑤ 选定 RFC 2047（Q 编码）承载非 ASCII 头字段：纯 ASCII 原样
// 返回（mime 的编码器只对需要编码的串动手），非 ASCII 产出 `=?utf-8?q?...?=`，
// 接收侧 `git am` 解码后逐字还原（实测：Subject 与作者名都还原）。
func encodeWord(s string) string {
	return mime.QEncoding.Encode("utf-8", s)
}

// formatAddress 拼 mailbox 的 `From:` 值：`Name <email>`；名字非 ASCII 时与
// Subject 同口径编码（否则头字段会带裸非 ASCII 字节）。
func formatAddress(meta CommitMeta) string {
	if meta.Author == "" {
		return "<" + meta.Email + ">"
	}
	return encodeWord(meta.Author) + " <" + meta.Email + ">"
}

// ArtifactName 是产物文件名 `"<derived-sha7>-<tmpl-sha7>-<slug>.patch"`（§20.6）。
//
// 文件名含**模板 commit 短 sha**：模板发新版后，同一派生 sha 的重新导出是不同
// 产物，不得静默覆盖已送出的补丁。同输入同产物（slug 的 kebab 化是确定性的）。
func ArtifactName(derivedSha, tmplSha, subject string) string {
	return shortSha7(derivedSha) + "-" + shortSha7(tmplSha) + "-" + kebabSlug(subject) + ".patch"
}

// maxSlug 是 slug 的字符上限（「短形」；文件名总长因此有界）。
const maxSlug = 40

// shortSha7 取短 sha（不足 7 位时原样——产物名只是可读前缀，臂长由调用方给值决定）。
func shortSha7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// kebabSlug 把提交主题折成 kebab 短形：只留 ASCII 字母（小写）与数字，其余字节
// 折叠成一个 '-'，去首尾 '-'，截断到 maxSlug（截断后再去尾 '-'）；折完为空则退化
// 为 "patch"（名字恒非空；不同提交不撞名由 sha 前缀保证）。非 ASCII 主题折完为
// 空，是「kebab 化短形」的字面后果（文件名保持全 ASCII 安全字符）。
func kebabSlug(subject string) string {
	if i := strings.IndexAny(subject, "\r\n"); i >= 0 {
		subject = subject[:i] // 只取主题首行
	}
	var b strings.Builder
	pendingDash := false
	for i := 0; i < len(subject); i++ {
		c := subject[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteByte(c + ('a' - 'A'))
		default:
			pendingDash = true
		}
	}
	s := b.String()
	if len(s) > maxSlug {
		s = s[:maxSlug]
	}
	s = strings.TrimRight(s, "-")
	if s == "" {
		return "patch"
	}
	return s
}
