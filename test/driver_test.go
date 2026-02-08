package test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/authz"
	"github.com/pthm/melange/test/testutil"
)

// TestDriverCompatibility verifies that the melange Checker works correctly
// with multiple database/sql drivers. Each driver opens its own connection
// to the same migrated database and runs the same permission check assertions.
func TestDriverCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	drivers := []struct {
		name       string
		driverName string // for sql.Open
	}{
		{"pgx", "pgx"},
		{"lib_pq", "postgres"},
	}

	for _, drv := range drivers {
		t.Run(drv.name, func(t *testing.T) {
			dsn := testutil.DSN(t)
			db, err := sql.Open(drv.driverName, dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			err = db.Ping()
			require.NoError(t, err, "driver %s should connect successfully", drv.name)

			ctx := context.Background()

			// Create test data
			var orgID, repoID, ownerID, memberID, outsiderID int64

			err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ($1) RETURNING id`, fmt.Sprintf("drv_%s_owner", drv.name)).Scan(&ownerID)
			require.NoError(t, err)
			err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ($1) RETURNING id`, fmt.Sprintf("drv_%s_member", drv.name)).Scan(&memberID)
			require.NoError(t, err)
			err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ($1) RETURNING id`, fmt.Sprintf("drv_%s_outsider", drv.name)).Scan(&outsiderID)
			require.NoError(t, err)

			err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, fmt.Sprintf("drv_%s_org", drv.name)).Scan(&orgID)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, ownerID)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, memberID)
			require.NoError(t, err)

			err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, $2) RETURNING id`, orgID, fmt.Sprintf("drv_%s_repo", drv.name)).Scan(&repoID)
			require.NoError(t, err)

			checker := melange.NewChecker(db)
			owner := authz.User(ownerID)
			member := authz.User(memberID)
			outsider := authz.User(outsiderID)
			org := authz.Organization(orgID)

			t.Run("Check_allow", func(t *testing.T) {
				ok, err := checker.Check(ctx, owner, authz.RelCanRead, org)
				require.NoError(t, err)
				assert.True(t, ok, "owner should have can_read")
			})

			t.Run("Check_deny", func(t *testing.T) {
				ok, err := checker.Check(ctx, outsider, authz.RelCanRead, org)
				require.NoError(t, err)
				assert.False(t, ok, "outsider should not have can_read")
			})

			t.Run("ListObjects", func(t *testing.T) {
				ids, err := checker.ListObjectsAll(ctx, member, authz.RelCanRead, authz.TypeRepository)
				require.NoError(t, err)
				assert.Contains(t, ids, fmt.Sprint(repoID), "member should see repo via org membership")
			})

			t.Run("ListSubjects", func(t *testing.T) {
				ids, err := checker.ListSubjectsAll(ctx, org, authz.RelCanRead, authz.TypeUser)
				require.NoError(t, err)
				assert.Contains(t, ids, fmt.Sprint(ownerID), "owner should appear in subjects")
				assert.Contains(t, ids, fmt.Sprint(memberID), "member should appear in subjects")
				assert.NotContains(t, ids, fmt.Sprint(outsiderID), "outsider should not appear in subjects")
			})

			t.Run("BulkCheck", func(t *testing.T) {
				res, err := checker.NewBulkCheck(ctx).
					Add(owner, authz.RelCanRead, org).
					Add(outsider, authz.RelCanRead, org).
					Execute()
				require.NoError(t, err)
				assert.Equal(t, 2, res.Len())
				assert.True(t, res.Get(0).IsAllowed(), "owner should be allowed")
				assert.False(t, res.Get(1).IsAllowed(), "outsider should be denied")
			})

			t.Run("Transaction", func(t *testing.T) {
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()

				// Insert a new user and org membership within the transaction
				var txUserID int64
				err = tx.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ($1) RETURNING id`, fmt.Sprintf("drv_%s_tx_user", drv.name)).Scan(&txUserID)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, txUserID)
				require.NoError(t, err)

				// Check within transaction should see uncommitted data
				txChecker := melange.NewChecker(tx)
				txUser := authz.User(txUserID)
				ok, err := txChecker.Check(ctx, txUser, authz.RelCanRead, org)
				require.NoError(t, err)
				assert.True(t, ok, "tx user should have can_read within transaction")

				// Verify outside the transaction, the user is NOT visible
				ok, err = checker.Check(ctx, txUser, authz.RelCanRead, org)
				require.NoError(t, err)
				assert.False(t, ok, "tx user should NOT be visible outside transaction")

				// Rollback - the user is not persisted
			})
		})
	}
}
