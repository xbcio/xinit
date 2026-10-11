package backflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/state"
)

// TestArtifactNameShape 钉住 §20.6 的产物命名：`<ds7>-<ts7>-<slug>.patch`，
// slug 取主题 kebab 化短形（ASCII 化、有长度上限、空则退化）。
func TestArtifactNameShape(t *testing.T) {
	const ds = "abcdef1234567890abcdef1234567890abcdef12"
	const ts = "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name    string
		subject string
		want    string
	}{
		{"ASCII 逐词", "feat: share fix", "abcdef1-0123456-feat-share-fix.patch"},
		{"标点与大小写", "Fix: A/B path & more", "abcdef1-0123456-fix-a-b-path-more.patch"},
		{"只取主题首行", "feat: x\n\nbody here", "abcdef1-0123456-feat-x.patch"},
		{"前导分隔符剥净", "  ...feat  ", "abcdef1-0123456-feat.patch"},
		{"非 ASCII 折叠后为空则退化", "改共享面", "abcdef1-0123456-patch.patch"},
		{"中英混合", "feat: 改共享面", "abcdef1-0123456-feat.patch"},
		{"超长截断", strings.Repeat("a", 60), "abcdef1-0123456-" + strings.Repeat("a", 40) + ".patch"},
		{"截断不吃尾分隔符", strings.Repeat("a", 39) + "-b", "abcdef1-0123456-" + strings.Repeat("a", 39) + ".patch"},
		{"路径分隔符不逃逸", "../../etc", "abcdef1-0123456-etc.patch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ArtifactName(ds, ts, c.subject); got != c.want {
				t.Fatalf("ArtifactName = %q, want %q", got, c.want)
			}
			// 名字里永远只出现 ASCII 安全字符（可作文件名，且路径分隔符被折叠）。
			if strings.ContainsAny(ArtifactName(ds, ts, c.subject), `/\`) {
				t.Fatalf("产物名含路径分隔符：%q", ArtifactName(ds, ts, c.subject))
			}
		})
	}
}

// TestResolveOutDir 钉住 §20.8 的 --out 语义：默认 .xinit/backflow/；项目内相对
// 允许；项目外必须绝对路径（拒绝 `..` 穿越）。
func TestResolveOutDir(t *testing.T) {
	proj := t.TempDir()
	cases := []struct {
		name    string
		out     string
		want    string
		wantErr string // 非空 = 必须报错且文案含该要点
	}{
		{name: "默认", out: "", want: filepath.Join(proj, ".xinit", "backflow")},
		{name: "项目内相对", out: "artifacts/patches", want: filepath.Join(proj, "artifacts", "patches")},
		{name: "项目内相对含 .. 但不越界", out: "a/../artifacts", want: filepath.Join(proj, "artifacts")},
		{name: "项目根自身", out: ".", want: proj},
		{name: "项目外绝对", out: "/tmp/xinit-backflow-out", want: "/tmp/xinit-backflow-out"},
		{name: "绝对路径先 Clean", out: "/tmp/xinit-out/../patches", want: "/tmp/patches"},
		{name: "拒绝相对穿越", out: "../outside", wantErr: "绝对路径"},
		{name: "拒绝深穿越", out: "a/../../outside", wantErr: "绝对路径"},
		{name: "拒绝自身 ..", out: "..", wantErr: "绝对路径"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveOutDir(proj, c.out)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("应当报错，实际 got=%q", got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("错误文案 = %q, want 含 %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveOutDir: %v", err)
			}
			if got != c.want {
				t.Fatalf("ResolveOutDir = %q, want %q", got, c.want)
			}
		})
	}
}

// TestWriteArtifactDryRunZeroWrite：--dry-run 全链只返回路径，零写——目标目录
// 不存在也不建、不加锁（否则 .xinit/ 会被凭空创建）。
func TestWriteArtifactDryRunZeroWrite(t *testing.T) {
	proj := t.TempDir()
	name := filepath.Join(".xinit", "backflow", "1111111-2222222-feat.patch")

	path, err := WriteArtifact(proj, true, name, []byte("PATCH"))
	if err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}
	if path != filepath.Join(proj, name) || !filepath.IsAbs(path) {
		t.Fatalf("dry-run 路径 = %q, want %q（绝对）", path, filepath.Join(proj, name))
	}
	if _, err := os.Stat(filepath.Join(proj, ".xinit")); !os.IsNotExist(err) {
		t.Fatalf("dry-run 不得创建 .xinit（零写、不加锁），stat err=%v", err)
	}
}

// TestWriteArtifactWritesAndLocks：真写落盘（原子写、0644）且锁路径被走到
// （LockProject 会建 .xinit/lock，解锁不删该文件）。
func TestWriteArtifactWritesAndLocks(t *testing.T) {
	proj := t.TempDir()
	name := filepath.Join(".xinit", "backflow", "1111111-2222222-feat.patch")

	path, err := WriteArtifact(proj, false, name, []byte("PATCH"))
	if err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}
	if !filepath.IsAbs(path) || path != filepath.Join(proj, name) {
		t.Fatalf("路径 = %q, want %q", path, filepath.Join(proj, name))
	}
	if got := string(fileBytesT(t, path)); got != "PATCH" {
		t.Fatalf("产物内容 = %q", got)
	}
	if got := permT(t, path); got != 0o644 {
		t.Fatalf("产物权限 = %o, want 644", got)
	}
	if _, err := os.Stat(filepath.Join(proj, ".xinit", "lock")); err != nil {
		t.Fatalf("写产物应走 state.LockProject（.xinit/lock 常驻）：%v", err)
	}
}

// TestWriteArtifactFailsWhenLocked：同项目已在跑（占锁）时报错退出，不排队、不落盘。
func TestWriteArtifactFailsWhenLocked(t *testing.T) {
	proj := t.TempDir()
	lock, err := state.LockProject(proj)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()

	name := filepath.Join(".xinit", "backflow", "1111111-2222222-feat.patch")
	path, err := WriteArtifact(proj, false, name, []byte("PATCH"))
	if err == nil {
		t.Fatalf("占锁时必须报错，实际 path=%q", path)
	}
	if !strings.Contains(err.Error(), "锁定") {
		t.Fatalf("错误文案 = %q, want 含「锁定」", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(proj, name)); !os.IsNotExist(statErr) {
		t.Fatalf("占锁失败时不得落盘，stat err=%v", statErr)
	}
}

// TestWriteArtifactRejectsTraversalName：产物名相对形态越出项目即拒（§9.5 精神；
// 纵深防御，挡住调用方拼错路径时把产物写到项目外）。
func TestWriteArtifactRejectsTraversalName(t *testing.T) {
	proj := t.TempDir()
	_, err := WriteArtifact(proj, false, filepath.Join("..", "outside.patch"), []byte("PATCH"))
	if err == nil {
		t.Fatal("`..` 穿越的产物名必须被拒")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Fatalf("错误文案 = %q", err.Error())
	}
}
