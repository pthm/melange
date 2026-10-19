package studio

import (
	"regexp"
	"strings"
)

// fgaToken is a run of DSL text with a highlight class ("" for plain).
type fgaToken struct {
	Class string
	Text  string
}

// fgaSyntax matches one token per alternative; the submatch that fired picks
// the class (see fgaClasses).
var fgaSyntax = regexp.MustCompile(
	`\b(model|schema|relations|conditions?|module)\b` + // 1 keyword
		`|\b((?:extend\s+)?type|define)(\s+)([\w./-]+)` + // 2 keyword, 3 space, 4 declared name
		`|\b(or|and|but\s+not|from|with)\b` + // 5 operator
		`|(\d+\.\d+)` + // 6 version
		`|(#[\w-]+|:\*)` + // 7 userset relation or wildcard
		`|([\[\],:])`, // 8 punctuation
)

var fgaClasses = [...]string{1: "kw", 2: "kw", 3: "", 4: "name", 5: "op", 6: "num", 7: "ref", 8: "punct"}

// highlightFGA splits OpenFGA DSL into classed tokens. ponytail: a regex
// lexer, not a parser; it colors valid models and degrades to plain text.
func highlightFGA(dsl string) []fgaToken {
	var out []fgaToken
	for i, line := range strings.Split(dsl, "\n") {
		if i > 0 {
			out = append(out, fgaToken{Text: "\n"})
		}
		code, comment := splitComment(line)
		pos := 0
		for _, m := range fgaSyntax.FindAllStringSubmatchIndex(code, -1) {
			out = append(out, fgaToken{Text: code[pos:m[0]]})
			for g := 1; g < len(fgaClasses); g++ {
				if m[2*g] >= 0 {
					out = append(out, fgaToken{Class: fgaClasses[g], Text: code[m[2*g]:m[2*g+1]]})
				}
			}
			pos = m[1]
		}
		out = append(out, fgaToken{Text: code[pos:]}, fgaToken{Class: "comment", Text: comment})
	}
	return out
}

// splitComment cuts a line at a "#" that starts a comment: at line start or
// after whitespace, so "group#member" stays code.
func splitComment(line string) (code, comment string) {
	for i, r := range line {
		if r == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i], line[i:]
		}
	}
	return line, ""
}
