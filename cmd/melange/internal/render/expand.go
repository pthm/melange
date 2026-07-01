package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/pthm/melange/melange"
)

// Expand writes a human-readable rendering of an OpenFGA-shaped
// UsersetTree to w. A nil tree prints nothing; an empty Root prints
// just a "(no nodes)" placeholder so the output is never silent on a
// successfully-deserialised but degenerate response.
//
// Output shape mirrors the Explain renderer's tree style but using
// OpenFGA's terminology: each node prints its name followed by either
// the leaf payload (users / computed pointer / TTU pointer) or the
// union/intersection/difference structure with children.
func Expand(w io.Writer, t *melange.UsersetTree, options ...Option) {
	if t == nil {
		return
	}
	var o opts
	for _, opt := range options {
		opt(&o)
	}
	if t.Root == nil {
		fmt.Fprintln(w, "(empty tree)")
		return
	}
	writeExpandNode(w, t.Root, "", true, o)
}

// ExpandString renders an UsersetTree into a string. Convenience
// wrapper around Expand for tests, log lines, and HTTP responses.
func ExpandString(t *melange.UsersetTree, options ...Option) string {
	var b strings.Builder
	Expand(&b, t, options...)
	return b.String()
}

// writeExpandNode prints a single UsersetTreeNode and recurses through
// children. The prefix/isLast args are the standard tree-drawing
// parameters; the Explain renderer's connector glyphs are reused so
// the visual style stays consistent between melange explain and
// melange expand output.
func writeExpandNode(w io.Writer, n *melange.UsersetTreeNode, prefix string, isLast bool, o opts) {
	if n == nil {
		return
	}
	branch, childPrefix := connectors(prefix, isLast)
	fmt.Fprintf(w, "%s%s\n", paint(o, colorDim, prefix+branch), formatExpandHeader(o, n))

	switch {
	case n.Leaf != nil:
		writeLeaf(w, n.Leaf, childPrefix, o)
	case n.Union != nil:
		writeNodes(w, n.Union.Nodes, childPrefix, o)
	case n.Intersection != nil:
		writeNodes(w, n.Intersection.Nodes, childPrefix, o)
	case n.Difference != nil:
		// Two named slots in fixed order; not-last for the first child so
		// the connector column flows into the subtract branch correctly.
		// writeExpandNode handles nil children, so no guards needed.
		writeExpandNode(w, n.Difference.Base, childPrefix, false, o)
		writeExpandNode(w, n.Difference.Subtract, childPrefix, true, o)
	}
}

// formatExpandHeader produces the single-line summary for a node. The
// name (`<type>:<id>#<relation>`) is colourised via paintUsersetIdent;
// the slot descriptor is dimmed as keyword prose so the identifier
// pops visually against it.
func formatExpandHeader(o opts, n *melange.UsersetTreeNode) string {
	name := paintUsersetIdent(o, n.Name)
	sep := " " + paintKeyword(o, "•") + " "
	switch {
	case n.Leaf != nil:
		return name + sep + paintKeyword(o, leafKind(n.Leaf))
	case n.Union != nil:
		return name + sep + paintKeyword(o, fmt.Sprintf("union of %d", len(n.Union.Nodes)))
	case n.Intersection != nil:
		return name + sep + paintKeyword(o, fmt.Sprintf("intersection of %d", len(n.Intersection.Nodes)))
	case n.Difference != nil:
		return name + sep + paintKeyword(o, "difference (base / subtract)")
	default:
		return name
	}
}

// leafKind returns the discriminator name for a leaf's populated slot.
// Exactly one of Users / Computed / TupleToUserset is populated on a
// well-formed leaf; an unpopulated leaf returns "empty" so the failure
// mode is visible rather than silent.
func leafKind(l *melange.Leaf) string {
	switch {
	case l.Users != nil:
		return "users"
	case l.Computed != nil:
		return "computed pointer"
	case l.TupleToUserset != nil:
		return "tuple-to-userset pointer"
	default:
		return "empty"
	}
}

// writeLeaf renders the leaf's value slot below the header. Each
// user-string in Leaf.Users gets its own tree leaf so consumers can scan
// the column for a known subject without word-wrapping concerns.
// Computed and TupleToUserset pointers print as single lines with a
// "(follow with melange expand …)" hint so users know they can chase
// them.
func writeLeaf(w io.Writer, l *melange.Leaf, prefix string, o opts) {
	switch {
	case l.Users != nil:
		users := l.Users.Users
		if len(users) == 0 {
			branch, _ := connectors(prefix, true)
			fmt.Fprintf(w, "%s%s\n",
				paint(o, colorDim, prefix+branch),
				paintKeyword(o, "(no users)"))
		}
		for i, u := range users {
			last := i == len(users)-1
			branch, _ := connectors(prefix, last)
			fmt.Fprintf(w, "%s%s\n",
				paint(o, colorDim, prefix+branch),
				paintUsersetIdent(o, u))
		}
		// Truncation on an empty result is degenerate but possible if the
		// cap is 0; still surface the warning so the user knows something
		// was elided.
		if l.Users.UsersTruncated {
			fmt.Fprintf(w, "%s%s\n",
				paint(o, colorDim, prefix),
				paint(o, colorDeny, "(users_truncated — raise --max-leaf to see more)"))
		}
	case l.Computed != nil:
		branch, _ := connectors(prefix, true)
		fmt.Fprintf(w, "%s%s %s %s  %s\n",
			paint(o, colorDim, prefix+branch),
			paintKeyword(o, "computed"),
			paintKeyword(o, "→"),
			paintUsersetIdent(o, l.Computed.Userset),
			paintKeyword(o, "(melange expand "+l.Computed.Userset+" to chase)"))
	case l.TupleToUserset != nil:
		branch, _ := connectors(prefix, true)
		fmt.Fprintf(w, "%s%s %s %s\n",
			paint(o, colorDim, prefix+branch),
			paintKeyword(o, "tupleset"),
			paintKeyword(o, "→"),
			paintUsersetIdent(o, l.TupleToUserset.Tupleset))
		for i, c := range l.TupleToUserset.Computed {
			last := i == len(l.TupleToUserset.Computed)-1
			sub, _ := connectors(prefix+indentLast, last)
			fmt.Fprintf(w, "%s%s %s %s\n",
				paint(o, colorDim, prefix+indentLast+sub),
				paintKeyword(o, "computed"),
				paintKeyword(o, "→"),
				paintUsersetIdent(o, c.Userset))
		}
	}
}

// writeNodes is the union / intersection child walker. Same shape for
// both — the discriminator is the wrapper, not the child layout.
func writeNodes(w io.Writer, nodes []*melange.UsersetTreeNode, prefix string, o opts) {
	for i, child := range nodes {
		writeExpandNode(w, child, prefix, i == len(nodes)-1, o)
	}
}
