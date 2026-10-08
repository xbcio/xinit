package manifest

import (
	"fmt"
	"sort"

	"github.com/goccy/go-yaml"
)

// 逐字段的期望类型。不能一律要求字符串 —— required 是合法的 bool。
var (
	varSpecStringFields = []string{"name", "prompt", "default", "pattern"}
	varSpecBoolFields   = []string{"required"}

	substStringFields = []string{"from", "to", "when_preceded_by", "when_followed_by", "target_case"}
	substListFields   = []string{"paths"}

	renderStringFields = []string{"strip_suffix"}
	renderListFields   = []string{"paths"}

	renameStringFields = []string{"from", "to"}
)

// checkFieldTypes 用 any 侧解码重跑一遍，逐字段校验类型。
//
// 这么做是因为 goccy 会把 default: false 静默转成字符串 "false" 且不报错，
// 而 "false" 在 {{ if .feature }} 里是真值 —— 正是规范 §3.1 要防的 bug。
// 类型系统拦不住它。
//
// vars 写成映射这种情况不需要在这里查：Go 的类型系统会直接报
// "mapping was used where sequence is expected"。
func checkFieldTypes(data []byte) error {
	var raw struct {
		Vars       []map[string]any `yaml:"vars"`
		Substitute []map[string]any `yaml:"substitute"`
		Render     []map[string]any `yaml:"render"`
		Rename     []map[string]any `yaml:"rename"`
	}
	// 这里刻意**不**用 Strict：raw 只声明了它要检查的那几个顶层键，
	// Strict 会把 manifest_version、definitions、protected_literals、
	// exclude、generated、hooks 这些合法键全当未知键拒掉（实测报
	// `unknown field "definitions"`，会让 Task 6 的 TestParseMinimal
	// 和 TestDefinitionsIsAcceptedAndIgnored 直接变红）。
	// 未知键的拒斥由 Parse 里对 Manifest 的那次 Strict 解码负责 ——
	// 那才是完整 schema；本函数只管类型。
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}

	for i, vm := range raw.Vars {
		where := fmt.Sprintf("vars[%d]", i)
		if err := checkStringFields(vm, varSpecStringFields, where); err != nil {
			return err
		}
		if err := checkBoolFields(vm, varSpecBoolFields, where); err != nil {
			return err
		}
	}
	for i, m := range raw.Substitute {
		where := fmt.Sprintf("substitute[%d]", i)
		if err := checkStringFields(m, substStringFields, where); err != nil {
			return err
		}
		if err := checkStringListFields(m, substListFields, where); err != nil {
			return err
		}
	}
	for i, m := range raw.Render {
		where := fmt.Sprintf("render[%d]", i)
		if err := checkStringFields(m, renderStringFields, where); err != nil {
			return err
		}
		if err := checkStringListFields(m, renderListFields, where); err != nil {
			return err
		}
	}
	for i, m := range raw.Rename {
		where := fmt.Sprintf("rename[%d]", i)
		if err := checkStringFields(m, renameStringFields, where); err != nil {
			return err
		}
	}
	return nil
}

// checkStringFields 要求指定字段若存在则必须是字符串。
// 显式 null 视为缺省（与 goccy 对 string 字段的处理一致）。
//
// 按字段名的字节序遍历，保证同样的输入报同样的错。
func checkStringFields(m map[string]any, fields []string, where string) error {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[f] = true
	}
	for _, k := range sortedAnyKeys(m) {
		if !want[k] {
			continue
		}
		v := m[k]
		if v == nil {
			continue
		}
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s.%s: 值必须是字符串，实际是 %T (%v)", where, k, v, v)
		}
	}
	return nil
}

// checkBoolFields 要求指定字段若存在则必须是 bool。
func checkBoolFields(m map[string]any, fields []string, where string) error {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[f] = true
	}
	for _, k := range sortedAnyKeys(m) {
		if !want[k] {
			continue
		}
		v := m[k]
		if v == nil {
			continue
		}
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s.%s: 值必须是布尔，实际是 %T (%v)", where, k, v, v)
		}
	}
	return nil
}

// checkStringListFields 要求指定字段若存在则必须是字符串序列。
func checkStringListFields(m map[string]any, fields []string, where string) error {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[f] = true
	}
	for _, k := range sortedAnyKeys(m) {
		if !want[k] {
			continue
		}
		v := m[k]
		if v == nil {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s.%s: 值必须是序列，实际是 %T", where, k, v)
		}
		for i, e := range list {
			if _, ok := e.(string); !ok {
				return fmt.Errorf("%s.%s[%d]: 元素必须是字符串，实际是 %T (%v)", where, k, i, e, e)
			}
		}
	}
	return nil
}

func sortedAnyKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
