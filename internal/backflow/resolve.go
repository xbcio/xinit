package backflow

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
	"github.com/xbcio/xinit/internal/render"
)

// defaultMaxCandidates 是候选枚举的默认防御上限（§20.7 规则 1 末句）。
//
// 候选是「每段至多一个替换点」的组合枚举，规模随段数与规则数相乘增长；
// 不设上限会让一个病态 dst（或病态清单）把解析拖成指数级。超限即**放弃
// 自动解析**（按零命中回退）——自动解析不必穷尽，`--map` 是权威出口。
const defaultMaxCandidates = 64

// Resolver 把新增文件的派生路径解析回模板态路径（§20.7）。
//
// 坐标系与调用方约定：
//   - Compiled = 记录 commit 的清单（T6 从记录 commit 读出 Parse+Compile）；
//   - Vars = state.Vars 原样（不重跑 EvalVars——与 restore/CheckConverged
//     的重建判据同源）；
//   - TreePaths = 记录 commit 的模板树全路径集（T6 由 template.ListTree 传入），
//     只被统一闸门读：候选/`--map` 目标**不得已存在**于其中，否则补丁会在
//     模板仓 "already exists" 处必败。
//
// 只服务 ClassNewFile：受管路径的 `--map` 救援归 ResolveDomain 的 flagMap
// （闸门方向相反：目标须在模板树内——§20.5 句 2 的「来源已不存在」出口）。
type Resolver struct {
	Compiled      *manifest.Compiled
	Vars          render.Vars
	TreePaths     map[string]bool
	MaxCandidates int // <= 0 取 defaultMaxCandidates
}

// ResolveNew 自动解析新增文件的模板态路径（§20.7 规则 1–3）。
//
// 候选集（有限、确定性序）：① dst 原样；② 逐条 compiled rename 规则，段内
// 含渲染后的 T_r（字母边界同正向判据）→ 段内替换回 from；③ 逐个变量，段内
// 精确等于或含 V_v → 替换回 `{{ .v }}`；④ 逐条声明 strip_suffix 的 render
// 规则枚举补后缀。②③ 每段至多一个替换点，②④ 之间组合枚举；总数超过
// MaxCandidates 即放弃自动解析（按零命中回退）。
//
// 校验（§20.7 规则 2）：候选 P 过正向重放 `render.Paths(P) == dst`（合成
// 单文件 FS，见 synthFS），且 P 不得已在模板树中（相撞拒——可能是映射臆测）。
// 正向管线在候选上报错（形状非法 / rename 替换值含 '/' / render 命中却不带
// strip_suffix 等）一律按「不是命中」处置：那说明该候选不是合法模板态路径，
// 而不是本函数的故障。
//
// 返回值：唯一命中 → 该 P 与空 cands；零/多命中 → 空 src 与全部**命中**
// （已过重放与相撞闸门，字节序排序、去重）——调用方据此警告不导出 + 提示
// `--map <Dst>=<Src>` 显式给出。error 仅内部错误（Compiled 缺席、rename.to
// 渲染失败等输入侧缺陷）。
func (r *Resolver) ResolveNew(dst string) (string, []string, error) {
	if r.Compiled == nil {
		return "", nil, fmt.Errorf(
			"Resolver.Compiled 不能为空：路径解析以记录 commit 的清单为坐标系（§20.7）")
	}

	cands, ok, err := r.candidates(dst)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, nil // 候选超限：放弃自动解析（零命中回退）
	}

	var hits []string
	seen := make(map[string]bool, len(cands))
	for _, p := range cands {
		if seen[p] {
			continue // 不同替换路径可能产出同一候选（如 from == to）
		}
		seen[p] = true
		if validateTemplatePath(p) != nil {
			// 替换值引入的非法形状（如值里含 '/' 变成新段）：候选根本到不了
			// 重放；先挡掉也保证 WalkDir 不会被病态路径搅乱。
			continue
		}
		if r.TreePaths[p] {
			continue // 相撞拒（§20.7 规则 2）
		}
		if r.replayMatches(p, dst) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil, nil
	}
	sort.Strings(hits)
	return "", hits, nil
}

// candidates 按 §20.7 规则 1 枚举候选路径。
//
// ok=false 表示候选总数超限（放弃自动解析）。枚举次序固定，是 cands 确定性
// 的一半（另一半是 hits 的字节序排序）：后缀选项空串在前、随后按 render
// 规则声明序去重；逐段选项恒等在前、rename 与变量按各自声明序。
func (r *Resolver) candidates(dst string) (paths []string, ok bool, err error) {
	renames, err := r.resolvedRenames()
	if err != nil {
		return nil, false, err
	}
	limit := r.maxCandidates()
	order := varOrder(r.Compiled, r.Vars)

	var out []string
	for _, suf := range r.stripSuffixes() {
		segs := strings.Split(dst+suf, "/")
		part := []string{""}
		for i, seg := range segs {
			opts := segmentOptions(seg, renames, r.Vars, order)
			if i == 0 {
				part = opts
			} else {
				next := make([]string, 0, len(part)*len(opts))
				for _, pre := range part {
					for _, o := range opts {
						next = append(next, pre+"/"+o)
					}
				}
				part = next
			}
			if len(part) > limit {
				return nil, false, nil
			}
		}
		out = append(out, part...)
		if len(out) > limit {
			return nil, false, nil
		}
	}
	return out, true, nil
}

func (r *Resolver) maxCandidates() int {
	if r.MaxCandidates > 0 {
		return r.MaxCandidates
	}
	return defaultMaxCandidates
}

// stripSuffixes 是补后缀候选（§20.7 规则④）用到的后缀集合：空串（恒等基）
// 在前，随后按 render 规则声明序去重。
//
// 不预先判断「dst 是否已带后缀」：stage 5 的 render 判定匹配的是**加后缀前**
// 的原 Src（render/paths.go 的 strip 点在其后），「先判命中再补后缀」会次序
// 循环——直接枚举、由重放裁决。
func (r *Resolver) stripSuffixes() []string {
	out := []string{""}
	seen := map[string]bool{"": true}
	for _, rr := range r.Compiled.Render {
		s := rr.Spec.StripSuffix
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// resolvedRename 是替换值已渲染好的 rename 规则。T_r 的口径与正向
// resolveRenames 一致（render 只读、零改动，故这里独立重实现同判据）。
type resolvedRename struct {
	from, to string
}

// resolvedRenames 渲染全部 rename 的 to。
//
// 与正向同口径：渲染失败必须在候选枚举之前报出来——静默零命中会把「to 引用
// 未声明变量」这类清单缺陷伪装成「无候选」。
func (r *Resolver) resolvedRenames() ([]resolvedRename, error) {
	out := make([]resolvedRename, 0, len(r.Compiled.Rename))
	for i, rn := range r.Compiled.Rename {
		to := rn.Spec.To
		if strings.Contains(to, "{{") {
			rendered, err := render.RenderTemplate(
				fmt.Sprintf("rename[%d].to", i), []byte(to), r.Vars)
			if err != nil {
				return nil, fmt.Errorf("rename[%d].to: %w", i, err)
			}
			to = string(rendered)
		}
		out = append(out, resolvedRename{from: rn.Spec.From, to: to})
	}
	return out, nil
}

// varOrder 返回变量的确定性遍历序：清单声明序在前（default 可以引用先前
// 变量，声明序是清单的规范序），再按字节序补上声明之外的键（防御：调用方
// 手工构造 Vars 时既不丢候选、也不让结果随 map 迭代序抖动）。
func varOrder(c *manifest.Compiled, v render.Vars) []string {
	out := make([]string, 0, len(v))
	seen := make(map[string]bool, len(v))
	if c.Raw != nil {
		for _, spec := range c.Raw.Vars {
			if _, ok := v[spec.Name]; !ok || seen[spec.Name] {
				continue
			}
			seen[spec.Name] = true
			out = append(out, spec.Name)
		}
	}
	extra := make([]string, 0, len(v)-len(out))
	for k := range v {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// segmentOptions 枚举单个路径段的反向替换点（§20.7 规则 1：每段**至多一个**
// 替换点）。次序固定（恒等 → rename 声明序 → 变量声明序）。
//
// 变量值为空串时不产出候选：空串替换回 `{{ .v }}` 会把每个段都变成占位符，
// 而正向渲染回空串的段本来就会被 validateDstPath 拒绝（无意义候选只会白占
// 上限配额）。
func segmentOptions(seg string, renames []resolvedRename, vars render.Vars, order []string) []string {
	opts := []string{seg}
	for _, rn := range renames {
		if out, ok := reverseToken(seg, rn.to, rn.from); ok {
			opts = append(opts, out)
		}
	}
	for _, name := range order {
		val := vars[name]
		if val == "" || !strings.Contains(seg, val) {
			continue
		}
		opts = append(opts, strings.ReplaceAll(seg, val, "{{ ."+name+" }}"))
	}
	return opts
}

// reverseToken 把 seg 里带字母边界的 to 记号替换回 from（§20.7 规则②的
// 「字母边界同正向判据」），返回 (结果, 是否发生替换)。
//
// 与正向 render.replaceToken 同判据（那边是 from → to，这里方向相反）；
// 逐字节前进，命中点两侧都不是 ASCII 字母才替换，未命中的字节原样搬移
// （含尾部剩余字节）。
func reverseToken(seg, to, from string) (string, bool) {
	if to == "" || !strings.Contains(seg, to) {
		return seg, false
	}
	var b strings.Builder
	changed := false
	i := 0
	for i+len(to) <= len(seg) {
		if seg[i:i+len(to)] == to &&
			(i == 0 || !isASCIILetter(seg[i-1])) &&
			(i+len(to) == len(seg) || !isASCIILetter(seg[i+len(to)])) {
			b.WriteString(from)
			changed = true
			i += len(to)
			continue
		}
		b.WriteByte(seg[i])
		i++
	}
	b.WriteString(seg[i:])
	if !changed {
		return seg, false
	}
	return b.String(), true
}

// isASCIILetter 与 render §7.3 的已声明取舍一致：只认 ASCII 字母
// （'é' 这类非 ASCII 字母会被当作合法邻居）。
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ApplyMap 是 `--map <Dst>=<Src>` 的覆盖面（§20.7 规则 4）：跳过自动解析
// 直接采用 Src，但仍过**统一闸门** —— 路径合法性 + P 不得已存在于记录
// commit 的模板树（与规则 2 同闸）。自动解析不必穷尽，`--map` 是权威出口。
//
// 受管路径的 `--map` 救援不归这里（归 ResolveDomain 的 flagMap，闸门方向
// 相反：目标须在模板树内）；本函数只服务 ClassNewFile。
func (r *Resolver) ApplyMap(dst, src string) (string, error) {
	if err := validateTemplatePath(src); err != nil {
		return "", fmt.Errorf(
			"--map %s=%s：模板路径 %q 形状非法（%v）；模板态路径必须是 / 分隔的相对路径——不得含空段、`.` 或 `..` 段，不得是绝对路径",
			dst, src, src, err)
	}
	if r.TreePaths[src] {
		return "", fmt.Errorf(
			"--map %s=%s：该路径在模板中已存在，不是新增文件（模板仓已占用 %s，补丁会在 `git am` 的 \"already exists\" 处必败）；请换一个尚未被占用的模板路径，或人工判断",
			dst, src, src)
	}
	return src, nil
}

// validateTemplatePath 校验模板态路径的形状（§20.7 规则 4 的「路径合法性」）。
//
// 判据即 §22.4 的单点谓词 fsutil.CheckRelPath（非空 / 非绝对 / 任一段不含
// `\` 与 `:` / 无空段与 `.`、`..` 段 / Clean 不变），与正向
// render.validateDstPath 同口径，但作用对象是**模板侧**路径：`--map` 的显式值
// 与自动候选。`..` 段必须挡在这里 —— 补丁路径最终写进模板仓，穿越段会把目标
// 带出仓库（fail-closed）。
func validateTemplatePath(p string) error {
	return fsutil.CheckRelPath(p)
}

// replayMatches 用合成单文件 FS 做正向重放，判定候选 P 是否恰好渲染为 dst
// （§20.7 规则 2：输入输出方向 = 「候选 P → Dst」）。
//
// 正向管线在候选上报错一律按「不是命中」处置（见 ResolveNew 的说明）。
func (r *Resolver) replayMatches(p, dst string) bool {
	entries, _, err := render.Paths(synthFS{path: p}, r.Compiled, r.Vars)
	if err != nil {
		return false
	}
	return len(entries) == 1 && entries[0].Src == p && entries[0].Dst == dst
}

// synthFileContent 是合成 FS 里唯一文件的内容。路径重放只关心路径（内容仅
// 参与 Binary 判定），取一个最小文本即可。
var synthFileContent = []byte("x\n")

// synthFS 是「只含一个文件」的合成只读 fs.FS，承载候选的正向重放。
//
// 为什么必须自建：候选 P 按闸门定义就不该存在于模板树（相撞拒），借模板树
// 本体重放无从谈起（§20.8）。
//
// 为什么是自写小实现而不是 os.DirFS(临时目录)：
//   - 重放零落盘（`--dry-run` 的「不写产物」在这里收紧为「什么都不写」）；
//   - 候选形状在触碰文件系统之前不会变成真实路径（没有 `..` 的写盘逃逸面）；
//   - 没有临时目录的生命周期与清理面。
//
// §20.8 明确允许这两种实现；testing/fstest 是测试专属包，禁进生产二进制。
//
// 只实现 render.Paths 实际触到的面：fs.Stat（回退到 Open+Stat）、fs.ReadDir、
// fs.ReadFile（回退到 Open+ReadAll）、DirEntry.Info。目录按 path 的段前缀
// 虚拟出来，条目名逐段给出（不含 '/'，符合 io/fs 约定）。
type synthFS struct{ path string }

// dirChild 报告 name 是否是合成树里的目录，并给出其直接子项名。
// 目录 = 根（"."）与文件路径的真前缀（按段边界）。
func (s synthFS) dirChild(name string) (string, bool) {
	if s.path == "" {
		return "", false
	}
	segs := strings.Split(s.path, "/")
	if name == "." {
		return segs[0], true
	}
	for i := 0; i+1 < len(segs); i++ {
		if name == strings.Join(segs[:i+1], "/") {
			return segs[i+1], true
		}
	}
	return "", false
}

func (s synthFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if s.path != "" && name == s.path {
		return &synthFile{name: path.Base(name), data: synthFileContent}, nil
	}
	if child, ok := s.dirChild(name); ok && child != "" {
		return &synthDir{name: path.Base(name)}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (s synthFS) ReadDir(name string) ([]fs.DirEntry, error) {
	child, ok := s.dirChild(name)
	if !ok || child == "" {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	full := child
	if name != "." {
		full = name + "/" + child
	}
	return []fs.DirEntry{synthEntry{name: child, dir: full != s.path}}, nil
}

// synthEntry 是合成 FS 的 DirEntry。
type synthEntry struct {
	name string
	dir  bool
}

func (e synthEntry) Name() string { return e.name }
func (e synthEntry) IsDir() bool  { return e.dir }

func (e synthEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0 // 普通文件：symlink 检查在正向管线里，合成 FS 永不产出链接
}

func (e synthEntry) Info() (fs.FileInfo, error) {
	return synthInfo{name: e.name, dir: e.dir}, nil
}

// synthInfo 是合成 FS 的 FileInfo。Mode 固定只读：exec 位不进路径判据
// （Entry.Exec 只影响内容面），ModTime 取零值（重放结果不得依赖时钟）。
type synthInfo struct {
	name string
	dir  bool
}

func (i synthInfo) Name() string { return i.name }

func (i synthInfo) Size() int64 {
	if i.dir {
		return 0
	}
	return int64(len(synthFileContent))
}

func (i synthInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

func (i synthInfo) ModTime() time.Time { return time.Time{} }
func (i synthInfo) IsDir() bool        { return i.dir }
func (i synthInfo) Sys() any           { return nil }

// synthFile 是合成 FS 的文件句柄（只读；fs.ReadFile 的回退路径会 Read 到 EOF）。
type synthFile struct {
	name string
	data []byte
	off  int
}

func (f *synthFile) Stat() (fs.FileInfo, error) { return synthInfo{name: f.name}, nil }

func (f *synthFile) Read(p []byte) (int, error) {
	if f.off >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += n
	return n, nil
}

func (f *synthFile) Close() error { return nil }

// synthDir 是合成 FS 的目录句柄（fs.Stat 的回退路径需要它；列目录走
// synthFS.ReadDir —— fs.ReadDir 优先用 ReadDirFS 面）。
type synthDir struct{ name string }

func (d *synthDir) Stat() (fs.FileInfo, error) {
	return synthInfo{name: d.name, dir: true}, nil
}

func (d *synthDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}

func (d *synthDir) Close() error { return nil }
