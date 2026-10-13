package update

// M3a C4 断言：verdictOf 表驱动（I27，设计稿 §4.1 行 / §2.3「优先级 3 ＞ 2 ＞ 0
// 由 verdictOf 单点实现并表驱动单测」）。

import "testing"

func TestVerdictOf(t *testing.T) {
	tests := []struct {
		name       string
		pending    bool
		hookFailed bool
		want       Verdict
	}{
		{"干净", false, false, VerdictOK},
		{"待解决", true, false, VerdictPending},
		{"钩子失败", false, true, VerdictHookFail},
		{"待解决+钩子失败：优先级 3 ＞ 2 ＞ 0（钩子失败胜）", true, true, VerdictHookFail},
	}
	for _, tc := range tests {
		if got := verdictOf(tc.pending, tc.hookFailed); got != tc.want {
			t.Errorf("%s：verdictOf(%v, %v) = %v，want %v", tc.name, tc.pending, tc.hookFailed, got, tc.want)
		}
	}
}
