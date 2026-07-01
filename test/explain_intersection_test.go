package test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/testutil"
)

// Custom-schema Explain integration tests for patterns the shared
// test/testutil/testdata/schema.fga does not cover: intersection
// (`a and b`), userset references (`[group#member]`), and self-recursive
// TTU (which exercises the cycle-detection path). Each scenario builds its
// own database from an ad-hoc schema with a plain melange_tuples table,
// then drives Explain through the dispatcher and asserts both the tree
// shape and Check/Explain parity. The codegen tests in lib/sqlgen pin the
// SQL emission shape; this file is the end-to-end pin — JSONB → Trace
// decoding against a live PG instance with real tuples.

const intersectionSchema = `model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define both: writer and editor
`

const usersetSchema = `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type document
  relations
    define viewer: [user, group#member]
`

const recursiveTTUSchema = `model
  schema 1.1

type user

type folder
  relations
    define parent: [folder]
    define viewer: [user] or viewer from parent
`

// installAdHocSchema spins up an empty DB, installs a plain melange_tuples
// table backing the schema, applies the migration, and returns the live
// *sql.DB. Mirrors test/modular_test.go's setup so the pattern stays
// consistent across the explain custom-schema tests.
func installAdHocSchema(t *testing.T, ctx context.Context, schemaContent, version string) *sql.DB {
	t.Helper()
	db := testutil.EmptyDB(t)
	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			subject_relation TEXT NOT NULL DEFAULT '',
			relation TEXT NOT NULL,
			object_type TEXT NOT NULL,
			object_id TEXT NOT NULL
		)
	`)
	require.NoError(t, err, "creating melange_tuples table")

	_, migration := fullMigration(t, schemaContent, version)
	applyMigrationUp(t, ctx, db, migration)
	return db
}

// insertTuple appends a single row to the ad-hoc melange_tuples table.
// Kept inline so the fixture setup in each test reads top-to-bottom.
func insertTuple(t *testing.T, ctx context.Context, db *sql.DB, subjectType, subjectID, relation, objectType, objectID string) {
	t.Helper()
	_, err := db.ExecContext(ctx,
		`INSERT INTO melange_tuples (subject_type, subject_id, relation, object_type, object_id) VALUES ($1, $2, $3, $4, $5)`,
		subjectType, subjectID, relation, objectType, objectID)
	require.NoError(t, err, "inserting tuple")
}

// TestExplain_IntersectionSuccess pins the AND-group success path: when
// every part of an intersection group satisfies, the trace's root is a
// NodeIntersection whose children carry the per-part NodeDirect (or
// underlying) success traces. Explain.Result must also equal Check for
// the same inputs.
func TestExplain_IntersectionSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, intersectionSchema, "v1.5.0-isect-ok")

	// alice is both writer and editor — both must hold for `both`.
	insertTuple(t, ctx, db, "user", "alice", "writer", "document", "doc1")
	insertTuple(t, ctx, db, "user", "alice", "editor", "document", "doc1")

	checker := melange.NewChecker(db)
	subject := melange.Object{Type: "user", ID: "alice"}
	object := melange.Object{Type: "document", ID: "doc1"}

	allowed, err := checker.Check(ctx, subject, melange.Relation("both"), object)
	require.NoError(t, err)
	assert.True(t, allowed)

	trace, err := checker.Explain(ctx, subject, melange.Relation("both"), object)
	require.NoError(t, err)
	require.NotNil(t, trace.Result)
	assert.True(t, *trace.Result, "Explain must agree with Check for the success case")
	require.NotNil(t, trace.Root)
	assert.Equal(t, melange.NodeIntersection, trace.Root.Type,
		"success root for an AND-group is a NodeIntersection")
	require.Len(t, trace.Root.Children, 2,
		"intersection wraps one child per part (writer + editor)")
	for i, child := range trace.Root.Children {
		require.NotNilf(t, child.Result, "intersection child %d must carry result", i)
		assert.Truef(t, *child.Result, "every child of a successful intersection is true (child %d)", i)
	}
}

// TestExplain_IntersectionMissingPart pins the AND-group failure path:
// when even one part fails, the whole intersection is denied, the wrapping
// NodeIntersection (under v_attempts → NodeUnion) carries result=false, and
// the failing child is marked false while the passing one stays true.
func TestExplain_IntersectionMissingPart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, intersectionSchema, "v1.5.0-isect-fail")

	// bob has writer but NOT editor — the AND must fail.
	insertTuple(t, ctx, db, "user", "bob", "writer", "document", "doc2")

	checker := melange.NewChecker(db)
	subject := melange.Object{Type: "user", ID: "bob"}
	object := melange.Object{Type: "document", ID: "doc2"}

	allowed, err := checker.Check(ctx, subject, melange.Relation("both"), object)
	require.NoError(t, err)
	assert.False(t, allowed)

	trace, err := checker.Explain(ctx, subject, melange.Relation("both"), object)
	require.NoError(t, err)
	require.NotNil(t, trace.Result)
	assert.False(t, *trace.Result, "Explain must agree with Check for the failure case")
	require.NotNil(t, trace.Root)
	assert.Equal(t, melange.NodeUnion, trace.Root.Type,
		"failure root is the attempts union")

	// The union should carry exactly one NodeIntersection child with
	// result=false and per-part traces for writer (true) + editor (false).
	var isect *melange.Node
	for _, c := range trace.Root.Children {
		if c.Type == melange.NodeIntersection {
			isect = c
			break
		}
	}
	require.NotNil(t, isect, "failure union must record the attempted intersection")
	require.NotNil(t, isect.Result)
	assert.False(t, *isect.Result, "intersection attempt as a whole is false")
	require.Len(t, isect.Children, 2)
	// Find the writer (pass) vs editor (fail) sub-traces by their inner
	// result field. The renderer doesn't guarantee child order, so we
	// inspect by Result rather than position.
	var sawTrueChild, sawFalseChild bool
	for _, child := range isect.Children {
		require.NotNil(t, child.Result, "intersection part must carry result")
		if *child.Result {
			sawTrueChild = true
		} else {
			sawFalseChild = true
		}
	}
	assert.True(t, sawTrueChild, "writer part should resolve to true")
	assert.True(t, sawFalseChild, "editor part should resolve to false")
}

// TestExplain_UsersetReference exercises the FOR-loop emission for
// userset-subject grants (`viewer: [group#member]`). alice is a member of
// group:eng, and group:eng#member is granted viewer on doc — Explain must
// trace through the NodeUserset wrapping the group lookup, with the inner
// recursion resolving member through the dispatcher.
func TestExplain_UsersetReference(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, usersetSchema, "v1.4.0-userset")

	// alice → member of group:eng; group:eng#member → viewer of doc1.
	insertTuple(t, ctx, db, "user", "alice", "member", "group", "eng")
	insertTuple(t, ctx, db, "group", "eng#member", "viewer", "document", "doc1")

	checker := melange.NewChecker(db)
	subject := melange.Object{Type: "user", ID: "alice"}
	object := melange.Object{Type: "document", ID: "doc1"}

	allowed, err := checker.Check(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err)
	assert.True(t, allowed, "alice should be a viewer via group:eng#member")

	trace, err := checker.Explain(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err)
	require.NotNil(t, trace.Result)
	assert.True(t, *trace.Result, "Explain must agree with Check on the userset path")
	require.NotNil(t, trace.Root)
	assert.Equal(t, melange.NodeUserset, trace.Root.Type,
		"success root for a userset-grant resolution is a NodeUserset")
	assert.Contains(t, trace.Root.Label, "[group#member]",
		"userset label should name the pattern")
	assert.Contains(t, trace.Root.Label, "group:eng",
		"userset label should inline the resolved group identifier")
}

// TestExplain_UsersetReferenceFailureRecordsAttempts exercises the userset
// failure path: a grant tuple exists for group:eng#member but alice is not
// a member, so the membership recursion fails. The failure trace must record
// the NodeUserset attempt as a child of the attempts union.
func TestExplain_UsersetReferenceFailureRecordsAttempts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, usersetSchema, "v1.4.0-userset-fail")

	// group:eng#member granted viewer, but bob never joined the group.
	insertTuple(t, ctx, db, "group", "eng#member", "viewer", "document", "doc2")

	checker := melange.NewChecker(db)
	subject := melange.Object{Type: "user", ID: "bob"}
	object := melange.Object{Type: "document", ID: "doc2"}

	allowed, err := checker.Check(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err)
	assert.False(t, allowed)

	trace, err := checker.Explain(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err)
	require.NotNil(t, trace.Result)
	assert.False(t, *trace.Result)
	require.NotNil(t, trace.Root)
	assert.Equal(t, melange.NodeUnion, trace.Root.Type)
	var foundUsersetFailure bool
	for _, child := range trace.Root.Children {
		if child.Type == melange.NodeUserset && child.Result != nil && !*child.Result {
			foundUsersetFailure = true
			break
		}
	}
	assert.True(t, foundUsersetFailure,
		"failure union must record a userset attempt as result=false")
}

// TestExplain_RecursiveTTUDepthStops exercises the cycle-detection /
// depth-limit guard. A folder linked to itself as its own parent would
// recurse forever; the renderer's M2002 raise must convert that into a
// runtime error rather than an infinite loop or stack overflow.
//
// The check_permission counterpart raises with code M2002 ("resolution too
// complex"); explain_permission_internal mirrors the same guard. Both
// behaviors are required for the Explain tree to stop on pathological
// schemas. We also walk the returned tree to confirm a NodeCycle subtree
// is present — without that assertion the test would pass even if the
// cycle guard silently fell through without recording the cycle node,
// hiding a real regression.
func TestExplain_RecursiveTTUDepthStops(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, recursiveTTUSchema, "v1.3.0-recursive")

	// folder:a links to folder:a as its parent — a one-node cycle. No
	// direct viewer grant exists, so the resolver MUST go through the TTU
	// parent loop and re-enter explain_folder_viewer with v_key already in
	// p_visited, triggering the cycle guard.
	insertTuple(t, ctx, db, "folder", "a", "parent", "folder", "a")

	checker := melange.NewChecker(db)
	subject := melange.Object{Type: "user", ID: "alice"}
	object := melange.Object{Type: "folder", ID: "a"}

	trace, err := checker.Explain(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err, "depth/cycle guard must stop recursion without erroring")
	require.NotNil(t, trace.Result, "Explain must return a deterministic result")
	assert.False(t, *trace.Result, "no direct grant + self-cycle → deny")
	require.NotNil(t, trace.Root, "trace must have a root even when recursion stops on cycle")

	// Walk the tree looking for a NodeCycle. The cycle is reached inside
	// the TTU sub-trace, so it shows up as a descendant of a NodeTTU child
	// in the final failure union — not as the root itself.
	var hasCycle func(*melange.Node) bool
	hasCycle = func(n *melange.Node) bool {
		if n == nil {
			return false
		}
		if n.Type == melange.NodeCycle {
			return true
		}
		for _, c := range n.Children {
			if hasCycle(c) {
				return true
			}
		}
		return false
	}
	assert.True(t, hasCycle(trace.Root),
		"trace must surface a NodeCycle when the recursive TTU re-enters with v_key in visited; got:\n%+v",
		trace.Root)
}

// TestExplain_UsersetSubjectSelfReferential exercises the userset-subject
// pre-check Case 1 — when the SUBJECT being checked is itself a userset
// reference whose relation lives in the closure of the wrapping relation.
// Example: checking whether `group:eng#admin` has `member` on `group:eng`,
// where `member ← admin` in the closure. No tuple is required; the match
// is structural against the inlined closure VALUES table emitted by
// `blocks.UsersetSubjectSelfCheck`.
//
// Codegen test TestRenderExplainFunction_UsersetSubjectPreCheck pins the
// SQL shape; this is the only integration test that exercises Case 1
// against a live PostgreSQL.
func TestExplain_UsersetSubjectSelfReferential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	// group.member inherits from admin (member: [user] or admin). Checking
	// group:eng#admin against group:eng#member should resolve via the
	// closure without needing any tuples — admin ∈ member's closure.
	db := installAdHocSchema(t, ctx, `model
  schema 1.1

type user

type group
  relations
    define admin: [user]
    define member: [user] or admin
`, "v1.4.0-userset-subject")

	checker := melange.NewChecker(db)
	// Subject IS a userset reference: group:eng#admin
	subject := melange.Object{Type: "group", ID: "eng#admin"}
	object := melange.Object{Type: "group", ID: "eng"}

	allowed, err := checker.Check(ctx, subject, melange.Relation("member"), object)
	require.NoError(t, err)
	assert.True(t, allowed,
		"Check should resolve the self-referential userset via the member closure")

	trace, err := checker.Explain(ctx, subject, melange.Relation("member"), object)
	require.NoError(t, err)
	require.NotNil(t, trace.Result)
	assert.True(t, *trace.Result, "Explain must agree with Check on the self-referential path")
	require.NotNil(t, trace.Root)
	assert.Equal(t, melange.NodeUserset, trace.Root.Type,
		"the userset-subject pre-check returns a NodeUserset success root")
	assert.Contains(t, trace.Root.Label, "self-referential",
		"Case 1's success label calls out the self-referential match")
}

// recursiveMultiParentSchema covers a TTU with two allowed linking types
// (folder.parent: [folder, workspace]). The codegen handles the IN clause
// (lib/sqlgen/explain_render.go:buildExplainParentLinkingSelect adds
// `AllowedLinkingTypes` to the WHERE) but no integration test exercises a
// real multi-type linking row.
const recursiveMultiParentSchema = `model
  schema 1.1

type user

type workspace
  relations
    define member: [user]
    define viewer: member

type folder
  relations
    define parent: [folder, workspace]
    define viewer: [user] or viewer from parent
`

// TestExplain_TTUMultipleLinkingTypes pins the multi-parent-types TTU
// path: folder.parent can be either folder OR workspace, and a viewer on
// folder must successfully traverse the workspace.viewer branch when the
// parent is a workspace. Without this test, a regression in the
// AllowedLinkingTypes IN clause (e.g., quoting the wrong type, or dropping
// non-first entries) would only surface in production schemas that mix
// parent types.
func TestExplain_TTUMultipleLinkingTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	db := installAdHocSchema(t, ctx, recursiveMultiParentSchema, "v1.3.0-multiparent")

	// alice → member of workspace:acme → viewer of workspace:acme; folder:f
	// has workspace:acme as a parent (not a folder), so folder.viewer must
	// resolve via the workspace branch of the linking IN-list.
	insertTuple(t, ctx, db, "user", "alice", "member", "workspace", "acme")
	insertTuple(t, ctx, db, "workspace", "acme", "parent", "folder", "f")

	checker := melange.NewChecker(db)
	subject := melange.Object{Type: "user", ID: "alice"}
	object := melange.Object{Type: "folder", ID: "f"}

	allowed, err := checker.Check(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err)
	assert.True(t, allowed, "alice should view folder:f via workspace:acme parent")

	trace, err := checker.Explain(ctx, subject, melange.Relation("viewer"), object)
	require.NoError(t, err)
	require.NotNil(t, trace.Result)
	assert.True(t, *trace.Result,
		"Explain must agree with Check when the resolved parent is the non-first allowed type")
	require.NotNil(t, trace.Root)
	assert.Equal(t, melange.NodeTTU, trace.Root.Type,
		"success root for the resolved TTU path is a NodeTTU")
	assert.Contains(t, trace.Root.Label, "workspace:acme",
		"label should inline the resolved parent's type:id (workspace branch, not folder)")
}

// TestExplain_EmptyRawBytes_ReturnsError pins the defensive behavior at
// melange/explain.go:Explain when the underlying SQL scan yields NULL/empty
// bytes. In practice the dispatcher always returns a structurally valid
// JSONB envelope (even on unknown pairs — see explainNoEntrySentinelSQL),
// so a NULL response would be a bug; the test confirms callers see a
// JSON-decode error rather than a deceptive zero-valued Trace.
func TestExplain_EmptyRawBytes_ReturnsError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	// Schema doesn't matter — we're bypassing the dispatcher and forcing
	// a NULL response from a synthetic function. Use the recursive schema
	// because it's the smallest one we already build for this file.
	db := installAdHocSchema(t, ctx, recursiveTTUSchema, "v1.3.0-null-resp")

	// Replace explain_permission with a NULL-returning stub for this test
	// only. The signature matches explainDispatcherPublicArgs() in
	// lib/sqlgen/explain_functions.go.
	_, err := db.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION explain_permission(
			p_subject_type TEXT,
			p_subject_id TEXT,
			p_relation TEXT,
			p_object_type TEXT,
			p_object_id TEXT,
			p_max_nodes INTEGER DEFAULT NULL
		) RETURNS JSONB
		LANGUAGE sql IMMUTABLE AS $$ SELECT NULL::JSONB $$
	`)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	_, err = checker.Explain(ctx,
		melange.Object{Type: "user", ID: "alice"},
		melange.Relation("viewer"),
		melange.Object{Type: "folder", ID: "f"})
	require.Error(t, err, "NULL trace bytes must surface as a JSON decode error, not a zero Trace")
}
