package renderstore

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/state"
)

// fetchFixture 预置一条有效条目（engine "e1" / dir 源 / commit c1 / vars{a:1}）：
// a.txt 是 LF 文本；b.txt 的**原文**是 CRLF（供归一化孪生负例），记账 hash
// 用 CompareHash(raw, false)（归一化门）。
func fetchFixture(t *testing.T) (*state.State, string, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	st := baseState()
	rendered := map[string][]byte{
		"a.txt": []byte("alpha\n"),
		"b.txt": []byte("line1\r\nline2\r\n"),
	}
	files := make(map[string]state.FileState, len(rendered))
	for dst, raw := range rendered {
		files[dst] = state.FileState{Src: dst, Hash: fsutil.CompareHash(raw, false)}
	}
	st.Files = files
	if err := Write(st, root, rendered); err != nil {
		t.Fatalf("预置条目失败：%v", err)
	}
	return st, root, rendered
}

func fetchEntryDir(t *testing.T, st *state.State, root string) string {
	t.Helper()
	return entryDir(root, testKey(st))
}

// tamperManifest 现场改写条目清单（模拟损坏/篡改），供负例使用。
func tamperManifest(t *testing.T, dir string, mutate func(*manifest)) {
	t.Helper()
	p := filepath.Join(dir, manifestName)
	m, err := readManifest(p)
	if err != nil {
		t.Fatalf("读清单: %v", err)
	}
	mutate(m)
	data, err := m.encode()
	if err != nil {
		t.Fatalf("编码清单: %v", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("写清单: %v", err)
	}
}

func TestFetchHit(t *testing.T) {
	st, root, rendered := fetchFixture(t)
	got := Fetch(st, root, []string{"a.txt", "b.txt"})
	if len(got) != 2 {
		t.Fatalf("应全部命中：%v", got)
	}
	for dst, raw := range rendered {
		if !bytes.Equal(got[dst], raw) {
			t.Errorf("%s 命中字节必须逐字节等于当轮渲染原文：got %q", dst, got[dst])
		}
	}
}

func TestFetchManifestFieldMismatchesMiss(t *testing.T) {
	// 坏 version / engine / kind / source / anchor / vars_sha256 → 整体静默 miss。
	cases := []struct {
		name   string
		mutate func(*manifest)
	}{
		{"version", func(m *manifest) { m.Version = 2 }},
		{"engine", func(m *manifest) { m.Engine = "e2" }},
		{"kind", func(m *manifest) { m.Kind = "git" }},
		{"source", func(m *manifest) { m.Source = "/other" }},
		{"anchor", func(m *manifest) { m.Anchor = "sha256/other" }},
		{"vars_sha256", func(m *manifest) { m.VarsSHA256 = strings.Repeat("0", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, root, _ := fetchFixture(t)
			tamperManifest(t, fetchEntryDir(t, st, root), tc.mutate)
			if got := Fetch(st, root, []string{"a.txt", "b.txt"}); got != nil {
				t.Errorf("清单 %s 不符 → 必须全 miss，得 %v", tc.name, got)
			}
		})
	}
}

func TestFetchVarsChangeMisses(t *testing.T) {
	// 写后改 st.Vars（现算键与 vars_sha256 双双不符）→ 全 miss。
	st, root, _ := fetchFixture(t)
	st.Vars["a"] = "2"
	if got := Fetch(st, root, []string{"a.txt", "b.txt"}); got != nil {
		t.Errorf("vars 变化必须 miss，得 %v", got)
	}
}

func TestFetchTamperedRawMissesOnlyThatPath(t *testing.T) {
	// tree/ 被改一字节 → 该路径 miss、其余命中（raw 门 + 逐路径恢复语义）。
	st, root, _ := fetchFixture(t)
	treeB := filepath.Join(fetchEntryDir(t, st, root), treeDirName, "b.txt")
	if err := os.WriteFile(treeB, []byte("line1\r\nlineX\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Fetch(st, root, []string{"a.txt", "b.txt"})
	if len(got) != 1 || got["a.txt"] == nil {
		t.Fatalf("a.txt 应仍命中、b.txt 应 miss：%v", got)
	}
	if _, ok := got["b.txt"]; ok {
		t.Error("被篡改的路径必须 miss")
	}
}

func TestFetchRecordHashMismatchMissesThatPath(t *testing.T) {
	// 记录门：state 侧 hash 与快照原文不符 → 该路径 miss（update_test.go:339
	// 金丝雀同款——命中路径必须保留逐路径归一化比对）。
	st, root, _ := fetchFixture(t)
	st.Files["a.txt"] = state.FileState{Src: "a.txt", Hash: "sha256/normlf-v1:" + strings.Repeat("0", 64)}
	got := Fetch(st, root, []string{"a.txt", "b.txt"})
	if len(got) != 1 || got["b.txt"] == nil {
		t.Fatalf("b.txt 应命中、a.txt 应 miss：%v", got)
	}
	if _, ok := got["a.txt"]; ok {
		t.Error("记录不符的路径必须 miss")
	}
}

func TestFetchCRLFTwinMisses(t *testing.T) {
	// raw 门正面防线：把 CRLF 原文换成 LF 孪生——归一化 hash 不变、raw 变，
	// 只有 raw 门能拦下 → 必须 miss。
	st, root, _ := fetchFixture(t)
	lone := []byte("line1\nline2\n")
	if fsutil.CompareHash(lone, false) != st.Files["b.txt"].Hash {
		t.Fatal("夹具失效：孪生应能过归一化门（否则证不到 raw 门）")
	}
	treeB := filepath.Join(fetchEntryDir(t, st, root), treeDirName, "b.txt")
	if err := os.WriteFile(treeB, lone, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Fetch(st, root, []string{"b.txt"}); got != nil {
		t.Errorf("LF/CRLF 孪生必须被 raw 门拦下：%v", got)
	}
}

func TestFetchRejectsTraversalNeed(t *testing.T) {
	// CheckWithinBase 失败 = 按未命中处理：不硬错、不读 tree/、不写回（§21.6）。
	st, root, _ := fetchFixture(t)
	got := Fetch(st, root, []string{"a.txt", "../evil", "/etc/passwd"})
	if len(got) != 1 || got["a.txt"] == nil {
		t.Fatalf("合法路径应命中、坏路径应 miss：%v", got)
	}

	// 分支覆盖：篡改清单 + 记账，让坏路径"看起来"可命中——仍必须 miss
	// （若校验链缺 CheckWithinBase，它会读到 dir/outside.txt 并通过 raw 门）。
	dir := fetchEntryDir(t, st, root)
	outside := []byte("outside\n")
	if err := os.WriteFile(filepath.Join(dir, "outside.txt"), outside, 0o644); err != nil {
		t.Fatal(err)
	}
	tamperManifest(t, dir, func(m *manifest) {
		m.Files = append(m.Files, fileEntry{
			Dst:       "../outside.txt",
			RawSHA256: sha256Hex(outside),
			Size:      len(outside),
		})
	})
	st.Files["../outside.txt"] = state.FileState{Src: "../outside.txt", Hash: fsutil.CompareHash(outside, false)}
	if got := Fetch(st, root, []string{"../outside.txt"}); got != nil {
		t.Errorf("坏路径不得命中（CheckWithinBase 必须先于 tree/ 读取）：%v", got)
	}
}

func TestFetchZeroRootCreation(t *testing.T) {
	// 读 API 绝不创建缓存目录（§21.6；check 零建根契约的延伸）。
	st := baseState()
	st.Files = map[string]state.FileState{"a.txt": {Src: "a.txt"}}
	missing := filepath.Join(t.TempDir(), "never-created")
	if got := Fetch(st, missing, []string{"a.txt"}); got != nil {
		t.Errorf("缓存根不存在 → 全 miss：%v", got)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("读侧不得创建缓存根：%v", err)
	}
}

func TestFetchUnreadableCacheMisses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视权限位")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 权限位不生效（R9：Windows 运行期 park）")
	}
	st, root, _ := fetchFixture(t)

	// 探针实测（darwin/arm64，/tmp）：0500 = r-x，exact-path 读取照常成功；
	// 0400 去掉 x 才真正不可遍历 → 读 manifest 得 EACCES。brief 的 0500
	// 对 exact-path 读取不生效，故取 0400。
	parent := filepath.Join(root, renderDirName)
	if err := os.Chmod(parent, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) }) // 恢复，否则 TempDir 清理失败

	if got := Fetch(st, root, []string{"a.txt", "b.txt"}); got != nil {
		t.Errorf("缓存不可读必须静默 miss：%v", got)
	}
}
