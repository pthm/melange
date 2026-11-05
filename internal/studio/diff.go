package studio

import "strings"

// diffLine is one line of a line diff: Op is ' ' (both), '-' (old only) or
// '+' (new only).
type diffLine struct {
	Op   byte
	Text string
}

// diffHunk is a run of changes with up to diffContext unchanged lines around
// it. OldStart and NewStart are 1-based line numbers of its first line.
type diffHunk struct {
	OldStart, NewStart int
	Lines              []diffLine
}

const diffContext = 3

// lineDiff diffs old against new line by line: the common prefix and suffix
// are trimmed, and the middle is aligned by longest common subsequence.
//
// ponytail: the LCS table is O(n*m) in the changed middle. Schema edits are
// small, so the middle is too; switch to Myers if whole-schema rewrites of
// thousands of lines get slow.
func lineDiff(oldText, newText string) []diffLine {
	a, b := splitLines(oldText), splitLines(newText)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]

	// lcs[i][j] is the LCS length of ma[i:] and mb[j:].
	lcs := make([][]int32, len(ma)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(mb)+1)
	}
	for i := len(ma) - 1; i >= 0; i-- {
		for j := len(mb) - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	out := make([]diffLine, 0, len(a)+len(b)-len(ma)-len(mb))
	for _, l := range a[:pre] {
		out = append(out, diffLine{' ', l})
	}
	i, j := 0, 0
	for i < len(ma) || j < len(mb) {
		switch {
		case i < len(ma) && j < len(mb) && ma[i] == mb[j]:
			out = append(out, diffLine{' ', ma[i]})
			i++
			j++
		case j < len(mb) && (i == len(ma) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, diffLine{'+', mb[j]})
			j++
		default:
			out = append(out, diffLine{'-', ma[i]})
			i++
		}
	}
	for _, l := range a[len(a)-suf:] {
		out = append(out, diffLine{' ', l})
	}
	return out
}

// hunks groups a diff's changes with diffContext lines of context, merging
// hunks whose context would touch. No changes yields no hunks.
func hunks(lines []diffLine) []diffHunk {
	var out []diffHunk
	oldN, newN := 1, 1
	var cur *diffHunk
	lastChange := -1 // index in lines of cur's last change
	for k, l := range lines {
		if l.Op != ' ' {
			if cur == nil || k-lastChange > 2*diffContext {
				start := max(k-diffContext, lastChange+1, 0)
				if cur != nil {
					cur.Lines = append(cur.Lines, lines[lastChange+1:min(lastChange+1+diffContext, len(lines))]...)
					start = max(start, lastChange+1+diffContext)
				}
				back := k - start
				out = append(out, diffHunk{OldStart: oldN - back, NewStart: newN - back})
				cur = &out[len(out)-1]
				cur.Lines = append(cur.Lines, lines[start:k]...)
			} else {
				cur.Lines = append(cur.Lines, lines[lastChange+1:k]...)
			}
			cur.Lines = append(cur.Lines, l)
			lastChange = k
		}
		if l.Op != '+' {
			oldN++
		}
		if l.Op != '-' {
			newN++
		}
	}
	if cur != nil {
		cur.Lines = append(cur.Lines, lines[lastChange+1:min(lastChange+1+diffContext, len(lines))]...)
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
