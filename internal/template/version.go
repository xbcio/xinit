package template

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// SemverTags 过滤出能解析且非预发布的 tag，按 (版本序, 名字节序) 升序排序。
//
// 规范 §11.3：
//   - x/mod/semver 要求 `v` 前缀（实测 "1.2.3" 与 "V1.2.3" 均无效）；
//   - 排除预发布版的判据是 Prerelease(tag) != ""，不是「含 -」——
//     实测 v1.0.0+build-5 含 '-' 但 Prerelease 为空，它不是预发布版；
//   - 并列（Compare==0，如 "v1.2" vs "v1.2.0"）必须写死：按名字节序。
func SemverTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if semver.IsValid(t) && semver.Prerelease(t) == "" {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if c := semver.Compare(out[i], out[j]); c != 0 {
			return c < 0
		}
		return out[i] < out[j]
	})
	return out
}

// ListTags 列出模板仓库的全部 tag 短名（不排序；过滤交给 SemverTags）。
// allowClone=false 时镜像缺失返回 ErrMirrorMissing（status 默认：不联网、
// 不创建缓存，§11.4；评审 M-1/F2）。
func ListTags(src, cacheRoot string, fetch, allowClone bool) ([]string, error) {
	repoDir, err := repoDirFor(src, cacheRoot, fetch, allowClone)
	if err != nil {
		return nil, err
	}
	// lstrip=2 而非 refname:short：分支与 tag 同名时 short 会回退成 "tags/<name>"，
	// 静默丢掉该 tag（评审 Task 2 ；不变量 9）。
	out, err := runGit(repoDir, "for-each-ref", "--format=%(refname:lstrip=2)", "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("列出模板 tag: %w", err)
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			tags = append(tags, line)
		}
	}
	return tags, nil
}

// SelectRevision 按规范 §11.3 选出默认目标 revision（未显式 --revision 时）。
//
// 语义（逐条对应 §11.3）：
//   - 候选 = 可解析、非预发布的 tag；
//   - 要求比当前 revision 更高（Compare）→ 取最高，并列按名字节序最大；
//   - F6：没有更高的 tag、而当前 revision 本身可解析 → 取当前继续（幂等
//     第二次调用、冲突解决后的同版本重跑都依赖它）；
//   - 当前 revision 不可解析（sha / HEAD / 分支名）→ 报错要求显式指定
//     （R8：semver 比较不适用就不能猜，猜 = 可能静默降级）；
//   - 仅当候选集为空（模板没有任何 semver tag）→ 按 §11.3 第一条报错。
//
// `--accept` 没有专属的选版逻辑（§10.3）：重跑（含 update --accept）照常
// 经本函数按 §11.3 正常选版。
func SelectRevision(src, cacheRoot string, fetch bool, current string) (string, error) {
	tags, err := ListTags(src, cacheRoot, fetch, true) // 选版只在 update 路径，允许首次 clone
	if err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("模板里没有任何 tag；无法自动选择版本，请显式指定 --revision")
	}
	cands := SemverTags(tags)
	if len(cands) == 0 {
		return "", fmt.Errorf("未找到符合 vX.Y.Z 格式的 tag；请显式指定 --revision")
	}
	if !semver.IsValid(current) {
		return "", fmt.Errorf(
			"当前记录的 revision %q 不是 vX.Y.Z 格式，无法做版本比较；请显式指定 --revision（此后默认模式即可继续工作）",
			current)
	}
	best := ""
	for _, t := range cands {
		if semver.Compare(t, current) > 0 {
			best = t // cands 已按 (版本序, 名字节序) 升序，最后一个满足者即答案
		}
	}
	if best == "" {
		return current, nil
	}
	return best, nil
}

// IsAncestor 报告 older 是否是 newer 的祖先（git merge-base --is-ancestor）。
//
// 退出码 0 = 是，1 = 不是；其余（对象不存在等）返回错误。用于 §11.3
// 显式 --revision 的降级警告：不是后代 → 警告但不阻止。
func IsAncestor(src, cacheRoot string, fetch bool, older, newer string) (bool, error) {
	repoDir, err := repoDirFor(src, cacheRoot, fetch, true) // 只在 update 的降级警告里用
	if err != nil {
		return false, err
	}
	for _, r := range []string{older, newer} {
		if err := checkRefArg("rev", r); err != nil {
			return false, err
		}
	}
	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor",
		"--end-of-options", older, newer)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w\n%s", err, strings.TrimSpace(stderr.String()))
}
