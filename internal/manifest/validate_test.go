package manifest

import (
	"strings"
	"testing"
)

func TestCheckFieldTypesAcceptsStrings(t *testing.T) {
	src := "manifest_version: 1\nvars:\n  - name: a\n    default: x\n    required: true\n"
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("合法清单不应报错: %v", err)
	}
}

func TestCheckFieldTypesRejectsNonStringVarValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"bool 默认值", "manifest_version: 1\nvars:\n  - name: a\n    default: false\n"},
		{"数字默认值", "manifest_version: 1\nvars:\n  - name: a\n    default: 42\n"},
		{"映射默认值", "manifest_version: 1\nvars:\n  - name: a\n    default: {x: 1}\n"},
		{"bool 变量名", "manifest_version: 1\nvars:\n  - name: true\n"},
		{"数字 pattern", "manifest_version: 1\nvars:\n  - name: a\n    pattern: 5\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src))
			if err == nil {
				t.Fatal("应报错：非字符串的变量值会让 {{ if .x }} 静默判真")
			}
			if !strings.Contains(err.Error(), "字符串") {
				t.Errorf("错误信息应说明需要字符串，实际: %v", err)
			}
		})
	}
}

func TestCheckFieldTypesAllowsNullAsAbsent(t *testing.T) {
	// 显式 null 与字段缺省同义（goccy 对 string 字段都把两者落成 ""）
	src := "manifest_version: 1\nvars:\n  - name: a\n    prompt: null\n"
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("null 应视为缺省: %v", err)
	}
}

func TestCheckFieldTypesRejectsNonBoolRequired(t *testing.T) {
	src := "manifest_version: 1\nvars:\n  - name: a\n    required: \"yes\"\n"
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("required 不是 bool 应报错")
	}
}

func TestCheckFieldTypesRejectsNonStringRuleFields(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"from 是数字", "manifest_version: 1\nsubstitute:\n  - from: 5\n    to: x\n"},
		{"to 是 bool", "manifest_version: 1\nsubstitute:\n  - from: a\n    to: false\n"},
		{"paths 元素是数字", "manifest_version: 1\nsubstitute:\n  - from: a\n    to: b\n    paths: [1]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.src)); err == nil {
				t.Fatal("应报错：规则字段类型不符")
			}
		})
	}
}

func TestCheckFieldTypesHitIsDeterministic(t *testing.T) {
	// 多个错误字段时，报出的必须是排序后的第一个 —— 否则同样的输入
	// 每次报不同的错，测试会抖。
	//
	// 夹具必须用「字段名合法但类型错」的两个字段：多个**未知键**的
	// 报错来自 goccy 的 Strict 解码，实测不稳定（zeta/alpha 五次里
	// 报出过两种）。default < pattern，所以必须报 default。
	src := "manifest_version: 1\nvars:\n  - name: a\n    pattern: 5\n    default: false\n"
	var first error
	for i := 0; i < 20; i++ {
		_, err := Parse([]byte(src))
		if err == nil {
			t.Fatal("应报错")
		}
		if first == nil {
			first = err
			if !strings.Contains(first.Error(), "default") {
				t.Fatalf("多个坏字段时应报排序后的第一个（default），实际: %v", first)
			}
			continue
		}
		if err.Error() != first.Error() {
			t.Fatalf("错误信息不稳定:\n  %v\n  %v", first, err)
		}
	}
}
