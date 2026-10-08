package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/xbcio/xinit/internal/fsutil"
	"github.com/xbcio/xinit/internal/manifest"
)

// goldenRoot 是夹具目录。每个子目录的布局：
//
//	<case>/template/      模板树（xinit.yml 在这里）
//	<case>/vars.yml       变量（扁平 key-value）
//	<case>/expected.hash  期望的渲染树 hash
//
// 模板必须放在 template/ 子目录里。夹具的 vars.yml 与 expected.hash
// 本身也是文件 —— 直接拿 <case>/ 当模板根会把它们一并渲染进去，
// 于是 golden 里混进了测试元数据。
const goldenRoot = "testdata/golden"

// goldenCase 是一个夹具完整渲染一遍的结果。
type goldenCase struct {
	entries []Entry
	blobs   map[string][]byte
	hash    string // 渲染树 hash（不含 exec 位）
	body    string // Dst+内容的顺序摘要，用于确定性比对
}

func loadGolden(t *testing.T, dir string) *goldenCase {
	t.Helper()

	manifestData, err := os.ReadFile(filepath.Join(dir, "template", "xinit.yml"))
	if err != nil {
		t.Fatalf("读清单: %v", err)
	}
	m, err := manifest.Parse(manifestData)
	if err != nil {
		t.Fatalf("解析清单: %v", err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatalf("编译清单: %v", err)
	}

	provided, err := loadFlatVars(filepath.Join(dir, "vars.yml"))
	if err != nil {
		t.Fatalf("读 vars: %v", err)
	}
	v, err := EvalVars(c, provided)
	if err != nil {
		t.Fatalf("求值 vars: %v", err)
	}

	fsys := os.DirFS(filepath.Join(dir, "template"))
	entries, _, err := Paths(fsys, c, v)
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}

	res := &goldenCase{entries: entries, blobs: map[string][]byte{}}
	var sumEntries []fsutil.TreeEntry
	body := sha256.New()

	for _, e := range entries {
		content, _, _, err := Content(fsys, c, v, e)
		if err != nil {
			t.Fatalf("Content(%s): %v", e.Dst, err)
		}
		res.blobs[e.Dst] = content

		// 树 hash **刻意不含 exec 位** —— Windows 上读不出执行位，
		// tar 解包也会丢。exec 由 TestGoldenExecBit 单独断言。
		sumEntries = append(sumEntries, fsutil.TreeEntry{
			Src:  e.Dst,
			Kind: e.Kind.String(),
			Exec: false, // 刻意置零
			Hash: fsutil.CompareHash(content, e.Binary),
		})
		fmt.Fprintf(body, "%s\x00%s\n", e.Dst, content)
	}

	res.hash = fsutil.RenderTreeHash(sumEntries)
	res.body = hex.EncodeToString(body.Sum(nil))
	return res
}

// loadFlatVars 读夹具的 vars.yml。值与 --config 一样必须是字符串。
func loadFlatVars(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("%s: 变量 %q 的值必须是字符串，实际是 %T", path, k, val)
		}
		out[k] = s
	}
	return out, nil
}

func goldenCases(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(goldenRoot)
	if os.IsNotExist(err) {
		t.Skipf("尚无夹具目录 %s", goldenRoot)
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Skipf("%s 下没有夹具", goldenRoot)
	}
	return names
}

func TestGoldenTrees(t *testing.T) {
	for _, name := range goldenCases(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(goldenRoot, name)
			got := loadGolden(t, dir)

			expectedPath := filepath.Join(dir, "expected.hash")
			want, err := os.ReadFile(expectedPath)
			if os.IsNotExist(err) {
				if os.Getenv("UPDATE_GOLDEN") == "1" {
					if err := os.WriteFile(expectedPath, []byte(got.hash+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					t.Logf("已写入期望值 %s", got.hash)
					return
				}
				t.Fatalf("缺少 %s。确认输出无误后用 UPDATE_GOLDEN=1 go test ./internal/render -run TestGoldenTrees 生成", expectedPath)
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(want)) != got.hash {
				t.Errorf("渲染树 hash 不匹配\n  got:  %s\n  want: %s\n"+
					"若这是有意的改动，请先核对渲染结果再用 UPDATE_GOLDEN=1 重新生成",
					got.hash, strings.TrimSpace(string(want)))
			}
		})
	}
}

// TestGoldenIsDeterministic 断言同一输入渲染两次逐字节相同（不变量 1）。
func TestGoldenIsDeterministic(t *testing.T) {
	for _, name := range goldenCases(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(goldenRoot, name)
			first := loadGolden(t, dir).body
			for i := 0; i < 5; i++ {
				if got := loadGolden(t, dir).body; got != first {
					t.Fatalf("渲染不确定: %s != %s", got, first)
				}
			}
		})
	}
}

// TestGoldenExecBit 断言 exec 位 —— 它不进树 hash，所以必须单独测。
//
// 这条断言在 Windows 上会失败（执行位读不出来），属已声明的平台限制。
func TestGoldenExecBit(t *testing.T) {
	dir := filepath.Join(goldenRoot, "basic")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skip("无 basic 夹具")
	}
	got := loadGolden(t, dir)

	wantExec := map[string]bool{"run.sh": true}
	for _, e := range got.entries {
		want, checked := wantExec[e.Dst]
		if !checked {
			continue
		}
		if e.Exec != want {
			t.Errorf("%s 的 Exec = %v, want %v", e.Dst, e.Exec, want)
		}
		delete(wantExec, e.Dst)
	}
	for name := range wantExec {
		t.Errorf("夹具里没有 %s，无法断言它的 exec 位", name)
	}
}

// TestGoldenDump 把渲染结果打印出来，供人工核对。
//
// 只在 DUMP_GOLDEN=1 时执行。无脑 UPDATE_GOLDEN 会让 golden 失去意义，
// 这个开关就是核对的手段。
func TestGoldenDump(t *testing.T) {
	if os.Getenv("DUMP_GOLDEN") != "1" {
		t.Skip("设置 DUMP_GOLDEN=1 以转储渲染结果")
	}
	for _, name := range goldenCases(t) {
		dir := filepath.Join(goldenRoot, name)
		got := loadGolden(t, dir)
		t.Logf("=== %s  树 hash %s ===", name, got.hash)
		for _, e := range got.entries {
			t.Logf("--- %s (src=%s exec=%v binary=%v mode=%v generated=%v) ---\n%s",
				e.Dst, e.Src, e.Exec, e.Binary, e.Mode, e.Generated, got.blobs[e.Dst])
		}
	}
}
