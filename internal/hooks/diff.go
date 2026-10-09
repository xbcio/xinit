package hooks

import (
	"fmt"
	"strings"
)

// LineDiff 返回 old → new 的行级差异（LCS），已变行前缀 "- "/"+ "，
// 未变行前缀两空格；两串相等返回 ""。仅用于信任通知的展示。
func LineDiff(old, new string) string {
	o, n := splitLines(old), splitLines(new)
	// LCS 长度表（自后向前）。
	lcs := make([][]int, len(o)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(n)+1)
	}
	for i := len(o) - 1; i >= 0; i-- {
		for j := len(n) - 1; j >= 0; j-- {
			if o[i] == n[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	if lcs[0][0] == len(o) && lcs[0][0] == len(n) {
		return ""
	}
	var b strings.Builder
	i, j := 0, 0
	for i < len(o) && j < len(n) {
		switch {
		case o[i] == n[j]:
			fmt.Fprintf(&b, "  %s\n", o[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			fmt.Fprintf(&b, "- %s\n", o[i])
			i++
		default:
			fmt.Fprintf(&b, "+ %s\n", n[j])
			j++
		}
	}
	for ; i < len(o); i++ {
		fmt.Fprintf(&b, "- %s\n", o[i])
	}
	for ; j < len(n); j++ {
		fmt.Fprintf(&b, "+ %s\n", n[j])
	}
	return b.String()
}

// splitLines 切行；空串 → nil；行尾 \n 不产生空尾行。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
