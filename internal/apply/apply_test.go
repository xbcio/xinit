package apply

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
)

func mustPerm(t *testing.T, dir, rel string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func mustContent(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// exactNames 返回 dir 下条目的精确名字（os.ReadDir；区分大小写）。
// 「缺席」断言必须用它、不能用 os.Lstat：APFS 默认大小写不敏感，目录里只有
// LICENSE 时 Lstat(dir+"/license") 会命中它——那是文件系统语义，不是实现行为
// （裁定 Y，2026-10-09：原 Lstat 缺席断言在正确实现下恒红，于 APFS 实测复现）。
func exactNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestExecuteWritesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "old", 0o644)
	writeFile(t, dir, "run.sh", "#!/bin/sh\n", 0o755)

	res, err := Execute(dir, Changes{
		Writes: map[string][]byte{
			"a.txt":  []byte("new"),
			"run.sh": []byte("#!/bin/sh\necho hi\n"),
		},
		ExecBits: map[string]bool{"a.txt": false, "run.sh": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustContent(t, dir, "a.txt"); got != "new" {
		t.Errorf("a.txt = %q", got)
	}
	if got := mustPerm(t, dir, "a.txt"); got != 0o644 {
		t.Errorf("已有文件应保 ours mode + exec 调整：got %v", got)
	}
	if got := mustPerm(t, dir, "run.sh"); got != 0o755 {
		t.Errorf("run.sh exec 应保留：got %v", got)
	}
	if !slices.Equal(res.Written, []string{"a.txt", "run.sh"}) {
		t.Errorf("Written 应排序：%v", res.Written)
	}
}

// §10.1：已有文件按 §8.3 结论调整 exec——模板把可执行改成普通时必须清位。
func TestExecuteClearsExecWhenTemplatePlain(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "x.sh", "old", 0o755)

	if _, err := Execute(dir, Changes{
		Writes:   map[string][]byte{"x.sh": []byte("new")},
		ExecBits: map[string]bool{"x.sh": false},
	}); err != nil {
		t.Fatal(err)
	}
	if got := mustPerm(t, dir, "x.sh"); got != 0o644 {
		t.Errorf("got %v, want 0644", got)
	}
}

func TestExecuteNewFilesUseUmask(t *testing.T) {
	dir := t.TempDir()
	umask := currentUmask()

	if _, err := Execute(dir, Changes{
		Writes:   map[string][]byte{"plain.txt": []byte("p"), "bin/x.sh": []byte("x")},
		ExecBits: map[string]bool{"plain.txt": false, "bin/x.sh": true},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := mustPerm(t, dir, "plain.txt"), os.FileMode(0o666&^umask); got != want {
		t.Errorf("plain.txt perm = %v, want %v", got, want)
	}
	if got, want := mustPerm(t, dir, "bin/x.sh"), os.FileMode(0o777&^umask); got != want {
		t.Errorf("x.sh perm = %v, want %v", got, want)
	}
}

// §8.3「不动」：没有 exec 结论的写入路径（settle 第二分支——用户改过权限）
// → 写内容时连 exec 一起保持磁盘原样。
func TestExecuteWriteKeepsDiskExecWithoutTarget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "s.sh", "old", 0o755)

	if _, err := Execute(dir, Changes{
		Writes: map[string][]byte{"s.sh": []byte("new")}, // ExecBits 未给
	}); err != nil {
		t.Fatal(err)
	}
	if got := mustContent(t, dir, "s.sh"); got != "new" {
		t.Errorf("内容应更新：%q", got)
	}
	if got := mustPerm(t, dir, "s.sh"); got != 0o755 {
		t.Errorf("没有 exec 结论时不得动磁盘 exec：got %v, want 0755", got)
	}
}

// R7/R17：不写内容、只对齐 exec（模板只改了 exec 位 → 行 1/3 Noop）。
func TestExecuteChmodOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "m.txt", "same", 0o644)

	res, err := Execute(dir, Changes{ExecBits: map[string]bool{"m.txt": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustPerm(t, dir, "m.txt"); got != 0o755 {
		t.Errorf("got %v, want 0755（只动 exec 位）", got)
	}
	if !slices.Equal(res.Chmodded, []string{"m.txt"}) {
		t.Errorf("Chmodded = %v", res.Chmodded)
	}
	if got := mustContent(t, dir, "m.txt"); got != "same" {
		t.Errorf("chmod 通道不得改内容：%q", got)
	}

	// 反向：再跑一次恢复 0644（幂等）
	if _, err := Execute(dir, Changes{ExecBits: map[string]bool{"m.txt": false}}); err != nil {
		t.Fatal(err)
	}
	if got := mustPerm(t, dir, "m.txt"); got != 0o644 {
		t.Errorf("got %v, want 0644", got)
	}
}

// §22.6：无 x 位语义平台（Windows）adjustExec 真实现 no-op——ExecBits 恒空后
// 本不可达，保留为纵深防御：不触磁盘、mode 原样。
func TestAdjustExecUnsupportedNoop(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "m.txt", "same", 0o644)

	old := fsutil.ExecSupported
	defer func() { fsutil.ExecSupported = old }()
	fsutil.ExecSupported = false

	changed, err := adjustExec(dir, "m.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("无 x 位语义平台必须 no-op（changed=false）")
	}
	if got := mustPerm(t, dir, "m.txt"); got != 0o644 {
		t.Errorf("mode 不得变：got %v, want 0644", got)
	}

	// 对照：恢复 true 后同一调用真实调整（确认夹具与调用面到位）。
	fsutil.ExecSupported = true
	changed, err = adjustExec(dir, "m.txt", true)
	if err != nil || !changed {
		t.Fatalf("对照：adjustExec = %v,%v want true,nil", changed, err)
	}
	if got := mustPerm(t, dir, "m.txt"); got != 0o755 {
		t.Errorf("对照：got %v, want 0755", got)
	}
}

// 先删后写：折叠改名场景（许可文件 license → LICENSE）。在大小写不敏感的文件系统上，
// 顺序颠倒会先写 LICENSE（打在 license 上）再删 license（删掉刚写的文件）。
func TestExecuteDeleteThenWriteOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "license", "旧", 0o644)

	res, err := Execute(dir, Changes{
		Deletes:  []string{"license"},
		Writes:   map[string][]byte{"LICENSE": []byte("新")},
		ExecBits: map[string]bool{"LICENSE": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustContent(t, dir, "LICENSE"); got != "新" {
		t.Errorf("LICENSE = %q，先删后写顺序被破坏", got)
	}
	// 精确条目名判断（裁定 Y）：APFS 默认大小写不敏感，目录里只有 LICENSE 时
	// Lstat("license") 会命中它——「缺席」断言不能用 Lstat（正确实现下恒红）。
	if slices.Contains(exactNames(t, dir), "license") {
		t.Error("license 应已被删除")
	}
	if !slices.Equal(res.Deleted, []string{"license"}) {
		t.Errorf("Deleted = %v", res.Deleted)
	}
}

// 验收 ⑤ 的另一半：未跟踪的 license 挡着 LICENSE → 拒绝、磁盘不动、其余路径照写。
func TestExecuteRefusedWriteKeepsDisk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "license", "用户的", 0o644)

	res, err := Execute(dir, Changes{
		Writes:   map[string][]byte{"LICENSE": []byte("模板"), "ok.txt": []byte("o")},
		ExecBits: map[string]bool{"LICENSE": false, "ok.txt": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != 1 || res.Refusals[0].Path != "LICENSE" {
		t.Fatalf("Refusals = %v", res.Refusals)
	}
	if got := mustContent(t, dir, "license"); got != "用户的" {
		t.Errorf("被拒路径的磁盘内容不得改变：%q", got)
	}
	// 精确条目名判断（裁定 Y）：被拒路径未被写出。
	if slices.Contains(exactNames(t, dir), "LICENSE") {
		t.Error("LICENSE 不该被写出")
	}
	if got := mustContent(t, dir, "ok.txt"); got != "o" {
		t.Error("被拒路径之外的写入必须继续（§9.3 不中止）")
	}
}

func TestExecuteConflictsRebuild(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xinit/conflicts/a.txt", "旧冲突", 0o644)
	writeFile(t, dir, ".xinit/conflicts/b/x.txt", "旧冲突", 0o644)

	res, err := Execute(dir, Changes{Conflicts: map[string][]byte{
		"b/x.txt": []byte("新 theirs"),
		"c.txt":   []byte("新冲突"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustContent(t, dir, ".xinit/conflicts/b/x.txt"); got != "新 theirs" {
		t.Errorf("仍在冲突的条目应重写：%q", got)
	}
	if got := mustContent(t, dir, ".xinit/conflicts/c.txt"); got != "新冲突" {
		t.Errorf("新冲突条目应写入：%q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".xinit/conflicts/a.txt")); !os.IsNotExist(err) {
		t.Error("已解决条目应删除")
	}
	if !slices.Equal(res.Resolved, []string{"a.txt"}) {
		t.Errorf("Resolved = %v", res.Resolved)
	}
}

// TestForecastMatchesExecuteRefusals 是 I10 的防漂移回归闸（设计稿 §3-M0a，
// 评审 WN-7）：dry-run 的拒绝预测（Forecast）与实际落盘（Execute 步骤 1-2）
// 必须给出逐条相等的拒绝集。M0a 之后两侧共用 forecast 单点，本测试钉的是
// 「此后任一侧改回独立实现/改叶子调用即红」——它钉两侧漂移，不钉单点内部
// 文案（单点内的文案语义由既有 check_test.go 与 txtar 面兜底）。
//
// M3a（R-M3a-4 合并增强）：扩输入域——追加第二组 fail-closed 与「不穿透」
// 场景（悬空链接、指向项目内的中间段链接 R16、路径本身符号链接）；两径
// 相等断言抽 mustExecuteAgreeWithForecast 供两组共用。
func TestForecastMatchesExecuteRefusals(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link-dir")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "blocker", "我是文件", 0o644)
	writeFile(t, dir, "license", "用户的", 0o644)
	writeFile(t, dir, "trash.txt", "旧", 0o644)

	changes := Changes{
		// 删除段 §9.5：父路径外指 → 拒绝；trash.txt 是放行的正常删除。
		Deletes: []string{"link-dir/old.txt", "trash.txt"},
		Writes: map[string][]byte{
			"LICENSE":           []byte("模板"), // 折叠：未跟踪的 license 挡道
			"blocker/child.txt": []byte("x"),  // 障碍物：父路径是常规文件
			"link-dir/new.txt":  []byte("y"),  // 写入段 §9.5：父路径外指
			"ok.txt":            []byte("o"),  // 干净路径：不得被牵连
		},
		ExecBits: map[string]bool{"LICENSE": false, "ok.txt": false},
	}

	// Forecast 是只读预测：先跑它、再跑 Execute——预测若动了盘，Execute 看到的
	// 输入就与预测不同，两径相等这一断言本身即被破坏。
	predicted, err := Forecast(dir, changes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "ok.txt")); !os.IsNotExist(err) {
		t.Fatal("Forecast 不得落盘")
	}
	if _, err := os.Lstat(filepath.Join(dir, "trash.txt")); err != nil {
		t.Fatalf("Forecast 不得落盘：%v", err)
	}

	// 夹具自检：四类拒绝都得真出现，且按 Path 排序（否则相等断言会退化为
	// 对空集/单条的空洞通过）。
	for _, want := range []struct{ path, reasonContains string }{
		{"LICENSE", "折叠等价"},           // 折叠（§9.3）
		{"blocker/child.txt", "常规文件"}, // 障碍物（§9.3）
		{"link-dir/new.txt", "项目根之外"}, // 写入段 §9.5
		{"link-dir/old.txt", "项目根之外"}, // 删除段 §9.5
	} {
		r, ok := refusalFor(t, predicted, want.path)
		if !ok || !strings.Contains(r.Reason, want.reasonContains) {
			t.Fatalf("夹具自检失败：%s 应被拒且理由含 %q，got %+v", want.path, want.reasonContains, r)
		}
	}
	if got := refusalPaths(predicted); !slices.Equal(got, []string{"LICENSE", "blocker/child.txt", "link-dir/new.txt", "link-dir/old.txt"}) {
		t.Fatalf("拒绝集应按 Path 排序且只含夹具构造的四条：%v", got)
	}

	mustExecuteAgreeWithForecast(t, dir, changes, predicted)
	// 拒绝之外的路径照常落盘（§9.3 不中止其余路径）——确认执行确实走到了写段。
	if got := mustContent(t, dir, "ok.txt"); got != "o" {
		t.Errorf("ok.txt = %q", got)
	}

	// 第二组（M3a 扩输入域）：fail-closed 与「不穿透」三变体——悬空链接
	// （EvalSymlinks 失败）、指向项目内的中间段链接（R16）、路径本身是链接。
	dir2 := t.TempDir()
	if err := os.Symlink("nowhere", filepath.Join(dir2, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir2, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir2, "in-link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir2, "real.txt", "x", 0o644)
	if err := os.Symlink("real.txt", filepath.Join(dir2, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	changes2 := Changes{
		Writes: map[string][]byte{
			"dangling/x.txt": []byte("a"),
			"in-link/f.txt":  []byte("b"),
			"alias.txt":      []byte("c"),
			"fresh.txt":      []byte("d"), // 干净路径：不得被牵连
		},
	}
	predicted2, err := Forecast(dir2, changes2)
	if err != nil {
		t.Fatal(err)
	}
	// 夹具自检：三类拒绝都真出现（否则相等断言会退化为空洞通过）。
	for _, want := range []struct{ path, reasonContains string }{
		{"dangling/x.txt", "无法安全解析"},
		{"in-link/f.txt", "符号链接"},
		{"alias.txt", "符号链接"},
	} {
		r, ok := refusalFor(t, predicted2, want.path)
		if !ok || !strings.Contains(r.Reason, want.reasonContains) {
			t.Fatalf("第二组夹具自检失败：%s 应被拒且理由含 %q，got %+v", want.path, want.reasonContains, r)
		}
	}
	if got := refusalPaths(predicted2); !slices.Equal(got, []string{"alias.txt", "dangling/x.txt", "in-link/f.txt"}) {
		t.Fatalf("第二组拒绝集应按 Path 排序且只含三条：%v", got)
	}
	mustExecuteAgreeWithForecast(t, dir2, changes2, predicted2)
	if got := mustContent(t, dir2, "fresh.txt"); got != "d" {
		t.Errorf("fresh.txt = %q", got)
	}
}

// mustExecuteAgreeWithForecast 断言 Execute 的拒绝集与 Forecast 预测逐条相等
// （Path/Reason 逐字且同序）——M3a 起供 TestForecastMatchesExecuteRefusals
// 的两组输入共用（R-M3a-4 合并增强的抽取）。
func mustExecuteAgreeWithForecast(t *testing.T, dir string, changes Changes, predicted []Refusal) {
	t.Helper()
	res, err := Execute(dir, changes)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != len(predicted) {
		t.Fatalf("拒绝集条数漂移：Execute=%d Forecast=%d\nExecute=%v\nForecast=%v",
			len(res.Refusals), len(predicted), res.Refusals, predicted)
	}
	for i := range predicted {
		if res.Refusals[i] != predicted[i] {
			t.Errorf("第 %d 条拒绝漂移（Path/Reason 须逐字且同序）：\nExecute =%+v\nForecast=%+v",
				i, res.Refusals[i], predicted[i])
		}
	}
}

// refusalPaths 按顺序取拒绝集的 Path（夹具自检与相等断言的诊断用）。
func refusalPaths(rs []Refusal) []string {
	paths := make([]string, 0, len(rs))
	for _, r := range rs {
		paths = append(paths, r.Path)
	}
	return paths
}

// 评审 I-1（T1 fix 轮）（a）：手搓重叠输入「ExecBits ∩ Deletes 且删除段被 §9.5 拒」。
// HEAD 的 refused 是合并拒绝集（含删除段），故步骤 5 跳过、不穿透外指符号链接；
// BASE 的 refused 只含 CheckWrites 键，会经 link-dir 把**项目根之外**的 old.txt
// chmod（0755→0644）。本测试钉死新语义（更安全方向）。该输入 CLI 不可达：
// plan 每路径一 Action，buildChanges/execConclusion 保证 Deletes 与 ExecBits 互斥。
func TestExecuteRefusedDeleteNotChmoddedThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "old.txt", "根外文件", 0o755)
	if err := os.Symlink(outside, filepath.Join(dir, "link-dir")); err != nil {
		t.Fatal(err)
	}

	res, err := Execute(dir, Changes{
		Deletes:  []string{"link-dir/old.txt"},               // 删除段 §9.5：父路径外指 → 拒绝
		ExecBits: map[string]bool{"link-dir/old.txt": false}, // 重叠输入（CLI 不可达）
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != 1 || res.Refusals[0].Path != "link-dir/old.txt" {
		t.Fatalf("删除段 §9.5 应拒绝该路径：%v", res.Refusals)
	}
	if len(res.Chmodded) != 0 {
		t.Errorf("被拒的重叠路径不得做纯 exec 变更：Chmodded=%v", res.Chmodded)
	}
	if got := mustPerm(t, outside, "old.txt"); got != 0o755 {
		t.Errorf("项目根之外的文件权限被改动：got %v, want 0755", got)
	}
}

// §10.4：冲突全清后连 .xinit/conflicts/ 目录一起消失（「存在即未解决冲突」）。
func TestExecuteConflictsEmptyRemovesTree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".xinit/conflicts/a.txt", "旧", 0o644)

	res, err := Execute(dir, Changes{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".xinit", "conflicts")); !os.IsNotExist(err) {
		t.Error("空 conflicts 目录应被移除")
	}
	if !slices.Equal(res.Resolved, []string{"a.txt"}) {
		t.Errorf("Resolved = %v", res.Resolved)
	}
}
