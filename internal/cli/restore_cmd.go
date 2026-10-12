package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

// RestoreOptions 是 restore 的全部输入（R1–R14）。
type RestoreOptions struct {
	ProjectDir string   // 项目根（"." → Abs）
	Paths      []string // 1–n 个记账路径（state.files 的键）；去重保首现序
	Force      bool     // 覆盖内容不同的现有文件（默认拒绝）
}

func newRestoreCmd() *cobra.Command {
	var o RestoreOptions
	cmd := &cobra.Command{
		Use:   "restore <path>...",
		Short: "把记账路径从 .xinit/base/ 恢复到工作树（找回模板已记录的版本）",
		Long: "把 1–n 个记账路径（state.files 的键，即模板已记录的 Dst）从 .xinit/base/ 恢复到项目工作树——\n" +
			"即 update 报告里「找回模板已记录的版本：cp .xinit/base/<P> <P>」提示的命令化。\n\n" +
			"三态：\n" +
			"  - 目标不存在 → 恢复（写入 base 的字节与 exec 位）；\n" +
			"  - 目标与 base 逐字节一致 → 无变化（幂等成功；仅 exec 位不符时对齐 exec 位）；\n" +
			"  - 目标存在且内容不同 → 默认拒绝（rc=1），加 --force 才覆盖（重置回模板已记录的版本）。\n\n" +
			"预检全过才写（all-or-nothing）：任一路径非法 / 未记账 / base 里缺该文件 / 拒绝覆盖 → rc=1，\n" +
			"一个字节都不写。零状态写入：不改 .xinit/state.json、不写 .xinit/base/（锁文件除外）——\n" +
			"恢复后该路径与 base 一致，下轮 update 不再告警。base 里缺该文件时 restore 不重建 base\n" +
			"（重建需要模板源与 vars）：请先跑 `xinit update` 重建 base 后再 restore。\n" +
			"退出码：0 = 全部恢复或无变化；1 = 错误（含拒绝覆盖，一个字节都不写）。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			o.Paths = args
			return Restore(o)
		},
	}
	cmd.Flags().BoolVar(&o.Force, "force", false,
		"覆盖内容不同的现有文件（默认拒绝覆盖这些路径，rc=1、不改动磁盘）")
	return cmd
}

// Restore 把记账路径从 .xinit/base/（模板已记录的版本）恢复到工作树（R1–R7）。
//
// 锁（R4）：全程持 state.LockProject——写工作树的命令，与 update/adopt/backflow
// 同语义；预检与执行都在锁内。取锁排在 state.Load 之后：非 xinit 项目在
// LockProject 造出 .xinit/ 之前就报错退出（不给这类项目留残迹）。
//
// 零状态写入（R6）：不写 state.json、不写 base 树（锁文件除外）——下轮 update
// 自然收敛（文件与 base 一致 → 无告警）。
func Restore(o RestoreOptions) error {
	dest, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return err
	}
	// Load 失败原文透出（非 xinit 项目 / state.json 损坏，与其它命令同口径）。
	st, err := state.Load(dest)
	if err != nil {
		return err
	}

	lock, err := state.LockProject(dest)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	items, err := restorePrecheck(dest, st, o.Paths, o.Force)
	if err != nil {
		return err // 预检失败：尚未写任何字节（all-or-nothing）
	}
	return restoreExecute(dest, items)
}

// restoreItem 是预检产出的单路径动作。
type restoreItem struct {
	dst   string
	data  []byte      // 写侧字节 = base 原始字节（R14：不做 EOL 归一化）
	mode  fs.FileMode // 0o644 / 0o755，按 state.files[dst].Exec（R13）
	write bool        // false = 无变化（内容与 exec 位都已与记账一致）
}

// restorePrecheck 是 R1/R2/R3/R8 的预检：全部路径过检才返回动作集，任一不过
// 即错误返回——调用方据此保证 all-or-nothing（写只发生在 restoreExecute）。
//
// 错误分两类：输入错误（非法路径 / 未记账 / base 缺失）就地短路——它们是
// 参数或状态的问题，先修好再谈其余；「目标内容不同且没有 --force」是唯一
// 收集成集的拒绝项（一次列全，用户加 --force 即可全部通过）。
func restorePrecheck(dest string, st *state.State, args []string, force bool) ([]restoreItem, error) {
	// 去重保首现序（R8）：重复参数只出一条输出、只写一次。
	paths := make([]string, 0, len(args))
	seen := make(map[string]bool, len(args))
	for _, p := range args {
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}

	items := make([]restoreItem, 0, len(paths))
	var refused []string
	for _, p := range paths {
		// R2：CLI 参数语境直调 §22.4 单点谓词，措辞用「路径 %q 非法」而非
		// base 前缀（CheckWithinBase 的语境是 base 树内部路径）。
		if err := fsutil.CheckRelPath(p); err != nil {
			return nil, fmt.Errorf("路径 %q 非法：%v", p, err)
		}
		fst, ok := st.Files[p]
		if !ok {
			return nil, fmt.Errorf("路径 %q 不在记账路径里：restore 只能恢复记账路径（%s/state.json 的 files 键）",
				p, state.DirName)
		}
		// R3：base 是唯一数据源（有没有 --force 都一样）。单文件缺失即错误，
		// 指引重跑 update——restore 自己不做重建（重建链要模板源与 vars，
		// 超出本命令边界，见包内 restoreBase）。
		base, err := state.ReadBase(dest, p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%s/base/%s 缺失（记账里有、base 树里没有）：请先重跑 `xinit update` 重建 base 后再 restore",
					state.DirName, p)
			}
			return nil, err
		}
		mode := fs.FileMode(0o644)
		if fst.Exec {
			mode = 0o755
		}
		item := restoreItem{dst: p, data: base, mode: mode}

		full := filepath.Join(dest, filepath.FromSlash(p))
		fi, statErr := os.Stat(full)
		if errors.Is(statErr, fs.ErrNotExist) {
			item.write = true // 目标不存在 → 恢复
			items = append(items, item)
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("读取目标 %s: %w", p, statErr)
		}
		// R5：不 special-case 符号链接/目录目标——目录在这里自然失败（os.ReadFile
		// 报 is a directory），符号链接按 ReadFile 跟随后的内容判定。
		target, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("读取目标 %s: %w", p, err)
		}
		equal := bytes.Equal(target, base)
		if equal && execAligned(fi, fst.Exec) {
			items = append(items, item) // 无变化：幂等成功，不写
			continue
		}
		if !equal && !force {
			refused = append(refused, p) // R1：存在且内容不同 → 默认拒绝
			continue
		}
		// 待恢复：base 里没有（已在上面拦截）之外的两条路——内容一致但 exec 位
		// 不符（重写对齐，无内容丢失风险），或内容不同 + --force（显式覆盖重置）。
		item.write = true
		items = append(items, item)
	}
	if len(refused) > 0 {
		return nil, fmt.Errorf("目标已存在且与模板已记录的版本不同：%s；确认覆盖请加 --force",
			strings.Join(refused, ", "))
	}
	return items, nil
}

// execAligned 报告目标磁盘的 exec 位是否已与记账一致（R13：state 的 Exec 是
// 权威记录；不读 base 文件的 mode——那是另一份可能漂移的数据，Windows 上
// 尤其不可靠）。
//
// !fsutil.ExecSupported（无 x 位语义的平台，如 Windows）恒 true：那里
// info.Mode()&0o111 恒 false，照常比较会让 Exec=true 的路径每轮都判「不符」
// → 反复重写、幂等性破坏（§22.6 的既有短路口径，同 execConclusion/adjustExec）。
func execAligned(fi fs.FileInfo, want bool) bool {
	if !fsutil.ExecSupported {
		return true
	}
	return (fi.Mode()&0o111 != 0) == want
}

// restoreExecute 按预检顺序落盘（R5/R7），stdout 逐行「已恢复：<p>」/
// 「无变化：<p>」。写中失败即返回——此前已写的保留（不承诺回滚），错误点名
// 失败路径。
func restoreExecute(dest string, items []restoreItem) error {
	for _, it := range items {
		if !it.write {
			fmt.Printf("无变化：%s\n", it.dst)
			continue
		}
		full := filepath.Join(dest, filepath.FromSlash(it.dst))
		// 父目录不必显式 MkdirAll：state.WriteFileAtomic 自己建（0o755），
		// 与 update 第 8 步走同一通道。
		if err := state.WriteFileAtomic(full, it.data, it.mode); err != nil {
			return fmt.Errorf("恢复 %s: %w", it.dst, err)
		}
		fmt.Printf("已恢复：%s\n", it.dst)
	}
	return nil
}
