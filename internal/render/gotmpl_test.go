package render

import (
	"strings"
	"testing"
)

func TestRenderTemplateSubstitutesVar(t *testing.T) {
	out, err := RenderTemplate("x.tmpl", []byte("hello {{ .name }}"), Vars{"name": "World"})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if string(out) != "hello World" {
		t.Errorf("= %q, want %q", out, "hello World")
	}
}

func TestRenderTemplateFuncMap(t *testing.T) {
	v := Vars{"n": "Hello World", "empty": ""}
	tests := []struct {
		tmpl, want string
	}{
		{"{{ .n | lower }}", "hello world"},
		{"{{ .n | upper }}", "HELLO WORLD"},
		{"{{ .n | trimPrefix \"Hello \" }}", "World"},
		{"{{ .n | trimSuffix \" World\" }}", "Hello"},
		{"{{ .n | replace \"o\" \"0\" }}", "Hell0 W0rld"},
		{"{{ .n | trim }}", "Hello World"},
		{"{{ .empty | default \"fallback\" }}", "fallback"},
		{"{{ .n | default \"fallback\" }}", "Hello World"},
	}
	for _, tt := range tests {
		out, err := RenderTemplate("x.tmpl", []byte(tt.tmpl), v)
		if err != nil {
			t.Errorf("%s: %v", tt.tmpl, err)
			continue
		}
		if string(out) != tt.want {
			t.Errorf("%s = %q, want %q", tt.tmpl, out, tt.want)
		}
	}
}

func TestRenderTemplateDeclaredButUnsetVarIsEmpty(t *testing.T) {
	// 所有已声明变量都在 map 里（值为 ""），否则条件块与 default 都会失败
	out, err := RenderTemplate("x.tmpl", []byte("{{ if .feature }}ON{{ else }}OFF{{ end }}"), Vars{"feature": ""})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if string(out) != "OFF" {
		t.Errorf("= %q, want %q", out, "OFF")
	}
}

func TestRenderTemplateConditionalTrue(t *testing.T) {
	out, err := RenderTemplate("x.tmpl", []byte("{{ if .feature }}ON{{ end }}"), Vars{"feature": "yes"})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if string(out) != "ON" {
		t.Errorf("= %q, want %q", out, "ON")
	}
}

func TestRenderTemplateUndeclaredVarErrors(t *testing.T) {
	_, err := RenderTemplate("x.tmpl", []byte("{{ .nope }}"), Vars{"name": "x"})
	if err == nil {
		t.Fatal("引用未声明变量必须报错（missingkey=error）")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("错误信息应包含变量名: %v", err)
	}
}

func TestRenderTemplateBareNameIsNotValid(t *testing.T) {
	// {{ project_name }} 在 text/template 里是**函数调用**，不是字段。
	// 这是一个很容易写错、且错误信息不直观的形式 —— 测试把它固定下来，
	// 好让文档里的示例不会悄悄失效。
	_, err := RenderTemplate("x.tmpl", []byte("{{ project_name }}"), Vars{"project_name": "x"})
	if err == nil {
		t.Fatal("{{ project_name }} 应报错：它是函数调用，FuncMap 里没有这个名字")
	}
	if !strings.Contains(err.Error(), "project_name") {
		t.Errorf("错误信息应包含名字: %v", err)
	}
}

func TestRenderTemplateSyntaxErrorNamesFile(t *testing.T) {
	_, err := RenderTemplate("config/app.tmpl", []byte("{{ .name "), Vars{})
	if err == nil {
		t.Fatal("语法错误应报错")
	}
	if !strings.Contains(err.Error(), "config/app.tmpl") {
		t.Errorf("错误信息应包含文件名以便定位: %v", err)
	}
}

func TestRenderTemplateRejectsSprigLikeNondeterminism(t *testing.T) {
	// FuncMap 必须封闭：now/randAlpha/env 这类函数会破坏渲染确定性
	for _, fn := range []string{"now", "randAlpha", "env", "getenv"} {
		if _, err := RenderTemplate("x.tmpl", []byte("{{ "+fn+" }}"), Vars{}); err == nil {
			t.Errorf("函数 %q 不应存在（会破坏渲染确定性）", fn)
		}
	}
}

func TestRenderTemplateDeterministic(t *testing.T) {
	v := Vars{"a": "1", "b": "2", "c": "3"}
	tmpl := []byte("{{ .a }}-{{ .b }}-{{ .c }}")
	first, err := RenderTemplate("x.tmpl", tmpl, v)
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := RenderTemplate("x.tmpl", tmpl, v)
		if err != nil {
			t.Fatalf("RenderTemplate: %v", err)
		}
		if string(got) != string(first) {
			t.Fatalf("渲染不确定: %q != %q", got, first)
		}
	}
}
