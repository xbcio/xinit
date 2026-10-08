// Package manifest 解析模板项目的 xinit.yml。
//
// 只做解析与结构/类型校验，不读取模板树、不渲染。
package manifest

import (
	"errors"
	"fmt"

	"github.com/goccy/go-yaml"
)

// ManifestVersion 是本工具能解析的最高清单版本。
// 更高版本明确报错退出，不尽力解析。
const ManifestVersion = 1

type Manifest struct {
	Version           int            `yaml:"manifest_version"`
	Definitions       map[string]any `yaml:"definitions"`
	Vars              []VarSpec      `yaml:"vars"`
	ProtectedLiterals []string       `yaml:"protected_literals"`
	Substitute        []SubstRule    `yaml:"substitute"`
	Render            []RenderRule   `yaml:"render"`
	Rename            []RenameRule   `yaml:"rename"`
	Exclude           PathSpec       `yaml:"exclude"`
	Generated         PathSpec       `yaml:"generated"`
	Hooks             HooksSpec      `yaml:"hooks"`
}

// VarSpec 是一个变量声明。变量名必须是合法 Go 标识符（[A-Za-z_][A-Za-z0-9_]*），
// 因为它要作为 text/template 的字段名使用。
type VarSpec struct {
	Name     string `yaml:"name"`
	Prompt   string `yaml:"prompt"`
	Default  string `yaml:"default"`
	Pattern  string `yaml:"pattern"`
	Required bool   `yaml:"required"`
}

// SubstRule 是一条声明式替换规则。按序求值，首个命中的规则在该位置获胜。
type SubstRule struct {
	From           string   `yaml:"from"`
	To             string   `yaml:"to"`
	Paths          []string `yaml:"paths"`
	WhenPrecededBy string   `yaml:"when_preceded_by"`
	WhenFollowedBy string   `yaml:"when_followed_by"`
	TargetCase     string   `yaml:"target_case"` // "" (默认不变换) | none | lower | upper
}

// RenderRule 让匹配的路径走 Go template 渲染，并从 substitute 作用域排除。
type RenderRule struct {
	Paths       []string `yaml:"paths"`
	StripSuffix string   `yaml:"strip_suffix"`
}

// RenameRule 对路径的每一段做替换，匹配规则同 §7.3 的字母边界。
type RenameRule struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// PathSpec 是一个路径 glob 列表。exclude 匹配模板内原路径 Src，
// generated 匹配派生侧目标路径 Dst。
type PathSpec struct {
	Paths []string `yaml:"paths"`
}

// HooksSpec 是块标量钩子。整块交给一个 sh -c 执行，不逐行 ——
// 逐行会让 cd、heredoc、set -e 全部失效。
type HooksSpec struct {
	PostInit   string `yaml:"post_init"`
	PostUpdate string `yaml:"post_update"`
}

// checkFieldTypes 在 Task 7 实现。此处是存根：本任务的测试全部关于
// Strict 解析与版本判定，不涉及逐字段类型，所以存根不影响它们通过；
// Task 7 的测试会针对存根失败，再把它换掉。
func checkFieldTypes(data []byte) error { return nil }

// Parse 解析并校验 xinit.yml。
//
// 解析用 Strict 模式：未知键报错，且 goccy 会附带行列号与源码摘录。
// 随后做两轮校验：逐字段类型（见 validate.go，类型系统漏掉的那部分），
// 以及结构约束（见 compile.go，同时编译全部正则）。
func Parse(data []byte) (*Manifest, error) {
	if err := checkFieldTypes(data); err != nil {
		return nil, err
	}

	var m Manifest
	if err := yaml.UnmarshalWithOptions(data, &m, yaml.Strict()); err != nil {
		return nil, err
	}

	if m.Version == 0 {
		return nil, errors.New("缺少 manifest_version")
	}
	if m.Version > ManifestVersion {
		return nil, fmt.Errorf(
			"清单版本 %d 高于本工具支持的 %d；请升级 xinit",
			m.Version, ManifestVersion)
	}

	return &m, nil
}
