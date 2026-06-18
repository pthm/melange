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
