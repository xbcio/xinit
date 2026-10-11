package backflow

import (
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/fsutil"
)

// crlfT 把 LF 夹具转成 CRLF（EOL 用例夹具；§20.3 硬规则的判别面）。
func crlfT(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// TestTranslateFileShapes 是 §20.3 的形状矩阵：INV-1..4 + 冲突半径 + EOL 硬规则
// + 新增文件特例。
//
// 冲突用例一律断言 `FNew == nil`：冲突标记绝不外流（§20.1 不变量 10 延伸），
// 且 O 的内容绝不出现（INV-1 fail-closed）。非冲突用例在 want 之外还逐例断言
// INV-4（`DetectEOL(FNew) == DetectEOL(T)`）——EOL 域是覆盖全矩阵的性质。
func TestTranslateFileShapes(t *testing.T) {
	// 三段式夹具：T/B/O 各自的形状在用例里逐字写出（不用生成器——
	// 评审对抗形状的可读性优先于紧凑）。
	const (
		tok = "TOKEN"
		der = "DERIVED"
	)
	cases := []struct {
		name     string
		T, B, O  string
		isNew    bool
		want     string // FNew 期望（wantNil 时不看）
		wantNil  bool   // 冲突：FNew 必须为 nil
		conflict bool
		blocks   int
	}{
		// —— 退化面 ——
		{
			name: "all-equal-noop",
			T:    "l1\nl2 " + tok + "\nl3\n", B: "l1\nl2 " + tok + "\nl3\n", O: "l1\nl2 " + tok + "\nl3\n",
			want: "l1\nl2 " + tok + "\nl3\n",
		},

		// —— INV-2：T[r] == B[r] ⇒ F_new[r] == O[r]（逐字） ——
		{name: "inv2-user-modify", T: "a\nb\nc\n", B: "a\nb\nc\n", O: "a\nB2\nc\n", want: "a\nB2\nc\n"},
		{name: "inv2-user-insert", T: "a\nb\n", B: "a\nb\n", O: "a\nnew\nb\n", want: "a\nnew\nb\n"},
		{name: "inv2-user-delete", T: "a\nb\nc\n", B: "a\nb\nc\n", O: "a\nc\n", want: "a\nc\n"},
		{name: "inv2-user-append-eof", T: "a\nb\n", B: "a\nb\n", O: "a\nb\ntail\n", want: "a\nb\ntail\n"},
		{name: "inv2-multi-hunks", T: "a\nb\nc\nd\ne\nf\n", B: "a\nb\nc\nd\ne\nf\n",
			O: "a\nb2\nc\nd\ne\nf2\n", want: "a\nb2\nc\nd\ne\nf2\n"},

		// —— INV-3：O[r] == B[r] ⇒ F_new[r] == T[r]（T 域取值） ——
		{name: "inv3-render-only", T: "pkg/x.go\n", B: "acme/x.go\n", O: "acme/x.go\n", want: "pkg/x.go\n"},
		{name: "inv3-multi-rule", T: "s1 PKG\ns2 PKG\n", B: "s1 acme\ns2 acme\n", O: "s1 acme\ns2 acme\n",
			want: "s1 PKG\ns2 PKG\n"},
		{name: "inv3-target-case-upper", T: "name: pkg\n", B: "name: PKG\n", O: "name: PKG\n", want: "name: pkg\n"},
		{name: "inv3-target-case-lower", T: "name: PKG\n", B: "name: pkg\n", O: "name: pkg\n", want: "name: PKG\n"},

		// —— 渲染差异区与用户改动区间隔 ⇒ 干净合并取两侧值 ——
		{
			name: "clean-separated-regions",
			T:    "l1\nl2 " + tok + "\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n",
			B:    "l1\nl2 " + der + "\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n",
			O:    "l1\nl2 " + tok + "\nl3\nl4\nl5\nl6\nl7\nl8\nline9 user\nl10\n",
			want: "l1\nl2 " + tok + "\nl3\nl4\nl5\nl6\nl7\nl8\nline9 user\nl10\n",
		},
		{
			name: "clean-two-render-regions",
			T:    "l1\nl2 PKG\nl3\nl4\nl5\nl6\nl7\nl8 PKG2\nl9\n",
			B:    "l1\nl2 acme\nl3\nl4\nl5\nl6\nl7\nl8 acme2\nl9\n",
			O:    "l1\nl2 PKG\nl3\nl4\nl5 user\nl6\nl7\nl8 PKG2\nl9\n",
			want: "l1\nl2 PKG\nl3\nl4\nl5 user\nl6\nl7\nl8 PKG2\nl9\n",
		},
		{
			// 半径边界（实测外的第一格）：距 token 行 2 行 ⇒ 不冲突。
			// 这条钉住「不越界扩大冲突面」——半径表不是「邻域一律冲突」。
			name: "clean-radius-boundary-2-away",
			T:    "a\nb " + tok + "\nc\nd\ne\n",
			B:    "a\nb " + der + "\nc\nd\ne\n",
			O:    "a\nb " + tok + "\nc\nD user\ne\n",
			want: "a\nb " + tok + "\nc\nD user\ne\n",
		},

		// —— INV-1 / 冲突半径表（实测口径固化） ——
		{name: "conflict-same-line", T: "a\n" + tok + "\nc\n", B: "a\n" + der + "\nc\n", O: "a\nuser-token\nc\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-token-line-minus1", T: "a\nb " + tok + "\nc\n", B: "a\nb " + der + "\nc\n", O: "A user\nb " + tok + "\nc\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-token-line-plus1", T: "a\nb " + tok + "\nc\n", B: "a\nb " + der + "\nc\n", O: "a\nb " + tok + "\nC user\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-insert-before-token-line", T: "a\nb " + tok + "\nc\n", B: "a\nb " + der + "\nc\n", O: "a\ninserted\nb " + tok + "\nc\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-insert-after-token-line", T: "a\nb " + tok + "\nc\n", B: "a\nb " + der + "\nc\n", O: "a\nb " + tok + "\ninserted\nc\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-delete-after-token-line", T: "a\nb " + tok + "\nc\nd\n", B: "a\nb " + der + "\nc\nd\n", O: "a\nb " + tok + "\nd\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-delete-token-line", T: "a\nb " + tok + "\nc\n", B: "a\nb " + der + "\nc\n", O: "a\nc\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-following-line-modified", T: "a\nb " + tok + "\nc\nd\n", B: "a\nb " + der + "\nc\nd\n", O: "a\nb " + tok + "\nC user\nd\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "radius-tail-append-token-last", T: "a\nb\n" + tok + "\n", B: "a\nb\n" + der + "\n", O: "a\nb\n" + tok + "\nappended\n",
			wantNil: true, conflict: true, blocks: 1},
		{name: "conflict-two-blocks", T: "a\nT1\nb\nc\nd\ne\nf\nT2\ng\n", B: "a\nD1\nb\nc\nd\ne\nf\nD2\ng\n",
			O: "a\nT1x\nb\nc\nd\ne\nf\nT2x\ng\n", wantNil: true, conflict: true, blocks: 2},
		{name: "conflict-no-common-line", T: "a\n", B: "b\n", O: "c\n",
			wantNil: true, conflict: true, blocks: 1},

		// —— EOL 硬规则（F_new = FromLF(merge输出, DetectEOL(T))，禁用 O 的 EOL） ——
		{
			name: "eol-crlf-inv2-bytes-equal-O",
			T:    crlfT("a\nb\nc\n"), B: crlfT("a\nb\nc\n"), O: crlfT("a\nb2\nc\n"),
			want: crlfT("a\nb2\nc\n"),
		},
		{
			// 判别用例：O 被编辑器归一成 LF，但 EOL 必须按 T（CRLF）还原。
			// 用 O 的 EOL ⇒ 输出 LF（全文件行尾改写，§20.3 点名）。
			name: "eol-crlf-T-crlf-O-normalized-lf",
			T:    crlfT("a\nb\nc\n"), B: crlfT("a\nb\nc\n"), O: "a\nb2\nc\n",
			want: crlfT("a\nb2\nc\n"),
		},
		{
			// 判别用例：T=LF、O 整份 CRLF ⇒ 输出仍为 LF（用 O 的 EOL 会得到
			// 全文件行尾改写）。O 经合并内部 ToLF 后与 B 的差异只剩用户那一行。
			name: "eol-lf-T-crlf-O",
			T:    "a\nb\nc\n", B: "a\nb\nc\n", O: "a\r\nB2\r\nc\r\n",
			want: "a\nB2\nc\n",
		},
		{
			// 渲染改动与用户改动分处两区：全 CRLF 输出，且用户改动之外的字节
			// 与 T 逐字相同（不做全文件行尾改写）。
			name: "eol-crlf-render-change-kept",
			T:    crlfT("head\nmid " + tok + "\nf3\nf4\nf5\nf6\nf7\ntail\n"),
			B:    crlfT("head\nmid " + der + "\nf3\nf4\nf5\nf6\nf7\ntail\n"),
			O:    crlfT("head\nmid " + tok + "\nf3\nf4\nf5\nf6\nf7\ntail user\n"),
			want: crlfT("head\nmid " + tok + "\nf3\nf4\nf5\nf6\nf7\ntail user\n"),
		},
		{
			name: "eol-crlf-inv3",
			T:    crlfT("x\n" + tok + "\n"), B: crlfT("x\n" + der + "\n"), O: crlfT("x\n" + der + "\n"),
			want: crlfT("x\n" + tok + "\n"),
		},

		// —— 新增文件特例：F_new = O（零翻译、零合并） ——
		{name: "isnew-content-verbatim", isNew: true, O: "pkg/x.go\n",
			want: "pkg/x.go\n"},
		{name: "isnew-empty-verbatim", isNew: true, want: ""},
		{name: "isnew-crlf-verbatim", isNew: true, O: "a\r\nb\r\n", want: "a\r\nb\r\n"},
		{name: "isnew-ignores-tb", isNew: true, T: "whatever\n", B: "other\n", O: "payload\n", want: "payload\n"},
		{
			// 新增内容里的派生词逐字保留（翻译的诚实边界）；冲突标记形状的文本
			// 也不得被处理 —— 不合并就没有冲突判定。
			name: "isnew-token-and-marker-verbatim", isNew: true,
			O:    "<<<<<<< ours\nacme KEEPME\n",
			want: "<<<<<<< ours\nacme KEEPME\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranslateFile(TranslateInput{
				T: []byte(tc.T), B: []byte(tc.B), O: []byte(tc.O), IsNew: tc.isNew,
			})
			if err != nil {
				t.Fatalf("TranslateFile: %v", err)
			}
			if got.Conflict != tc.conflict {
				t.Fatalf("Conflict = %v, want %v", got.Conflict, tc.conflict)
			}
			if got.Blocks != tc.blocks {
				t.Fatalf("Blocks = %d, want %d", got.Blocks, tc.blocks)
			}
			if tc.wantNil {
				if got.FNew != nil {
					t.Fatalf("冲突路径 FNew 必须为 nil（冲突标记与 O 的内容都不得外流），实际 %q", got.FNew)
				}
				return
			}
			if string(got.FNew) != tc.want {
				t.Fatalf("FNew = %q, want %q", got.FNew, tc.want)
			}
			// INV-4：F_new 的 EOL == DetectEOL(T)。新增文件 = O 原样，不在此列。
			if !tc.isNew {
				gotEOL, wantEOL := fsutil.DetectEOL(got.FNew), fsutil.DetectEOL([]byte(tc.T))
				if gotEOL != wantEOL {
					t.Fatalf("INV-4: FNew 的 EOL = %v, want %v（T 的 EOL）", gotEOL, wantEOL)
				}
			}
		})
	}
}

// TestTranslateFileCRLFOnlyTouchedLines 钉住 §20.3 的「非全文件行尾改写」判据：
// T=CRLF、渲染改动与用户改动分处两区时，输出相对 O 只差用户行，行尾不重写。
func TestTranslateFileCRLFOnlyTouchedLines(t *testing.T) {
	tIn := crlfT("l1\nl2 TOKEN\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n")
	bIn := crlfT("l1\nl2 DERIVED\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n")
	oIn := crlfT("l1\nl2 TOKEN\nl3\nl4\nl5\nl6\nl7\nl8\nl9 user\nl10\n")

	got, err := TranslateFile(TranslateInput{T: []byte(tIn), B: []byte(bIn), O: []byte(oIn)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Conflict {
		t.Fatalf("不该冲突：Blocks = %d", got.Blocks)
	}
	// 期望 = T 的模板措辞 + 用户那一行；逐字节比较即排除全文件行尾改写。
	want := crlfT("l1\nl2 TOKEN\nl3\nl4\nl5\nl6\nl7\nl8\nl9 user\nl10\n")
	if string(got.FNew) != want {
		t.Fatalf("FNew = %q, want %q", got.FNew, want)
	}
	if n := strings.Count(string(got.FNew), "\r\n"); n != 10 {
		t.Fatalf("CRLF 行数 = %d, want 10（行尾不得被改写）", n)
	}
}

// TestTranslateFileMergeError 钉住 error 面的边界：merge 结构性失败（含 NUL 的
// 三方被 git 拒绝，rc=255 ⇒ Outcome Error）是 error，不是 Conflict —— fail-closed
// 的调用方必须能区分「冲突」与「合并跑不了」。
func TestTranslateFileMergeError(t *testing.T) {
	_, err := TranslateFile(TranslateInput{
		T: []byte("a\x00b\n"), B: []byte("a\x00b\n"), O: []byte("a\x00c\n"),
	})
	if err == nil {
		t.Fatal("含 NUL 的三方合并应报错（git merge-file rc=255），实际 err == nil")
	}
}
