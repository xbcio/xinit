package render

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Vars 是求值后的变量表。
//
// 调用方保证**所有已声明变量都在这里**（未提供的落成空串）。这是必须的：
// 若某个声明的变量缺席，FuncMap 里的 default 永远用不上，条件块
// {{ if .feature }} 也会直接报 "map has no entry for key"。
type Vars map[string]string

// funcMap 是**封闭**的函数集合。
//
// 绝不引入 sprig：它带 now、randAlpha、env 等非确定性函数，会直接破坏
// 渲染确定性（不变量 1），而渲染确定性是「重建 base 后断言 hash 一致」
// 与 golden 测试的前提。
var funcMap = template.FuncMap{
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
	"title": strings.Title, //nolint:staticcheck // 与旧实现保持一致的标题化行为
	"trim":  strings.TrimSpace,
	// replace/trimSuffix/trimPrefix 不能直接传 stdlib 函数：text/template 的
	// pipeline 把被管道的值追加为**最后一个**参数（如 `.n | trimPrefix "Hello "`
	// 调用的是 fn("Hello ", .n)），而 strings.TrimPrefix/TrimSuffix/ReplaceAll
	// 都是「主体 s 在前，操作数在后」。直接传会导致主体和操作数颠倒，必须用
	// 显式包装把参数顺序掰正。
	"replace":    func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
	"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
	"default":    func(def, val string) string { return orDefault(def, val) },
}

func orDefault(def, val string) string {
	if val == "" {
		return def
	}
	return val
}

// RenderTemplate 用 vars 渲染一段 Go template。
//
// 根数据是 map[string]any 而非结构体：reflect.StructOf 拒绝小写字段名
// （实测 "field \"project_name\" is unexported but missing PkgPath"），
// 而变量名按规范全小写。
//
// name 只用于错误定位，通常是模板内的原路径。
func RenderTemplate(name string, content []byte, v Vars) ([]byte, error) {
	tmpl, err := template.New(name).
		Funcs(funcMap).
		Option("missingkey=error").
		Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	data := make(map[string]any, len(v))
	for k, val := range v {
		data[k] = val
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return buf.Bytes(), nil
}
