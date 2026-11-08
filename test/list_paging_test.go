package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/testutil"
)

// TestListObjects_PagesPastRepeatedLinkTuple pages pull requests, which a list
// page reads a page of each repository at a time, with a contextual tuple
// repeating a stored linking tuple. melange_tuples then has the link twice;
// each repository's page must count the pull request once, or a page of one
// fills with the repeated pull request and the next is never reached.
func TestListObjects_PagesPastRepeatedLinkTuple(t *testing.T) {
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

		user := id(`INSERT INTO users (username) VALUES ('paging_member') RETURNING id`)
		org := id(`INSERT INTO organizations (name) VALUES ('paging_org') RETURNING id`)
		_, err := db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, org, user)
		require.NoError(t, err)
		repo := id(`INSERT INTO repositories (organization_id, name) VALUES ($1, 'paging_repo') RETURNING id`, org)
		for k := range 3 {
			id(`INSERT INTO pull_requests (repository_id, author_id, title, source_branch) VALUES ($1, $2, $3, $3) RETURNING id`,
				repo, user, fmt.Sprintf("paging_pr_%d", k))
		}

		checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))
		subject := melange.Object{Type: "user", ID: fmt.Sprint(user)}
		all, err := checker.ListObjectsAll(ctx, subject, melange.Relation("can_read"), "pull_request")
		require.NoError(t, err)
		require.Len(t, all, 3)

		// The first pull request's link, again.
		repeated := []melange.ContextualTuple{{
			Subject:  melange.Object{Type: "repository", ID: fmt.Sprint(repo)},
			Relation: "repo",
			Object:   melange.Object{Type: "pull_request", ID: all[0]},
		}}
		var paged []string
		page := melange.PageOptions{Limit: 1}
		for range len(all) + 1 {
			ids, next, err := checker.ListObjectsWithContextualTuples(ctx, subject, melange.Relation("can_read"), "pull_request", repeated, page)
			require.NoError(t, err)
			paged = append(paged, ids...)
			if next == nil {
				break
			}
			page.After = next
		}
		require.Equal(t, all, paged)
	})
}
