package cli

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xbcio/xinit/internal/hooks"
)

func TestAskLineTrimsAndNormalizes(t *testing.T) {
	// 归一 = 去行尾换行 + TrimSpace（含 \r，兼容 CRLF 与管道）。
	for _, tc := range []struct{ in, want string }{
		{"  v  \n", "v"},
		{"v\r\n", "v"},
		{"v\n", "v"},
		{"\tv \n", "v"},
	} {
		var out bytes.Buffer
		ps := newPromptSession(strings.NewReader(tc.in), &out)
		line, eof, err := ps.askLine("name: ")
		if err != nil || eof || line != tc.want {
			t.Fatalf("输入 %q 应归一为 %q: line=%q eof=%v err=%v", tc.in, tc.want, line, eof, err)
		}
		if out.String() != "name: " {
			t.Fatalf("提示应原样写 stdout: %q", out.String())
		}
	}
}

func TestAskLineEmptyVsEOF(t *testing.T) {
	// 空行 ≠ EOF：空行是「空答」（eof=false，调用方取 default），
	// EOF 才是「输入已结束」。
	var out bytes.Buffer
	ps := newPromptSession(strings.NewReader("\n"), &out)
	if line, eof, err := ps.askLine("q: "); err != nil || eof || line != "" {
		t.Fatalf("单独一个换行应是空答而非 EOF: line=%q eof=%v err=%v", line, eof, err)
	}

	out.Reset()
	ps = newPromptSession(strings.NewReader(""), &out)
	if line, eof, err := ps.askLine("q: "); err != nil || !eof || line != "" {
		t.Fatalf("空输入应是 EOF: line=%q eof=%v err=%v", line, eof, err)
	}

	// 半行无换行（终端 canonical mode 下 Ctrl-D 打在非空行上）：
	// eof=true 但 line 有效，调用方照常使用该行（§19.2.3）。
	out.Reset()
	ps = newPromptSession(strings.NewReader("半行无换行"), &out)
	line, eof, err := ps.askLine("q: ")
	if err != nil || !eof || line != "半行无换行" {
		t.Fatalf("半行应带内容返回: line=%q eof=%v err=%v", line, eof, err)
	}
}

func TestAskLineEOFSticky(t *testing.T) {
	// EOF 锁存：置位后不再打印提示，直接 (\"\", true, nil)——否则
	// 多问题场景会在流已耗尽后刷出一串无人回答的提示行。
	var out bytes.Buffer
	ps := newPromptSession(strings.NewReader(""), &out)
	if _, eof, err := ps.askLine("q1: "); err != nil || !eof {
		t.Fatalf("首次应 EOF: eof=%v err=%v", eof, err)
	}
	before := out.Len()
	line, eof, err := ps.askLine("q2: ")
	if err != nil || !eof || line != "" {
		t.Fatalf("锁存后应直接按 EOF 返回: line=%q eof=%v err=%v", line, eof, err)
	}
	if out.Len() != before {
		t.Fatalf("锁存后不应再打印提示: 新增 %q", out.String()[before:])
	}
}

// probeReader 在第一次 Read 被调用时记录 out 的当时内容——用来断言
// 「先打印提示、再读」（§19.2.3：若实现先读后打印，记录里就没有提示）。
type probeReader struct {
	out   *bytes.Buffer
	at    string
	probe bool
	r     *strings.Reader
}

func (p *probeReader) Read(b []byte) (int, error) {
	if !p.probe {
		p.at = p.out.String()
		p.probe = true
	}
	return p.r.Read(b)
}

func TestAskLineWritesBeforeRead(t *testing.T) {
	var out bytes.Buffer
	r := &probeReader{out: &out, r: strings.NewReader("v\n")}
	ps := newPromptSession(r, &out)
	if _, _, err := ps.askLine("name: "); err != nil {
		t.Fatal(err)
	}
	if !r.probe {
		t.Fatal("未观察到读操作")
	}
	if !strings.Contains(r.at, "name: ") {
		t.Fatalf("提示必须早于首次读：读到时 out=%q", r.at)
	}
}

func TestAskYesNo(t *testing.T) {
	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n", "  y  \n"} {
		var out bytes.Buffer
		ps := newPromptSession(strings.NewReader(in), &out)
		yes, eof, err := ps.askYesNo("改用模板版？[y/N] ")
		if err != nil || eof || !yes {
			t.Fatalf("输入 %q 应为真: yes=%v eof=%v err=%v", in, yes, eof, err)
		}
	}
	for _, in := range []string{"n\n", "\n", "foo\n", ""} {
		var out bytes.Buffer
		ps := newPromptSession(strings.NewReader(in), &out)
		yes, _, err := ps.askYesNo("改用模板版？[y/N] ")
		if err != nil || yes {
			t.Fatalf("输入 %q 应为假且不出错: yes=%v err=%v", in, yes, err)
		}
	}
}

func TestAskLineReadError(t *testing.T) {
	// 非 EOF 的读错误必须上抛（普通错误，退 1），不当成 EOF 静默吞掉。
	var out bytes.Buffer
	ps := newPromptSession(errReader{}, &out)
	line, eof, err := ps.askLine("q: ")
	if !errors.Is(err, errBoom) {
		t.Fatalf("应上抛读错误: line=%q eof=%v err=%v", line, eof, err)
	}
	if eof || line != "" {
		t.Fatalf("读错误不应伪装成 EOF: line=%q eof=%v", line, eof)
	}
	// 出错不锁存：与 EOF 不同，这不是「输入已结束」。
	if ps.eof {
		t.Fatal("读错误不应置 EOF 锁存")
	}
}

var errBoom = errors.New("boom")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBoom }

func TestConfirmMatchesHooksConfirm(t *testing.T) {
	// 未锁存时 ps.confirm 与 hooks.Confirm 逐字节等价（提示与判定一字不改，
	// §19.2.3）：y/Y/yes/YES 为真，其余与 EOF 为假且不出错。
	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n", "  y  \n", "n\n", "\n", "foo\n", ""} {
		var got, want bytes.Buffer
		ps := newPromptSession(strings.NewReader(in), &got)
		ok, err := ps.confirm()
		wantOK, wantErr := hooks.Confirm(bufio.NewReader(strings.NewReader(in)), &want)
		if ok != wantOK || (err == nil) != (wantErr == nil) {
			t.Fatalf("输入 %q：confirm=(%v,%v) 与 hooks.Confirm=(%v,%v) 不一致",
				in, ok, err, wantOK, wantErr)
		}
		if got.String() != want.String() {
			t.Fatalf("输入 %q：提示不一致\n got %q\nwant %q", in, got.String(), want.String())
		}
	}
}

func TestConfirmSkipsPromptAfterEOF(t *testing.T) {
	// 会话已 EOF 锁存（用户在更早的提问处耗尽输入，如 4b 的 C3 路径）⇒ 信任门
	// 不再打印提问行、直接按「未确认」返回：管道下的空 stdin 只该留下 C3 行，
	// 不该多出一行无人回答的「信任并执行？[y/N] 」（M3，2026-10-11 M7）。
	var out bytes.Buffer
	ps := newPromptSession(strings.NewReader(""), &out)
	if _, eof, err := ps.askLine("q: "); err != nil || !eof {
		t.Fatalf("首次读应 EOF: eof=%v err=%v", eof, err)
	}
	out.Reset() // 只观察 confirm 自己的写入
	ok, err := ps.confirm()
	if err != nil || ok {
		t.Fatalf("锁存后应直接按未确认返回: ok=%v err=%v", ok, err)
	}
	if out.Len() != 0 {
		t.Fatalf("锁存后不应打印信任门提示: %q", out.String())
	}
}

func TestConfirmEOFDoesNotLatch(t *testing.T) {
	// 信任门是 init/update 的**最后一次**交互读取（无后续消费点）⇒ Confirm
	// 自身读到的 EOF 不另行锁存（裁定 R2，2026-10-11 M7）：此断言把该裁定写进
	// 测试，避免后来者把「顺手加锁存」当成必要动作。
	var out bytes.Buffer
	ps := newPromptSession(strings.NewReader(""), &out)
	ok, err := ps.confirm()
	if err != nil || ok {
		t.Fatalf("EOF 应判未确认: ok=%v err=%v", ok, err)
	}
	if ps.eof {
		t.Fatal("信任门读到的 EOF 不锁存（无后续读取点，2026-10-11 M7）")
	}
}

func TestSharedReaderNoLoss(t *testing.T) {
	// §19.2.3 的硬约束：一次运行只有一条 reader，且与 hooks.Confirm 共用。
	// 各问各建 reader 时 bufio 的预读会把后续回答吞进上一问的缓冲区。
	var out bytes.Buffer
	ps := newPromptSession(strings.NewReader("y\nv1\nyes\nv2\nn\n"), &out)

	ok, err := hooks.Confirm(ps.r, &out)
	if err != nil || !ok {
		t.Fatalf("第 1 行 y 应由 Confirm 消费: ok=%v err=%v", ok, err)
	}
	if line, eof, err := ps.askLine("name: "); err != nil || eof || line != "v1" {
		t.Fatalf("第 2 行应读到 v1: line=%q eof=%v err=%v", line, eof, err)
	}
	if yes, eof, err := ps.askYesNo("改用模板版？[y/N] "); err != nil || eof || !yes {
		t.Fatalf("第 3 行 yes 应为真: yes=%v eof=%v err=%v", yes, eof, err)
	}
	if line, eof, err := ps.askLine("name2: "); err != nil || eof || line != "v2" {
		t.Fatalf("第 4 行应读到 v2: line=%q eof=%v err=%v", line, eof, err)
	}
	if yes, eof, err := ps.askYesNo("改用模板版？[y/N] "); err != nil || eof || yes {
		t.Fatalf("第 5 行 n 应为假: yes=%v eof=%v err=%v", yes, eof, err)
	}

	// 提示一行一问、按提问顺序出现（不丢不串）
	if got, want := out.String(), "信任并执行？[y/N] name: 改用模板版？[y/N] name2: 改用模板版？[y/N] "; got != want {
		t.Fatalf("提示序列不符:\n got %q\nwant %q", got, want)
	}
}
