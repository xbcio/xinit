package template

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCacheRoot 返回镜像缓存的根目录。
//
// 用 os.UserCacheDir 而不是项目内目录：镜像可在多个派生项目之间共享，
// 一个模板只拉一次。
func DefaultCacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("无法确定缓存目录: %w", err)
	}
	return filepath.Join(base, "xinit"), nil
}

// mirrorDirName 把来源路径映射到稳定的目录名。
func mirrorDirName(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// ErrMirrorMissing 表示本地镜像不存在，而调用方不允许首次克隆（status
// 默认路径：§11.4「默认不联网，只读本地镜像缓存」——不联网、不创建缓存根）。
var ErrMirrorMissing = errors.New("本地镜像未命中（该命令默认不联网、不创建缓存；请先运行 init/update 拉取镜像）")

// ensureMirror 确保镜像存在并更新到最新，返回镜像目录。
//
// allowClone=false（status 默认）时：镜像缺失直接返回 ErrMirrorMissing——
// 联网 clone 与创建缓存根目录都发生在该检查之后，因此两者都不会发生；
// 镜像存在时 fetch 照常更新（§11.4「--fetch 才更新缓存」）。
// allowClone=true（update/init）保持「缺失即首次 clone」的现状。
//
// 并发安全：用锁文件 + 重试一次。多进程同时 fetch 会撞 git 的 ref 锁。
func ensureMirror(source, cacheRoot string, fetch, allowClone bool) (string, error) {
	dir := filepath.Join(cacheRoot, mirrorDirName(source))

	if !allowClone {
		if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
			return "", ErrMirrorMissing // 在 MkdirAll 之前——缓存根也不创建
		}
	}

	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", fmt.Errorf("创建缓存目录: %w", err)
	}

	unlock, err := acquireMirrorLock(dir + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		// 尚未镜像（含并发清理导致镜像消失的竞态兜底）
		if !allowClone {
			return "", ErrMirrorMissing
		}
		if err := cloneMirror(source, dir); err != nil {
			// 克隆失败时清掉半成品，避免下次误判为已镜像
			_ = os.RemoveAll(dir)
			return "", err
		}
	}

	if fetch {
		if err := fetchMirror(dir); err != nil {
			// 联网失败不致命：已镜像的 commit 仍可读
			fmt.Fprintf(os.Stderr, "xinit: 更新模板镜像失败，使用本地缓存: %v\n", err)
		}
	}

	return dir, nil
}

func cloneMirror(source, dir string) error {
	_, err := runGit("", "clone", "--mirror", "--", source, dir)
	if err != nil {
		return fmt.Errorf("克隆模板仓库 %s: %w", source, err)
	}
	return nil
}

func fetchMirror(dir string) error {
	_, err := runGit(dir, "fetch", "--prune", "--tags", "origin")
	return err
}

// acquireMirrorLock 是一个简单的文件锁。
//
// O_CREATE|O_EXCL 独占创建（不引 flock(2)，免平台依赖）。已存在时看
// 时间戳：超过 10 分钟视为陈旧（进程被 kill 遗留的锁）并接管，否则报错。
// O_EXCL 唯一的残留风险就是崩溃遗留，这条陈旧接管路径正是为它准备的。
func acquireMirrorLock(path string) (func(), error) {
	const staleAfter = 10 * time.Minute

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("创建镜像锁 %s: %w", path, err)
		}

		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > staleAfter {
			// 陈旧锁：清掉再来一次
			_ = os.Remove(path)
			continue
		}
		return nil, fmt.Errorf(
			"模板镜像正被另一个 xinit 进程使用（锁文件 %s 存在）", path)
	}
	return nil, fmt.Errorf("无法获取模板镜像锁 %s（存在陈旧锁）", path)
}

// resolveCommit 把一个 revision 解析成 commit sha。
//
// 用 ^{commit} 后缀强制解引用注解 tag —— 否则 v1.0.0 这类 tag 会返回
// tag 对象自身的 sha，ls-tree 用不了。
func resolveCommit(repoDir, revision string) (string, error) {
	rev := revision
	if !strings.Contains(rev, "^{") {
		rev = rev + "^{commit}"
	}
	out, err := runGit(repoDir, "rev-parse", "--verify", "--end-of-options", rev)
	if err != nil {
		return "", fmt.Errorf("模板中不存在版本 %q: %w", revision, err)
	}
	return strings.TrimSpace(string(out)), nil
}
