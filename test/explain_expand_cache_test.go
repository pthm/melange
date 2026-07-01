package test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/testutil"
)

// TestExplain_CacheHit verifies Checker.Explain populates the
// ExplainCache and subsequent calls with the same key hit the cache
// rather than the DB. We prove the cache was consulted by mutating
// the underlying tuple between calls and asserting the second Explain
// still returns the pre-mutation trace.
func TestExplain_CacheHit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, userID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('explain_cache_user') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('explain_cache_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`,
			orgID, userID)
		require.NoError(t, err)

		cache := melange.NewCache()
		checker := melange.NewChecker(db,
			melange.WithDatabaseSchema(databaseSchema),
			melange.WithCache(cache))

		user := melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)}
		org := melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)}
		rel := melange.Relation("can_read")

		// First call — populates the cache.
		trace1, err := checker.Explain(ctx, user, rel, org)
		require.NoError(t, err)
		require.NotNil(t, trace1)
		require.NotNil(t, trace1.Result)
		assert.True(t, *trace1.Result, "member should have can_read")

		// Mutate the underlying membership so a fresh DB query would
		// return false. If the second Explain hits the cache, it
		// still returns the true trace.
		_, err = db.ExecContext(ctx,
			`DELETE FROM organization_members WHERE organization_id = $1 AND user_id = $2`,
			orgID, userID)
		require.NoError(t, err)

		trace2, err := checker.Explain(ctx, user, rel, org)
		require.NoError(t, err)
		require.NotNil(t, trace2)
		require.NotNil(t, trace2.Result)
		assert.True(t, *trace2.Result,
			"second Explain should hit the cache and return the stale-but-true trace")

		// After Clear, the next Explain must go to the DB and see the
		// current (deleted-membership) state.
		cache.Clear()
		trace3, err := checker.Explain(ctx, user, rel, org)
		require.NoError(t, err)
		require.NotNil(t, trace3)
		require.NotNil(t, trace3.Result)
		assert.False(t, *trace3.Result,
			"third Explain (post-Clear) should hit the DB and reflect the deleted membership")
	})
}

// TestExpand_CacheHit mirrors TestExplain_CacheHit for the Expand
// path. Insert a member, expand, remove the member, expand again
// (cache hit), Clear, expand again (DB hit).
func TestExpand_CacheHit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, userID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('expand_cache_user') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('expand_cache_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)

		cache := melange.NewCache()
		checker := melange.NewChecker(db,
			melange.WithDatabaseSchema(databaseSchema),
			melange.WithCache(cache))

		org := melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)}
		rel := melange.Relation("owner")

		tree1, err := checker.Expand(ctx, org, rel)
		require.NoError(t, err)
		require.NotNil(t, tree1)
		require.NotNil(t, tree1.Root.Leaf)
		require.NotNil(t, tree1.Root.Leaf.Users)
		firstUsers := tree1.Root.Leaf.Users.Users
		require.Len(t, firstUsers, 1)

		// Mutate: delete the member.
		_, err = db.ExecContext(ctx,
			`DELETE FROM organization_members WHERE organization_id = $1 AND user_id = $2`,
			orgID, userID)
		require.NoError(t, err)

		tree2, err := checker.Expand(ctx, org, rel)
		require.NoError(t, err)
		require.NotNil(t, tree2.Root.Leaf.Users)
		assert.Equal(t, firstUsers, tree2.Root.Leaf.Users.Users,
			"cached Expand should return the pre-delete user list")

		// Clear — next Expand goes to the DB.
		cache.Clear()
		tree3, err := checker.Expand(ctx, org, rel)
		require.NoError(t, err)
		require.NotNil(t, tree3.Root.Leaf.Users)
		assert.Empty(t, tree3.Root.Leaf.Users.Users,
			"post-Clear Expand should see the deleted membership")
	})
}

// TestExplain_CacheKeyIncludesMaxNodes verifies the cache key
// composition promise: two Explain calls that differ only in
// WithExplainMaxNodes must NOT alias — each cap gets its own entry
// so a truncated trace can't be served to an uncapped caller.
func TestExplain_CacheKeyIncludesMaxNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()

		var orgID, userID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('explain_key_user') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('explain_key_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`,
			orgID, userID)
		require.NoError(t, err)

		cache := melange.NewCache()
		checker := melange.NewChecker(db,
			melange.WithDatabaseSchema(databaseSchema),
			melange.WithCache(cache))

		user := melange.Object{Type: "user", ID: strconv.FormatInt(userID, 10)}
		org := melange.Object{Type: "organization", ID: strconv.FormatInt(orgID, 10)}
		rel := melange.Relation("can_read")

		// Populate at cap=0 (unset).
		_, err = checker.Explain(ctx, user, rel, org)
		require.NoError(t, err)
		assert.Equal(t, 1, cache.Size(), "cap=0 populated one entry")

		// Different cap → distinct entry, not a hit.
		_, err = checker.Explain(ctx, user, rel, org, melange.WithExplainMaxNodes(50))
		require.NoError(t, err)
		assert.Equal(t, 2, cache.Size(), "cap=50 should NOT alias cap=0")

		// Same cap again → hit.
		_, err = checker.Explain(ctx, user, rel, org, melange.WithExplainMaxNodes(50))
		require.NoError(t, err)
		assert.Equal(t, 2, cache.Size(), "repeat cap=50 should hit the existing entry")
	})
}
