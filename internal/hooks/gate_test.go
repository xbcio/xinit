package hooks

import (
	"bytes"
	"strings"
	"testing"
)

// I-2 M5 C1：信任门单点（gate.go）的四缝——Seen / Decline / Yes-Consent /
// PrepareDiff 惰性。行为判据与两调用点（update trust / init 步骤 4）逐字同构。

// TestConfirmGateSeenOnNoHooks 钉 --no-hooks 分支：只登记 Seen marker（键存在
// ∧ 空串）、不得有任何输出、不得询问。
func TestConfirmGateSeenOnNoHooks(t *testing.T) {
	trusted := map[string]string{}
	var out bytes.Buffer
	declined, err := ConfirmGate(trusted, GateInput{
		Name:    "post_update",
		Block:   "echo hi\n",
		Version: "v1",
		Out:     &out,
	}, true, false)
	if err != nil || declined {
		t.Fatalf("noHooks：declined=%v err=%v，want false/nil", declined, err)
	}
	v, ok := trusted[Hash("echo hi\n")]
	if !ok || v != "" {
		t.Fatalf("noHooks 应写 Seen marker（键存在 ∧ 空串），得到 ok=%v v=%q", ok, v)
	}
	if out.Len() != 0 {
		t.Errorf("noHooks 不得有任何输出：%q", out.String())
	}
}

// TestConfirmGateDeclineWritesNothing 钉拒绝分支：declined=true、Confirm 恰被
// 调 1 次、trusted 不留任何记录（无 Consent、无 Seen）——调用方据此映射
// rc4 零写。Out 须同时含 PrintNotice 披露与 PrintAsk 指引（未 --yes）。
func TestConfirmGateDeclineWritesNothing(t *testing.T) {
	trusted := map[string]string{}
	var out bytes.Buffer
	calls := 0
	declined, err := ConfirmGate(trusted, GateInput{
		Name:    "post_init",
		Block:   "echo hi\n",
		Version: "v1",
		Out:     &out,
		Confirm: func() (bool, error) { calls++; return false, nil },
	}, false, false)
	if err != nil || !declined {
		t.Fatalf("拒绝：declined=%v err=%v，want true/nil", declined, err)
	}
	if calls != 1 {
		t.Fatalf("Confirm 调用次数 = %d，want 1", calls)
	}
	if _, ok := trusted[Hash("echo hi\n")]; ok {
		t.Fatalf("拒绝不得写任何记录：trusted = %v", trusted)
	}
	if !NeedConfirm(trusted, Hash("echo hi\n")) {
		t.Fatal("拒绝后仍须需要确认")
	}
	got := out.String()
	if !strings.Contains(got, "需要确认") || !strings.Contains(got, "echo hi") {
		t.Errorf("Out 应含 PrintNotice 文本；got %q", got)
	}
	if !strings.Contains(got, "退出码 4") {
		t.Errorf("Out 应含 PrintAsk 指引（未 --yes）；got %q", got)
	}
}

// TestConfirmGateYesConsentsWithoutConfirm 钉 --yes 预确认分支：不打指引、
// 不经 Confirm、直接以 Version 记 Consent。
func TestConfirmGateYesConsentsWithoutConfirm(t *testing.T) {
	trusted := map[string]string{}
	var out bytes.Buffer
	declined, err := ConfirmGate(trusted, GateInput{
		Name:    "post_init",
		Block:   "echo hi\n",
		Version: "v1.2.3",
		Out:     &out,
		Confirm: nil, // --yes 路径不应触碰交互面
	}, false, true)
	if err != nil || declined {
		t.Fatalf("--yes：declined=%v err=%v，want false/nil", declined, err)
	}
	if got := trusted[Hash("echo hi\n")]; got != "v1.2.3" {
		t.Fatalf("Consent 记录版本 = %q，want v1.2.3", got)
	}
	if NeedConfirm(trusted, Hash("echo hi\n")) {
		t.Fatal("确认后不应再需要确认")
	}
	if s := out.String(); strings.Contains(s, "退出码 4") {
		t.Errorf("--yes 预先确认不得打 PrintAsk 指引；got %q", s)
	}
}

// TestConfirmGatePrepareDiffLazy 钉 PrepareDiff 的调用时机：noHooks 与已确认
// 轮 0 次（不得多做模板 Open——原 update 内环语义）；NeedConfirm 轮恰 1 次，
// 且返回值（oldVer/diff）进 Out。
func TestConfirmGatePrepareDiffLazy(t *testing.T) {
	const block = "echo hi\n"
	newIn := func(calls *int, out *bytes.Buffer) GateInput {
		return GateInput{
			Name:    "post_update",
			Block:   block,
			Version: "v1",
			Out:     out,
			Confirm: func() (bool, error) { return true, nil },
			PrepareDiff: func() (string, string) {
				*calls++
				return "v0.9.0", "- old\n+ new\n"
			},
		}
	}

	t.Run("noHooks 路径不调用", func(t *testing.T) {
		var out bytes.Buffer
		calls := 0
		if _, err := ConfirmGate(map[string]string{}, newIn(&calls, &out), true, false); err != nil {
			t.Fatal(err)
		}
		if calls != 0 {
			t.Fatalf("noHooks 不得调用 PrepareDiff：%d 次", calls)
		}
	})

	t.Run("已确认轮不调用", func(t *testing.T) {
		var out bytes.Buffer
		calls := 0
		trusted := map[string]string{Hash(block): "v0.9.0"}
		if _, err := ConfirmGate(trusted, newIn(&calls, &out), false, false); err != nil {
			t.Fatal(err)
		}
		if calls != 0 {
			t.Fatalf("已确认轮不得调用 PrepareDiff：%d 次", calls)
		}
	})

	t.Run("NeedConfirm 恰一次且文本进 Out", func(t *testing.T) {
		var out bytes.Buffer
		calls := 0
		declined, err := ConfirmGate(map[string]string{}, newIn(&calls, &out), false, false)
		if err != nil || declined {
			t.Fatalf("declined=%v err=%v，want false/nil", declined, err)
		}
		if calls != 1 {
			t.Fatalf("PrepareDiff 调用次数 = %d，want 1", calls)
		}
		got := out.String()
		if !strings.Contains(got, "v0.9.0") || !strings.Contains(got, "- old") {
			t.Errorf("oldVer/diff 文本应出现在 Out；got %q", got)
		}
	})
}
