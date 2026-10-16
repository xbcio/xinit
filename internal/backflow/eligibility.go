package backflow

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Qualified 是资格预检（§20.4 守卫 1–5）的通过结果。
type Qualified struct {
	RepoDir   string   // git 目录的 -C 目标（项目根）
	Commit    string   // 解析后的 40 位 commit sha
	Message   string   // 完整提交消息（%B）
	Untracked []string // 未跟踪文件（警告面，非错误）
}

// 判据原文——守卫 4/5 失败时逐字打印（§20.4 表；§20.10 文案基准表第 5 行）。
const (
	critSingleCommit = "提交存在且非 root/非 merge：`git rev-list --parents -n 1 <commit>` 词数 == 2"
	critTrailer      = "提交消息末段 trailer 块含 `Template-Upstream: yes`（判定全落 `git interpret-trailers --parse` 输出上）——键大小写须逐字；空白与 CRLF 属 parse 归一"
)

// trailerLine 是 trailer 的判定目标行（比对落在 parse 输出上，非原始消息）。判定全落
// `git interpret-trailers --parse` 的输出上（§20.4 行 5）：**键大小写敏感**（parse 对键
// 原样输出，非逐字即拒）；**空白与 CRLF 由 `--parse` 归一 → 接受**（§20.10 用例 8 实测定形：
// `Key:value`、冒号后多空格、键尾空格、值尾空格四形状都归一出本行）；本包不自行重实现
// 块判定与逐字差分。
const trailerLine = "Template-Upstream: yes"

// inProgressOps 是守卫 2 的五探测（§20.4），按此顺序报告。
var inProgressOps = []struct {
	marker string // `git rev-parse --git-path <marker>` 的参数（= git 目录下的标记）
	what   string // 文案里点名的状态
	abort  string // 出口命令
}{
	{"MERGE_HEAD", "merge", "git merge --abort"},
	{"CHERRY_PICK_HEAD", "cherry-pick", "git cherry-pick --abort"},
	{"REVERT_HEAD", "revert", "git revert --abort"},
	{"rebase-merge", "rebase", "git rebase --abort"},
	{"rebase-apply", "rebase 或 git am", "git rebase --abort 或 git am --abort"},
}

// Qualify 跑 §20.4 守卫 1–5：派生是 git 仓、无进行中操作、已跟踪文件干净、
// 提交存在且非 root/非 merge、提交消息末段 trailer 块判出目标行（见 checkTrailer）。
// 任一不过返回普通 error（CLI 层负责包 ExitError 与退出码）。
func Qualify(projectDir, commit string) (*Qualified, error) {
	if _, err := runGit(projectDir, nil, "rev-parse", "--git-dir"); err != nil {
		// %w 保真底层 git 错误：git 缺失/损坏时不能被读成「不在 git 仓库内」
		// （那会把人引向同样失败的 git init）。
		return nil, fmt.Errorf("回流要求派生项目是 git 仓库（opt-in）：%s 不在 git 仓库内（%w），README「3.2 Create a derived project」有这条 opt-in 说明（派生项目不必是 git 仓，回流只在 git 派生上可用）；请在该目录 git init，或放弃回流", projectDir, err)
	}
	if err := checkNoOperationInProgress(projectDir); err != nil {
		return nil, err
	}
	dirty, untracked, err := worktreeState(projectDir)
	if err != nil {
		return nil, err
	}
	if len(dirty) > 0 {
		return nil, fmt.Errorf("工作树有未提交的已跟踪文件改动，拒绝回流：%s；请先 commit 或 git stash 后重跑（未跟踪文件只警告、不算脏）", strings.Join(dirty, "、"))
	}
	sha, err := checkCommitShape(projectDir, commit)
	if err != nil {
		return nil, err
	}
	// --no-show-signature：`log.showSignature=true` 时 git 会把 gpg 程序的输出
	// （走 stderr）转发到自身 stdout 且**前置**于消息正文，Message 会被静默污染
	// （它是 T2 生成 mailbox Subject/正文的唯一来源）。
	raw, err := runGit(projectDir, nil, "log", "-1", "--no-show-signature", "--format=%B", sha)
	if err != nil {
		return nil, fmt.Errorf("读取提交消息失败：%w", err)
	}
	if err := checkTrailer(projectDir, string(raw)); err != nil {
		return nil, err
	}
	return &Qualified{RepoDir: projectDir, Commit: sha, Message: string(raw), Untracked: untracked}, nil
}

// checkNoOperationInProgress 跑守卫 2：`git rev-parse --git-path` 探测五个标记，
// 任一存在即拒绝（rebase/merge 中 HEAD 是重放提交，导出它语义可疑）。
func checkNoOperationInProgress(repoDir string) error {
	for _, op := range inProgressOps {
		out, err := runGit(repoDir, nil, "rev-parse", "--git-path", op.marker)
		if err != nil {
			return fmt.Errorf("探测 %s 失败：%w", op.marker, err)
		}
		// --git-path 的输出相对 -C 目标，仓外相对路径一律落回项目根（绝对路径直接用）。
		p := strings.TrimSpace(string(out))
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoDir, p)
		}
		switch _, statErr := os.Stat(p); {
		case statErr == nil:
			return fmt.Errorf("工作树上有进行中的操作：%s（检测到 %s）。请完成该操作或 %s 后再跑", op.what, op.marker, op.abort)
		case errors.Is(statErr, fs.ErrNotExist):
			// 标记不存在 = 该操作不在进行中，继续探测下一个。
		default:
			// 存在性无法判定（EACCES / ELOOP 等）不得当作「不存在」——漏检进行中
			// 操作会让后续导出一个语义可疑的提交，故 fail-closed。
			return fmt.Errorf("探测进行中操作失败：无法判定 %s 是否存在（%s）：%w；判据 = 五探测（MERGE_HEAD / CHERRY_PICK_HEAD / REVERT_HEAD / rebase-merge / rebase-apply）任一存在即拒绝，无法判定时按 fail-closed 拒绝", op.marker, p, statErr)
		}
	}
	return nil
}

// worktreeState 跑守卫 3：一次 `git status --porcelain -z` 全量解析——`??` 条目
// 归未跟踪（只警告），其余任何条目归脏（拒绝面）。
// 等价性：spec 判据「`--untracked-files=no` 为空」⇔「无非 `??` 条目」。
func worktreeState(repoDir string) (dirty, untracked []string, err error) {
	// --untracked-files=normal 显式钉住：用户全局 `status.showUntrackedFiles=no`
	// 会让未跟踪警告静默消失（spec「未跟踪文件只警告」的语义面）。
	out, err := runGit(repoDir, nil, "status", "--porcelain", "--untracked-files=normal", "-z")
	if err != nil {
		return nil, nil, err
	}
	segs := splitNUL(out)
	for i := 0; i < len(segs); i++ {
		s := segs[i]
		if len(s) < 4 || s[2] != ' ' {
			return nil, nil, fmt.Errorf("解析 git status --porcelain -z 失败：条目 %q 不是 `XY <path>` 形状", s)
		}
		status, path := s[:2], s[3:]
		// porcelain v1 -z 的 R/C 条目多一个 NUL 段（原路径，新路径在前），跳过它。
		if strings.ContainsAny(status, "RC") && i+1 < len(segs) {
			i++
		}
		if status == "??" {
			untracked = append(untracked, path)
			continue
		}
		dirty = append(dirty, path)
	}
	return dirty, untracked, nil
}

// checkCommitShape 跑守卫 4：`git rev-list --parents -n 1 <commit>` 词数须为 2
// （root 提交 1 词、merge 提交 ≥3 词、命令失败均拒），返回解析出的完整 commit sha。
func checkCommitShape(repoDir, commit string) (string, error) {
	out, err := runGit(repoDir, nil, "rev-list", "--parents", "-n", "1", "--end-of-options", commit)
	if err != nil {
		return "", fmt.Errorf("提交不符合回流要求：判据 = %s，实际无法解析 %q（%s）。请确认该提交存在，或重拆提交（对齐 xkitora 场景 D）后再跑", critSingleCommit, commit, firstLine(err.Error()))
	}
	fields := strings.Fields(string(out))
	switch {
	case len(fields) == 2:
		return fields[0], nil
	case len(fields) == 1:
		return "", fmt.Errorf("提交不符合回流要求：判据 = %s，实际 %q 是 root 提交（无父提交）。请重拆提交（对齐 xkitora 场景 D）后再跑", critSingleCommit, commit)
	case len(fields) > 2:
		return "", fmt.Errorf("提交不符合回流要求：判据 = %s，实际 %q 是 merge 提交（%d 个父提交）。请重拆提交（对齐 xkitora 场景 D）后再跑", critSingleCommit, commit, len(fields)-1)
	default:
		return "", fmt.Errorf("提交不符合回流要求：判据 = %s，实际 %q 无法解析（git rev-list 没有输出）。请确认该提交存在，或重拆提交（对齐 xkitora 场景 D）后再跑", critSingleCommit, commit)
	}
}

// checkTrailer 跑守卫 5：把提交消息喂给 `git interpret-trailers --parse`，按该进程的
// 输出判定——「末段块 / 单行消息 / CRLF / 空白整族」口径全部交给它，本包不自行重实现
// 块判定与逐字差分（§20.4 行 5）。通过条件 = 输出中恰有一行等于 `Template-Upstream: yes`
// 且没有其它同键行：键大小写不符、值非 `yes`、同键在末段块内出现多次一律 fail-closed 拒。
func checkTrailer(repoDir, message string) error {
	parsed, err := runGit(repoDir, []byte(message), "interpret-trailers", "--parse")
	if err != nil {
		return fmt.Errorf("判定提交消息的 trailer 失败：%w", err)
	}
	hits := 0
	var offbeat []string
	for _, line := range splitLines(parsed) {
		if line == trailerLine {
			hits++
			continue
		}
		if isTrailerKeyLine(line) {
			offbeat = append(offbeat, line)
		}
	}
	if hits == 1 && len(offbeat) == 0 {
		return nil
	}
	actual := "末段 trailer 块里没有该行"
	switch {
	case len(offbeat) > 0:
		actual = fmt.Sprintf("末段块内出现非逐字的键或值（%s）", strings.Join(offbeat, "、"))
	case hits > 1:
		actual = "末段块内该行出现多次，语义多义"
	}
	return fmt.Errorf("提交消息不含合格的 trailer：判据 = %s，实际 %s。请重拆提交（对齐 xkitora 场景 D），把逐字行 `%s` 放进消息末段 trailer 块后重跑", critTrailer, actual, trailerLine)
}

// isTrailerKeyLine 判一行是否是同键行（键部分大小写不敏感，用于把「键/值非逐字」
// 与「完全没有该行」区分开；值不参与判定）。
func isTrailerKeyLine(line string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "template-upstream:")
}

// runGit 在 dir 下跑一条 git 命令（`git -C dir …`），返回未做任何裁剪的 stdout。
// stdin 非 nil 时接为该进程的标准输入（trailer 解析走这条）。
func runGit(dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// stderr 为空（进程根本没起来，如 git 不在 PATH）时不拼括号，否则用户会看到
		// 一对空括号「（）」。%w 保留错误链（errors.Is 可用）。
		suffix := ""
		if detail := firstLine(stderr.String()); detail != "" {
			suffix = "（" + detail + "）"
		}
		return nil, fmt.Errorf("跑 git %s 失败：%w%s", strings.Join(args, " "), err, suffix)
	}
	return stdout.Bytes(), nil
}

// splitNUL 按 NUL 切分 git 的 -z 输出，并丢掉末尾分隔符带来的空段。
func splitNUL(b []byte) []string {
	segs := strings.Split(string(b), "\x00")
	if n := len(segs); n > 0 && segs[n-1] == "" {
		segs = segs[:n-1]
	}
	return segs
}

// splitLines 按 LF 切分，丢掉末尾空行（`--parse` 输出每行以 LF 收尾）。
func splitLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// firstLine 取一段多行文本的首行（错误文案里只留一行 git 提示）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
