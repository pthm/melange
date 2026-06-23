package melange

// ExplainOption configures a single Checker.Explain call. Options resolve
// either as direct SQL parameters to the explain dispatcher or as
// `SET LOCAL melange.*` statements at request start; both paths honour the
// three-tier priority: per-call > session GUC > built-in default.

// ExplainOption configures a single Checker.Explain call.
type ExplainOption func(*explainOpts)

// explainOpts holds resolved Explain options. The zero value means
// "use server-side defaults" (i.e. no SET LOCAL statements are emitted).
type explainOpts struct {
	maxNodes int // 0 => unset
}

// applyExplain runs each option against a fresh explainOpts and returns it.
func applyExplain(options []ExplainOption) explainOpts {
	var o explainOpts
	for _, opt := range options {
		opt(&o)
	}
	return o
}

// WithExplainMaxNodes caps the total node count in a single Explain trace.
// When unset, the value resolves via the session GUC
// `melange.max_explain_nodes`, falling back to the built-in default (100).
// Pass <= 0 to mean "unset". When the cap is hit the returned Trace has
// Truncated=true and ends in a NodeTruncated subtree.
func WithExplainMaxNodes(n int) ExplainOption {
	return func(o *explainOpts) {
		if n > 0 {
			o.maxNodes = n
		}
	}
}

// ExpandOption configures a single Checker.Expand call. Both options are
// Melange extensions on top of OpenFGA's Expand surface — OpenFGA itself
// has neither a per-leaf cap nor a subject-type filter, but both are
// genuinely useful for admin / "who from team X" flows and would
// otherwise force client-side filtering on potentially huge user sets.
type ExpandOption func(*expandOpts)

// expandOpts holds resolved Expand options. The zero value means "no
// extensions in effect" — i.e. full OpenFGA-compatible behaviour.
type expandOpts struct {
	subjectType ObjectType // empty => no filter (all subject types)
	maxLeaf     int        // 0 => unset (unbounded, OpenFGA-equivalent)
}

// applyExpand runs each option against a fresh expandOpts and returns it.
func applyExpand(options []ExpandOption) expandOpts {
	var o expandOpts
	for _, opt := range options {
		opt(&o)
	}
	return o
}

// WithSubjectTypeFilter narrows every Leaf.Users array to the given
// subject type. Concrete users of other types and userset references
// rooted in other types are dropped; the tree structure (Union /
// Intersection / Difference / Computed / TupleToUserset pointers) is
// unaffected. Empty means "no filter".
//
// This is a Melange extension — OpenFGA Expand returns all subject
// types together and leaves filtering to the client.
func WithSubjectTypeFilter(subjectType ObjectType) ExpandOption {
	return func(o *expandOpts) {
		o.subjectType = subjectType
	}
}

// WithExpandMaxLeaf caps the number of entries in any single
// Leaf.Users array. When the cap fires the affected Users carries
// UsersTruncated=true; OpenFGA consumers ignore the field (it
// serialises as omitempty), Melange clients surface a warning. Pass
// <= 0 to mean "unset" (unbounded, matching OpenFGA's behaviour).
//
// This is a Melange extension — OpenFGA Expand returns all matching
// subjects without a cap.
func WithExpandMaxLeaf(n int) ExpandOption {
	return func(o *expandOpts) {
		if n > 0 {
			o.maxLeaf = n
		}
	}
}
