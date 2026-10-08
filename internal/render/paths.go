package render

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
)

// implicitExcludes 无论清单怎么写都永不进入派生项目，也不参与树 hash。
//
// dir 型模板通常就是 git 工作副本，漏掉 .git/ 会把模板仓整个复制进
// 派生项目，并让树 hash 随任何 git 操作变化。
var implicitExcludes = []string{".git", ".xinit", "xinit.yml"}

// EvalVars 按声明顺序求值变量。
//
// 顺序是关键：default 可以引用先前声明的变量，Go 的 map 不保序，
// 所以清单里的 vars 必须是序列。
//
// provided 里没有、且没有 default 的变量取空串（不是缺席）—— 缺席会让
// {{ if .feature }} 直接报错。
func EvalVars(c *manifest.Compiled, provided map[string]string) (Vars, error) {
	vars := make(Vars, len(c.Raw.Vars))

	for _, spec := range c.Raw.Vars {
		val, ok := provided[spec.Name]
		if !ok {
			// 只有 default 走模板渲染（规范 §3.1）。--config 给的值是
			// 字面量：用户写 "{{ .a }}" 时不该被静默改写，"lit{{" 也不该
			// 报错（更不该把错误归咎于 default）。
			val = spec.Default
			if strings.Contains(val, "{{") {
				rendered, err := RenderTemplate("vars."+spec.Name, []byte(val), vars)
				if err != nil {
					return nil, fmt.Errorf("vars.%s.default: %w", spec.Name, err)
				}
				val = string(rendered)
			}
		}

		if spec.Required && val == "" {
			return nil, fmt.Errorf(
				"变量 %q 是必填项，未提供且无默认值；请用 --config 指定", spec.Name)
		}

		if spec.Pattern != "" && val != "" {
			re, err := regexp.Compile(spec.Pattern)
			if err != nil {
				return nil, fmt.Errorf("vars.%s.pattern: %w", spec.Name, err)
			}
			if !re.MatchString(val) {
				return nil, fmt.Errorf(
					"变量 %q 的值 %q 不匹配 pattern %q", spec.Name, val, spec.Pattern)
			}
		}

		vars[spec.Name] = val
	}

	// 配置里给了清单未声明的变量：静默忽略会让用户以为生效了
	for k := range provided {
		if _, ok := vars[k]; !ok {
			return nil, fmt.Errorf("--config 里出现了清单未声明的变量 %q", k)
		}
	}

	return vars, nil
}

// Paths 走路径管线（规范 §7.1 阶段 1–8），产出全部 Entry。
//
// 会读内容以判定 Binary，但只保留元数据，不把内容留在内存里。
//
// 阶段顺序中两个易错点：
//   - 路径 vars 渲染在 rename **之前**；
//   - render[].paths 判定在 strip_suffix **之前** —— 否则 **/*.tmpl
//     永远匹配不到。
func Paths(fsys fs.FS, c *manifest.Compiled, v Vars) ([]Entry, []Warning, error) {
	var entries []Entry
	var warnings []Warning

	// rename 的替换值在这里就渲染好：渲染失败必须在写任何文件之前报出来
	renames, err := resolveRenames(c, v)
	if err != nil {
		return nil, nil, err
	}

	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.IsDir() {
			// 隐式排除与 exclude 都要整树跳过：规范 §3.2 —— 目录被排除后
			// 其下的 ! 无法重新包含（gitignore 语义）。
			if isImplicitExcluded(p) {
				return fs.SkipDir
			}
			if c.Exclude.Match(p) {
				return fs.SkipDir
			}
			return nil
		}

		// io/fs 的约定：WalkDir 给出的路径一律以 '/' 分隔，与宿主平台无关，
		// 所以这里直接用，不做任何分隔符转换（filepath 只出现在 IO 边界）
		src := p

		// 排除判定在 symlink 检查**之前**：被排除的路径根本不进入派生项目，
		// 也就没有「跟随链接泄漏内容」可言。且 node_modules/.bin 这类目录
		// 全是 symlink —— 让它们中止整个运行是不可接受的。
		if isImplicitExcluded(src) {
			return nil
		}
		if c.Exclude.Match(src) {
			return nil
		}

		// 阶段 1：symlink 一律拒绝，绝不跟随。
		// 必须看 DirEntry 的类型 —— os.DirFS 的 Open 会跟随链接，
		// 用 Open/Stat 判断拒绝不生效，还会泄漏链接指向的内容。
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("模板包含符号链接 %q：xinit 拒绝跟随（会把链接指向的内容泄漏进派生项目）", src)
		}

		// 阶段 3：路径 vars 渲染（作用于每一段）
		srcRendered, err := renderPathVars(src, v)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}

		// 阶段 4：rename 应用到每一段
		dst0, err := applyRenames(srcRendered, renames)
		if err != nil {
			return err
		}

		// 阶段 5：render 判定，匹配模板内原路径（strip 之前）
		mode := ModeSubst
		var stripSuffix string
		for _, rr := range c.Render {
			if rr.Matcher.Match(src) {
				mode = ModeTemplate
				stripSuffix = rr.Spec.StripSuffix
				break
			}
		}

		// 阶段 6：strip_suffix（仅 Template 模式）
		dst := dst0
		if mode == ModeTemplate && stripSuffix != "" {
			if !strings.HasSuffix(dst, stripSuffix) {
				return fmt.Errorf(
					"%s: render 命中但路径不以 strip_suffix %q 结尾", src, stripSuffix)
			}
			dst = strings.TrimSuffix(dst, stripSuffix)
		}

		// 路径段的合法性校验：渲染后可能变空、含 /、或变成 . / ..
		if err := validateDstPath(dst, src); err != nil {
			return err
		}

		// 阶段 7：元数据
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}

		entries = append(entries, Entry{
			Src:       src,
			Dst:       dst,
			Kind:      KindRegular,
			Exec:      info.Mode()&0o111 != 0,
			Binary:    fsutil.IsBinary(content),
			Mode:      mode,
			Generated: c.Generated.Match(dst),
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// 阶段 8：碰撞检查。折叠后同名的两条路径在大小写不敏感的文件系统上
	// 会静默互相覆盖。
	seen := make(map[string]string, len(entries))
	for _, e := range entries {
		key := fsutil.Fold(e.Dst)
		if prev, ok := seen[key]; ok {
			return nil, nil, fmt.Errorf(
				"模板内路径冲突：%q 与 %q 在大小写不敏感的文件系统上是同一个文件",
				prev, e.Dst)
		}
		seen[key] = e.Dst
	}

	// 阶段 8b：文件与目录同名（折叠后亦然）。模板自相矛盾时必须在
	// **写盘前**报出来，而不是等到 mkdir 失败再回删。
	dirs := map[string]string{} // folded 目录路径 → 原目录
	for _, e := range entries {
		segs := strings.Split(e.Dst, "/")
		cur := ""
		for i := 0; i+1 < len(segs); i++ {
			if i == 0 {
				cur = segs[i]
			} else {
				cur += "/" + segs[i]
			}
			fk := fsutil.Fold(cur)
			if prev, ok := dirs[fk]; ok && prev != cur {
				return nil, nil, fmt.Errorf(
					"模板内路径冲突：目录 %q 与 %q 在大小写不敏感的文件系统上是同一个",
					prev, cur)
			}
			dirs[fk] = cur
			if file, ok := seen[fk]; ok {
				return nil, nil, fmt.Errorf(
					"模板内路径冲突：%q 是文件，%q 又要求它是目录", file, e.Dst)
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Dst < entries[j].Dst })

	return entries, warnings, nil
}

func isImplicitExcluded(p string) bool {
	for _, ex := range implicitExcludes {
		if p == ex || strings.HasPrefix(p, ex+"/") {
			return true
		}
	}
	return false
}

// containsTemplateAction 报告字符串里是否含 {{ —— 用来决定要不要走模板渲染。
func containsTemplateAction(s string) bool {
	return strings.Contains(s, "{{")
}

// renderPathVars 对路径的每一段做变量求值。
func renderPathVars(src string, v Vars) (string, error) {
	if !strings.Contains(src, "{{") {
		return src, nil
	}
	segs := strings.Split(src, "/")
	for i, seg := range segs {
		if !strings.Contains(seg, "{{") {
			continue
		}
		out, err := RenderTemplate(src, []byte(seg), v)
		if err != nil {
			return "", err
		}
		// 渲染结果必须仍是单一段：值里含 '/' 会把一个路径段变成多级目录。
		// 事后的 validateDstPath 抓不到 —— "a/b/main.go" 本身是干净路径。
		// （渲染成空串的情况由 validateDstPath 的 IsAbs / 空段检查兜住。）
		if strings.Contains(string(out), "/") {
			return "", fmt.Errorf("路径段 %q 渲染为 %q：含 '/'，会把一个段变成多级目录", seg, out)
		}
		segs[i] = string(out)
	}
	return strings.Join(segs, "/"), nil
}

// resolvedRename 是替换值已渲染好的改名规则。
type resolvedRename struct {
	from, to string
}

// resolveRenames 在遍历前把全部 rename 的替换值渲染好。
//
// 必须在遍历之前做两件事：一是渲染值只算一次而不是每个文件算一遍；
// 二是**替换值里的变量引用错误要在写任何文件之前就报出来**。
//
// 替换值支持 {{ .var }} —— 清单里写 to: "{{ .app_slug }}" 是主要用法，
// 直接用原始 To 会把占位符字面量写进路径。
func resolveRenames(c *manifest.Compiled, v Vars) ([]resolvedRename, error) {
	out := make([]resolvedRename, 0, len(c.Rename))
	for i, r := range c.Rename {
		to := r.Spec.To
		if containsTemplateAction(to) {
			rendered, err := RenderTemplate(fmt.Sprintf("rename[%d].to", i), []byte(to), v)
			if err != nil {
				return nil, fmt.Errorf("rename[%d].to: %w", i, err)
			}
			to = string(rendered)
		}
		out = append(out, resolvedRename{from: r.Spec.From, to: to})
	}
	return out, nil
}

// applyRenames 对路径的每一段做替换。匹配规则复用 §7.3 的字母边界语义。
//
// 替换后每段都必须仍是单一段：值里含 '/' 会把一个路径段变成多级目录，
// 而这种事后的 validateDstPath 抓不到（"x/a/b/y" 是干净路径）。
func applyRenames(p string, rules []resolvedRename) (string, error) {
	if len(rules) == 0 {
		return p, nil
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		for _, r := range rules {
			seg = replaceToken(seg, r.from, r.to)
		}
		if strings.Contains(seg, "/") {
			return "", fmt.Errorf(
				"rename 的替换值含 '/'，会把一个路径段变成多级目录（%q → %q）", p, seg)
		}
		segs[i] = seg
	}
	return strings.Join(segs, "/"), nil
}

// replaceToken 做带字母边界的子串替换。
//
// 循环条件 i+len(from) <= len(s) 只保证"还有足够的剩余字节可以尝试匹配"，
// 不代表尾部不含普通字符：一旦剩余长度 < len(from)，循环直接退出，
// 尾部字节必须在退出后原样补上，否则会被静默丢弃（而不是原样保留）。
func replaceToken(s, from, to string) string {
	if from == "" || !strings.Contains(s, from) {
		return s
	}
	var b strings.Builder
	i := 0
	for i+len(from) <= len(s) {
		if s[i:i+len(from)] == from &&
			(i == 0 || !isASCIILetter(s[i-1])) &&
			(i+len(from) == len(s) || !isASCIILetter(s[i+len(from)])) {
			b.WriteString(to)
			i += len(from)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	b.WriteString(s[i:])
	return b.String()
}

// validateDstPath 校验渲染后的路径。
//
// 实测 {{ if .feat }}feat{{ end }}/main.go 在 feat 为空时得到 "/main.go"，
// Clean 之后文件会静默移到上一级。含 / 的值会让一个路径段变成多级目录，
// 而路径变量未必有 pattern。
func validateDstPath(dst, src string) error {
	if dst == "" {
		return fmt.Errorf("%s: 渲染后的路径为空", src)
	}
	if path.IsAbs(dst) {
		return fmt.Errorf("%s: 渲染后的路径 %q 是绝对路径", src, dst)
	}
	for _, seg := range strings.Split(dst, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%s: 渲染后的路径 %q 含空路径段", src, dst)
		case ".", "..":
			return fmt.Errorf("%s: 渲染后的路径 %q 含 %q 段", src, dst, seg)
		}
	}
	if cleaned := path.Clean(dst); cleaned != dst {
		return fmt.Errorf("%s: 渲染后的路径 %q 不规范（Clean 后为 %q）", src, dst, cleaned)
	}
	return nil
}
