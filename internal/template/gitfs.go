package template

import (
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// gitFS 把一个 git commit 的树暴露成只读 fs.FS。
//
// blob 内容是懒加载的：首次访问时起一个 cat-file --batch 进程把该 commit
// 的全部 blob 一次性读进来。模板通常只有几 MB。
type gitFS struct {
	commit  string
	repoDir string

	files map[string]TreeEntry // 精确路径 → 条目
	dirs  map[string][]string  // 目录路径 → 直接子项名字（已排序）
	blobs map[string][]byte    // blobHash → 内容
	ready bool
}

// NewGitFS 构造一个 git 树的 fs.FS。
func NewGitFS(repoDir, commit string) (fs.FS, error) {
	entries, err := ListTree(repoDir, commit)
	if err != nil {
		return nil, err
	}

	g := &gitFS{
		commit:  commit,
		repoDir: repoDir,
		files:   make(map[string]TreeEntry, len(entries)),
		dirs:    map[string][]string{".": {}},
		blobs:   map[string][]byte{},
	}

	for _, e := range entries {
		g.files[e.Path] = e
		g.addToParentDirs(e.Path)
	}

	// 目录的排序在合成完成后再做一次，保证确定性
	for d := range g.dirs {
		sort.Strings(g.dirs[d])
	}

	return g, nil
}

// addToParentDirs 从路径前缀合成目录树 —— ls-tree -r 不输出目录条目。
//
// 必须逐级补齐每一层前缀目录、并把每一层挂到**各自的**父目录上。
// 把文件基名直接加进根目录是错的：deep/er/b.go 会让 ReadDir(".")
// 列出 b.go，而 Open("b.go") 并不存在，fs.WalkDir 于是直接失败。
func (g *gitFS) addToParentDirs(p string) {
	segs := strings.Split(p, "/")

	cur := ""
	for i := 0; i < len(segs)-1; i++ {
		if cur == "" {
			cur = segs[i]
		} else {
			cur = cur + "/" + segs[i]
		}
		if _, ok := g.dirs[cur]; !ok {
			g.dirs[cur] = []string{}
		}
		g.dirs[parentOf(cur)] = appendUnique(g.dirs[parentOf(cur)], lastSeg(cur))
	}

	// 文件本身挂到它真正的父目录（顶层文件即 "."）
	g.dirs[parentOf(p)] = appendUnique(g.dirs[parentOf(p)], lastSeg(p))
}

func parentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "."
	}
	return p[:i]
}

func lastSeg(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func appendUnique(list []string, s string) []string {
	for _, e := range list {
		if e == s {
			return list
		}
	}
	return append(list, s)
}

func (g *gitFS) loadBlobs() error {
	if g.ready {
		return nil
	}
	hashes := make([]string, 0, len(g.files))
	for _, e := range g.files {
		hashes = append(hashes, e.BlobHash)
	}
	blobs, err := ReadBlobs(g.repoDir, hashes)
	if err != nil {
		return err
	}
	g.blobs = blobs
	g.ready = true
	return nil
}

// Open 实现 fs.FS。
func (g *gitFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	if name == "." {
		return &gitDirFile{fs: g, name: ".", entries: g.dirs["."]}, nil
	}

	if e, ok := g.files[name]; ok {
		if err := g.loadBlobs(); err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		content, ok := g.blobs[e.BlobHash]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		return &gitFile{name: name, info: gitFileInfo{name: lastSeg(name), entry: e}, content: content}, nil
	}

	if children, ok := g.dirs[name]; ok {
		return &gitDirFile{fs: g, name: name, entries: children}, nil
	}

	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadDir 实现 fs.ReadDirFS —— fs.WalkDir 依赖它。
func (g *gitFS) ReadDir(name string) ([]fs.DirEntry, error) {
	children, ok := g.dirs[name]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(children))
	for _, child := range children {
		childPath := child
		if name != "." {
			childPath = name + "/" + child
		}
		if e, ok := g.files[childPath]; ok {
			out = append(out, gitDirEntry{info: gitFileInfo{name: child, entry: e}})
			continue
		}
		out = append(out, gitDirEntry{info: gitFileInfo{name: child, isDir: true}})
	}
	return out, nil
}

type gitFile struct {
	name    string
	info    fs.FileInfo
	content []byte
	offset  int
}

func (f *gitFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *gitFile) Read(p []byte) (int, error) {
	if f.offset >= len(f.content) {
		return 0, io.EOF
	}
	n := copy(p, f.content[f.offset:])
	f.offset += n
	return n, nil
}

func (f *gitFile) Close() error { return nil }

type gitDirFile struct {
	fs      *gitFS
	name    string
	entries []string
	offset  int
}

func (f *gitDirFile) Stat() (fs.FileInfo, error) {
	return gitFileInfo{name: lastSeg(f.name), isDir: true}, nil
}

func (f *gitDirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: f.name, Err: errors.New("是一个目录")}
}

func (f *gitDirFile) Close() error { return nil }

func (f *gitDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	all, err := f.fs.ReadDir(f.name)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return all, nil
	}
	if f.offset >= len(all) {
		return nil, io.EOF
	}
	end := f.offset + n
	if end > len(all) {
		end = len(all)
	}
	out := all[f.offset:end]
	f.offset = end
	return out, nil
}

type gitFileInfo struct {
	name  string
	entry TreeEntry
	isDir bool
}

func (i gitFileInfo) Name() string { return i.name }
func (i gitFileInfo) Size() int64  { return 0 }
func (i gitFileInfo) Mode() fs.FileMode {
	if i.isDir {
		return fs.ModeDir | 0o755
	}
	// symlink 必须报 ModeSymlink，好让 render 的单一检查拒绝它
	if i.entry.Symlink {
		return fs.ModeSymlink | 0o777
	}
	return i.entry.Mode
}
func (i gitFileInfo) ModTime() time.Time { return time.Time{} }
func (i gitFileInfo) IsDir() bool        { return i.isDir }
func (i gitFileInfo) Sys() any           { return nil }

type gitDirEntry struct{ info gitFileInfo }

func (e gitDirEntry) Name() string               { return e.info.Name() }
func (e gitDirEntry) IsDir() bool                { return e.info.IsDir() }
func (e gitDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e gitDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }
