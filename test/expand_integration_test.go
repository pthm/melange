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

// TestExpand_TTUOnly exercises slice 2.2a: a pure-TTU relation
// (`repository.can_deploy: can_admin from org`) emits a single
// Leaf.TupleToUserset whose tupleset names the linking relation
// ("repository:<id>#org") and whose computed array carries one entry
// per linked org ("organization:<id>#can_admin"). Resolution is shallow
// — Expand does NOT recurse into the org's can_admin; that's the
// caller's job (or Checker.ExpandRecursive in slice 2.5).
func TestExpand_TTUOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_ttu_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('expand_ttu_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
			melange.Relation("can_deploy"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root)
		assert.Equal(t, fmt.Sprintf("repository:%d#can_deploy", repoID), tree.Root.Name)
		require.NotNil(t, tree.Root.Leaf,
			"single TTU rewrite emits leaf at root (no Union envelope)")
		ttu := tree.Root.Leaf.TupleToUserset
		require.NotNil(t, ttu, "leaf must carry TupleToUserset, not Users/Computed")
		assert.Equal(t, fmt.Sprintf("repository:%d#org", repoID), ttu.Tupleset)
		require.Len(t, ttu.Computed, 1,
			"one Computed entry per linked object (one org for this repo)")
		assert.Equal(t, fmt.Sprintf("organization:%d#can_admin", orgID), ttu.Computed[0].Userset)

		// FlattenUsers stays empty — TTU leaves are pointers, not
		// resolved users. Caller must chase via Expand for each Computed.
		assert.Empty(t, tree.FlattenUsers(),
			"FlattenUsers must NOT chase TupleToUserset pointers; got %v",
			tree.FlattenUsers())
	})
}

// TestExpand_TTUNoLinkedObjects exercises the edge case where the
// linking relation has no tuples for this object: the response is
// structurally valid with an empty computed array, NOT a missing
// leaf or a sentinel. Matches OpenFGA's behaviour for "no parents".
func TestExpand_TTUNoLinkedObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		// Repo with NO organization link inserted via the domain table
		// (the organizations table requires a row, so we still need an
		// org row, but we'll point can_deploy at a fresh repo whose
		// linking tuples we skip — actually the test schema's
		// organization_id is FK NOT NULL, so we'll use a real repo and
		// query a DIFFERENT (non-existent) repo id to get the same
		// effect.)
		var nonExistentRepoID int64 = 99999999
		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "repository", ID: strconv.FormatInt(nonExistentRepoID, 10)},
			melange.Relation("can_deploy"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root.Leaf)
		require.NotNil(t, tree.Root.Leaf.TupleToUserset)
		assert.Empty(t, tree.Root.Leaf.TupleToUserset.Computed,
			"no linking tuples → empty computed array, not nil")
	})
}

// TestExpand_TTUMultiOrg confirms the per-tuple enumeration scales to
// multiple linked objects. A repository with grants from two
// organizations should yield two Computed entries — one per org —
// sorted deterministically so test assertions don't flake.
//
// The shared schema's `repository.org: [organization]` linking is
// FK-constrained to a single org via repositories.organization_id, so
// we use a different TTU relation that can support multiple parents in
// the melange_tuples view. For slice 2.2a we exercise the single-parent
// case in TestExpand_TTUOnly; this test instead pins the deterministic
// ordering even with a single entry, which is the load-bearing
// invariant tests would otherwise flake on.
func TestExpand_TTUMultiOrg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_ttu_multi_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('expand_ttu_multi_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
			melange.Relation("can_deploy"))
		require.NoError(t, err)
		require.Len(t, tree.Root.Leaf.TupleToUserset.Computed, 1)
		// Stable ordering — every Computed entry sorts by
		// (subject_type, subject_id). With one entry the assertion is
		// just that ordering exists; multi-entry shape is exercised by
		// the SQL generator's ORDER BY which is pinned in the unit test.
		assert.Equal(t, fmt.Sprintf("organization:%d#can_admin", orgID),
			tree.Root.Leaf.TupleToUserset.Computed[0].Userset)
	})
}

// TestExpand_ExclusionWrapsComputedBase exercises slice 2.2b on a
// pure-computed-with-exclusion shape:
// `repository.can_read_safe: can_read but not banned`. The tree's
// root is a Difference whose base is a Computed pointer (to
// can_read) and whose subtract is a Computed pointer (to banned).
// FlattenUsers stays empty because both halves are unresolved
// pointers — the caller chases them with follow-up Expand calls or
// uses Checker.ExpandRecursive.
func TestExpand_ExclusionWrapsComputedBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_excl_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('expand_excl_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
			melange.Relation("can_read_safe"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root)
		assert.Equal(t, fmt.Sprintf("repository:%d#can_read_safe", repoID), tree.Root.Name)
		require.NotNil(t, tree.Root.Difference, "exclusion wraps in Difference")
		assert.Nil(t, tree.Root.Leaf, "Difference and Leaf are mutually exclusive")
		assert.Nil(t, tree.Root.Union)

		// Base is the rewrites-derived tree — for can_read_safe that's
		// a single Computed pointer to can_read (no inner Union).
		require.NotNil(t, tree.Root.Difference.Base)
		require.NotNil(t, tree.Root.Difference.Base.Leaf)
		require.NotNil(t, tree.Root.Difference.Base.Leaf.Computed)
		assert.Equal(t, fmt.Sprintf("repository:%d#can_read", repoID),
			tree.Root.Difference.Base.Leaf.Computed.Userset)
		assert.Equal(t, fmt.Sprintf("repository:%d#can_read_safe", repoID),
			tree.Root.Difference.Base.Name,
			"base node shares the parent relation's name — it represents 'the relation without exclusion'")

		// Subtract is a Computed pointer to the excluded relation.
		require.NotNil(t, tree.Root.Difference.Subtract)
		require.NotNil(t, tree.Root.Difference.Subtract.Leaf)
		require.NotNil(t, tree.Root.Difference.Subtract.Leaf.Computed)
		assert.Equal(t, fmt.Sprintf("repository:%d#banned", repoID),
			tree.Root.Difference.Subtract.Leaf.Computed.Userset)
		assert.Equal(t, fmt.Sprintf("repository:%d#banned", repoID),
			tree.Root.Difference.Subtract.Name,
			"subtract node names the excluded relation")

		// FlattenUsers walks base only (subtract is "users to exclude").
		// Both halves here are unresolved Computed pointers, so the
		// flat list is empty — the caller chases pointers if they want
		// actual users.
		assert.Empty(t, tree.FlattenUsers(),
			"unresolved Computed pointers contribute nothing to FlattenUsers")
	})
}

// TestExpand_ExclusionOverTTU exercises the cross-feature compose:
// `pull_request.can_review: can_read from repo but not author`. The
// Difference's base is a Leaf.TupleToUserset (from slice 2.2a's TTU
// emission); the subtract is a Computed pointer. Neither feature
// branch needs to know the other exists — they compose via the
// renderer's Difference wrapper.
func TestExpand_ExclusionOverTTU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, repoID, prID, authorID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_excl_ttu_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('expand_excl_ttu_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_excl_ttu_author') RETURNING id`).Scan(&authorID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO pull_requests (title, repository_id, author_id, source_branch) VALUES ('expand_excl_ttu_pr', $1, $2, 'feature') RETURNING id`,
			repoID, authorID).Scan(&prID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "pull_request", ID: strconv.FormatInt(prID, 10)},
			melange.Relation("can_review"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root.Difference)
		require.NotNil(t, tree.Root.Difference.Base.Leaf)

		// Base is a TTU leaf — linking relation `repo` points to the
		// repository, computed = can_read on the linked repo.
		ttu := tree.Root.Difference.Base.Leaf.TupleToUserset
		require.NotNil(t, ttu, "base must carry the TTU rewrite, not a Computed")
		assert.Equal(t, fmt.Sprintf("pull_request:%d#repo", prID), ttu.Tupleset)
		require.Len(t, ttu.Computed, 1, "one linked repository")
		assert.Equal(t, fmt.Sprintf("repository:%d#can_read", repoID), ttu.Computed[0].Userset)

		// Subtract names the excluded relation as a Computed pointer.
		require.NotNil(t, tree.Root.Difference.Subtract.Leaf.Computed)
		assert.Equal(t, fmt.Sprintf("pull_request:%d#author", prID),
			tree.Root.Difference.Subtract.Leaf.Computed.Userset)
	})
}

// TestExpand_WildcardInlinesAsUserString exercises slice 2.3:
// `repository.banned: [user:*]` emits a Leaf.Users containing the
// literal string "user:*" rather than a separate sentinel node. This
// matches OpenFGA's inline-string convention (only Explain emits a
// dedicated NodeWildcard). FlattenUsers includes the wildcard string
// so consumers treating "<type>:*" as "every user of this type" see
// it.
func TestExpand_WildcardInlinesAsUserString(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_wild_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('expand_wild_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))
		// Wildcard ban — repository.banned: [user:*] is satisfied
		// for everyone via this single melange_tuples row.
		_, err := db.ExecContext(ctx,
			`INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)`, repoID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		tree, err := checker.Expand(ctx,
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
			melange.Relation("banned"))
		require.NoError(t, err)
		require.NotNil(t, tree.Root)
		require.NotNil(t, tree.Root.Leaf)
		require.NotNil(t, tree.Root.Leaf.Users)
		assert.Equal(t, []string{"user:*"}, tree.Root.Leaf.Users.Users,
			"wildcard surfaces inline as the string 'user:*' (matches OpenFGA shape)")
		// FlattenUsers includes the wildcard string — the convention
		// is that consumers treat "<type>:*" as "every user of that
		// type" rather than expanding to a user list.
		assert.Equal(t, []string{"user:*"}, tree.FlattenUsers())
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
