package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange"
	"github.com/pthm/melange/test/authz"
	"github.com/pthm/melange/test/testutil"
)

// TestDB_Integration verifies that the test database is properly set up
// with melange schema, domain tables, and the tuples view.
func TestDB_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Verify melange_model table exists and has data
	var modelCount int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM melange_model").Scan(&modelCount)
	require.NoError(t, err)
	assert.Greater(t, modelCount, 0, "melange_model should have entries from GitHub schema")

	// Verify domain tables exist
	tables := []string{"users", "organizations", "repositories", "issues", "pull_requests"}
	for _, table := range tables {
		var exists bool
		err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_name = $1
			)
		`, table).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "table %s should exist", table)
	}

	// Verify melange_tuples view exists
	var viewExists bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.views
			WHERE table_name = 'melange_tuples'
		)
	`).Scan(&viewExists)
	require.NoError(t, err)
	assert.True(t, viewExists, "melange_tuples view should exist")
}

// TestOrganization_Permissions tests organization permission checks
// using the generated authz types.
func TestOrganization_Permissions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, ownerID, adminID, memberID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('org_owner') RETURNING id`).Scan(&ownerID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('org_admin') RETURNING id`).Scan(&adminID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('org_member') RETURNING id`).Scan(&memberID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('acme') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// Add members with different roles
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, ownerID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, adminID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, memberID)
	require.NoError(t, err)

	// Create checker using generated types
	checker := melange.NewChecker(db)
	org := authz.Organization(orgID)
	owner := authz.User(ownerID)
	admin := authz.User(adminID)
	member := authz.User(memberID)

	// Test owner permissions (owners can do everything)
	t.Run("owner has all permissions", func(t *testing.T) {
		ok, err := checker.Check(ctx, owner, authz.RelCanRead, org)
		require.NoError(t, err)
		assert.True(t, ok, "owner should have can_read")

		ok, err = checker.Check(ctx, owner, authz.RelCanAdmin, org)
		require.NoError(t, err)
		assert.True(t, ok, "owner should have can_admin")

		ok, err = checker.Check(ctx, owner, authz.RelCanDelete, org)
		require.NoError(t, err)
		assert.True(t, ok, "owner should have can_delete")
	})

	// Test admin permissions (admin -> member, so can read but not delete)
	t.Run("admin has admin and read permissions", func(t *testing.T) {
		ok, err := checker.Check(ctx, admin, authz.RelCanRead, org)
		require.NoError(t, err)
		assert.True(t, ok, "admin should have can_read")

		ok, err = checker.Check(ctx, admin, authz.RelCanAdmin, org)
		require.NoError(t, err)
		assert.True(t, ok, "admin should have can_admin")

		ok, err = checker.Check(ctx, admin, authz.RelCanDelete, org)
		require.NoError(t, err)
		assert.False(t, ok, "admin should NOT have can_delete")
	})

	// Test member permissions (read only)
	t.Run("member has read permission only", func(t *testing.T) {
		ok, err := checker.Check(ctx, member, authz.RelCanRead, org)
		require.NoError(t, err)
		assert.True(t, ok, "member should have can_read")

		ok, err = checker.Check(ctx, member, authz.RelCanAdmin, org)
		require.NoError(t, err)
		assert.False(t, ok, "member should NOT have can_admin")

		ok, err = checker.Check(ctx, member, authz.RelCanDelete, org)
		require.NoError(t, err)
		assert.False(t, ok, "member should NOT have can_delete")
	})
}

// TestRepository_InheritedPermissions tests permission inheritance from organization
// using the generated authz types.
func TestRepository_InheritedPermissions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, orgMemberID, repoWriterID, outsiderID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('org_member') RETURNING id`).Scan(&orgMemberID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('repo_writer') RETURNING id`).Scan(&repoWriterID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('outsider') RETURNING id`).Scan(&outsiderID)
	require.NoError(t, err)

	// Create organization and add member
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('org1') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, orgMemberID)
	require.NoError(t, err)

	// Create repository under organization
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'repo1') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	// Add direct collaborator
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'writer')`, repoID, repoWriterID)
	require.NoError(t, err)

	// Create checker using generated types
	checker := melange.NewChecker(db)
	repo := authz.Repository(repoID)
	orgMember := authz.User(orgMemberID)
	repoWriter := authz.User(repoWriterID)
	outsider := authz.User(outsiderID)

	t.Run("org member inherits read access", func(t *testing.T) {
		ok, err := checker.Check(ctx, orgMember, authz.RelCanRead, repo)
		require.NoError(t, err)
		assert.True(t, ok, "org member should have can_read via inheritance")

		ok, err = checker.Check(ctx, orgMember, authz.RelCanWrite, repo)
		require.NoError(t, err)
		assert.False(t, ok, "org member should NOT have can_write (only reader via inheritance)")
	})

	t.Run("repo writer has read and write access", func(t *testing.T) {
		ok, err := checker.Check(ctx, repoWriter, authz.RelCanRead, repo)
		require.NoError(t, err)
		assert.True(t, ok, "repo writer should have can_read")

		ok, err = checker.Check(ctx, repoWriter, authz.RelCanWrite, repo)
		require.NoError(t, err)
		assert.True(t, ok, "repo writer should have can_write")

		ok, err = checker.Check(ctx, repoWriter, authz.RelCanAdmin, repo)
		require.NoError(t, err)
		assert.False(t, ok, "repo writer should NOT have can_admin")
	})

	t.Run("outsider has no access", func(t *testing.T) {
		ok, err := checker.Check(ctx, outsider, authz.RelCanRead, repo)
		require.NoError(t, err)
		assert.False(t, ok, "outsider should NOT have can_read")
	})
}

// TestPullRequest_ExclusionPattern tests the "but not" exclusion pattern.
// PR authors cannot review their own PRs.
func TestPullRequest_ExclusionPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, prID, authorID, reviewerID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('pr_author') RETURNING id`).Scan(&authorID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('pr_reviewer') RETURNING id`).Scan(&reviewerID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('org2') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// Add both users as org members (so they can read the repo)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, authorID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, reviewerID)
	require.NoError(t, err)

	// Create repository
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'repo2') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	// Create pull request
	err = db.QueryRowContext(ctx, `
		INSERT INTO pull_requests (repository_id, author_id, title, source_branch)
		VALUES ($1, $2, 'Test PR', 'feature-branch')
		RETURNING id
	`, repoID, authorID).Scan(&prID)
	require.NoError(t, err)

	// Create checker using generated types
	checker := melange.NewChecker(db)
	pr := authz.PullRequest(prID)
	author := authz.User(authorID)
	reviewer := authz.User(reviewerID)

	t.Run("author can read their PR", func(t *testing.T) {
		ok, err := checker.Check(ctx, author, authz.RelCanRead, pr)
		require.NoError(t, err)
		assert.True(t, ok, "author should be able to read their PR")
	})

	t.Run("author can edit their PR", func(t *testing.T) {
		ok, err := checker.Check(ctx, author, authz.RelCanEdit, pr)
		require.NoError(t, err)
		assert.True(t, ok, "author should be able to edit their PR")
	})

	t.Run("author cannot review their own PR", func(t *testing.T) {
		ok, err := checker.Check(ctx, author, authz.RelCanReview, pr)
		require.NoError(t, err)
		assert.False(t, ok, "author should NOT be able to review their own PR (exclusion pattern)")
	})

	t.Run("other org member can review PR", func(t *testing.T) {
		ok, err := checker.Check(ctx, reviewer, authz.RelCanReview, pr)
		require.NoError(t, err)
		assert.True(t, ok, "reviewer should be able to review PR")
	})
}

// TestListObjects tests the ListObjects functionality using generated types.
func TestListObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repo1ID, repo2ID, repo3ID, userID, outsiderID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('list_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('list_outsider') RETURNING id`).Scan(&outsiderID)
	require.NoError(t, err)

	// Create organization and add user
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('list_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	// Create 3 repositories under the organization
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'list_repo1') RETURNING id`, orgID).Scan(&repo1ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'list_repo2') RETURNING id`, orgID).Scan(&repo2ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'list_repo3') RETURNING id`, orgID).Scan(&repo3ID)
	require.NoError(t, err)

	// Create checker using generated types
	checker := melange.NewChecker(db)
	user := authz.User(userID)
	outsider := authz.User(outsiderID)

	t.Run("org member can list accessible repositories", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, user, authz.RelCanRead, authz.TypeRepository)
		require.NoError(t, err)
		assert.Len(t, ids, 3, "should find 3 repositories")
		assert.Contains(t, ids, idStr(repo1ID))
		assert.Contains(t, ids, idStr(repo2ID))
		assert.Contains(t, ids, idStr(repo3ID))
	})

	t.Run("outsider cannot list any repositories", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, outsider, authz.RelCanRead, authz.TypeRepository)
		require.NoError(t, err)
		assert.Empty(t, ids, "outsider should not see any repositories")
	})
}

// TestListSubjects tests the ListSubjects functionality using generated types.
func TestListSubjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, user1ID, user2ID, user3ID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('subj_user1') RETURNING id`).Scan(&user1ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('subj_user2') RETURNING id`).Scan(&user2ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('subj_user3') RETURNING id`).Scan(&user3ID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('subj_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// Add users with different roles
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, user1ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, user2ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, user3ID)
	require.NoError(t, err)

	// Create checker using generated types
	checker := melange.NewChecker(db)
	org := authz.Organization(orgID)

	t.Run("list users who can read org", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, org, authz.RelCanRead, authz.TypeUser)
		require.NoError(t, err)
		assert.Len(t, ids, 3, "should find 3 users with can_read")
		assert.Contains(t, ids, idStr(user1ID))
		assert.Contains(t, ids, idStr(user2ID))
		assert.Contains(t, ids, idStr(user3ID))
	})

	t.Run("list users who can admin org", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, org, authz.RelCanAdmin, authz.TypeUser)
		require.NoError(t, err)
		assert.Len(t, ids, 2, "should find 2 users with can_admin (owner + admin)")
		assert.Contains(t, ids, idStr(user1ID))
		assert.Contains(t, ids, idStr(user2ID))
	})

	t.Run("list users who can delete org", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, org, authz.RelCanDelete, authz.TypeUser)
		require.NoError(t, err)
		assert.Len(t, ids, 1, "should find 1 user with can_delete (owner only)")
		assert.Contains(t, ids, idStr(user1ID))
	})
}

// TestTransaction_SeeUncommittedChanges verifies that permission checks
// within a transaction see uncommitted changes.
func TestTransaction_SeeUncommittedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create a user and organization outside the transaction
	var userID, orgID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('tx_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('tx_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	user := authz.User(userID)
	org := authz.Organization(orgID)

	// Start a transaction
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	// Create checker with transaction
	checker := melange.NewChecker(tx)

	// Before adding membership, user should not have access
	ok, err := checker.Check(ctx, user, authz.RelCanRead, org)
	require.NoError(t, err)
	assert.False(t, ok, "user should NOT have access before membership")

	// Add membership within the transaction
	_, err = tx.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	// Now user should have access (uncommitted changes visible)
	ok, err = checker.Check(ctx, user, authz.RelCanRead, org)
	require.NoError(t, err)
	assert.True(t, ok, "user should have access after uncommitted insert")

	// Rollback - changes are not committed
	err = tx.Rollback()
	require.NoError(t, err)

	// Verify the membership was rolled back
	checkerOutside := melange.NewChecker(db)
	ok, err = checkerOutside.Check(ctx, user, authz.RelCanRead, org)
	require.NoError(t, err)
	assert.False(t, ok, "user should NOT have access after rollback")
}

// TestDatabaseIsolation verifies that each test gets an isolated database.
func TestDatabaseIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create two databases in parallel
	t.Run("db1", func(t *testing.T) {
		t.Parallel()
		db := testutil.DB(t)
		ctx := context.Background()

		// Insert a user
		_, err := db.ExecContext(ctx, `INSERT INTO users (username) VALUES ('isolation_test_1')`)
		require.NoError(t, err)

		// Count users - should be exactly 1
		var count int
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "db1 should have exactly 1 user")
	})

	t.Run("db2", func(t *testing.T) {
		t.Parallel()
		db := testutil.DB(t)
		ctx := context.Background()

		// Insert a different user
		_, err := db.ExecContext(ctx, `INSERT INTO users (username) VALUES ('isolation_test_2')`)
		require.NoError(t, err)

		// Count users - should be exactly 1 (not 2!)
		var count int
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "db2 should have exactly 1 user (isolated from db1)")
	})
}

// TestGeneratedTypes verifies that the generated authz package types work correctly.
func TestGeneratedTypes(t *testing.T) {
	t.Run("object constructors create correct objects", func(t *testing.T) {
		user := authz.User(123)
		assert.Equal(t, "user", string(user.Type))
		assert.Equal(t, "123", user.ID)

		org := authz.Organization(456)
		assert.Equal(t, "organization", string(org.Type))
		assert.Equal(t, "456", org.ID)

		repo := authz.Repository(789)
		assert.Equal(t, "repository", string(repo.Type))
		assert.Equal(t, "789", repo.ID)
	})

	t.Run("wildcard constructors create correct objects", func(t *testing.T) {
		anyUser := authz.AnyUser()
		assert.Equal(t, "user", string(anyUser.Type))
		assert.Equal(t, "*", anyUser.ID)

		anyOrg := authz.AnyOrganization()
		assert.Equal(t, "organization", string(anyOrg.Type))
		assert.Equal(t, "*", anyOrg.ID)
	})

	t.Run("type constants are correct", func(t *testing.T) {
		assert.Equal(t, "user", string(authz.TypeUser))
		assert.Equal(t, "organization", string(authz.TypeOrganization))
		assert.Equal(t, "repository", string(authz.TypeRepository))
		assert.Equal(t, "issue", string(authz.TypeIssue))
		assert.Equal(t, "pull_request", string(authz.TypePullRequest))
		assert.Equal(t, "team", string(authz.TypeTeam))
	})

	t.Run("relation constants are correct", func(t *testing.T) {
		assert.Equal(t, "can_read", string(authz.RelCanRead))
		assert.Equal(t, "can_write", string(authz.RelCanWrite))
		assert.Equal(t, "can_admin", string(authz.RelCanAdmin))
		assert.Equal(t, "can_delete", string(authz.RelCanDelete))
		assert.Equal(t, "owner", string(authz.RelOwner))
		assert.Equal(t, "admin", string(authz.RelAdmin))
		assert.Equal(t, "member", string(authz.RelMember))
	})
}

// idStr converts an int64 ID to a string.
func idStr(id int64) string {
	return fmt.Sprintf("%d", id)
}
