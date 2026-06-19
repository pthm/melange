package test

import (
	"context"
	"database/sql"
	"os/exec"
	"strconv"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/test/testutil"
)

// TestExplainExamples is a documentation aid: it spins up the test database,
// inserts a few tuples per scenario, then shells out to the `melange explain`
// CLI and prints what it returns. Run with:
//
//	go test ./test -run TestExplainExamples -v
func TestExplainExamples(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Get the DSN and open our own connection against it — the testutil
	// helpers create a *new isolated database* per call, so we can't mix
	// DBWithDatabaseSchema and DSNWithDatabaseSchema.
	dsn := testutil.DSNWithDatabaseSchema(t, "public")
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	run := func(t *testing.T, label string, args ...string) {
		t.Helper()
		full := append([]string{"run", "../cmd/melange", "explain"}, args...)
		full = append(full, "--db", dsn)
		out, err := exec.CommandContext(ctx, "go", full...).CombinedOutput()
		t.Logf("\n=== %s ===\n$ melange explain %v\n%s", label, args, out)
		require.NoError(t, err, "explain CLI exited non-zero")
	}

	t.Run("direct grant success", func(t *testing.T) {
		var userID, orgID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ex_direct_ok') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('ex_direct_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)

		run(t, "direct grant — owner tuple matches",
			"user:"+strconv.FormatInt(userID, 10),
			"owner",
			"organization:"+strconv.FormatInt(orgID, 10),
		)
	})

	t.Run("direct grant failure", func(t *testing.T) {
		var userID, orgID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ex_direct_fail') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('ex_direct_fail_org') RETURNING id`).Scan(&orgID))

		run(t, "direct grant — no tuple, every branch records as failure",
			"user:"+strconv.FormatInt(userID, 10),
			"owner",
			"organization:"+strconv.FormatInt(orgID, 10),
		)
	})

	t.Run("ttu success", func(t *testing.T) {
		var userID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ex_ttu_ok') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('ex_ttu_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, userID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('ex_ttu_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))

		run(t, "ttu — repo.can_deploy resolves via org → owner",
			"user:"+strconv.FormatInt(userID, 10),
			"can_deploy",
			"repository:"+strconv.FormatInt(repoID, 10),
		)
	})

	t.Run("wildcard sentinel", func(t *testing.T) {
		var userID, ownerID, orgID, repoID int64
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ex_wild_alice') RETURNING id`).Scan(&userID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO users (username) VALUES ('ex_wild_owner') RETURNING id`).Scan(&ownerID))
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO organizations (name) VALUES ('ex_wild_org') RETURNING id`).Scan(&orgID))
		_, err := db.ExecContext(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`,
			orgID, ownerID)
		require.NoError(t, err)
		require.NoError(t, db.QueryRowContext(ctx,
			`INSERT INTO repositories (name, organization_id) VALUES ('ex_wild_repo', $1) RETURNING id`,
			orgID).Scan(&repoID))
		_, err = db.ExecContext(ctx,
			`INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)`, repoID)
		require.NoError(t, err)

		run(t, "wildcard — repository.banned: [user:*] matches all users",
			"user:"+strconv.FormatInt(userID, 10),
			"banned",
			"repository:"+strconv.FormatInt(repoID, 10),
		)
	})
}
