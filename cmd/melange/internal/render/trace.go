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
func Trace(w io.Writer, t *melange.Trace) {
	if t == nil {
		return
	}

	writeHeader(w, t)

	if t.Root != nil {
		writeNode(w, t.Root, "", true)
	}

	if t.Truncated {
		fmt.Fprintf(w, "\n(truncated after %d nodes — raise --max-nodes for more)\n", t.NodeCount)
	}
}

// TraceString renders t into a string. Convenience wrapper around Trace
// for callers that want the rendered output as a value (tests, log lines,
// HTTP responses).
func TraceString(t *melange.Trace) string {
	var b strings.Builder
	Trace(&b, t)
	return b.String()
}

func writeHeader(w io.Writer, t *melange.Trace) {
	switch {
	case t.Subject != "" && t.Result != nil && *t.Result:
		fmt.Fprintf(w, "%s %s has %s on %s\n", markerAllow, t.Subject, t.Relation, t.Object)
	case t.Subject != "" && t.Result != nil && !*t.Result:
		fmt.Fprintf(w, "%s %s does NOT have %s on %s\n", markerDeny, t.Subject, t.Relation, t.Object)
	case t.Subject != "":
		// Explain trace missing Result is unusual but render something sensible.
		fmt.Fprintf(w, "? %s ?? %s on %s\n", t.Subject, t.Relation, t.Object)
	default:
		fmt.Fprintf(w, "%s on %s\n", t.Relation, t.Object)
	}
}

// writeNode renders a single node and recurses into its children + evidence.
// The prefix/isLast args are the standard recursive tree-drawing parameters.
// Evidence rows are rendered as additional sub-items below children, except
// when a node has only one evidence row and no children — in which case the
// label inlines the tuple for compactness.
func writeNode(w io.Writer, n *melange.Node, prefix string, isLast bool) {
	branch, childPrefix := connectors(prefix, isLast)
	// Per-node Result mark (Explain only) — failed branches get a ✗ so
	// users can scan the failure path quickly.
	mark := ""
	if n.Result != nil && !*n.Result {
		mark = markerDeny + " "
	}
	fmt.Fprintf(w, "%s%s%s%s\n", prefix, branch, mark, formatNode(n))

	// Track which sub-items are last so connectors line up.
	subEvidence := n.Evidence
	if shouldInlineEvidence(n) {
		subEvidence = nil
	}

	total := len(n.Children) + len(subEvidence)
	for i, child := range n.Children {
		writeNode(w, child, childPrefix, i == total-1 && len(subEvidence) == 0)
	}
	for i, ev := range subEvidence {
		last := len(n.Children)+i == total-1
		branch, _ := connectors(childPrefix, last)
		fmt.Fprintf(w, "%s%stuple: %s\n", childPrefix, branch, formatTuple(ev))
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
func formatNode(n *melange.Node) string {
	switch n.Type {
	case melange.NodeDirect:
		if shouldInlineEvidence(n) {
			return fmt.Sprintf("direct: %s", formatTuple(n.Evidence[0]))
		}
		return labelOr(n.Label, "direct grant")
	case melange.NodeImplied:
		return fmt.Sprintf("implied: %s", labelOr(n.Label, "via rewrite"))
	case melange.NodeUserset:
		return fmt.Sprintf("via userset: %s", labelOr(n.Label, "[type#relation]"))
	case melange.NodeTTU:
		return fmt.Sprintf("via parent: %s", labelOr(n.Label, "from parent"))
	case melange.NodeUnion:
		return fmt.Sprintf("union of %d branches", len(n.Children))
	case melange.NodeIntersection:
		return fmt.Sprintf("intersection of %d parts", len(n.Children))
	case melange.NodeExclusion:
		return labelOr(n.Label, "exclusion (base but not excluded)")
	case melange.NodeWildcard:
		if len(n.Users) > 0 {
			return fmt.Sprintf("wildcard: %s:*", n.Users[0].Type)
		}
		return "wildcard"
	case melange.NodeCycle:
		return fmt.Sprintf("cycle at %s (resolution stopped)", labelOr(n.Label, "<unknown>"))
	case melange.NodeTruncated:
		return "... truncated (raise --max-nodes to extend)"
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
// Single line, deterministic, easy to grep.
func formatTuple(t melange.TupleRef) string {
	return fmt.Sprintf("%s:%s → %s → %s:%s",
		t.SubjectType, t.SubjectID, t.Relation, t.ObjectType, t.ObjectID)
}
