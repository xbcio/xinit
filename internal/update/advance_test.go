package update

// T7b/M3b C3 断言：恢复推进域（I14）。落点 internal/update（§4.3 规则 1）。

import (
	"os"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
	"github.com/xbcio/xinit/internal/template"
)

// TestAdvanceSelfCheckAbortsOnHashMismatch 钉 I14（§1.3 advanceState /
// 不变量 5）：记账 hash 与落盘字节不符 → 写入自检中止；且顺序固定
// 「先写 base → 自检 → 最后写 state.json」——state.json 必须**未**被写
// （顺序证据：中止发生在 st.Save 之前）。
func TestAdvanceSelfCheckAbortsOnHashMismatch(t *testing.T) {
	const p = "p.txt"
	proj := t.TempDir()
	landed := []byte("landed\n")
	// 记账的 NewBase.Hash 与真正落盘的字节（baseSnap.Bytes[p]）不符。
	wrongMeta := plan.FileMeta{Kind: render.KindRegular, Hash: fsutil.CompareHash([]byte("other\n"), false)}
	b := &Base{Metas: plan.Snap{p: wrongMeta}, Bytes: map[string][]byte{p: landed}}
	st := state.New(engineVersionT)
	st.Files = map[string]state.FileState{} // files 无 p：保持分支取 newSrc[p]

	// advanced 判定 *NewBase == baseSnap.Metas[p] → false（保持旧 base）→
	// 落盘内容 = baseSnap.Bytes[p]；自检比 ContentHash(landed) 与记账 hash。
	err := advanceState(proj, st, b,
		[]plan.Action{{Path: p, Kind: plan.WriteTheirs, NewBase: &wrongMeta}},
		nil, map[string]string{p: "p.tmpl"}, render.Vars{},
		&template.Source{Kind: template.KindDir}, true, t.TempDir(), engineVersionT)
	if err == nil || !strings.Contains(err.Error(), "写入自检失败") {
		t.Fatalf("hash 不符应中止于写入自检（不变量 5）：err = %v", err)
	}
	if _, statErr := os.Stat(state.Path(proj)); !os.IsNotExist(statErr) {
		t.Errorf("自检中止必须发生在写 state.json 之前（顺序证据）：stat err = %v", statErr)
	}
}
