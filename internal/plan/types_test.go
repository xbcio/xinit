package plan

import "testing"

// §4.2：枚举值与顺序是冻结契约（ActionKind 会进报告/夹具断言，改数值即破坏兼容）。
func TestActionKindValuesAreFrozen(t *testing.T) {
	if Noop != 0 || WriteTheirs != 1 || Delete != 2 || Merge != 3 ||
		ConflictBinary != 4 || ConflictBothAdded != 5 || ConflictKind != 6 ||
		KeepWarned != 7 || KeepLocalDelete != 8 {
		t.Fatal("ActionKind 枚举值必须保持 §4.2 的顺序")
	}
}

func TestActionKindString(t *testing.T) {
	cases := []struct {
		k    ActionKind
		want string
	}{
		{Noop, "noop"},
		{WriteTheirs, "write"},
		{Delete, "delete"},
		{Merge, "merge"},
		{ConflictBinary, "conflict-binary"},
		{ConflictBothAdded, "conflict-both-added"},
		{ConflictKind, "conflict-kind"},
		{KeepWarned, "keep-warned"},
		{KeepLocalDelete, "keep-local-delete"},
		{ActionKind(200), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("ActionKind(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}
