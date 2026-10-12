package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbcio/xinit/internal/state"
)

// pruneProjT 造 prune 的最小 fixture（无模板、无网络）：.xinit/ 目录 +
// state.json（stateJSON 为 "" 时不落该文件，覆盖「state 缺失」用例）。
// prune 不 Load state（R6 裁量），state.json 内容可以是垃圾——用例 9 专门
// 钉住它既不读也不写。
func pruneProjT(t *testing.T, stateJSON string) string {
	t.Helper()
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, state.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if stateJSON != "" {
		writeFilesT(t, proj, map[string]string{".xinit/state.json": stateJSON})
	}
	return proj
}

// mkBackupT 造一份备份 .xinit/backup/<name>/f.txt（内容 = 目录名，内容对照用）。
func mkBackupT(t *testing.T, proj, name string) {
	t.Helper()
	dir := backupPathT(proj, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// backupPathT 拼 .xinit/backup/ 下的路径。
func backupPathT(proj string, elems ...string) string {
	return filepath.Join(append([]string{proj, state.DirName, "backup"}, elems...)...)
}

// pruneT 直调 PruneBackups 并返回 stdout：错误在闭包外断言（t.Fatalf 不进
// captureStdoutT 的闭包——那里 os.Stdout 还没还原）。
func pruneT(t *testing.T, o BackupPruneOptions) (string, error) {
	t.Helper()
	var err error
	out := captureStdoutT(t, func() { err = PruneBackups(o) })
	return out, err
}

// 用例 1（R2/R5/R9）：保留 N 删旧——三份留一，旧两份 os.Stat ENOENT、
// 最新一份在，stdout 逐行 + 末行全字面对照（冻结文案）。
func TestPruneBackupsKeepsNewestN(t *testing.T) {
	proj := pruneProjT(t, "{ garbage\n")
	names := []string{"20200101-000000", "20200102-000000", "20200103-000000"}
	for _, n := range names {
		mkBackupT(t, proj, n)
	}

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	const want = "已删除：.xinit/backup/20200101-000000\n" +
		"已删除：.xinit/backup/20200102-000000\n" +
		"已删除 2 份（保留最近 1 份）\n"
	if out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	for _, n := range names[:2] {
		if pathExists(backupPathT(proj, n)) {
			t.Errorf("旧份 %s 应已删除", n)
		}
	}
	if !pathExists(backupPathT(proj, names[2])) {
		t.Error("最新一份必须保留")
	}
}

// 用例 2（R9 幂等）：M ≤ N → K=0、无错误、单行「无删除项」，目录内容原样。
func TestPruneBackupsNoopWhenFewerThanKeep(t *testing.T) {
	proj := pruneProjT(t, "")
	mkBackupT(t, proj, "20200101-000000")
	mkBackupT(t, proj, "20200102-000000")
	// 短名乱目录：正则门必须先于任何按名字切片的处理（否则这里会 panic）；
	// 计数也不得把它算成一份——stdout 的「备份共 2 份」即该断言。
	if err := os.MkdirAll(backupPathT(proj, "misc"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 5})
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if want := "无删除项（备份共 2 份，保留数 5 份）\n"; out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	for _, n := range []string{"20200101-000000", "20200102-000000"} {
		if got := mustContent(t, proj, ".xinit/backup/"+n+"/f.txt"); got != n+"\n" {
			t.Errorf("%s 内容被改动：%q", n, got)
		}
	}
	if !pathExists(backupPathT(proj, "misc")) {
		t.Error("短名乱目录不得被动")
	}
}

// 用例 3（R3 安全核心）：严格命名匹配——乱名目录、普通文件、匹配名字的
// 普通文件、指向匹配名目录的符号链接（IsDir 不追链接）一律不动；
// M 只数匹配目录，输出/删除面不受杂项影响。
//
// 杂项名字一律 ≥ 15 字符（stamp 定长）：本用例兼作变异靶场，名字过短会在
// 候选实现被放宽时先触发切片 panic、盖住断言红（短名安全另由用例 2 的
// `misc` 钉住）。
func TestPruneBackupsIgnoresNonMatchingEntries(t *testing.T) {
	proj := pruneProjT(t, "")
	mkBackupT(t, proj, "20200101-000000") // 真候选（旧）
	mkBackupT(t, proj, "20200102-000000") // 真候选（新）
	if err := os.MkdirAll(backupPathT(proj, "zz-not-a-stamp-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPathT(proj, "plain-notes-file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 匹配名字的普通文件（排序上比真候选都旧，若被当成一份就会被删）。
	if err := os.WriteFile(backupPathT(proj, "20191231-000000"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 符号链接：名字匹配、目标也是匹配名目录——必须整体忽略（含目标）。
	link := backupPathT(proj, "20200103-000000")
	linkOK := os.Symlink("20200102-000000", link) == nil
	if !linkOK {
		t.Logf("本平台不支持/不允许符号链接：只跑链接段之外的口径")
	}

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	const want = "已删除：.xinit/backup/20200101-000000\n" +
		"已删除 1 份（保留最近 1 份）\n" // 杂项（含链接）不进候选：K=1 而非 2/3
	if out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	if pathExists(backupPathT(proj, "20200101-000000")) {
		t.Error("真候选旧份应已删除")
	}
	if !pathExists(backupPathT(proj, "20200102-000000")) {
		t.Error("符号链接若被跟随会连带删掉它指向的份（R3 回归）")
	}
	if !pathExists(backupPathT(proj, "zz-not-a-stamp-dir")) {
		t.Error("乱名目录不得被动")
	}
	if got := mustContent(t, proj, ".xinit/backup/plain-notes-file.txt"); got != "x\n" {
		t.Errorf("普通文件不得被动：%q", got)
	}
	fi, err := os.Lstat(backupPathT(proj, "20191231-000000"))
	if err != nil {
		t.Fatalf("匹配名字的普通文件不得被删：%v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("匹配名字的普通文件形状被改动：%v", fi.Mode())
	}
	if linkOK {
		li, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("符号链接不得被删：%v", err)
		}
		if li.Mode()&os.ModeSymlink == 0 {
			t.Errorf("符号链接形状被改动：%v", li.Mode())
		}
		if target, err := os.Readlink(link); err != nil || target != "20200102-000000" {
			t.Errorf("符号链接指向被改动：%q, %v", target, err)
		}
	}
}

// 用例 4（R4）：同秒 -N 按数值序——`-10` 比 `-2` 新，--keep 1 保留 `-10`；
// 字典序实现会误删 `-10`（本用例即该回归的判别点）。
func TestPruneBackupsNumericSuffixOrder(t *testing.T) {
	proj := pruneProjT(t, "")
	names := []string{"20200101-000000", "20200101-000000-2", "20200101-000000-10"}
	for _, n := range names {
		mkBackupT(t, proj, n)
	}

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	const want = "已删除：.xinit/backup/20200101-000000\n" +
		"已删除：.xinit/backup/20200101-000000-2\n" +
		"已删除 2 份（保留最近 1 份）\n"
	if out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	for _, n := range names[:2] {
		if pathExists(backupPathT(proj, n)) {
			t.Errorf("%s 应已删除", n)
		}
	}
	if !pathExists(backupPathT(proj, "20200101-000000-10")) {
		t.Error("数值序：-10 最新，必须保留（字典序会误判为最旧）")
	}
}

// 用例 5（R1）：--keep < 1 拒绝——错误文案含「≥ 1」与手工全清路径，
// 且一份都不删。
func TestPruneBackupsRejectsKeepBelowOne(t *testing.T) {
	proj := pruneProjT(t, "")
	mkBackupT(t, proj, "20200101-000000")

	for _, keep := range []int{0, -1} {
		_, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: keep})
		if err == nil {
			t.Fatalf("--keep %d 必须拒绝", keep)
		}
		for _, want := range []string{"≥ 1", "rm -rf .xinit/backup"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--keep %d 拒绝文案 %q 缺 %q", keep, err.Error(), want)
			}
		}
	}
	if !pathExists(backupPathT(proj, "20200101-000000")) {
		t.Error("拒绝时不得删除任何份")
	}
}

// 用例 6（R8）：--dry-run 零删——目录集与抽样文件内容（含 mtime）前后对照。
func TestPruneBackupsDryRunDeletesNothing(t *testing.T) {
	proj := pruneProjT(t, "{ garbage\n")
	names := []string{"20200101-000000", "20200102-000000", "20200103-000000"}
	for _, n := range names {
		mkBackupT(t, proj, n)
	}
	type snap struct {
		content string
		mtime   time.Time
	}
	before := make(map[string]snap, len(names))
	for _, n := range names {
		c, mt := snapT(t, proj, ".xinit/backup/"+n+"/f.txt")
		before[n] = snap{c, mt}
	}

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1, DryRun: true})
	if err != nil {
		t.Fatalf("PruneBackups --dry-run: %v", err)
	}
	const want = "将删除：.xinit/backup/20200101-000000\n" +
		"将删除：.xinit/backup/20200102-000000\n" +
		"将删除 2 份（保留最近 1 份；--dry-run，未执行）\n"
	if out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	for _, n := range names {
		// snapT 内部 os.Stat：目录集消失会在这里 Fatal。
		c, mt := snapT(t, proj, ".xinit/backup/"+n+"/f.txt")
		if c != before[n].content {
			t.Errorf("dry-run 改动了 %s 的内容：%q", n, c)
		}
		if !mt.Equal(before[n].mtime) {
			t.Errorf("dry-run 重写了 %s（内容相同也不许）：mtime %v → %v", n, before[n].mtime, mt)
		}
	}
}

// 用例 7（R6 顺序承重）：非 xinit 项目 → 报错，且不得凭空造出 .xinit/。
// 项目判据（os.Stat）必须先于 LockProject（后者内含 MkdirAll .xinit/）。
func TestPruneBackupsRejectsNonProjectWithoutCreatingRoot(t *testing.T) {
	proj := t.TempDir() // 空目录：没有 .xinit/

	_, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err == nil {
		t.Fatal("非 xinit 项目必须报错")
	}
	if !strings.Contains(err.Error(), "不是 xinit 项目") {
		t.Errorf("错误文案 %q 缺「不是 xinit 项目」", err.Error())
	}
	if pathExists(filepath.Join(proj, state.DirName)) {
		t.Error("非 xinit 项目不得被造出 .xinit/（os.Stat 判据必须先于 LockProject）")
	}
}

// 用例 8（R6 裁量）：`.xinit/` 在而 state.json 缺失 → 仍正常删（prune 不
// Load state：state 损坏时备份恰是救援资源），也不重建 state.json。
func TestPruneBackupsWorksWithoutStateJSON(t *testing.T) {
	proj := pruneProjT(t, "") // state.json 缺失
	mkBackupT(t, proj, "20200101-000000")
	mkBackupT(t, proj, "20200102-000000")

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err != nil {
		t.Fatalf("state.json 缺失也必须能跑：%v", err)
	}
	if want := "已删除：.xinit/backup/20200101-000000\n已删除 1 份（保留最近 1 份）\n"; out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	if pathExists(filepath.Join(proj, state.DirName, "state.json")) {
		t.Error("prune 不得造 state.json（R10）")
	}
}

// 用例 9（R10）：零状态写入——垃圾 state.json 在真删路径（K>0）前后
// 逐字节一致、mtime 也不得变（内容相同也不许重写，同 restore 的快照口径）。
func TestPruneBackupsWritesNoState(t *testing.T) {
	proj := pruneProjT(t, "{ this is not json\n")
	mkBackupT(t, proj, "20200101-000000")
	mkBackupT(t, proj, "20200102-000000")
	stateBefore, stateMtimeBefore := snapT(t, proj, ".xinit/state.json")

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if !strings.Contains(out, "已删除 1 份") {
		t.Fatalf("前置：本用例要求真删了一份；stdout = %q", out)
	}
	stateAfter, stateMtimeAfter := snapT(t, proj, ".xinit/state.json")
	if stateAfter != stateBefore {
		t.Error("prune 不得写 state.json（R10）")
	}
	if !stateMtimeAfter.Equal(stateMtimeBefore) {
		t.Errorf("prune 不得重写 state.json（R10）mtime %v → %v", stateMtimeBefore, stateMtimeAfter)
	}
}

// 用例 10（R9）：backup/ 不存在（.xinit/ 在）→ K=0、rc=0，且不得新建 backup/。
func TestPruneBackupsMissingBackupDirDoesNotCreateIt(t *testing.T) {
	proj := pruneProjT(t, "")

	out, err := pruneT(t, BackupPruneOptions{ProjectDir: proj, Keep: 1})
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if want := "无删除项（备份共 0 份，保留数 1 份）\n"; out != want {
		t.Errorf("stdout = %q，want %q", out, want)
	}
	if pathExists(backupPathT(proj)) {
		t.Error("backup/ 不存在时不得新建（R9）")
	}
}

// R1/R12：注册与旗标面——`backup prune` 可从 root 解析到，缺 --keep 时由
// cobra 的 required 门在 RunE 之前拦住（RunE 里 ProjectDir="." 会在真实
// cwd 上执行，故不做经 root 的成功路径实跑）。
func TestBackupPruneCmdRequiresKeep(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"backup", "prune"})
	if err != nil {
		t.Fatalf("root.Find backup prune: %v（注册面）", err)
	}
	if cmd.Name() != "prune" {
		t.Fatalf("解析到 %q，want prune", cmd.Name())
	}
	if cmd.Flags().Lookup("dry-run") == nil {
		t.Error("缺 --dry-run 旗标（R12）")
	}

	root.SetArgs([]string{"backup", "prune"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "required flag") {
		t.Errorf("缺 --keep 必须被 required 门拦住（rc=1）；err = %v", err)
	}
}
