package melange

// Trace is the root of a resolution tree returned by Explain or Expand.
//
// Explain populates Subject and Result; Expand leaves Subject empty and
// Result nil. The Root node is the entry point into the resolution tree;
// every other field is metadata about the response shape.
//
// Trace values mirror the JSONB returned by the generated explain_* and
// expand_* PostgreSQL functions. JSON tags use snake_case to match the
// SQL column conventions documented in specs/proposals/EXPLAIN_AND_EXPAND.md;
// downstream code can rebind to camelCase via separate struct types if needed.
type Trace struct {
	Object   string `json:"object"`
	Relation string `json:"relation"`
	Subject  string `json:"subject,omitempty"`

	// Result is populated by Explain only. nil for Expand.
	Result *bool `json:"result,omitempty"`

	// Root is the top-level node of the resolution tree.
	Root *Node `json:"root"`

	// Truncated is set when the trace was capped by p_max_nodes / p_max_leaf.
	Truncated bool `json:"truncated,omitempty"`

	// NodeCount is the number of resolution nodes the function visited
	// before returning, regardless of whether truncation occurred.
	NodeCount int `json:"node_count,omitempty"`
}

// NodeType discriminates Node variants. Each value names a distinct
// resolution shape the renderer must know how to draw.
type NodeType string

const (
	// NodeDirect: satisfied by a direct tuple in melange_tuples.
	NodeDirect NodeType = "direct"
	// NodeImplied: satisfied by a rewrite (e.g. viewer ← editor).
	NodeImplied NodeType = "implied"
	// NodeUserset: satisfied via a [type#relation] subject reference.
	NodeUserset NodeType = "userset"
	// NodeTTU: satisfied via "relation from parent" traversal.
	NodeTTU NodeType = "ttu"
	// NodeUnion: OR aggregation node. Appears in Expand output only.
	NodeUnion NodeType = "union"
	// NodeIntersection: AND aggregation node.
	NodeIntersection NodeType = "intersection"
	// NodeExclusion: BUT NOT aggregation node.
	NodeExclusion NodeType = "exclusion"
	// NodeWildcard: a [type:*] sentinel; never enumerated.
	NodeWildcard NodeType = "wildcard"
	// NodeCycle: cycle detected during recursive resolution; subtree omitted.
	NodeCycle NodeType = "cycle"
	// NodeTruncated: p_max_nodes hit; subtree omitted.
	NodeTruncated NodeType = "truncated"
)

// Node is a single step in the resolution tree. The semantics of each
// field depend on Type (see NodeType constants).
//
//   - Evidence is populated on leaf-like Explain nodes that resolve via
//     melange_tuples (NodeDirect, NodeUserset, NodeTTU).
//   - Children carries sub-resolutions: branches of a union/intersection/
//     exclusion, or nested resolution steps for implied/userset/TTU.
//   - Users carries a single sentinel entry with ID="*" on NodeWildcard.
type Node struct {
	Type     NodeType     `json:"type"`
	Label    string       `json:"label,omitempty"`
	Evidence []TupleRef   `json:"evidence,omitempty"`
	Children []*Node      `json:"children,omitempty"`
	Users    []SubjectRef `json:"users,omitempty"`

	// Result records whether the branch succeeded or failed. Required for
	// failure-path tracing — the renderer marks "✗ no editor grant"-style
	// entries on denied subtrees. nil on safety-stop nodes
	// (NodeCycle / NodeTruncated).
	Result *bool `json:"result,omitempty"`
}

// TupleRef points at a melange_tuples row that contributed to the resolution.
// The five fields uniquely identify the tuple within melange_tuples.
type TupleRef struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Relation    string `json:"relation"`
	ObjectType  string `json:"object_type"`
	ObjectID    string `json:"object_id"`
}

// SubjectRef names a subject in an Expand leaf or wildcard sentinel.
// Type may include a userset suffix (e.g. "group#member") to preserve the
// usersetreferences semantics; ID="*" on a wildcard node.
type SubjectRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
