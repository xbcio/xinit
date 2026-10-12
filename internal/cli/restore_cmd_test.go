package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

// snapT 取「内容 + mtime」快照（R6 零状态写入断言用）：字节对照之外，mtime
// 还能逮到「用相同内容重写」这类字节看不见的写——st.Save 对同一 state 是
// 确定性输出（state.go:111-123），只有 mtime 能看见它。
func snapT(t *testing.T, dir, rel string) (string, time.Time) {
	t.Helper()
	fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return mustContent(t, dir, rel), fi.ModTime()
}

// fileStateT 造一条记账项（hash 按内容现算；restore 不读 hash，但夹具保持真实）。
func fileStateT(rel, content string, exec bool) state.FileState {
	return state.FileState{
		Src:  rel,
		Hash: fsutil.CompareHash([]byte(content), false),
		Exec: exec,
	}
}

// restoreProjectT 造最小受管项目（无模板、无网络）：.xinit/state.json（记账）
// + .xinit/base/（base 树）。base 里的文件一律落 0o644——exec 位只认记账
// （R13：base 文件自身的 mode 不是数据源），夹具刻意让两者不一致以钉住这一点。
func restoreProjectT(t *testing.T, files map[string]state.FileState, base map[string]string) string {
	t.Helper()
	proj := projectWithState(t, state.TemplateRef{Source: "/nonexistent", Kind: "dir"}, files)
	for rel, content := range base {
		full := filepath.Join(state.BaseDir(proj), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proj
}

// 用例 1（R1/R5/R13）：找回被删路径——字节恢复、父目录建出、exec 位按记账
// 置 0755（base 文件自己是 0644，实现若从 base 读 mode 即红）。
// 写路径同样钉零状态写入（R6）：state.json 与 base 树在写前后逐字节一致、
// mtime 也不得变（用例 2 只覆盖 no-op 路径，写路径此前是判别缺口）。
func TestRestoreBringsBackDeletedFile(t *testing.T) {
	const content = "#!/bin/sh\necho hi\n"
	proj := restoreProjectT(t,
		map[string]state.FileState{"scripts/run.sh": fileStateT("scripts/run.sh", content, true)},
		map[string]string{"scripts/run.sh": content})

	stateBefore, stateMtimeBefore := snapT(t, proj, ".xinit/state.json")
	baseBefore, baseMtimeBefore := snapT(t, proj, ".xinit/base/scripts/run.sh")

	var err error
	out := captureStdoutT(t, func() {
		err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"scripts/run.sh"}})
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := mustContent(t, proj, "scripts/run.sh"); got != content {
		t.Errorf("恢复的内容 = %q，want %q", got, content)
	}
	fi, err := os.Stat(filepath.Join(proj, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("exec 记账为真时必须置可执行位（R5/R13）；mode = %v", fi.Mode())
	}
	if out != "已恢复：scripts/run.sh\n" {
		t.Errorf("stdout = %q，want %q", out, "已恢复：scripts/run.sh\n")
	}
	stateAfter, stateMtimeAfter := snapT(t, proj, ".xinit/state.json")
	if stateAfter != stateBefore {
		t.Error("写路径也不得改 state.json（R6）")
	}
	if !stateMtimeAfter.Equal(stateMtimeBefore) {
		t.Errorf("写路径也不得重写 state.json（R6；内容相同也不许——st.Save 的确定性输出下字节对照看不见）mtime %v → %v",
			stateMtimeBefore, stateMtimeAfter)
	}
	baseAfter, baseMtimeAfter := snapT(t, proj, ".xinit/base/scripts/run.sh")
	if baseAfter != baseBefore {
		t.Error("写路径也不得改 base 树（R6）")
	}
	if !baseMtimeAfter.Equal(baseMtimeBefore) {
		t.Errorf("写路径也不得重写 base 树（R6）mtime %v → %v", baseMtimeBefore, baseMtimeAfter)
	}
}

// 用例 2（R1/R6）：幂等——内容与 exec 位都已一致 → 「无变化」、rc=0，
// 且 state.json 与 base 树一个字节都不动。
func TestRestoreIsIdempotentAndWritesNoState(t *testing.T) {
	const content = "hello\n"
	proj := restoreProjectT(t,
		map[string]state.FileState{"a.txt": fileStateT("a.txt", content, false)},
		map[string]string{"a.txt": content})
	writeFilesT(t, proj, map[string]string{"a.txt": content})

	stateBefore := mustContent(t, proj, ".xinit/state.json")
	baseBefore := mustContent(t, proj, ".xinit/base/a.txt")

	var err error
	out := captureStdoutT(t, func() {
		err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt"}})
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if out != "无变化：a.txt\n" {
		t.Errorf("stdout = %q，want %q", out, "无变化：a.txt\n")
	}
	if got := mustContent(t, proj, "a.txt"); got != content {
		t.Errorf("无变化路径的内容被改动：%q", got)
	}
	if got := mustContent(t, proj, ".xinit/state.json"); got != stateBefore {
		t.Error("restore 不得写 state.json（R6）")
	}
	if got := mustContent(t, proj, ".xinit/base/a.txt"); got != baseBefore {
		t.Error("restore 不得写 base 树（R6）")
	}
}

// 用例 3（R1/R13）：内容一致、仅 exec 位不符 → 重写对齐（两个方向）。
func TestRestoreAlignsExecBit(t *testing.T) {
	const content = "x\n"
	cases := []struct {
		name     string
		wantExec bool
		diskMode os.FileMode
	}{
		{"补上缺失的可执行位", true, 0o644},
		{"清掉多余的可执行位", false, 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := restoreProjectT(t,
				map[string]state.FileState{"p.sh": fileStateT("p.sh", content, tc.wantExec)},
				map[string]string{"p.sh": content})
			writeFilesT(t, proj, map[string]string{"p.sh": content})
			if err := os.Chmod(filepath.Join(proj, "p.sh"), tc.diskMode); err != nil {
				t.Fatal(err)
			}

			var err error
			out := captureStdoutT(t, func() {
				err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"p.sh"}})
			})
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if out != "已恢复：p.sh\n" {
				t.Errorf("stdout = %q，want %q（仅 exec 位不符按恢复处理）", out, "已恢复：p.sh\n")
			}
			fi, err := os.Stat(filepath.Join(proj, "p.sh"))
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode()&0o111 != 0; got != tc.wantExec {
				t.Errorf("对齐后 exec 位 = %v，want %v（mode = %v）", got, tc.wantExec, fi.Mode())
			}
			if got := mustContent(t, proj, "p.sh"); got != content {
				t.Errorf("内容被改动：%q", got)
			}
		})
	}
}

// §22.6 注入用例（Minor-2；导出 var 先例 check_test.go:449 / update_test.go:739）：
// 无 x 位语义平台（Windows）——记账 exec=true 而磁盘没有可执行位时不判「不符」，
// 否则该路径每轮都判「待恢复」→ 反复重写、幂等性破坏（execAligned 的短路）。
//
// 两段结构：**对照前置**（本平台有 x 位语义时同一夹具必判「不符」并重写——防
// 注入段因夹具恒真而假阳性）+ **注入段**（ExecSupported=false 时同夹具判
// 「无变化」、不重写、不 exec 位、mtime 不变）。
func TestRestoreSkipsExecAlignmentWithoutExecSemantics(t *testing.T) {
	const content = "x\n"
	// 夹具：内容一致、记账 exec=true、磁盘无可执行位（0644）。
	mk := func(t *testing.T) string {
		t.Helper()
		proj := restoreProjectT(t,
			map[string]state.FileState{"p.sh": fileStateT("p.sh", content, true)},
			map[string]string{"p.sh": content})
		writeFilesT(t, proj, map[string]string{"p.sh": content})
		return proj
	}

	// 对照前置：本平台（darwin/linux，exec_unix.go）有 x 位语义。
	if !fsutil.ExecSupported {
		t.Fatal("对照前置要求本平台有 x 位语义；夹具前提不成立")
	}
	proj := mk(t)
	var err error
	// t.Fatal 不进闭包（captureStdoutT 靠闭包返回后还原 os.Stdout）。
	out := captureStdoutT(t, func() { err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"p.sh"}}) })
	if err != nil {
		t.Fatalf("对照前置 Restore: %v", err)
	}
	if out != "已恢复：p.sh\n" {
		t.Fatalf("对照前置：同夹具在本平台必判 exec 不符并重写；stdout = %q", out)
	}

	old := fsutil.ExecSupported
	defer func() { fsutil.ExecSupported = old }() // 精确还原（defer 纪律）
	fsutil.ExecSupported = false

	proj2 := mk(t)
	before, mtimeBefore := snapT(t, proj2, "p.sh")
	out2 := captureStdoutT(t, func() { err = Restore(RestoreOptions{ProjectDir: proj2, Paths: []string{"p.sh"}}) })
	if err != nil {
		t.Fatalf("Restore（注入）: %v", err)
	}
	if out2 != "无变化：p.sh\n" {
		t.Errorf("无 x 位语义平台不应判 exec 不符：stdout = %q", out2)
	}
	after, mtimeAfter := snapT(t, proj2, "p.sh")
	if after != before {
		t.Errorf("内容被改动：%q", after)
	}
	if !mtimeAfter.Equal(mtimeBefore) {
		t.Errorf("无 x 位语义平台不得重写该路径（否则每轮都写、幂等性破坏）：mtime %v → %v",
			mtimeBefore, mtimeAfter)
	}
	fi, err := os.Stat(filepath.Join(proj2, "p.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 != 0 {
		t.Errorf("无 x 位语义平台不得动 exec 位：mode = %v", fi.Mode())
	}
}

// 用例 4（R1）：存在且内容不同 + 无 --force → 错误、目标原封不动、无任何输出。
func TestRestoreRefusesDifferentContentWithoutForce(t *testing.T) {
	proj := restoreProjectT(t,
		map[string]state.FileState{"a.txt": fileStateT("a.txt", "base\n", false)},
		map[string]string{"a.txt": "base\n"})
	writeFilesT(t, proj, map[string]string{"a.txt": "local\n"})

	var err error
	out := captureStdoutT(t, func() {
		err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt"}})
	})
	if err == nil {
		t.Fatal("内容不同的现有目标必须默认拒绝")
	}
	for _, want := range []string{"a.txt", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案 %q 缺 %q", err.Error(), want)
		}
	}
	if out != "" {
		t.Errorf("拒绝时不该有 stdout 输出：%q", out)
	}
	if got := mustContent(t, proj, "a.txt"); got != "local\n" {
		t.Errorf("拒绝后目标被改动：%q", got)
	}
}

// 用例 5（R1）：--force 覆盖成功——内容重置回 base 的字节。
func TestRestoreForceOverwrites(t *testing.T) {
	proj := restoreProjectT(t,
		map[string]state.FileState{"a.txt": fileStateT("a.txt", "base\n", false)},
		map[string]string{"a.txt": "base\n"})
	writeFilesT(t, proj, map[string]string{"a.txt": "local\n"})

	var err error
	out := captureStdoutT(t, func() {
		err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt"}, Force: true})
	})
	if err != nil {
		t.Fatalf("Restore --force: %v", err)
	}
	if got := mustContent(t, proj, "a.txt"); got != "base\n" {
		t.Errorf("--force 后内容 = %q，want %q", got, "base\n")
	}
	if out != "已恢复：a.txt\n" {
		t.Errorf("stdout = %q，want %q", out, "已恢复：a.txt\n")
	}
}

// 用例 6（R2）：预检 all-or-nothing——多路径里任一不过，已通过预检的路径也
// 一个字节都不写（拒绝项与未记账项两种成因各测一遍）。
func TestRestorePrecheckWritesNothingOnAnyReject(t *testing.T) {
	t.Run("拒绝覆盖", func(t *testing.T) {
		proj := restoreProjectT(t,
			map[string]state.FileState{
				"a.txt": fileStateT("a.txt", "base-a\n", false),
				"b.txt": fileStateT("b.txt", "base-b\n", false),
			},
			map[string]string{"a.txt": "base-a\n", "b.txt": "base-b\n"})
		writeFilesT(t, proj, map[string]string{"b.txt": "local-b\n"})

		if err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt", "b.txt"}}); err == nil {
			t.Fatal("含拒绝项时必须整体失败")
		}
		if _, err := os.Stat(filepath.Join(proj, "a.txt")); !os.IsNotExist(err) {
			t.Error("整体失败时先前的待恢复路径也不得写入（all-or-nothing）")
		}
		if got := mustContent(t, proj, "b.txt"); got != "local-b\n" {
			t.Errorf("拒绝路径被改动：%q", got)
		}
	})
	t.Run("未记账", func(t *testing.T) {
		proj := restoreProjectT(t,
			map[string]state.FileState{"a.txt": fileStateT("a.txt", "base-a\n", false)},
			map[string]string{"a.txt": "base-a\n"})

		if err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt", "nope.txt"}}); err == nil {
			t.Fatal("含未记账路径时必须整体失败")
		}
		if _, err := os.Stat(filepath.Join(proj, "a.txt")); !os.IsNotExist(err) {
			t.Error("整体失败时先前的待恢复路径也不得写入（all-or-nothing）")
		}
	})
}

// 用例 7（R2）：非法路径（§22.4 单点谓词）——`..` 段 / 绝对路径 / 反斜杠 /
// 空串一律拒绝，且拒绝时不产生任何磁盘写入。
func TestRestoreRejectsIllegalPaths(t *testing.T) {
	for _, p := range []string{"../x", "/abs/x", `a\b`, ""} {
		t.Run(p, func(t *testing.T) {
			proj := restoreProjectT(t, map[string]state.FileState{}, nil)
			err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{p}})
			if err == nil {
				t.Fatalf("非法路径 %q 必须拒绝", p)
			}
			if !strings.Contains(err.Error(), "非法") {
				t.Errorf("错误文案 %q 应点明「非法」", err.Error())
			}
		})
	}
	// `../x` 的越界落点必须不存在（拒绝发生在写之前）。
	t.Run("越界落点不存在", func(t *testing.T) {
		proj := restoreProjectT(t,
			map[string]state.FileState{"x": fileStateT("x", "x\n", false)},
			map[string]string{"x": "x\n"})
		_ = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"../x"}})
		if _, err := os.Stat(filepath.Join(filepath.Dir(proj), "x")); !os.IsNotExist(err) {
			t.Error("非法路径不得写出项目根之外")
		}
	})
}

// 用例 8（R2）：未记账路径 → 错误并点明「restore 只能恢复记账路径」。
func TestRestoreRejectsUnaccountedPath(t *testing.T) {
	proj := restoreProjectT(t, map[string]state.FileState{}, nil)
	err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"nope.txt"}})
	if err == nil {
		t.Fatal("未记账路径必须拒绝")
	}
	if !strings.Contains(err.Error(), "restore 只能恢复记账路径") {
		t.Errorf("错误文案 %q 缺「restore 只能恢复记账路径」", err.Error())
	}
}

// 用例 9（R3）：记账有、base 树里没有 → 错误并指引重跑 update（不重建 base）。
func TestRestoreMissingBasePointsToUpdate(t *testing.T) {
	proj := restoreProjectT(t,
		map[string]state.FileState{"gone.txt": fileStateT("gone.txt", "x\n", false)}, nil)

	err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"gone.txt"}})
	if err == nil {
		t.Fatal("base 单文件缺失必须报错")
	}
	if !strings.Contains(err.Error(), "update") || !strings.Contains(err.Error(), "gone.txt") {
		t.Errorf("错误文案 %q 应含路径与 update 指引", err.Error())
	}
	if _, err := os.Stat(filepath.Join(proj, "gone.txt")); !os.IsNotExist(err) {
		t.Error("base 缺失时不得写入目标")
	}
	// --force 不改变这一判据：base 是数据源，缺了就没得恢复。
	if err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"gone.txt"}, Force: true}); err == nil {
		t.Error("--force 也不得绕过 base 缺失")
	}
}

// 用例 10（R8）：重复参数去重保首现序——`a a b` 输出 a、b 各一次且按此序。
func TestRestoreDedupesKeepingFirstOrder(t *testing.T) {
	proj := restoreProjectT(t,
		map[string]state.FileState{
			"a.txt": fileStateT("a.txt", "a\n", false),
			"b.txt": fileStateT("b.txt", "b\n", false),
		},
		map[string]string{"a.txt": "a\n", "b.txt": "b\n"})

	var err error
	out := captureStdoutT(t, func() {
		err = Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt", "a.txt", "b.txt"}})
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if out != "已恢复：a.txt\n已恢复：b.txt\n" {
		t.Errorf("stdout = %q，want %q", out, "已恢复：a.txt\n已恢复：b.txt\n")
	}
}

// 用例 11：没有 .xinit/（非 xinit 项目）→ Load 错误，且不凭空造出 .xinit/。
func TestRestoreFailsWithoutState(t *testing.T) {
	proj := t.TempDir()
	if err := Restore(RestoreOptions{ProjectDir: proj, Paths: []string{"a.txt"}}); err == nil {
		t.Fatal("非 xinit 项目必须报错")
	}
	if _, err := os.Stat(filepath.Join(proj, state.DirName)); !os.IsNotExist(err) {
		t.Error("非 xinit 项目不得被造出 .xinit/（Load 先于取锁）")
	}
}

// R16：无参数报用法（cobra.MinimumNArgs(1)），不落到 RunE。经 root 执行，
// 顺带钉住注册（root.go 的 AddCommand）。
func TestRestoreCmdRequiresPathArg(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"restore"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil {
		t.Error("restore 无参数必须报用法错误")
	}
}
