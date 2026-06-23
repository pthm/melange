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

// TestExplain_DirectGrantSuccess inserts an owner tuple and verifies that
// explain_permission returns a Trace whose root is a NodeDirect with the
// matching evidence. Exercises Stage 1 slice 1's end-to-end pipeline:
// migration → dispatcher routing → per-relation function → JSONB envelope →
// Checker.Explain JSON deserialization.
func TestExplain_DirectGrantSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('explain_owner') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('explain_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("owner"),
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace)

		assert.Equal(t, fmt.Sprintf("organization:%d", orgID), trace.Object)
		assert.Equal(t, "owner", string(trace.Relation))
		assert.Equal(t, fmt.Sprintf("user:%d", userID), trace.Subject)
		require.NotNil(t, trace.Result, "Explain must populate Result")
		assert.True(t, *trace.Result, "owner of org should resolve to true")

		require.NotNil(t, trace.Root, "Trace must have a root node")
		assert.Equal(t, melange.NodeDirect, trace.Root.Type)
		require.Len(t, trace.Root.Evidence, 1, "direct grant must surface the matching tuple")
		ev := trace.Root.Evidence[0]
		assert.Equal(t, "user", ev.SubjectType)
		assert.Equal(t, strconv.FormatInt(userID, 10), ev.SubjectID)
		assert.Equal(t, "owner", ev.Relation)
		assert.Equal(t, "organization", ev.ObjectType)
		assert.Equal(t, strconv.FormatInt(orgID, 10), ev.ObjectID)
	})
}

// TestExplain_DirectGrantFailure verifies the failure-path trace shape: the
// envelope reports result=false and the root is a NodeUnion whose children
// include at least the direct-grant failure attempt. This is the structural
// promise; later slices will add implied/userset/TTU attempts to the union.
func TestExplain_DirectGrantFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('not_owner') RETURNING id`).Scan(&userID))
		// Org owned by a different user so 'not_owner' lacks the grant.
		var ownerUserID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('actual_owner') RETURNING id`).Scan(&ownerUserID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('other_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, ownerUserID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("owner"),
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace)

		require.NotNil(t, trace.Result)
		assert.False(t, *trace.Result, "non-owner must resolve to false")
		require.NotNil(t, trace.Root)
		assert.Equal(t, melange.NodeUnion, trace.Root.Type,
			"failure root is a union of all attempted branches")

		var foundDirectFailure bool
		for _, child := range trace.Root.Children {
			if child.Type == melange.NodeDirect && child.Result != nil && !*child.Result {
				foundDirectFailure = true
				break
			}
		}
		assert.True(t, foundDirectFailure,
			"failure union must record a direct-grant attempt as result=false")
	})
}

// TestExplain_TTUSuccess exercises slice 1.3: explain a relation that
// resolves through a TTU path (`can_admin from org` on repository). The
// trace's root is the user's ownership tuple wrapped in an implied →
// success chain on the parent organization, with each TTU/Implied node
// labelled informatively. This is the first integration test where slice
// 1.2's implied infrastructure also activates: the dispatcher routes
// through explain_permission_internal, which calls
// explain_organization_can_admin, whose body in turn finds the owner
// tuple via the inlined closure list.
func TestExplain_TTUSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		// alice → owner on org → org is the parent of repo → repo.can_admin
		// resolves via "can_admin from org".
		var userID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ttu_alice') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('ttu_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('ttu_repo', $1) RETURNING id`, orgID).Scan(&repoID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_deploy"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace)
		require.NotNil(t, trace.Result, "Explain must populate Result")
		assert.True(t, *trace.Result, "alice should can_deploy via TTU through org admin")

		require.NotNil(t, trace.Root)
		// Root is the success path — a NodeTTU wrapping the parent's child trace.
		assert.Equal(t, melange.NodeTTU, trace.Root.Type,
			"TTU success root carries the discovered linking path")
		assert.Contains(t, trace.Root.Label, "via org →",
			"label surfaces the linking relation")
		assert.Contains(t, trace.Root.Label, "can_admin",
			"label surfaces the parent relation")
		require.Len(t, trace.Root.Children, 1,
			"NodeTTU wraps a single child — the parent trace's root")
	})
}

// TestExplain_TTUFailureRecordsAttempts verifies that a failed TTU resolution
// records the attempted linking paths as failure children under a NodeUnion
// root, even when no linking tuples exist. This is the "tried but couldn't
// find a match" UX.
func TestExplain_TTUFailureRecordsAttempts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ttu_notmember') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('ttu_emptyorg') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('ttu_emptyrepo', $1) RETURNING id`, orgID).Scan(&repoID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_deploy"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace.Result)
		assert.False(t, *trace.Result)

		require.NotNil(t, trace.Root)
		assert.Equal(t, melange.NodeUnion, trace.Root.Type,
			"failure root is the union of attempted branches")
		// At minimum the TTU branch should be present as a failure attempt
		// (because the linking tuple exists — org links the repo to its org —
		// even though alice has no membership).
		var foundTTUFailure bool
		for _, child := range trace.Root.Children {
			if child.Type == melange.NodeTTU && child.Result != nil && !*child.Result {
				foundTTUFailure = true
				break
			}
		}
		assert.True(t, foundTTUFailure,
			"failure union should record a TTU attempt as result=false")
	})
}

// TestExplain_WildcardSentinel exercises slice 1.6's NodeWildcard
// emission. The test schema's `repository.banned: [user:*]` accepts a
// wildcard subject; when alice is checked, the trace should report a
// NodeWildcard rather than a regular NodeDirect.
func TestExplain_WildcardSentinel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, ownerID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('wildcard_alice') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('wildcard_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('wildcard_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, ownerID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('wildcard_repo', $1) RETURNING id`, orgID).Scan(&repoID))
		// Wildcard ban: applies to all users via the wildcard subject.
		_, err = db.ExecContext(ctx,
			`INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)`, repoID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("banned"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace.Result)
		assert.True(t, *trace.Result, "wildcard ban should match alice")

		require.NotNil(t, trace.Root)
		assert.Equal(t, melange.NodeWildcard, trace.Root.Type,
			"trace should surface the wildcard sentinel, not NodeDirect")
		require.Len(t, trace.Root.Users, 1)
		assert.Equal(t, "user", trace.Root.Users[0].Type)
		assert.Equal(t, "*", trace.Root.Users[0].ID)
	})
}

// TestExplain_TruncationCapsTrace exercises slice 1.6's truncation: when
// the per-call max-nodes is set low enough that accumulation crosses it,
// the returned trace's envelope marks `Truncated=true`. The root may be
// NodeTruncated (when the per-call check fires mid-recursion) or a
// regular union with the envelope flag set (when local appends only
// overshoot at the very end); we only assert the envelope flag here.
func TestExplain_TruncationCapsTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('trunc_user') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('trunc_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('trunc_repo', $1) RETURNING id`, orgID).Scan(&repoID))

		// Cap the budget at 1 node. The first recursive call inside any
		// TTU resolution will exceed it, forcing the body to return a
		// NodeTruncated trace.
		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_deploy"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
			melange.WithExplainMaxNodes(1),
		)
		require.NoError(t, err)
		assert.True(t, trace.Truncated, "trace should be marked truncated")
	})
}

// TestExplain_ImpliedChainSuccess exercises the implied-attempt path through
// real PG. `organization.admin: [user] or owner` resolves a non-direct admin
// grant via the owner closure entry — Explain must surface a NodeDirect whose
// label calls out the implied chain ("direct or implied grant via owner")
// rather than collapsing to "direct grant". This is the closure-list (HasDirect
// + multi-RelationList) implied path, which the codegen tests pin in isolation
// (TestRenderExplainFunction_ImpliedFunctionCallEmits) but never exercise
// end-to-end against real JSONB → Go decoding.
func TestExplain_ImpliedChainSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('implied_owner') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('implied_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("admin"),
			melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace.Result)
		assert.True(t, *trace.Result, "owner should imply admin")
		require.NotNil(t, trace.Root)
		// admin's RelationList includes both 'admin' and 'owner'; the success
		// surfaces a NodeDirect whose label calls out the implying relation.
		assert.Equal(t, melange.NodeDirect, trace.Root.Type)
		assert.Contains(t, trace.Root.Label, "owner",
			"implied label should name the underlying relation")
		require.Len(t, trace.Root.Evidence, 1)
		assert.Equal(t, "owner", trace.Root.Evidence[0].Relation,
			"evidence should carry the actual tuple's relation")
	})
}

// TestExplain_ExclusionSuccessNodeWrap exercises the exclusion success path:
// the base grant matches AND the exclusion predicate is false, so the root is
// a NodeExclusion{result: true} wrapping the underlying success node. The
// codegen test TestRenderExplainFunction_ExclusionWrapsSuccess pins the SQL
// shape; this checks the end-to-end deserialised tree.
func TestExplain_ExclusionSuccessNodeWrap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		// repository.can_read_safe: can_read but not banned. Grant the user
		// reader directly so can_read holds; do not insert a wildcard ban,
		// so the exclusion does not fire.
		var userID, ownerID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('excl_reader') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('excl_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('excl_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, ownerID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('excl_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))
		_, err = db.ExecContext(ctx,
			`INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`,
			repoID, userID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_read_safe"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace.Result)
		assert.True(t, *trace.Result, "reader without ban should pass can_read_safe")
		require.NotNil(t, trace.Root)
		assert.Equal(t, melange.NodeExclusion, trace.Root.Type,
			"successful exclusion wraps the inner success in NodeExclusion")
		assert.Contains(t, trace.Root.Label, "exclusion did not fire")
		require.Len(t, trace.Root.Children, 1, "NodeExclusion wraps exactly one inner node")
		inner := trace.Root.Children[0]
		require.NotNil(t, inner.Result)
		assert.True(t, *inner.Result, "inner success node should be marked true")
	})
}

// TestExplain_ExclusionFiredRecordsDenial exercises the failure path of the
// exclusion success-return helper: when the base grant matches but the
// exclusion predicate fires, the helper appends a NodeExclusion{result:false}
// to the attempts union and falls through. The final root is the failure
// NodeUnion whose children contain that excluded-grant attempt.
func TestExplain_ExclusionFiredRecordsDenial(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		// repository.can_read_safe: can_read but not banned, with a wildcard
		// ban applied so every user is excluded even when they have a reader
		// grant.
		var userID, ownerID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('excl_fired_reader') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('excl_fired_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('excl_fired_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, ownerID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('excl_fired_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))
		_, err = db.ExecContext(ctx,
			`INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`,
			repoID, userID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx,
			`INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)`, repoID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_read_safe"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
		)
		require.NoError(t, err)
		require.NotNil(t, trace.Result)
		assert.False(t, *trace.Result, "wildcard ban must cause exclusion to fire")
		require.NotNil(t, trace.Root)
		assert.Equal(t, melange.NodeUnion, trace.Root.Type,
			"failure root is the union of attempted branches")

		var foundExcluded bool
		for _, child := range trace.Root.Children {
			if child.Type == melange.NodeExclusion && child.Result != nil && !*child.Result {
				foundExcluded = true
				assert.Contains(t, child.Label, "excluded",
					"failed exclusion node should be labelled as excluded")
				break
			}
		}
		assert.True(t, foundExcluded,
			"failure union must record an exclusion attempt as result=false")
	})
}

// TestExplain_SessionGUCTruncates exercises the middle tier of the three-tier
// truncation precedence: `SET LOCAL melange.max_explain_nodes` should cap the
// trace without callers passing WithExplainMaxNodes. Per-call options also
// override the GUC — TestExplain_TruncationCapsTrace covers that — but the
// GUC path is the only way SDKs that don't know about Explain options can
// constrain the cost, so it deserves its own assertion.
func TestExplain_SessionGUCTruncates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('guc_trunc_user') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('guc_trunc_org') RETURNING id`).Scan(&orgID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('guc_trunc_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))

		// Acquire a dedicated connection so SET LOCAL stays scoped to our
		// transaction; the connection-pooled checker call below must inherit
		// the GUC for the assertion to be meaningful.
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		_, err = conn.ExecContext(ctx, "BEGIN")
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, "SET LOCAL melange.max_explain_nodes = 1")
		require.NoError(t, err)

		checker := melange.NewChecker(conn, melange.WithDatabaseSchema(databaseSchema))
		trace, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_deploy"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
		)
		require.NoError(t, err)
		assert.True(t, trace.Truncated, "session GUC should cap the trace just like WithExplainMaxNodes")
		// Per-call override must beat the GUC — same call with a generous
		// per-call cap should not truncate.
		trace2, err := checker.Explain(ctx,
			melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
			melange.Relation("can_deploy"),
			melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)},
			melange.WithExplainMaxNodes(1000),
		)
		require.NoError(t, err)
		assert.False(t, trace2.Truncated, "per-call WithExplainMaxNodes(1000) should override GUC=1")
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
	})
}

// TestExplain_NodeCountInvariant pins the NodeCount envelope: every Explain
// response must report a positive node count when the root is non-nil, so
// callers can ratio-check against their max-nodes budget without having to
// walk the tree. Several success-return helpers update v_node_count
// independently; a forgotten bump on one path would surface here as a zero
// count on a non-empty trace.
func TestExplain_NodeCountInvariant(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var userID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('nc_user') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('nc_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('nc_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		cases := []struct {
			label    string
			relation melange.Relation
			object   melange.Object
		}{
			{"direct success", melange.Relation("owner"), melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)}},
			{"ttu success", melange.Relation("can_deploy"), melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)}},
			{"failure union", melange.Relation("can_delete"), melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)}},
		}
		for _, tc := range cases {
			t.Run(tc.label, func(t *testing.T) {
				trace, err := checker.Explain(ctx,
					melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)},
					tc.relation, tc.object)
				require.NoError(t, err)
				require.NotNil(t, trace.Root, "non-truncated trace must have a root")
				assert.Greater(t, trace.NodeCount, 0,
					"NodeCount must reflect the emitted nodes, not stay at 0")
			})
		}
	})
}

// TestExplain_AgreesWithCheck is the cross-API parity invariant: Explain's
// returned Result must equal Check's boolean for the same inputs, across
// success, failure, TTU, implied, exclusion, and wildcard paths. The codegen
// tests pin shape; this test pins the load-bearing correctness contract
// (drift here means traces lie about the decision).
//
// JSON round-trip is also asserted: marshalling and unmarshalling the trace
// must reproduce the same envelope flags and root type, so external tools
// that re-serialise the trace see no shape drift.
func TestExplain_AgreesWithCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var ownerID, memberID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('parity_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('parity_member') RETURNING id`).Scan(&memberID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('parity_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, ownerID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`,
			orgID, memberID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('parity_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))
		// Wildcard ban on the repo so banned/can_read_safe scenarios fire.
		_, err = db.ExecContext(ctx,
			`INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)`, repoID)
		require.NoError(t, err)

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		owner := melange.Object{Type: "user", ID: strconv.FormatInt(ownerID, 10)}
		member := melange.Object{Type: "user", ID: strconv.FormatInt(memberID, 10)}
		org := melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)}
		repo := melange.Object{Type: "repository", ID: strconv.FormatInt(repoID, 10)}

		cases := []struct {
			label    string
			subject  melange.Object
			relation melange.Relation
			object   melange.Object
		}{
			{"owner has admin (implied)", owner, "admin", org},
			{"owner can_deploy (TTU success)", owner, "can_deploy", repo},
			{"member can_deploy (TTU failure)", member, "can_deploy", repo},
			{"owner banned (wildcard sentinel)", owner, "banned", repo},
			{"owner can_read_safe (exclusion fired)", owner, "can_read_safe", repo},
			{"member can_admin org", member, "can_admin", org},
		}
		for _, tc := range cases {
			t.Run(tc.label, func(t *testing.T) {
				allowed, err := checker.Check(ctx, tc.subject, tc.relation, tc.object)
				require.NoError(t, err)
				trace, err := checker.Explain(ctx, tc.subject, tc.relation, tc.object)
				require.NoError(t, err)
				require.NotNil(t, trace.Result)
				assert.Equal(t, allowed, *trace.Result,
					"Explain.Result must equal Check for the same inputs")

				// JSON round-trip pin: marshalling the trace and decoding it
				// again must produce an equal envelope shape, so downstream
				// tools that proxy the trace through JSON do not lose data.
				raw, err := json.Marshal(trace)
				require.NoError(t, err)
				var round melange.Trace
				require.NoError(t, json.Unmarshal(raw, &round))
				assert.Equal(t, trace.Truncated, round.Truncated)
				assert.Equal(t, trace.NodeCount, round.NodeCount)
				require.NotNil(t, round.Result)
				assert.Equal(t, *trace.Result, *round.Result)
				require.NotNil(t, round.Root)
				assert.Equal(t, trace.Root.Type, round.Root.Type)
			})
		}
	})
}

// TestExplain_UnknownPair confirms the dispatcher's no-entry sentinel: when
// the schema doesn't define (object_type, relation), the trace is still
// structurally valid (deserialises) and clearly marked as a failure.
func TestExplain_UnknownPair(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		// Direct SQL call to bypass Go-side validation — we want to see the
		// SQL dispatcher's behaviour on an unknown (type, relation).
		var raw []byte
		err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5)::text", sqldsl.PrefixIdent("explain_permission", databaseSchema)),
			"user", "1", "nonexistent_relation", "widget", "42",
		).Scan(&raw)
		require.NoError(t, err)

		var trace melange.Trace
		require.NoError(t, json.Unmarshal(raw, &trace))
		require.NotNil(t, trace.Result)
		assert.False(t, *trace.Result)
		require.NotNil(t, trace.Root)
		assert.Equal(t, melange.NodeUnion, trace.Root.Type)
		assert.Contains(t, trace.Root.Label, "explain not yet supported")
	})
}
