package template

import (
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/xbcio/xinit/internal/fsutil"
)

// implicitExcluded 与 render.implicitExcludes 保持一致。
//
// 这里重复一份而不是从 render 导入，是为了保持 template 包不反向依赖
// render —— 两份定义由同一条测试覆盖（TestTreeHashIgnoresGitDir）。
var implicitExcluded = []string{".git", ".xinit", "xinit.yml"}

// OpenDir 把一个本地目录当模板源，返回可遍历的 fs.FS 与树 hash。
//
// 树 hash 是 dir 源的「commit」：目录会被就地改动，没有稳定的版本标识，
// 只能对当前内容取指纹。
func OpenDir(dir string) (fs.FS, string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, "", fmt.Errorf("模板目录 %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("模板路径 %s 不是目录", dir)
	}

	hash, err := TreeHashOf(dir)
	if err != nil {
		return nil, "", err
	}
	return os.DirFS(dir), hash, nil
}

// TreeHashOf 计算一个目录的树 hash（规范 §5.4）。
//
// 按 **Src** 的字节序排序、写入 **Src**，不是 Dst —— Dst 依赖 vars 和
// 清单，用 Dst 会让同一个目录换个 vars 就得到不同的「版本锚点」，
// 而且算锚点之前还得先解析清单。
//
// 每条记录带分隔符与类型（见 fsutil.RenderTreeHash），否则
// a="ab", b="" 与 a="a", b="b" 会得到相同的 hash。
func TreeHashOf(dir string) (string, error) {
	fsys := os.DirFS(dir)

	var entries []fsutil.TreeEntry
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if isImplicitlyExcluded(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		kind := "regular"
		if d.Type()&fs.ModeSymlink != 0 {
			kind = "symlink"
		}

		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}

		entries = append(entries, fsutil.TreeEntry{
			Src:  path.Clean(p),
			Kind: kind,
			Exec: info.Mode()&0o111 != 0,
			Hash: fsutil.CompareHash(content, fsutil.IsBinary(content)),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("遍历模板目录: %w", err)
	}

	return fsutil.RenderTreeHash(entries), nil
}

func isImplicitlyExcluded(p string) bool {
	for _, ex := range implicitExcluded {
		if p == ex || len(p) > len(ex) && p[:len(ex)+1] == ex+"/" {
			return true
		}
	}
	return false
}
