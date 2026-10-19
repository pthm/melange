package studio

import (
	"regexp"
	"strings"
)

// codeToken is a run of source text with a highlight class ("" for plain).
type codeToken struct {
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
func highlightFGA(dsl string) []codeToken {
	var out []codeToken
	for i, line := range strings.Split(dsl, "\n") {
		if i > 0 {
			out = append(out, codeToken{Text: "\n"})
		}
		code, comment := splitComment(line)
		out = lex(out, fgaSyntax, fgaClasses[:], code)
		out = append(out, codeToken{Class: "comment", Text: comment})
	}
	return out
}

// lex appends src to out as tokens: text between matches is plain, and each
// match is split by its capture groups, classed by classes[group].
func lex(out []codeToken, re *regexp.Regexp, classes []string, src string) []codeToken {
	pos := 0
	for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
		out = append(out, codeToken{Text: src[pos:m[0]]})
		for g := 1; g < len(classes); g++ {
			if m[2*g] >= 0 {
				out = append(out, codeToken{Class: classes[g], Text: src[m[2*g]:m[2*g+1]]})
			}
		}
		pos = m[1]
	}
	return append(out, codeToken{Text: src[pos:]})
}

// sqlSyntax covers what pg_get_functiondef prints for generated functions:
// SQL and PL/pgSQL keywords, comments, strings, numbers, quoted identifiers
// and function calls (bold, so check_* dispatch is easy to follow).
var sqlSyntax = regexp.MustCompile(
	`(--[^\n]*|/\*[\s\S]*?\*/)` + // 1 comment
		`|('(?:[^']|'')*')` + // 2 string
		`|(\$\w*\$)` + // 3 dollar-quote delimiter
		`|(?i:\b(select|from|where|and|or|not|in|is|null|exists|union|all|as|on|join|left|inner|lateral|with|recursive|case|when|then|else|end|limit|order|by|group|having|distinct|values|create|replace|function|returns|return|query|table|setof|language|plpgsql|sql|stable|immutable|volatile|strict|parallel|safe|unsafe|security|definer|invoker|set|to|begin|declare|if|elsif|loop|for|foreach|while|exit|continue|perform|raise|exception|true|false|cast|array|into|using|execute|coalesce|any|boolean|integer|int|bigint|text|record|void|default)\b)` + // 4 keyword
		`|("[^"]*")` + // 5 quoted identifier
		`|\b(\d+(?:\.\d+)?)\b` + // 6 number
		`|\b([a-z_][\w.]*)(\s*\()`, // 7 function name, 8 "("
)

var sqlClasses = [...]string{1: "comment", 2: "ref", 3: "punct", 4: "kw", 5: "name", 6: "num", 7: "name", 8: ""}

// highlightSQL splits SQL into classed tokens. ponytail: a regex lexer, like
// highlightFGA; anything it misreads still renders, just uncolored.
func highlightSQL(src string) []codeToken {
	return lex(nil, sqlSyntax, sqlClasses[:], src)
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
