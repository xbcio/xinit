package template

import (
	"fmt"
	"strings"
	"unicode"
)

// Relation 描述「项目记录的版本」(from) 与「目标版本」(to) 在模板历史上的位置关系。
type Relation int

const (
	// RelSame：from 与 to 是同一个 commit。
	RelSame Relation = iota
	// RelAhead：to 在 from 之后（from 是 to 的祖先）——正常的升级方向，有分段可列。
	RelAhead
	// RelBehind：to 在 from 之前（to 是 from 的祖先）——回退目标，不列分段。
	RelBehind
	// RelDiverged：两者不在同一条历史线上，无法给出「之间」的提交。
	RelDiverged
)

// ChangePoint 是 changelog 的一段：从上一个点（不含）到本点（含）之间的提交标题。
type ChangePoint struct {
	Name     string   // 稳定 semver tag 名；末段若目标不是 tag 则为用户给的原文
	Commit   string   // 本点的 commit sha
	Subjects []string // 本段独有的提交标题，新→旧；已剥离控制字符；不含 merge 提交
}

// ChangelogResult 是 BuildChangelog 的结果。
type ChangelogResult struct {
	FromCommit string
	ToName     string // 目标的显示名：显式给出的原文，或默认选出的最高稳定 tag
	ToCommit   string
	Relation   Relation
	Points     []ChangePoint // 按历史先后（祖先在前）；Relation != RelAhead 时为空
}

// BuildChangelog 计算 fromCommit（项目记录的 commit）到 toRef 之间的 changelog。
//
// toRef 为空 ⇒ 取最高的稳定 semver tag（不含预发布、不含未打 tag 的 HEAD）；
// 没有任何稳定 tag 则报错并提示 --revision。
//
// 只读：镜像缺失返回 ErrMirrorMissing，不 clone、不创建缓存根（与 status 同口径，
// 即 RepoDir 的口径）；fetch=true 才更新已有镜像。
//
// 分段规则：from（不含）到 to（含）之间的每个稳定 semver tag 各成一段，按历史
// 先后（拓扑序，祖先在前）排列——不按版本号，维护分支上的补丁 tag、版本号与历史
// 位置不一致的 tag 也各归其位。每个提交只归入**第一个包含它的段**（即最早发布它的
// 版本）：段内是该 tag 可达、但 from 与前面各段都不可达的非 merge 提交标题，所以
// 任何提交都不会在两段里重复出现。预发布 tag 不单列，其提交并入下一个稳定段；同一
// commit 上的多个稳定 tag 合并为一段，取版本序靠后的名字；to 本身若不是稳定 tag
// （HEAD、sha、分支名），则以 toRef 原文追加末段。只有 tag 打在 merge 提交上、且
// 它带进来的提交都已归入前面的段时，该段才会没有标题。
func BuildChangelog(src, cacheRoot string, fetch bool, fromCommit, toRef string) (*ChangelogResult, error) {
	if err := checkRefArg("from", fromCommit); err != nil {
		return nil, err
	}
	if toRef != "" {
		if err := checkRefArg("revision", toRef); err != nil {
			return nil, err
		}
	}
	repoDir, err := RepoDir(src, cacheRoot, fetch)
	if err != nil {
		return nil, err
	}
	from, err := resolveCommit(repoDir, fromCommit)
	if err != nil {
		return nil, fmt.Errorf("项目记录的版本在模板历史中不存在（模板历史可能被改写过）: %w", err)
	}

	tags, err := listTagsAt(repoDir)
	if err != nil {
		return nil, err
	}
	stable := SemverTags(tags)

	toName := toRef
	if toName == "" {
		if len(stable) == 0 {
			return nil, fmt.Errorf("模板里没有任何符合 vX.Y.Z 格式的稳定 tag；请显式指定 --revision")
		}
		toName = stable[len(stable)-1]
	}
	to, err := resolveCommit(repoDir, toName)
	if err != nil {
		return nil, err
	}

	res := &ChangelogResult{FromCommit: from, ToName: toName, ToCommit: to}
	if from == to {
		res.Relation = RelSame
		return res, nil
	}
	ahead, err := isAncestorAt(repoDir, from, to)
	if err != nil {
		return nil, err
	}
	if !ahead {
		behind, err := isAncestorAt(repoDir, to, from)
		if err != nil {
			return nil, err
		}
		res.Relation = RelDiverged
		if behind {
			res.Relation = RelBehind
		}
		return res, nil
	}
	res.Relation = RelAhead

	// from（不含）到 to（含）之间的稳定 tag：对 to 可达、对 from 不可达。
	// --no-merged=from 同时排除指向 from 本身与 from 之前的祖先 tag。
	out, err := runGit(repoDir, "for-each-ref", "--format=%(refname:lstrip=2)",
		"--merged="+to, "--no-merged="+from, "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("列出版本区间内的 tag: %w", err)
	}
	inRange := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			inRange[line] = true
		}
	}
	var names []string
	for _, name := range stable { // 已按 (版本序, 名字节序) 升序
		if inRange[name] {
			names = append(names, name)
		}
	}
	commits, err := peelToCommits(repoDir, names)
	if err != nil {
		return nil, err
	}
	tagAt := map[string]string{} // commit → 段名
	for i, name := range names {
		tagAt[commits[i]] = name // 同一 commit 的并列 tag：升序遍历，留下序上靠后的名字
	}

	// 区间内全部提交按拓扑序排开（祖先在前）。to 是区间里所有提交的后代，必在最后。
	order, err := runGit(repoDir, "rev-list", "--topo-order", "--reverse",
		"--end-of-options", from+".."+to)
	if err != nil {
		return nil, fmt.Errorf("排列版本区间内的提交: %w", err)
	}
	var points []ChangePoint
	for _, c := range strings.Fields(string(order)) {
		if name, ok := tagAt[c]; ok {
			points = append(points, ChangePoint{Name: name, Commit: c})
		}
	}
	if n := len(points); n == 0 || points[n-1].Commit != to {
		points = append(points, ChangePoint{Name: toName, Commit: to})
	} else if toName != points[n-1].Name {
		points[n-1].Name = toName // 用户点名的目标优先于并列 tag 的名字
	}

	// 每段排除 from 与前面各段可达的提交：一个提交只归入第一个包含它的段。
	exclude := []string{"^" + from}
	for i := range points {
		subjects, err := commitSubjects(repoDir, points[i].Commit, exclude)
		if err != nil {
			return nil, err
		}
		points[i].Subjects = subjects
		exclude = append(exclude, "^"+points[i].Commit)
	}
	res.Points = points
	return res, nil
}

// peelToCommits 把 tag 名逐个解引用到 commit（含注解 tag、嵌套 tag），一次 git 调用。
// 返回值与 names 一一对应。
func peelToCommits(repoDir string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var in strings.Builder
	for _, name := range names {
		in.WriteString("refs/tags/")
		in.WriteString(name)
		in.WriteString("^{commit}\n")
	}
	out, err := runGitStdin(repoDir, in.String(), "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return nil, fmt.Errorf("解析版本区间内的 tag: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != len(names) {
		return nil, fmt.Errorf("解析版本区间内的 tag: 期望 %d 行，得到 %d 行", len(names), len(lines))
	}
	for i, line := range lines {
		// 成功行只有 sha；失败行形如 "<输入> missing"（例如 tag 不指向 commit）
		if strings.ContainsRune(line, ' ') {
			return nil, fmt.Errorf("tag %q 无法解析到 commit: %s", names[i], line)
		}
	}
	return lines, nil
}

// commitSubjects 返回 tip 可达、exclude（"^<sha>" 形式）均不可达的非 merge 提交标题，
// 新→旧。参数都是已解析的完整 sha，--end-of-options 之后不会被当成选项。
func commitSubjects(repoDir, tip string, exclude []string) ([]string, error) {
	args := append([]string{"log", "--no-merges", "--no-show-signature",
		"--format=%s", "--end-of-options", tip}, exclude...)
	out, err := runGit(repoDir, args...)
	if err != nil {
		return nil, fmt.Errorf("读取提交标题: %w", err)
	}
	var subjects []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = sanitizeLine(line); line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects, nil
}

// sanitizeLine 剥离控制字符（含 ESC、BEL、DEL、C1）与 Unicode 双向覆盖/隔离字符。
//
// 提交标题来自模板仓库（可能是第三方），原样打到终端会带来转义序列注入与文字
// 欺骗。**刻意保留**的不可见字符及其原因（评审 O-1，勿误当漏项）：
//   - U+200B（ZWSP）、U+FEFF（BOM）、U+2028/U+2029（行/段分隔）、U+206A–U+206F
//     （弃用的 bidi 控制）：都不含 ESC/C1，不构成转义注入，且可能合法出现在
//     真实标题里；剥掉它们反而会改动第三方提交的原文。
//   - ZWJ（U+200D）等格式字符：emoji 序列依赖它。
//
// tag 名不需要单独剥离：稳定 tag 过 semver.IsValid 门禁（不可能含控制字符）；
// 用户自给的 --revision 原文只回显给用户自己。该链是隐式的，改动本函数或
// BuildChangelog 的取名路径时别破坏它（评审 O-2）。
func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return -1
		case r >= 0x202A && r <= 0x202E, // LRE RLE PDF LRO RLO
			r >= 0x2066 && r <= 0x2069,            // LRI RLI FSI PDI
			r == 0x200E, r == 0x200F, r == 0x061C: // LRM RLM ALM
			return -1
		}
		return r
	}, s)
}
