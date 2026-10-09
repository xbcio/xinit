package hooks

import "testing"

func TestHashStableAndPrefixed(t *testing.T) {
	h := Hash("echo hi\n")
	if h != Hash("echo hi\n") {
		t.Fatal("同内容 hash 不稳定")
	}
	if h == Hash("echo hi") {
		t.Fatal("不同内容 hash 相同")
	}
	if len(h) != len("sha256:")+64 || h[:7] != "sha256:" {
		t.Fatalf("hash 形状不对: %q", h)
	}
}

func TestNeedConfirmFirstSight(t *testing.T) {
	m := map[string]string{}
	if !NeedConfirm(m, "h1") {
		t.Fatal("首次遇到必须要求确认")
	}
}

func TestConsentThenNoConfirm(t *testing.T) {
	m := map[string]string{}
	Consent(m, "h1", "v0.2.4")
	if m["h1"] != "v0.2.4" {
		t.Fatalf("Consent 未记录版本: %q", m["h1"])
	}
	if NeedConfirm(m, "h1") {
		t.Fatal("已确认的内容不应再要求确认")
	}
}

func TestSeenMarkerStillNeedsConfirm(t *testing.T) {
	m := map[string]string{}
	Seen(m, "h1")
	if v, ok := m["h1"]; !ok || v != "" {
		t.Fatalf("Seen 应写空串 marker，得到 ok=%v v=%q", ok, v)
	}
	if !NeedConfirm(m, "h1") {
		t.Fatal("Seen（--no-hooks 记录）之后仍必须要求确认（§10.5）")
	}
}

func TestSeenDoesNotDowngradeConsent(t *testing.T) {
	m := map[string]string{}
	Consent(m, "h1", "v1")
	Seen(m, "h1")
	if m["h1"] != "v1" {
		t.Fatalf("Seen 不得覆盖已确认记录: %q", m["h1"])
	}
	if NeedConfirm(m, "h1") {
		t.Fatal("已确认内容不应被 Seen 降级")
	}
}

func TestContentChangeReask(t *testing.T) {
	m := map[string]string{}
	Consent(m, Hash("old"), "v1")
	if !NeedConfirm(m, Hash("new")) {
		t.Fatal("内容变化必须重新确认（§10.5）")
	}
}
