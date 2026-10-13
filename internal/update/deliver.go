package update

// 来源：internal/update/update.go 的 Run 块 25-28（M2-P9 deliver 抽取；I-2
// 实施批 T6/M2c）+ warnIfDirty/backupTargets/restoreRefused/overwrittenPaths/
// joinNote 整函数搬家（§2.6 deliver.go 行；joinNote 按消费者 = restoreRefused
// 实证归此，brief「migrate.go 优先」为未命中猜测）。阶段判据（§3-M2）：函数体与源区间逐字
// 对应，差异仅「参数接入」「返回值包装」与附加裁定类（R-M2c-5）——逐条见
// task-6-report.md。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xbcio/xinit/internal/apply"
	"github.com/xbcio/xinit/internal/plan"
	"github.com/xbcio/xinit/internal/render"
	"github.com/xbcio/xinit/internal/state"
)

// Delivery 是落盘结果（P9 写，P10/P11 只读；§2.3）。
type Delivery struct {
	BackupDir   string
	Overwritten []string
	Result      apply.Result
	Pending     bool // I13：conflicted 全集 ∪ res.Refusals
}

// deliver 是 P9（§2.2）：备份（块 25）+ 行 5 覆盖保护（块 26）+ 执行（块 27）
// + restoreRefused 末位写者与 pending 合成（块 28）。块 28 就地改写 d.Actions
// 的 NewBase/Note（R-M2c-5，写者表「P9 末位写者」实现——advance 读改写后的值）。
// rep.* 留守段依 R-M2-3 在 Run（值经 w）。
func deliver(s *session, b *Base, o *Ours, d *Decision, g *Gate) (*Delivery, error) {
	w := &Delivery{}
	// 13. 备份（§10.2 第 7 步、§10.1）：计划内会被写入或删除的路径；目标以
	// oursSnap（Scan，不穿透软链）为准过滤——中间段是软链的路径（将被
	// CheckWrites 拒绝）不得进备份，否则 Backup 的穿透 Lstat 会错位备份
	// 链后文件（裁定 U/Z）。
	backupDir, err := apply.Backup(s.dir, time.Now().Format("20060102-150405"), backupTargets(g.Changes, o.Snap))
	if err != nil {
		return nil, fmt.Errorf("备份: %w", err)
	}
	w.BackupDir = backupDir

	// 13b. 行 5 覆盖保护（§8.2 :614）：本轮会被 generated 覆盖的路径，其
	// 本地版本先另存 .xinit/overwritten/<P>——持久保留，不随 update 清理
	//（同一路径再次被覆盖时更新为最近一次副本）。
	//
	// 先取「写前检查的拒绝集」再落盘：被折叠拒绝/障碍物挡下的路径**没有被
	// 覆盖**，§8.2 的另存义务只在真的发生覆盖时触发——既不写副本、也不进
	// 报告。拒绝集原样来自 predictRefusals（与 Execute 步骤 1-2 同一组函数、
	// 同一 changes），绝不手写近似判据：手写判据会开出「Execute 会写、却没
	// 另存」的窗口。
	if slices.ContainsFunc(d.Actions, func(a plan.Action) bool { return a.OverwriteGenerated }) {
		refusals, err := predictRefusals(s.dir, g.Changes)
		if err != nil {
			return nil, err
		}
		for _, p := range overwrittenPaths(d.Actions, refusalSet(refusals)) {
			data := o.Bytes[p]
			if data == nil {
				return nil, fmt.Errorf("内部错误：%q 判为 generated 覆盖但本地内容缺失", p)
			}
			mode := os.FileMode(0o644)
			if m, ok := o.Snap[p]; ok && m.Exec {
				mode = 0o755
			}
			full := filepath.Join(s.dir, state.DirName, "overwritten", filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, fmt.Errorf("另存被覆盖的本地版本: %w", err)
			}
			if err := state.WriteFileAtomic(full, data, mode); err != nil {
				return nil, fmt.Errorf("另存被覆盖的本地版本: %w", err)
			}
			w.Overwritten = append(w.Overwritten, p)
		}
	}

	// 14. 执行（§10.2 第 8 步）——折叠拒绝/障碍物路径跳过、不中止其余路径
	res, err := apply.Execute(s.dir, g.Changes)
	if err != nil {
		return nil, err
	}
	w.Result = res

	// 15. 被拒路径把 NewBase 还原为「本轮开始时的 base 条目」（§8.5
	//     折叠拒绝行）——必须在第 9 步之前，否则被拒路径的 base 会被推进。
	d.Actions = restoreRefused(d.Actions, b.Metas, refusalSet(w.Result.Refusals))

	w.Pending = len(w.Result.Refusals) > 0 || len(d.Conflicted) > 0
	return w, nil
}

// warnIfDirty 是 §10.2 第 5 步：git 仓脏 → 警告；非 git 仓 → 警告后继续。
// --force 跳过检查（§11.2）。两条路都不阻止执行。
func warnIfDirty(projectDir string, force bool) {
	if force {
		return
	}
	out, err := exec.Command("git", "-C", projectDir, "status", "--porcelain").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "xinit: 警告: 当前目录不是 git 仓库：出问题只能靠 .xinit/backup/ 还原")
		return
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		n := len(strings.Split(s, "\n"))
		fmt.Fprintf(os.Stderr,
			"xinit: 警告: 工作树有 %d 处未提交改动；update 会直接改写工作树，建议先提交（或用 --force 跳过检查）\n", n)
	}
}

// backupTargets 是 §10.1 的备份对象：计划内会被写入或删除、且 ours 侧确实是
// 常规文件的路径（含纯 exec 变更；删除也记入备份）。
//
// 过滤依据是 worktree.Scan 的快照（不穿透符号链接），而不是让 apply.Backup
// 的 os.Lstat 判定（穿透语义）：中间段是软链的路径在 ours 侧「不存在」，
// 穿透的 Lstat 却会看到链后的文件——错位备份（裁定 U/Z）。
// ours 不存在 → 无原内容可备份；目录/符号链接 → 本轮不会被改动。
func backupTargets(c apply.Changes, oursSnap plan.Snap) []string {
	targets := make([]string, 0, len(c.Writes)+len(c.Deletes)+len(c.ExecBits))
	add := func(p string) {
		if m, ok := oursSnap[p]; ok && m.Kind == render.KindRegular {
			targets = append(targets, p)
		}
	}
	for _, p := range sortedStringKeys(c.Writes) {
		add(p)
	}
	for _, p := range c.Deletes {
		add(p)
	}
	for _, p := range sortedStringKeys(c.ExecBits) {
		if _, inWrites := c.Writes[p]; !inWrites {
			add(p)
		}
	}
	sort.Strings(targets)
	return targets
}

// restoreRefused 把被拒路径（折叠拒绝、障碍物、删除路径逃逸）的 NewBase
// 还原为「本轮开始时的 base 条目」（§8.5 折叠拒绝行）——base✗ → nil
// （与行 15 的「保持旧值 = nil」同形）。必须在第 9 步之前调用。
func restoreRefused(actions []plan.Action, baseMeta plan.Snap, refused map[string]bool) []plan.Action {
	for i := range actions {
		a := &actions[i]
		if !refused[a.Path] {
			continue
		}
		if old, ok := baseMeta[a.Path]; ok {
			m := old
			a.NewBase = &m
		} else {
			a.NewBase = nil
		}
		a.Note = joinNote(a.Note, "本轮被拒绝（折叠/障碍物）：未落盘，保持旧 base，下一轮重新判定")
	}
	return actions
}

// overwrittenPaths 是本轮「会被 generated 覆盖（§8.2 行 5）、且未被写前检查
// 拒绝」的路径（排序）——13b 的落盘清单与 dry-run 的预测共用同一出处。
//
// refused 必须原样来自 predictRefusals（与 apply.Execute 步骤 1-2 同一组
// 函数）：被拒的路径没有被覆盖，另存义务不触发；换成手写近似判据会同时开出
// 「Execute 写而没另存」（漏档）与「没写而记档」（虚账）两个窗口。
func overwrittenPaths(actions []plan.Action, refused map[string]bool) []string {
	var out []string
	for _, a := range actions {
		if a.OverwriteGenerated && !refused[a.Path] {
			out = append(out, a.Path)
		}
	}
	sort.Strings(out)
	return out
}

// joinNote 把追加文案并入 Note（与 plan 内部的 noteJoin 同形；Note 是
// 用户可见文案，不做去重）。
func joinNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "；" + extra
}
