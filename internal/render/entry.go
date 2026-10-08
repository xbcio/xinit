package render

// Kind 是文件类型。
//
// Exec **不是** 一种 Kind，而是一个独立的 bool 字段。把它编进 Kind 枚举会
// 让「模板只给 release.sh 加 +x」在类型层被判成不匹配，mode 层永远到不了；
// 在 Windows 上更糟 —— ours 永远是 Regular，每个可执行脚本每轮都冲突。
type Kind uint8

const (
	KindRegular Kind = iota
	KindSymlink
)

func (k Kind) String() string {
	switch k {
	case KindRegular:
		return "regular"
	case KindSymlink:
		return "symlink"
	}
	return "unknown"
}

// RenderMode 决定一个文件走哪条内容管线。
type RenderMode uint8

const (
	// ModeSubst 走声明式替换（两遍 span 法）。
	ModeSubst RenderMode = iota
	// ModeTemplate 走 Go template，并从 substitute 作用域排除。
	ModeTemplate
)

func (m RenderMode) String() string {
	switch m {
	case ModeSubst:
		return "subst"
	case ModeTemplate:
		return "template"
	}
	return "unknown"
}

// Entry 是模板里一个文件的完整描述。
type Entry struct {
	Src       string // 模板内原路径（/ 分隔、相对）
	Dst       string // 派生项目内目标路径（已过路径 vars / rename / strip_suffix）
	Kind      Kind
	Exec      bool // 执行位 —— 不是一种 Kind
	Binary    bool
	Mode      RenderMode
	Generated bool // 由 theirs 清单的 generated glob 匹配 Dst 得出
}

// Warning 是一条不给中止的提示。
type Warning struct {
	Path string
	Msg  string
}
