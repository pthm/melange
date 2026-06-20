package render

import (
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
)

// boolPtr lifts a literal bool to a pointer for the Trace.Result field.
// Inlined into fixtures so the trace shape is easy to read.
func boolPtr(b bool) *bool { return &b }

func TestTrace_NilAndEmpty(t *testing.T) {
	if got := TraceString(nil); got != "" {
		t.Errorf("nil trace should render empty; got %q", got)
	}

	tr := &melange.Trace{Object: "doc:1", Relation: "viewer"}
	got := TraceString(tr)
	// Expand-style header — no result marker, no root.
	want := "viewer on doc:1\n"
	if got != want {
		t.Errorf("empty trace mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestTrace_ExplainAllowDirect_InlineEvidence(t *testing.T) {
	tr := &melange.Trace{
		Object:   "document:1",
		Relation: "viewer",
		Subject:  "user:alice",
		Result:   boolPtr(true),
		Root: &melange.Node{
			Type:  melange.NodeDirect,
			Label: "direct grant",
			Evidence: []melange.TupleRef{
				{SubjectType: "user", SubjectID: "alice", Relation: "viewer", ObjectType: "document", ObjectID: "1"},
			},
		},
		NodeCount: 1,
	}

	got := TraceString(tr)
	want := "✓ user:alice has viewer on document:1\n" +
		"└── direct: user:alice → viewer → document:1\n"
	if got != want {
		t.Errorf("inline-evidence allow mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTrace_ExplainDeny_NestedFailurePaths(t *testing.T) {
	tr := &melange.Trace{
		Object:   "document:1",
		Relation: "viewer",
		Subject:  "user:bob",
		Result:   boolPtr(false),
		Root: &melange.Node{
			Type: melange.NodeUnion,
			Children: []*melange.Node{
				{
					// First child has a grandchild so the continuation prefix
					// "    │   " gets exercised on a real line.
					Type: melange.NodeDirect,
					Children: []*melange.Node{
						{Type: melange.NodeImplied, Label: "no match"},
					},
				},
				{Type: melange.NodeImplied, Label: "editor ⇒ viewer (no editor grant)"},
				{
					Type:  melange.NodeUserset,
					Label: "via group:eng#member",
					Children: []*melange.Node{
						{Type: melange.NodeDirect}, // no label → "direct grant"
					},
				},
			},
		},
	}

	got := TraceString(tr)
	if !strings.Contains(got, "✗ user:bob does NOT have viewer on document:1") {
		t.Errorf("missing header line; got:\n%s", got)
	}
	if !strings.Contains(got, "union of 3 branches") {
		t.Errorf("missing union summary; got:\n%s", got)
	}
	// Non-last child uses ├──; last child uses └──.
	if !strings.Contains(got, "├── direct grant\n") {
		t.Errorf("non-last child should use branch connector; got:\n%s", got)
	}
	if !strings.Contains(got, "└── via userset: via group:eng#member\n") {
		t.Errorf("last child should use last-branch connector; got:\n%s", got)
	}
	// Grandchild under last child gets "    " (four-space) prefix.
	if !strings.Contains(got, "    └── direct grant") {
		t.Errorf("grandchild under last child must have spaced prefix; got:\n%s", got)
	}
	// Grandchild under non-last child carries the "│   " continuation prefix
	// so the column stays connected to the next sibling above.
	if !strings.Contains(got, "    │   ") {
		t.Errorf("continuation prefix missing for non-last siblings' subtree; got:\n%s", got)
	}
}

func TestTrace_Expand_UnionWithWildcard(t *testing.T) {
	// Expand-style trace (no Subject, no Result) with a wildcard sentinel as
	// one of the union children. Verifies the header is unmarked and the
	// wildcard label resolves through the SubjectRef Users field.
	tr := &melange.Trace{
		Object:   "document:1",
		Relation: "viewer",
		Root: &melange.Node{
			Type: melange.NodeUnion,
			Children: []*melange.Node{
				{Type: melange.NodeDirect, Label: "direct grant"},
				{
					Type: melange.NodeWildcard,
					Users: []melange.SubjectRef{
						{Type: "user", ID: "*"},
					},
				},
			},
		},
	}

	got := TraceString(tr)
	if strings.HasPrefix(got, "✓") || strings.HasPrefix(got, "✗") {
		t.Errorf("Expand header must not carry result marker; got:\n%s", got)
	}
	if !strings.HasPrefix(got, "viewer on document:1\n") {
		t.Errorf("unexpected expand header; got:\n%s", got)
	}
	if !strings.Contains(got, "wildcard: user:*") {
		t.Errorf("wildcard sentinel missing; got:\n%s", got)
	}
}

func TestTrace_PerNodeResultMarksFailures(t *testing.T) {
	// Explain failure-path tracing: each denied branch carries result=false
	// so the renderer marks it with ✗.
	tr := &melange.Trace{
		Object:   "doc:1",
		Relation: "viewer",
		Subject:  "user:bob",
		Result:   boolPtr(false),
		Root: &melange.Node{
			Type:   melange.NodeUnion,
			Result: boolPtr(false),
			Children: []*melange.Node{
				{Type: melange.NodeDirect, Label: "no direct grant", Result: boolPtr(false)},
				// Mixed: a succeeding sibling should NOT carry the ✗ marker.
				{Type: melange.NodeImplied, Label: "editor ⇒ viewer", Result: boolPtr(true)},
			},
		},
	}
	got := TraceString(tr)

	if !strings.Contains(got, "✗ no direct grant") {
		t.Errorf("failed node should be marked with ✗; got:\n%s", got)
	}
	// Successful node must not have a ✗.
	if strings.Contains(got, "✗ implied: editor") {
		t.Errorf("succeeding node must not carry deny mark; got:\n%s", got)
	}
}

func TestTrace_CycleAndTruncatedNodes(t *testing.T) {
	tr := &melange.Trace{
		Object:    "doc:1",
		Relation:  "viewer",
		Subject:   "user:alice",
		Result:    boolPtr(false),
		Truncated: true,
		NodeCount: 100,
		Root: &melange.Node{
			Type: melange.NodeUnion,
			Children: []*melange.Node{
				{Type: melange.NodeCycle, Label: "doc:1#viewer"},
				{Type: melange.NodeTruncated},
			},
		},
	}
	got := TraceString(tr)

	if !strings.Contains(got, "cycle at doc:1#viewer") {
		t.Errorf("cycle node missing; got:\n%s", got)
	}
	if !strings.Contains(got, "... truncated") {
		t.Errorf("truncated node missing; got:\n%s", got)
	}
	if !strings.Contains(got, "(truncated after 100 nodes") {
		t.Errorf("trace-level truncation footer missing; got:\n%s", got)
	}
}

func TestTrace_IntersectionAndExclusion(t *testing.T) {
	tr := &melange.Trace{
		Object:   "doc:1",
		Relation: "viewer",
		Subject:  "user:alice",
		Result:   boolPtr(true),
		Root: &melange.Node{
			Type: melange.NodeIntersection,
			Children: []*melange.Node{
				{Type: melange.NodeDirect, Label: "writer"},
				{
					Type: melange.NodeExclusion,
					Children: []*melange.Node{
						{Type: melange.NodeDirect, Label: "editor (base)"},
						{Type: melange.NodeDirect, Label: "blocked (excluded)"},
					},
				},
			},
		},
	}
	got := TraceString(tr)
	if !strings.Contains(got, "intersection of 2 parts") {
		t.Errorf("intersection summary missing; got:\n%s", got)
	}
	if !strings.Contains(got, "exclusion (base but not excluded)") {
		t.Errorf("exclusion summary missing; got:\n%s", got)
	}
}

func TestTrace_EvidenceMultiAndWithChildren(t *testing.T) {
	// Evidence is rendered as sibling tree leaves only when not inline-able.
	tr := &melange.Trace{
		Object:   "doc:1",
		Relation: "viewer",
		Subject:  "user:alice",
		Result:   boolPtr(true),
		Root: &melange.Node{
			Type:  melange.NodeUserset,
			Label: "via group:eng#member",
			Children: []*melange.Node{
				{Type: melange.NodeDirect, Label: "member of group:eng"},
			},
			Evidence: []melange.TupleRef{
				{SubjectType: "group", SubjectID: "eng#member", Relation: "editor", ObjectType: "doc", ObjectID: "1"},
				{SubjectType: "user", SubjectID: "alice", Relation: "member", ObjectType: "group", ObjectID: "eng"},
			},
		},
	}
	got := TraceString(tr)
	// Both tuples present
	if !strings.Contains(got, "tuple: group:eng#member → editor → doc:1") {
		t.Errorf("first tuple missing; got:\n%s", got)
	}
	if !strings.Contains(got, "tuple: user:alice → member → group:eng") {
		t.Errorf("second tuple missing; got:\n%s", got)
	}
	// Child precedes evidence so the connector order is child, then tuples.
	childIdx := strings.Index(got, "member of group:eng")
	firstTupleIdx := strings.Index(got, "tuple: group:eng#member")
	if childIdx < 0 || firstTupleIdx < 0 || childIdx > firstTupleIdx {
		t.Errorf("child should render before evidence; got:\n%s", got)
	}
}

func TestColor_OffByDefault_OnWhenEnabled(t *testing.T) {
	result := true
	tr := &melange.Trace{
		Subject: "user:alice", Relation: "viewer", Object: "document:1",
		Result: &result,
		Root: &melange.Node{
			Type:     melange.NodeDirect,
			Evidence: []melange.TupleRef{{SubjectType: "user", SubjectID: "alice", Relation: "viewer", ObjectType: "document", ObjectID: "1"}},
		},
	}

	plain := TraceString(tr)
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("default output should be plain, got escapes:\n%q", plain)
	}

	colored := TraceString(tr, WithColor(true))
	if !strings.Contains(colored, ansiGreen+markerAllow+ansiReset) {
		t.Errorf("expected green-wrapped ✓ marker; got:\n%q", colored)
	}
	if !strings.Contains(colored, ansiGrey+branchLast+ansiReset) {
		t.Errorf("expected grey-wrapped tree connector; got:\n%q", colored)
	}
}

func TestFormatTuple_Stable(t *testing.T) {
	got := formatTuple(melange.TupleRef{
		SubjectType: "user", SubjectID: "alice",
		Relation:   "viewer",
		ObjectType: "document", ObjectID: "1",
	})
	want := "user:alice → viewer → document:1"
	if got != want {
		t.Errorf("formatTuple = %q, want %q", got, want)
	}
}

