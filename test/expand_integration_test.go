package test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/testutil"
)

// TestExpand_DirectGrantUsers exercises slice 2.1's pure-direct path.
// `organization.owner: [user]` has a single rewrite (direct) so the
// renderer skips the Union envelope and emits the Leaf.Users directly
// under the root. Inserted users appear in the OpenFGA-formatted
// `user:<id>` form.
func TestExpand_DirectGrantUsers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, aliceID, bobID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_direct_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_direct_alice') RETURNING id`).Scan(&aliceID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_direct_bob') RETURNING id`).Scan(&bobID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'owner')`,
			orgID, aliceID, bobID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
			melange.Relation("owner"))
		require.NoError(t, err)
		require.NotNil(t, tree)
		require.NotNil(t, tree.Root)
		assert.Equal(t, fmt.Sprintf("organization:%d#owner", orgID), tree.Root.Name)
		require.NotNil(t, tree.Root.Leaf, "single-rewrite relation emits a leaf at root")
		require.NotNil(t, tree.Root.Leaf.Users)
		expected := []string{
			fmt.Sprintf("user:%d", aliceID),
			fmt.Sprintf("user:%d", bobID),
		}
		assert.ElementsMatch(t, expected, tree.Root.Leaf.Users.Users)
		assert.False(t, tree.Root.Leaf.Users.UsersTruncated,
			"no cap set — UsersTruncated must stay false")

		// FlattenUsers matches the leaf payload for a single-leaf tree
		assert.ElementsMatch(t, expected, tree.FlattenUsers())
	})
}

// TestExpand_DirectAndComputedUnion exercises slice 2.1's multi-rewrite
// path. `organization.admin: [user] or owner` emits a Union with two
// children — a Leaf.Users for the direct grant and a Leaf.Computed
// pointer to `<obj>:#owner`. The pointer is NOT chased (shallow
// expansion); the caller's Check or follow-up Expand resolves it.
func TestExpand_DirectAndComputedUnion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, ownerID, directAdminID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_union_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_union_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_union_admin') RETURNING id`).Scan(&directAdminID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'admin')`,
			orgID, ownerID, directAdminID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
			melange.Relation("admin"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root)
		assert.Equal(t, fmt.Sprintf("organization:%d#admin", orgID), tree.Root.Name)
		require.NotNil(t, tree.Root.Union, "multi-rewrite relation emits Union at root")
		require.Len(t, tree.Root.Union.Nodes, 2)

		// One child is the direct Leaf.Users (just the direct admin grant —
		// the owner does NOT appear in this leaf, that's the computed
		// pointer's domain).
		var direct, computed *melange.UsersetTreeNode
		for _, child := range tree.Root.Union.Nodes {
			switch {
			case child.Leaf != nil && child.Leaf.Users != nil:
				direct = child
			case child.Leaf != nil && child.Leaf.Computed != nil:
				computed = child
			}
		}
		require.NotNil(t, direct, "union must contain a direct-grant leaf")
		require.NotNil(t, computed, "union must contain a computed-pointer leaf")
		// Direct leaf carries only the [user]-direct grants. The owner is
		// a separate rewrite — it surfaces as the Computed pointer below,
		// not here.
		assert.Equal(t, []string{fmt.Sprintf("user:%d", directAdminID)}, direct.Leaf.Users.Users)
		// Computed pointer names the implied relation on the same object.
		assert.Equal(t, fmt.Sprintf("organization:%d#owner", orgID), computed.Leaf.Computed.Userset)

		// FlattenUsers collects only the direct grants (it does NOT chase
		// the Computed pointer — that's the caller's job via
		// Checker.ExpandRecursive, which lands in slice 2.5).
		assert.Equal(t, []string{fmt.Sprintf("user:%d", directAdminID)}, tree.FlattenUsers())
	})
}

// TestExpand_PureComputed exercises a relation with no direct grants:
// `organization.can_read: member` emits a single Leaf.Computed pointer.
// No melange_tuples lookup happens; the pointer is the entire response.
func TestExpand_PureComputed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_pure_computed_org') RETURNING id`).Scan(&orgID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
			melange.Relation("can_read"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root)
		assert.Equal(t, fmt.Sprintf("organization:%d#can_read", orgID), tree.Root.Name)
		require.NotNil(t, tree.Root.Leaf, "pure-computed relation emits a single leaf")
		require.NotNil(t, tree.Root.Leaf.Computed, "leaf is the Computed pointer")
		assert.Equal(t, fmt.Sprintf("organization:%d#member", orgID), tree.Root.Leaf.Computed.Userset)
		assert.Nil(t, tree.Root.Leaf.Users, "pure-computed leaf must not carry a Users payload")
	})
}

// TestExpand_SubjectTypeFilter exercises the Melange extension that
// narrows Leaf.Users to one subject type. With the filter set, users of
// other types are dropped from the array; the tree structure is
// otherwise unaffected.
//
// The shared schema's `organization.owner` only accepts users, so we
// test the filter's negative case (passing a type that has no grants)
// to verify the SELECT WHERE clause honours the filter rather than
// returning the unfiltered set.
func TestExpand_SubjectTypeFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, userID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_filter_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_filter_user') RETURNING id`).Scan(&userID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))

		// Baseline: no filter, the user appears.
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
			melange.Relation("owner"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root.Leaf)
		require.NotNil(t, tree.Root.Leaf.Users)
		assert.Contains(t, tree.Root.Leaf.Users.Users, fmt.Sprintf("user:%d", userID))

		// Filter to user — same result, the row is a user.
		filtered, err := checker.Expand(ctx,
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
			melange.Relation("owner"),
			melange.WithSubjectTypeFilter("user"))
		require.NoError(t, err)
		assert.Contains(t, filtered.Root.Leaf.Users.Users, fmt.Sprintf("user:%d", userID))

		// Filter to a non-matching type — the array empties.
		empty, err := checker.Expand(ctx,
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
			melange.Relation("owner"),
			melange.WithSubjectTypeFilter("never_a_real_type"))
		require.NoError(t, err)
		require.NotNil(t, empty.Root.Leaf)
		require.NotNil(t, empty.Root.Leaf.Users)
		assert.Empty(t, empty.Root.Leaf.Users.Users,
			"subject_type filter must drop non-matching rows; got %v",
			empty.Root.Leaf.Users.Users)
	})
}

// TestExpand_NotYetSupportedSentinel exercises the dispatcher's
// no-entry sentinel: when the (object_type, relation) pair is unknown
// OR the relation uses a feature gated out of slice 2.1 (TTU,
// intersection, exclusion, usersets, wildcards), the dispatcher returns
// an empty Leaf.Users tree — structurally valid OpenFGA-shape JSON so
// consumers don't crash, but caller must cross-reference Check to
// distinguish "no users" from "expand not supported".
//
// Direct SQL call so we hit the dispatcher's ELSE branch with an
// inarguably-unknown pair.
func TestExpand_NotYetSupportedSentinel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var raw []byte
		err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT %s($1, $2, $3, NULL, NULL)::text",
				sqldsl.PrefixIdent("expand_permission", databaseSchema)),
			"widget", "42", "nonexistent_relation").Scan(&raw)
		require.NoError(t, err)

		var tree melange.UsersetTree
		require.NoError(t, json.Unmarshal(raw, &tree))
		require.NotNil(t, tree.Root)
		assert.Equal(t, "widget:42#nonexistent_relation", tree.Root.Name)
		require.NotNil(t, tree.Root.Leaf, "sentinel emits a leaf, not a union")
		require.NotNil(t, tree.Root.Leaf.Users, "sentinel emits an empty Users payload")
		assert.Empty(t, tree.Root.Leaf.Users.Users)
	})
}

// TestExpand_AgreesWithListSubjects is the cross-API parity invariant:
// for relations slice 2.1 supports end-to-end (direct + direct+computed
// chains), the flattened Expand result — chasing the computed pointer
// once where present — must equal the list_accessible_subjects output.
// This is the "we didn't lose anyone" check that catches subtle SQL
// regressions (a typo in the WHERE clause, a missing subject_type filter)
// without needing dedicated assertions per relation.
//
// Slice 2.5's Checker.ExpandRecursive will subsume this; meanwhile the
// test exercises a one-level manual chase to keep the parity invariant
// active.
func TestExpand_AgreesWithListSubjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, ownerID, adminID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_parity_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_parity_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_parity_admin') RETURNING id`).Scan(&adminID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner'), ($1, $3, 'admin')`,
			orgID, ownerID, adminID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		object := melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)}

		// list_subjects for admin: returns both direct admin AND owner
		// (because Check resolves the implied closure).
		listed, err := checker.ListSubjectsAll(ctx, object, melange.Relation("admin"), melange.ObjectType("user"))
		require.NoError(t, err)
		listedUsers := make([]string, len(listed))
		for i, id := range listed {
			listedUsers[i] = "user:" + id
		}

		// Expand for admin: shallow tree with direct leaf + computed
		// pointer. Flatten the direct leaf, then issue a follow-up
		// Expand for the pointer and flatten that too. Union should
		// equal list_subjects.
		tree, err := checker.Expand(ctx, object, melange.Relation("admin"))
		require.NoError(t, err)
		got := tree.FlattenUsers()
		var computedPointer string
		for _, child := range tree.Root.Union.Nodes {
			if child.Leaf != nil && child.Leaf.Computed != nil {
				computedPointer = child.Leaf.Computed.Userset
			}
		}
		if computedPointer != "" {
			// Manual one-level chase — slice 2.5 wraps this in
			// Checker.ExpandRecursive.
			child, err := checker.Expand(ctx, object, melange.Relation("owner"))
			require.NoError(t, err)
			got = append(got, child.FlattenUsers()...)
		}

		// Sort + dedupe both sides before comparing (FlattenUsers
		// dedupes within a single tree; the manual chase can
		// re-introduce duplicates if the same user appears in both
		// rewrites).
		assert.ElementsMatch(t, listedUsers, dedupe(got),
			"Expand+chase must agree with list_subjects for the same (object, relation)")
	})
}

// dedupe returns a copy of in with duplicate entries removed. Order is
// not preserved; callers that compare via ElementsMatch don't care.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
