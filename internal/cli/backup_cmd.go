package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/xbcio/xinit/internal/state"
)

// BackupPruneOptions 是 `xinit backup prune` 的全部输入（R1–R12）。
type BackupPruneOptions struct {
	ProjectDir string // 项目根（"." → Abs）
	Keep       int    // 保留最近 N 份（≥ 1；< 1 拒绝）
	DryRun     bool   // 只列出将删除的份，不实际删除
}

// backupStampRe 严格匹配 apply/backup.go 的备份目录命名（R2/R3）：stamp 形如
// 20060102-150405，同一秒的第二次起追加 -2、-3……（每个目录各自算一份）。
var backupStampRe = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "管理 .xinit/backup/ 里的备份",
		Long: "管理 .xinit/backup/ 里的备份：update 在改写前把原文件复制到\n" +
			".xinit/backup/<时间戳>/（不覆盖、不自动清理），长期会无界增长；清理是\n" +
			"显式的手工动作（本命令），update 等命令的行为不受影响。\n\n" +
			"子命令：\n" +
			"  prune   删除较旧的份，只保留最近 N 份",
	}
	cmd.AddCommand(newBackupPruneCmd())
	return cmd
}

func newBackupPruneCmd() *cobra.Command {
	var o BackupPruneOptions
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "删除 .xinit/backup/ 里较旧的备份，只保留最近 N 份",
		Long: "删除 .xinit/backup/ 里较旧的备份目录，只保留最近 N 份——按目录名时间戳升序，\n" +
			"同秒追加的 -2/-3……后缀按数值序；「份」= 一个备份目录。\n\n" +
			"只认 update 的备份命名（<yyyyMMdd-HHmmss>，同秒追加 -2/-3……）的目录：\n" +
			"其它名字、普通文件、符号链接一律不动。默认执行删除——删除不可恢复\n" +
			"（确认这些份无需回滚后再删）；加 --dry-run 只列出将删除的项、不实际删除。\n" +
			"backup/ 不存在或份数不足 N 时不删任何东西（rc=0），也不会新建 backup/。\n" +
			"退出码：0 = 完成（含无删除项）；1 = 错误。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.ProjectDir = "."
			return PruneBackups(o)
		},
	}
	cmd.Flags().IntVar(&o.Keep, "keep", 0, "保留最近 N 份（必填；N ≥ 1）")
	_ = cmd.MarkFlagRequired("keep")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "只列出将删除的份，不实际删除")
	return cmd
}

// PruneBackups 清理 .xinit/backup/ 旧份：保留最新 N 份、删除其余（R1–R12）。
//
// 项目判据（R6，顺序承重）：先以 os.Stat 确认 .xinit/ 存在，再取锁——
// LockProject 内含 MkdirAll .xinit/（lock.go:27），顺序反了会给非 xinit 项目
// 凭空造根。不调用 state.Load（R6 裁量）：prune 的数据面是 backup/ 目录自己，
// 不依赖 state 健康——state 损坏时备份恰是救援资源。
//
// 零状态写入（R10）：不写 state.json、不写工作树（锁文件除外，同 update/
// restore 的既有常驻形态）。删除范围由 --keep 显式指定——无 --force、无自动清理（R13）。
func PruneBackups(o BackupPruneOptions) error {
	if o.Keep < 1 {
		return fmt.Errorf("--keep 必须 ≥ 1（如需清空全部备份，请手工 rm -rf %s/backup）", state.DirName)
	}
	dest, err := filepath.Abs(o.ProjectDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dest, state.DirName)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("不是 xinit 项目（未找到 %s/ 目录）", state.DirName)
		}
		return err
	}

	lock, err := state.LockProject(dest)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	backupDir := filepath.Join(dest, state.DirName, "backup")
	entries, err := os.ReadDir(backupDir)
	if errors.Is(err, fs.ErrNotExist) {
		entries = nil // backup/ 不存在（从未 update 过）：M=0，不建目录（R9）
	} else if err != nil {
		return err
	}

	cands := backupCandidates(entries) // R3/R4：只留匹配命名的目录，按 (stamp, i) 升序
	m := len(cands)
	keep := o.Keep
	if keep > m {
		keep = m
	}
	k := m - keep
	if k == 0 {
		fmt.Printf("无删除项（备份共 %d 份，保留数 %d 份）\n", m, o.Keep)
		return nil
	}
	doomed := cands[:k] // 排序尾部 = 最新 R 份，删除的是前 M−R 份

	if o.DryRun {
		for _, c := range doomed {
			fmt.Printf("将删除：%s\n", backupRel(c.name))
		}
		fmt.Printf("将删除 %d 份（保留最近 %d 份；--dry-run，未执行）\n", k, keep)
		return nil
	}
	for _, c := range doomed {
		if err := os.RemoveAll(filepath.Join(backupDir, c.name)); err != nil {
			return fmt.Errorf("删除 %s: %w", backupRel(c.name), err)
		}
		fmt.Printf("已删除：%s\n", backupRel(c.name))
	}
	fmt.Printf("已删除 %d 份（保留最近 %d 份）\n", k, keep)
	return nil
}

// backupRel 是面向输出的份路径（固定 slash 形态，跨平台一致）。
func backupRel(name string) string {
	return state.DirName + "/backup/" + name
}

// backupCand 是一个候选份（R2：一个备份目录 = 一份）。
type backupCand struct {
	name  string // 目录名（`\d{8}-\d{6}(-\d+)?`）
	stamp string // 前 15 字符（`\d{8}-\d{6}` 定长，ASCII）
	seq   int    // 同秒序号：无后缀 = 1，-N = N
}

// backupCandidates 从 backup/ 的直接子项里过滤出候选份（R3）并按 (stamp, i)
// 升序排序（R4）。不匹配项（乱名、普通文件、符号链接——DirEntry.IsDir 不追
// 链接、匹配名但非目录者）一律忽略，绝不对其做任何操作。
func backupCandidates(entries []os.DirEntry) []backupCand {
	cands := make([]backupCand, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue // R3：非目录（含符号链接、匹配名字的普通文件）忽略
		}
		mm := backupStampRe.FindStringSubmatch(e.Name())
		if mm == nil {
			continue // R3：名字不匹配严格命名忽略
		}
		seq := 1
		if mm[1] != "" {
			// mm[1] 形如 -N。数字长到溢出 int 时按最大序号算——它是「最新」
			// 一族，绝不因解析失败被误判为旧份删掉。
			n, convErr := strconv.Atoi(mm[1][1:])
			if convErr != nil {
				n = math.MaxInt
			}
			seq = n
		}
		cands = append(cands, backupCand{name: e.Name(), stamp: e.Name()[:15], seq: seq})
	}
	// R4：i 是数值比较（同秒 10 份时 -10 排在 -2 之后）；名字兜底保证全序
	// （-02 与 -2 数值相同但目录名不同，定序仍确定）。
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].stamp != cands[j].stamp {
			return cands[i].stamp < cands[j].stamp
		}
		if cands[i].seq != cands[j].seq {
			return cands[i].seq < cands[j].seq
		}
		return cands[i].name < cands[j].name
	})
	return cands
}
