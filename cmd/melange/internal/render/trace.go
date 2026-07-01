// Package render formats melange.Trace values for terminal display.
//
// The renderer is the shared presentation layer for both `melange explain`
// and `melange expand`. It walks a *melange.Trace and emits a unicode-tree-
// style summary suitable for human reading. JSON output is left to callers
// (encoding/json on the Trace value is sufficient — keys are already
// snake_case to match the SQL convention).
package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/pthm/melange/melange"
)

// Tree connector glyphs. Centralised so tests can spell them once.
const (
	branchLast = "└── "
	branchMore = "├── "
	indentLast = "    "
	indentMore = "│   "
)

// Header markers signalling the Explain result. Expand omits the marker
// entirely because it has no boolean result to communicate.
const (
	markerAllow = "✓"
	markerDeny  = "✗"
)

// Colour constants live in palette.go so trace.go and expand.go
// share the OpenFGA-mapped palette + structured painters. Retained
// alias names (ansiReset / ansiGreen / ansiRed / ansiGrey) keep
// pre-refactor test expectations working.

// Option configures a single Trace rendering. The zero value is colourless
// output suitable for piped writers and captured strings.
type Option func(*opts)

type opts struct {
	color bool
}

// WithColor enables ANSI colour escapes in the rendered output. Callers
// (e.g. the CLI command) typically detect TTY + NO_COLOR and pass the
// resolved bool.
func WithColor(enabled bool) Option {
	return func(o *opts) { o.color = enabled }
}

func paint(o opts, code, s string) string {
	if !o.color {
		return s
	}
	return code + s + ansiReset
}

// Trace writes a human-readable rendering of t to w. A nil trace prints
// nothing; an empty Root prints just the header line.
//
// The output shape:
//
//	<header>
//	└── <root node>
//	    ├── <child>
//	    └── <child>
//	        └── <grandchild>
//	<truncation footer if any>
//
// Explain headers include the subject and a ✓/✗ result marker. Expand
// headers carry only "<relation> on <object>" because the answer is the
// full tree, not a single boolean.
func Trace(w io.Writer, t *melange.Trace, options ...Option) {
	if t == nil {
		return
	}

	var o opts
	for _, opt := range options {
		opt(&o)
	}

	writeHeader(w, t, o)

	if t.Root != nil {
		writeNode(w, t.Root, "", true, o)
	}

	if t.Truncated {
		fmt.Fprintf(w, "\n(truncated after %d nodes — raise --max-nodes for more)\n", t.NodeCount)
	}
}

// TraceString renders t into a string. Convenience wrapper around Trace
// for callers that want the rendered output as a value (tests, log lines,
// HTTP responses).
func TraceString(t *melange.Trace, options ...Option) string {
	var b strings.Builder
	Trace(&b, t, options...)
	return b.String()
}

func writeHeader(w io.Writer, t *melange.Trace, o opts) {
	subj := paintUsersetIdent(o, t.Subject)
	obj := paintUsersetIdent(o, t.Object)
	rel := paintRelation(o, t.Relation)
	has := paintKeyword(o, "has")
	on := paintKeyword(o, "on")
	switch {
	case t.Subject != "" && t.Result != nil && *t.Result:
		fmt.Fprintf(w, "%s %s %s %s %s %s\n", paintAllowChip(o), subj, has, rel, on, obj)
	case t.Subject != "" && t.Result != nil && !*t.Result:
		notHas := paintKeyword(o, "does NOT have")
		fmt.Fprintf(w, "%s %s %s %s %s %s\n", paintDenyChip(o), subj, notHas, rel, on, obj)
	case t.Subject != "":
		// Explain trace missing Result is unusual but render something sensible.
		fmt.Fprintf(w, "? %s ?? %s %s %s\n", subj, rel, on, obj)
	default:
		fmt.Fprintf(w, "%s %s %s\n", rel, on, obj)
	}
}

// writeNode renders a single node and recurses into its children + evidence.
// The prefix/isLast args are the standard recursive tree-drawing parameters.
// Evidence rows are rendered as additional sub-items below children, except
// when a node has only one evidence row and no children — in which case the
// label inlines the tuple for compactness.
func writeNode(w io.Writer, n *melange.Node, prefix string, isLast bool, o opts) {
	branch, childPrefix := connectors(prefix, isLast)
	// Per-node Result mark (Explain only) — failed branches get a ✗ so
	// users can scan the failure path quickly.
	// Per-node failure markers use the pink foreground (not a chip)
	// so the tree stays visually calm — only the header verdict gets
	// the badge treatment. Bold for a bit more weight without a
	// background block.
	mark := ""
	if n.Result != nil && !*n.Result {
		mark = paint(o, "\x1b[1m"+colorDeny, markerDeny) + " "
	}
	fmt.Fprintf(w, "%s%s%s\n", paint(o, colorDim, prefix+branch), mark, formatNode(o, n))

	// Track which sub-items are last so connectors line up.
	subEvidence := n.Evidence
	if shouldInlineEvidence(n) {
		subEvidence = nil
	}

	total := len(n.Children) + len(subEvidence)
	for i, child := range n.Children {
		writeNode(w, child, childPrefix, i == total-1 && len(subEvidence) == 0, o)
	}
	for i, ev := range subEvidence {
		last := len(n.Children)+i == total-1
		branch, _ := connectors(childPrefix, last)
		fmt.Fprintf(w, "%s%s %s\n",
			paint(o, colorDim, childPrefix+branch),
			paintKeyword(o, "tuple:"),
			formatTuple(o, ev))
	}
}

func connectors(prefix string, isLast bool) (branch, childPrefix string) {
	if isLast {
		return branchLast, prefix + indentLast
	}
	return branchMore, prefix + indentMore
}

// shouldInlineEvidence is true when a node has exactly one evidence row and
// no other children, so we can collapse "label\n  └── tuple: …" into one line.
func shouldInlineEvidence(n *melange.Node) bool {
	return len(n.Evidence) == 1 && len(n.Children) == 0
}

// formatNode produces the human-readable summary line for a single node.
// The string never includes a newline — caller handles tree connectors.
// Nodes carrying free-form labels (via userset, via parent, exclusion,
// cycle) route labels through paintLabel so embedded userset refs and
// type restrictions get coloured too.
func formatNode(o opts, n *melange.Node) string {
	switch n.Type {
	case melange.NodeDirect:
		if shouldInlineEvidence(n) {
			return paintKeyword(o, "direct:") + " " + formatTuple(o, n.Evidence[0])
		}
		return paintLabel(o, labelOr(n.Label, "direct grant"))
	case melange.NodeImplied:
		return paintKeyword(o, "implied:") + " " + paintLabel(o, labelOr(n.Label, "via rewrite"))
	case melange.NodeUserset:
		return paintKeyword(o, "via userset:") + " " + paintLabel(o, labelOr(n.Label, "[type#relation]"))
	case melange.NodeTTU:
		return paintKeyword(o, "via parent:") + " " + paintLabel(o, labelOr(n.Label, "from parent"))
	case melange.NodeUnion:
		return paintKeyword(o, fmt.Sprintf("union of %d branches", len(n.Children)))
	case melange.NodeIntersection:
		return paintKeyword(o, fmt.Sprintf("intersection of %d parts", len(n.Children)))
	case melange.NodeExclusion:
		return paintLabel(o, labelOr(n.Label, "exclusion (base but not excluded)"))
	case melange.NodeWildcard:
		if len(n.Users) > 0 {
			return paintKeyword(o, "wildcard:") + " " +
				paint(o, colorType, n.Users[0].Type) + paintKeyword(o, ":*")
		}
		return paintKeyword(o, "wildcard")
	case melange.NodeCycle:
		return paintKeyword(o, "cycle at") + " " +
			paintUsersetIdent(o, labelOr(n.Label, "<unknown>")) + " " +
			paintKeyword(o, "(resolution stopped)")
	case melange.NodeTruncated:
		return paintKeyword(o, "... truncated (raise --max-nodes to extend)")
	default:
		return string(n.Type)
	}
}

func labelOr(label, fallback string) string {
	if label == "" {
		return fallback
	}
	return label
}

// formatTuple renders a TupleRef in the OpenFGA-style canonical form:
//
//	"user:alice" → "viewer" → "document:1"
//
// Single line, deterministic, easy to grep. Types render as type-name
// green, relations as relation cyan, arrows as keyword-grey.
func formatTuple(o opts, t melange.TupleRef) string {
	arrow := paintKeyword(o, "→")
	subj := paint(o, colorType, t.SubjectType) + paintKeyword(o, ":") + t.SubjectID
	obj := paint(o, colorType, t.ObjectType) + paintKeyword(o, ":") + t.ObjectID
	rel := paintRelation(o, t.Relation)
	return fmt.Sprintf("%s %s %s %s %s", subj, arrow, rel, arrow, obj)
}

// paintLabel colours embedded userset references and type
// restrictions inside a free-form label. Anything else in the label
// stays uncoloured. Two shapes are recognised:
//
//   - `[<inner>]` — anywhere in the label, wrapped in mint (matches
//     OpenFGA's type-restrictions colour).
//   - `<type>:<id>[#<rel>]` — wrapped via paintUsersetIdent so the
//     type / id / relation partitions match the palette.
//
// The rest of the label (prose like "via", "→", parent id names) is
// dimmed as keyword prose so it fades relative to the strong-tinted
// identifiers.
func paintLabel(o opts, label string) string {
	if !o.color || label == "" {
		return label
	}
	// Tokenise by whitespace so we can paint identifier-shaped tokens
	// individually. This keeps the highlighter deterministic (no
	// regex-driven surprises) and matches how VS Code's tokeniser
	// scans the DSL.
	fields := strings.Fields(label)
	if len(fields) == 0 {
		return label
	}
	for i, f := range fields {
		fields[i] = paintLabelToken(o, f)
	}
	return strings.Join(fields, " ")
}

// paintLabelToken applies one paint pass to a single whitespace-
// separated label token, in order:
//
//  1. Bracketed type restriction (`[type#rel]` or `[a, b]`)
//     → colorTypeRestr on the whole token.
//  2. Userset identifier (`type:id#rel`)
//     → paintUsersetIdent (type + id + rel partitions).
//  3. Object identifier (`type:id`) with no `#`
//     → paintObjectIdent.
//  4. Anything else → keyword-dim so it recedes visually.
//
// Trailing punctuation (`,`, `)`, `.`) is stripped before matching
// and re-appended dimmed so the token detection stays robust against
// label prose like "via editor, on document:1".
func paintLabelToken(o opts, tok string) string {
	if tok == "" {
		return tok
	}
	// Peel one trailing punctuation char so identifiers like
	// "document:1," classify correctly.
	trailing := ""
	if last := tok[len(tok)-1]; last == ',' || last == '.' || last == ')' || last == ';' {
		trailing = paintKeyword(o, string(last))
		tok = tok[:len(tok)-1]
	}
	// Similarly peel a leading `(` so "(via editor)" works.
	leading := ""
	if len(tok) > 0 && tok[0] == '(' {
		leading = paintKeyword(o, "(")
		tok = tok[1:]
	}

	switch {
	case len(tok) >= 2 && tok[0] == '[' && tok[len(tok)-1] == ']':
		return leading + paintTypeRestriction(o, tok) + trailing
	case strings.Contains(tok, "#") && strings.Contains(tok, ":"):
		return leading + paintUsersetIdent(o, tok) + trailing
	case strings.Contains(tok, ":") && !strings.ContainsAny(tok, "()[]{}<>"):
		return leading + paintObjectIdent(o, tok) + trailing
	default:
		return leading + paintKeyword(o, tok) + trailing
	}
}
