package cli

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// M-3（终审 mopup，验收 18「dry-run 同文」的持久锚）：dry-run 下快照命中
// 照常发生（只进内存）、`rep.Restored` 非空、diff 报告打印恢复行，且零写
// （base/ 不因恢复落盘、整棵树逐项不变）——把 update.go 的无条件赋值挪到
// dry-run 早退之后的门控变异会被本用例抓住。
func TestRestoreBaseDryRunReportsRestoredLine(t *testing.T) {
	isolateCacheT(t) // init 挂点把快照写到这里（§21.5），runUpdate/Diff 读回同一根
	repo, proj := initProjectT(t, map[string]string{
		"xinit.yml": tmplYAML,
		"a.txt":     "one\n",
	})
	if err := os.RemoveAll(state.BaseDir(proj)); err != nil {
		t.Fatal(err)
	}
	commitT(t, repo, map[string]string{"a.txt": "two\n"}, "v1.1.0") // 非 Noop，走完整 dry-run 面
	before := readTreeT(t, proj)

	rep, err := runReport(UpdateOptions{ProjectDir: proj, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Restored, []string{"a.txt"}) {
		t.Errorf("dry-run 的恢复同样进内存与报告：Restored = %v，want [a.txt]", rep.Restored)
	}
	if len(rep.Written) == 0 {
		t.Fatalf("夹具前提不成立：dry-run 应产出非 Noop 动作: %+v", rep)
	}

	// 报告面（diff = dry-run 镜像）：恢复行逐字出现。
	out := captureStdoutT(t, func() { _ = Diff(UpdateOptions{ProjectDir: proj}) })
	if !strings.Contains(out, "base 恢复（来自渲染快照，不依赖模板源，§5.2、§21）：1 条") {
		t.Errorf("diff 报告应含恢复行（dry-run 同文）：\n%s", out)
	}

	// 零写：base/ 不得因内存恢复落盘；整棵树逐项不变（含 state/lock）。
	if _, err := os.Stat(state.BaseDir(proj)); !os.IsNotExist(err) {
		t.Errorf("dry-run 恢复不得写回 base: %v", err)
	}
	if after := readTreeT(t, proj); !reflect.DeepEqual(before, after) {
		t.Error("dry-run 不得写任何文件（含 .xinit/base/ 的恢复写回）")
	}
}
