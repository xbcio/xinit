package template

import (
	"fmt"
	"io/fs"
	"os"
)

// Kind 是模板来源的类型。
type Kind uint8

const (
	KindGit Kind = iota
	KindDir
)

func (k Kind) String() string {
	switch k {
	case KindGit:
		return "git"
	case KindDir:
		return "dir"
	}
	return "unknown"
}

// Source 是一个已就绪的模板源。
type Source struct {
	Kind     Kind
	FS       fs.FS
	Revision string // 用户给的引用名（tag / sha / 分支）；dir 源为空
	// Commit 两段式填充：git 源在 Open 时就定下（commit sha）；
	// dir 源的树 hash 要排除清单 exclude 命中的文件（规范 §5.4），
	// 而 exclude 要等清单编译完才有 —— 此处留空，调用方须在编译清单
	// 之后调用 TreeHashOf 补上。
	Commit string
	// DirRoot 是 dir 源的磁盘根目录，供清单编译后计算树 hash；git 源为空。
	DirRoot string
}

// Open 打开一个模板源。
//
// revision 为空时用默认分支的 HEAD；dir 源忽略 revision（也不看 fetch/allowClone）。
//
// fetch 与 allowClone 组合决定联网与 clone 行为（两个 bool 都显式传，
// 调用点即可看出该路径的承诺）：
//
//   - fetch=false ∧ allowClone=false → 完全离线：只读本地镜像，镜像缺失
//     返回 ErrMirrorMissing —— 不 clone、连缓存根都不创建（§11.4 同口径；
//     Open 的调用面中现为 init --offline 一例）；
//   - fetch=false ∧ allowClone=true → 镜像优先；缺失回退首次 clone
//     （功能性回退：update/adopt/restore/backflow 取已记录版本时要能用）；
//   - fetch=true → 已有镜像会更新到最新（联网失败不致命，警告后按本地缓存
//     继续）；镜像缺失时按 allowClone 定（init 默认 true = 首次 clone）。
func Open(src, revision, cacheRoot string, fetch, allowClone bool) (*Source, error) {
	// revision 允许为空（空 = 用默认分支的 HEAD）：为空时不能过
	// checkRefArg —— 它把空串一律当非法，那条规则是给必填引用名准备的。
	if revision != "" {
		if err := checkRefArg("revision", revision); err != nil {
			return nil, err
		}
	}

	info, statErr := os.Stat(src)
	if statErr == nil && info.IsDir() {
		if isGitRepo(src) || isBareRepo(src) {
			return openGit(src, revision, cacheRoot, fetch, allowClone)
		}
		return openDirSource(src)
	}
	if statErr == nil && !info.IsDir() {
		return nil, fmt.Errorf("模板路径 %s 不是目录", src)
	}

	// 本地不存在，当作 git URL
	return openGit(src, revision, cacheRoot, fetch, allowClone)
}

// IsDirSource 报告 src 是否会被 Open 当作 dir 源打开：存在的本地目录，
// 且既不是 git 仓库也不是裸仓（与 Open 的派发判据严格一致）。dir 源没有
// 版本概念（§5.4）——adopt 用它跳过「取最高 semver tag」的选版。
func IsDirSource(src string) bool {
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return false
	}
	return !isGitRepo(src) && !isBareRepo(src)
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(dir + "/.git")
	return err == nil
}

// isBareRepo 判断裸仓（有 objects/ 与 HEAD，但没有 .git/ 子目录）。
//
// 裸仓不能当 dir 源：它没有工作树，只有对象库 —— 必须走镜像。
func isBareRepo(dir string) bool {
	if isGitRepo(dir) {
		return false
	}
	if _, err := os.Stat(dir + "/objects"); err != nil {
		return false
	}
	if _, err := os.Stat(dir + "/HEAD"); err != nil {
		return false
	}
	return true
}

func openDirSource(dir string) (*Source, error) {
	fsys, err := OpenDir(dir)
	if err != nil {
		return nil, err
	}
	return &Source{Kind: KindDir, FS: fsys, DirRoot: dir}, nil
}

// repoDirFor 返回可执行 git 命令的仓库目录：本地 git 仓直接用，其余走镜像。
// allowClone 语义见 ensureMirror（status/check 默认与 init --offline 传
// false：不联网、不创建缓存）。
func repoDirFor(src, cacheRoot string, fetch, allowClone bool) (string, error) {
	if isGitRepo(src) {
		return src, nil
	}
	return ensureMirror(src, cacheRoot, fetch, allowClone)
}

// RepoDir 返回可执行 git 命令的仓库目录（本地 git 仓直用，其余走镜像）。
// 只读定位：不允许首次 clone、不创建缓存根（§18.5，与 status 同口径）。
func RepoDir(src, cacheRoot string, fetch bool) (string, error) {
	return repoDirFor(src, cacheRoot, fetch, false)
}

func openGit(src, revision, cacheRoot string, fetch, allowClone bool) (*Source, error) {
	// 镜像缺失是否允许首次 clone 由调用方定（见 Open doc 的三条组合）：
	// init 不带 --offline 与 update/adopt/restore/backflow 传 true；init
	// --offline 传 false（缺失即 ErrMirrorMissing）。
	repoDir, err := repoDirFor(src, cacheRoot, fetch, allowClone)
	if err != nil {
		return nil, err
	}

	if revision == "" {
		revision = "HEAD"
	}
	commit, err := resolveCommit(repoDir, revision)
	if err != nil {
		return nil, err
	}

	fsys, err := NewGitFS(repoDir, commit)
	if err != nil {
		return nil, err
	}
	return &Source{Kind: KindGit, FS: fsys, Revision: revision, Commit: commit}, nil
}
