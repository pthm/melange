package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
)

// Slice 2.2c integration tests for Expand on relations that use
// intersection (`a and b`). The shared schema.fga has no intersection
// patterns so these run against ad-hoc schemas via installAdHocSchema
// (declared in test/explain_intersection_test.go). The renderer's
// per-rewrite plan derivation is pinned in
// lib/sqlgen/expand_render_test.go; here we exercise the end-to-end
// JSONB → UsersetTree decoding against a live PG instance with real
// tuples.

const expandIntersectionSchema = `model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define banned: [user]
    define both: writer and editor
    define both_safe: (writer and editor) but not banned
`

// TestExpand_IntersectionSimple pins `viewer: writer and editor` —
// emits a Nodes intersection with two children, each a shallow
// Leaf.Computed pointer to the part relation. Resolution is shallow
// (matches OpenFGA): the pointers are NOT chased; the caller
// inspects each part individually or uses Checker.ExpandRecursive
// (slice 2.5).
func TestExpand_IntersectionSimple(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, expandIntersectionSchema, "v2.2c-intersection")

	// alice is both writer and editor; bob is only writer. The
	// Expand tree shape is independent of the actual tuples because
	// it surfaces pointers, not resolved users — both fixture rows
	// exist primarily for the parity sweep below.
	insertTuple(t, ctx, db, "user", "alice", "writer", "document", "doc1")
	insertTuple(t, ctx, db, "user", "alice", "editor", "document", "doc1")
	insertTuple(t, ctx, db, "user", "bob", "writer", "document", "doc1")

	checker := melange.NewChecker(db)
	tree, err := checker.Expand(ctx,
		melange.Object{Type: "document", ID: "doc1"},
		melange.Relation("both"))
	require.NoError(t, err)
	require.NotNil(t, tree.Root)
	assert.Equal(t, "document:doc1#both", tree.Root.Name)
	require.NotNil(t, tree.Root.Intersection, "root must be a Nodes intersection")
	assert.Nil(t, tree.Root.Leaf, "Intersection and Leaf are mutually exclusive on a node")
	assert.Nil(t, tree.Root.Union, "single intersection group must not be wrapped in Union")
	assert.Nil(t, tree.Root.Difference, "no exclusion — Difference must not appear")

	require.Len(t, tree.Root.Intersection.Nodes, 2, "two parts: writer and editor")
	// Children share their part's name (not the parent's) so consumers
	// can correlate each intersection branch with the schema rewrite.
	names := []string{
		tree.Root.Intersection.Nodes[0].Name,
		tree.Root.Intersection.Nodes[1].Name,
	}
	assert.ElementsMatch(t, []string{"document:doc1#writer", "document:doc1#editor"}, names)

	// Each part is a Leaf.Computed pointer — Expand does NOT
	// recursively resolve intersection members.
	for i, child := range tree.Root.Intersection.Nodes {
		require.NotNilf(t, child.Leaf, "part %d must be a leaf", i)
		require.NotNilf(t, child.Leaf.Computed, "part %d must be a Computed pointer (Users would be resolution, not allowed in Expand)", i)
		assert.Equalf(t, child.Name, child.Leaf.Computed.Userset,
			"part %d Computed.userset must match the node name", i)
	}

	// FlattenUsers stays empty because every leaf is an unresolved
	// pointer. The caller would chase each Computed to get the user
	// list of each part and intersect them client-side (or call
	// Checker.ExpandRecursive in slice 2.5).
	assert.Empty(t, tree.FlattenUsers(),
		"unresolved Computed pointers contribute nothing to FlattenUsers")
}

// TestExpand_IntersectionWithExclusion exercises slice 2.2b × 2.2c
// composition: `viewer: (writer and editor) but not banned`. The
// Difference's base is the Nodes intersection from this slice; the
// subtract is a Computed pointer to the excluded relation from slice
// 2.2b. The two features compose via the renderer's wrap order:
// rewrites build the intersection root, then exclusion wraps it as
// the base of a Difference.
func TestExpand_IntersectionWithExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, expandIntersectionSchema, "v2.2c-intersection-excl")

	checker := melange.NewChecker(db)
	tree, err := checker.Expand(ctx,
		melange.Object{Type: "document", ID: "doc2"},
		melange.Relation("both_safe"))
	require.NoError(t, err)
	require.NotNil(t, tree.Root)
	require.NotNil(t, tree.Root.Difference, "exclusion must wrap in Difference")
	assert.Nil(t, tree.Root.Intersection,
		"intersection lives inside the Difference base, not at the root")

	// Base of the Difference carries the intersection node.
	base := tree.Root.Difference.Base
	require.NotNil(t, base)
	require.NotNil(t, base.Intersection,
		"the rewrites-derived tree (intersection) is the base of the Difference")
	assert.Equal(t, "document:doc2#both_safe", base.Name,
		"base shares the parent relation's name — it represents 'the relation without exclusion'")
	require.Len(t, base.Intersection.Nodes, 2)

	// Subtract is a Computed pointer to the excluded relation.
	sub := tree.Root.Difference.Subtract
	require.NotNil(t, sub)
	require.NotNil(t, sub.Leaf)
	require.NotNil(t, sub.Leaf.Computed)
	assert.Equal(t, "document:doc2#banned", sub.Leaf.Computed.Userset)
	assert.Equal(t, "document:doc2#banned", sub.Name,
		"subtract names the excluded relation")
}

// TestExpand_IntersectionEligibilityCheck confirms Expand is callable
// (non-error, well-formed tree) for an intersection relation even
// when no tuples exist — the response shape is determined by the
// schema, not by tuple presence. This is the "shallow Expand returns
// structure not data" invariant.
func TestExpand_IntersectionEligibilityCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, expandIntersectionSchema, "v2.2c-intersection-empty")

	checker := melange.NewChecker(db)
	tree, err := checker.Expand(ctx,
		melange.Object{Type: "document", ID: "empty"},
		melange.Relation("both"))
	require.NoError(t, err, "Expand must succeed even with zero tuples")
	require.NotNil(t, tree.Root)
	require.NotNil(t, tree.Root.Intersection,
		"intersection structure derives from the schema, not from tuples")
	assert.Len(t, tree.Root.Intersection.Nodes, 2)
}
