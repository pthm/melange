package test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/test/testutil"
)

// TestParentKeyAccessible_MatchesAccessibleSet checks that filtering pull
// requests by their repository through accessible_pull_request_{rel}_by_repo
// returns exactly the pull requests accessible_pull_request_{rel} returns,
// for subjects reaching repositories through the organization, as
// collaborators, as the owner, and not at all.
func TestParentKeyAccessible_MatchesAccessibleSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		db := testutil.DBWithDatabaseSchema(t, databaseSchema)
		ctx := context.Background()
		id := func(query string, args ...any) int64 {
			t.Helper()
			var v int64
			require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&v))
			return v
		}
		exec := func(query string, args ...any) {
			t.Helper()
			_, err := db.ExecContext(ctx, query, args...)
			require.NoError(t, err)
		}

		member := id(`INSERT INTO users (username) VALUES ('pk_member') RETURNING id`)
		admin := id(`INSERT INTO users (username) VALUES ('pk_admin') RETURNING id`)
		collab := id(`INSERT INTO users (username) VALUES ('pk_collab') RETURNING id`)
		owner := id(`INSERT INTO users (username) VALUES ('pk_owner') RETURNING id`)
		outsider := id(`INSERT INTO users (username) VALUES ('pk_outsider') RETURNING id`)
		org1 := id(`INSERT INTO organizations (name) VALUES ('pk_org1') RETURNING id`)
		org2 := id(`INSERT INTO organizations (name) VALUES ('pk_org2') RETURNING id`)
		exec(`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member'), ($1, $3, 'admin')`, org1, member, admin)
		repos := []int64{
			id(`INSERT INTO repositories (organization_id, name) VALUES ($1, 'pk_repo1') RETURNING id`, org1),
			id(`INSERT INTO repositories (organization_id, owner_id, name) VALUES ($1, $2, 'pk_repo2') RETURNING id`, org2, owner),
			id(`INSERT INTO repositories (organization_id, name) VALUES ($1, 'pk_repo3') RETURNING id`, org2),
		}
		exec(`INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'writer')`, repos[2], collab)
		for i, repo := range repos {
			for k := range 3 {
				exec(`INSERT INTO pull_requests (repository_id, author_id, title, source_branch) VALUES ($1, $2, $3, $3)`,
					repo, owner, fmt.Sprintf("pk_pr_%d_%d", i, k))
			}
		}

		// Inlined accessible functions resolve melange_tuples on the caller's
		// search_path.
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()
		_, err = conn.ExecContext(ctx, "SET search_path = "+sqldsl.QuoteIdent(databaseSchema)+", public")
		require.NoError(t, err)
		ids := func(query string, args ...any) []string {
			t.Helper()
			rows, err := conn.QueryContext(ctx, query, args...)
			require.NoError(t, err)
			defer func() { _ = rows.Close() }()
			var out []string
			for rows.Next() {
				var v string
				require.NoError(t, rows.Scan(&v))
				out = append(out, v)
			}
			require.NoError(t, rows.Err())
			sort.Strings(out)
			return out
		}
		fn := func(name string) string { return sqldsl.PrefixIdent(name, databaseSchema) }

		for _, rel := range []string{"can_read", "can_merge", "can_delete"} {
			for _, user := range []int64{member, admin, collab, owner, outsider} {
				uid := fmt.Sprint(user)
				byRepo := ids(fmt.Sprintf(`SELECT pr.id::text FROM pull_requests pr
					WHERE pr.repository_id::text IN (SELECT * FROM %s('user', $1))`, fn("accessible_pull_request_"+rel+"_by_repo")), uid)
				direct := ids(fmt.Sprintf(`SELECT DISTINCT a.object_id FROM %s('user', $1) a`, fn("accessible_pull_request_"+rel)), uid)
				assert.Equal(t, direct, byRepo, "%s for user %d", rel, user)
			}
		}

		// can_edit grants authors directly and can_review excludes them, so
		// neither has a parent-key form.
		for _, rel := range []string{"can_edit", "can_review"} {
			var n int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_proc WHERE proname = $1`,
				"accessible_pull_request_"+rel+"_by_repo").Scan(&n))
			assert.Zero(t, n, "%s must have no parent-key function", rel)
		}
	})
}
