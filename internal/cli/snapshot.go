package cli

import (
	"fmt"
	"os"

	"github.com/xbcio/xinit/internal/renderstore"
	"github.com/xbcio/xinit/internal/state"
)

// writeSnapshotBestEffort 是渲染快照（§21）写侧的唯一出口：init / update /
// adopt 三挂点共用。
//
// 前置契约（与 renderstore.Write 一致）：st 必须是本轮 st.Save **成功之后**
// 的持久化状态（键的 vars 分量 = 停盘值）；rendered 是当轮渲染源，update 传
// **theirsRendered**（全量渲染原文），不得传 blobs 混合体。
//
// best-effort（§21.5 失败语义）：任何写失败只记一行 stderr 警告——命令照常
// 成功，项目状态零影响；跳过/不写出的判断都在 renderstore.Write 内部。
func writeSnapshotBestEffort(cacheRoot string, st *state.State, rendered map[string][]byte) {
	if err := renderstore.Write(st, cacheRoot, rendered); err != nil {
		fmt.Fprintf(os.Stderr, "xinit: 渲染快照写入失败（不影响本轮）：%v\n", err)
	}
}
