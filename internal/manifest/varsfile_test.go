package manifest

// 来源：internal/cli/init_test.go 的两个 TestLoadConfig*（§4.3 规则 1「被测
// 符号在哪，测试在哪」：LoadVarsFile 归位 manifest 后用例随迁；I-2 实施批
// 并入 T2）。断言零改动，仅符号名随函数改名。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadVarsFileRejectsNonStringValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(p, []byte("feature: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadVarsFile(p)
	if err == nil {
		t.Fatal("非字符串的配置值应报错")
	}
	if !strings.Contains(err.Error(), "字符串") {
		t.Errorf("错误信息应说明需要字符串: %v", err)
	}
}

func TestLoadVarsFileFlatMap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vars.yml")
	if err := os.WriteFile(p, []byte("project_name: Xdesk\napp_slug: xdesk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadVarsFile(p)
	if err != nil {
		t.Fatalf("LoadVarsFile: %v", err)
	}
	if got["project_name"] != "Xdesk" || got["app_slug"] != "xdesk" {
		t.Errorf("= %+v", got)
	}
}
