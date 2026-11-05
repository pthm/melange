package studio

import (
	"fmt"
	"strings"
	"testing"
)

// render prints hunks as a unified diff body, for comparing in tests.
func render(hs []diffHunk) string {
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "@@ -%d +%d @@\n", h.OldStart, h.NewStart)
		for _, l := range h.Lines {
			b.WriteByte(l.Op)
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func numbered(n int, edit func(i int) string) string {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, edit(i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestLineDiff(t *testing.T) {
	plain := func(i int) string { return fmt.Sprintf("l%d", i) }
	for _, tc := range []struct {
		name, old, new, want string
	}{
		{name: "identical", old: "a\nb\n", new: "a\nb\n", want: ""},
		{name: "from empty", old: "", new: "a\nb\n", want: "@@ -1 +1 @@\n+a\n+b\n"},
		{
			name: "change in the middle keeps 3 lines of context",
			old:  numbered(10, plain),
			new:  numbered(10, func(i int) string { return map[bool]string{true: "changed", false: plain(i)}[i == 5] }),
			want: "@@ -2 +2 @@\n l2\n l3\n l4\n-l5\n+changed\n l6\n l7\n l8\n",
		},
		{
			name: "far apart changes make two hunks with correct line numbers",
			old:  numbered(20, plain),
			new:  strings.Replace(strings.Replace(numbered(20, plain), "l2\n", "", 1), "l18\n", "l18\nadded\n", 1),
			want: "@@ -1 +1 @@\n l1\n-l2\n l3\n l4\n l5\n@@ -15 +14 @@\n l15\n l16\n l17\n l18\n+added\n l19\n l20\n",
		},
		{
			name: "close changes merge into one hunk",
			old:  numbered(10, plain),
			new:  strings.Replace(strings.Replace(numbered(10, plain), "l3\n", "x\n", 1), "l7\n", "y\n", 1),
			want: "@@ -1 +1 @@\n l1\n l2\n-l3\n+x\n l4\n l5\n l6\n-l7\n+y\n l8\n l9\n l10\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(hunks(lineDiff(tc.old, tc.new))); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
