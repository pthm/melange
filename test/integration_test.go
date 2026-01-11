package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
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

	// Verify check_permission exists
	var result int
	err := db.QueryRowContext(ctx,
		"SELECT check_permission('__test__', '__test__', '__test__', '__test__', '__test__')",
	).Scan(&result)
	require.NoError(t, err)

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

	// Verify melange_tuples relation exists (view, table, or materialized view)
	var tuplesExists bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'melange_tuples'
			AND n.nspname = current_schema()
			AND c.relkind IN ('r', 'v', 'm')
		)
	`).Scan(&tuplesExists)
	require.NoError(t, err)
	assert.True(t, tuplesExists, "melange_tuples should exist")
}

// TestMigrator_GetStatus verifies the GetStatus method returns correct info.
func TestMigrator_GetStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	m := migrator.NewMigrator(db, "testdata")
	status, err := m.GetStatus(ctx)
	require.NoError(t, err)

	// Template database has tuples relation
	assert.True(t, status.TuplesExists, "melange_tuples should exist")
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
	defer func() { _ = tx.Rollback() }()

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

// ============================================================================
// SQL Edge Case Tests for Findings 1.1 - 1.6
// These tests verify semantic parity between check_permission and list functions
// ============================================================================

// TestExclusionViaImpliedBy verifies that exclusions respect implied-by relations.
// Finding 1.1: Exclusion checks must use closure table for implied relations.
//
// Schema: can_review = can_read but not author
//
//	author = [user] or owner  (owner implies author)
//
// A repository owner should be excluded from can_review because:
//   - owner implies author via the closure table
//   - can_review excludes author
func TestExclusionViaImpliedBy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, ownerID, readerID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('implied_owner') RETURNING id`).Scan(&ownerID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('implied_reader') RETURNING id`).Scan(&readerID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('implied_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// NOTE: We deliberately do NOT add users as org members here.
	// If we did, they would get can_read via org inheritance, which creates
	// an alternative access path that bypasses the direct exclusion check.
	// This test specifically verifies that exclusions work on direct tuple matches.

	// Create repository (needs org for schema validity)
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'implied_repo') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	// Add owner as repository owner (implies author via closure, also implies can_read)
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'owner')`, repoID, ownerID)
	require.NoError(t, err)

	// Add reader as repository reader
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`, repoID, readerID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	repo := authz.Repository(repoID)
	owner := authz.User(ownerID)
	reader := authz.User(readerID)

	// Verify owner has can_read (prerequisite for can_review)
	t.Run("owner has can_read", func(t *testing.T) {
		ok, err := checker.Check(ctx, owner, authz.RelCanRead, repo)
		require.NoError(t, err)
		assert.True(t, ok, "owner should have can_read")
	})

	// Verify owner is excluded from can_review (owner implies author)
	t.Run("owner excluded from can_review via implied author", func(t *testing.T) {
		ok, err := checker.Check(ctx, owner, melange.Relation("can_review"), repo)
		require.NoError(t, err)
		assert.False(t, ok, "owner should be excluded from can_review (owner implies author)")
	})

	// Verify reader can review (has can_read, is not author)
	t.Run("reader can review", func(t *testing.T) {
		ok, err := checker.Check(ctx, reader, melange.Relation("can_review"), repo)
		require.NoError(t, err)
		assert.True(t, ok, "reader should be able to review")
	})

	// Verify ListObjects excludes repo for owner (parity with Check)
	t.Run("list_accessible_objects excludes repo for owner", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, owner, melange.Relation("can_review"), authz.TypeRepository)
		require.NoError(t, err)
		assert.NotContains(t, ids, idStr(repoID), "list_accessible_objects should exclude repo where owner is author")
	})

	// Verify ListObjects includes repo for reader
	t.Run("list_accessible_objects includes repo for reader", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, reader, melange.Relation("can_review"), authz.TypeRepository)
		require.NoError(t, err)
		assert.Contains(t, ids, idStr(repoID), "list_accessible_objects should include repo for reader")
	})

	// Verify ListSubjects excludes owner (parity with Check)
	t.Run("list_accessible_subjects excludes owner", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, repo, melange.Relation("can_review"), authz.TypeUser)
		require.NoError(t, err)
		assert.NotContains(t, ids, idStr(ownerID), "list_accessible_subjects should exclude owner (implied author)")
		assert.Contains(t, ids, idStr(readerID), "list_accessible_subjects should include reader")
	})
}

// TestParentRelationMismatch verifies parent inheritance with different relation names.
// Finding 1.2: inherited_access must use m.parent_relation, not p_relation.
//
// Schema: repository.can_deploy = can_admin from org
//
// An org admin should have can_deploy on repositories in that org.
func TestParentRelationMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, adminID, outsiderID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('deploy_admin') RETURNING id`).Scan(&adminID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('deploy_outsider') RETURNING id`).Scan(&outsiderID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('deploy_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// Add admin to org as admin (implies can_admin on org)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, adminID)
	require.NoError(t, err)

	// Create repository under org
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'deploy_repo') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	repo := authz.Repository(repoID)
	admin := authz.User(adminID)
	outsider := authz.User(outsiderID)

	// Verify admin has can_admin on org
	t.Run("admin has can_admin on org", func(t *testing.T) {
		org := authz.Organization(orgID)
		ok, err := checker.Check(ctx, admin, authz.RelCanAdmin, org)
		require.NoError(t, err)
		assert.True(t, ok, "admin should have can_admin on org")
	})

	// Verify admin has can_deploy on repo (inherits from org.can_admin)
	t.Run("admin has can_deploy on repo", func(t *testing.T) {
		ok, err := checker.Check(ctx, admin, melange.Relation("can_deploy"), repo)
		require.NoError(t, err)
		assert.True(t, ok, "org admin should have can_deploy on repo via parent inheritance")
	})

	// Verify outsider does not have can_deploy
	t.Run("outsider does not have can_deploy", func(t *testing.T) {
		ok, err := checker.Check(ctx, outsider, melange.Relation("can_deploy"), repo)
		require.NoError(t, err)
		assert.False(t, ok, "outsider should not have can_deploy")
	})

	// Verify ListObjects returns repo for admin (parity with Check)
	t.Run("list_accessible_objects includes repo for admin", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, admin, melange.Relation("can_deploy"), authz.TypeRepository)
		require.NoError(t, err)
		assert.Contains(t, ids, idStr(repoID), "list_accessible_objects should include repo for org admin")
	})

	// Verify ListSubjects returns admin (parity with Check)
	t.Run("list_accessible_subjects includes admin", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, repo, melange.Relation("can_deploy"), authz.TypeUser)
		require.NoError(t, err)
		assert.Contains(t, ids, idStr(adminID), "list_accessible_subjects should include org admin")
	})
}

// TestDirectAccessExclusionParity verifies exclusions apply to direct access results.
// Finding 1.3: direct_access CTE must apply exclusion checks.
//
// A user with both can_read AND author on a repository should be excluded from can_review.
func TestDirectAccessExclusionParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, authorUserID, readerUserID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('direct_author') RETURNING id`).Scan(&authorUserID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('direct_reader') RETURNING id`).Scan(&readerUserID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('direct_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// Create repository with owner_id set (this creates an author tuple)
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, owner_id, name) VALUES ($1, $2, 'direct_repo') RETURNING id`, orgID, authorUserID).Scan(&repoID)
	require.NoError(t, err)

	// Add author as repository reader (so they have can_read directly)
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`, repoID, authorUserID)
	require.NoError(t, err)

	// Add reader as repository reader
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`, repoID, readerUserID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	repo := authz.Repository(repoID)
	authorUser := authz.User(authorUserID)
	readerUser := authz.User(readerUserID)

	// Verify author has can_read
	t.Run("author has can_read", func(t *testing.T) {
		ok, err := checker.Check(ctx, authorUser, authz.RelCanRead, repo)
		require.NoError(t, err)
		assert.True(t, ok, "author should have can_read")
	})

	// Verify author is excluded from can_review (has both can_read and author)
	t.Run("author excluded from can_review", func(t *testing.T) {
		ok, err := checker.Check(ctx, authorUser, melange.Relation("can_review"), repo)
		require.NoError(t, err)
		assert.False(t, ok, "author should be excluded from can_review despite having direct can_read")
	})

	// Verify reader can review
	t.Run("reader can review", func(t *testing.T) {
		ok, err := checker.Check(ctx, readerUser, melange.Relation("can_review"), repo)
		require.NoError(t, err)
		assert.True(t, ok, "reader should be able to review")
	})

	// Verify ListObjects excludes repo for author (parity with Check)
	t.Run("list_accessible_objects excludes repo for author", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, authorUser, melange.Relation("can_review"), authz.TypeRepository)
		require.NoError(t, err)
		assert.NotContains(t, ids, idStr(repoID), "list_accessible_objects should exclude repo for author")
	})

	// Verify ListSubjects excludes author (parity with Check)
	t.Run("list_accessible_subjects excludes author", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, repo, melange.Relation("can_review"), authz.TypeUser)
		require.NoError(t, err)
		assert.NotContains(t, ids, idStr(authorUserID), "list_accessible_subjects should exclude author")
		assert.Contains(t, ids, idStr(readerUserID), "list_accessible_subjects should include reader")
	})
}

// TestWildcardExclusion verifies wildcard exclusions work in list functions.
// Finding 1.4: list_accessible_subjects must handle subject_id='*' in exclusions.
//
// Schema: can_read_safe = can_read but not banned
//
//	banned = [user:*]  (supports wildcards)
//
// When a repository has a wildcard ban (all users banned), list_accessible_subjects
// should return empty, and check should deny.
func TestWildcardExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, userID int64

	// Create user
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('wildcard_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('wildcard_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// NOTE: Don't add user as org member to avoid org inheritance path.
	// The user will get can_read directly from repository_collaborators.

	// Create repository
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'wildcard_repo') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	// Add user as repository reader (so they have direct can_read)
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`, repoID, userID)
	require.NoError(t, err)

	// Add wildcard ban (all users banned)
	_, err = db.ExecContext(ctx, `INSERT INTO repository_bans (repository_id, user_id, banned_all) VALUES ($1, NULL, true)`, repoID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	repo := authz.Repository(repoID)
	user := authz.User(userID)

	// Verify user has can_read
	t.Run("user has can_read", func(t *testing.T) {
		ok, err := checker.Check(ctx, user, authz.RelCanRead, repo)
		require.NoError(t, err)
		assert.True(t, ok, "user should have can_read")
	})

	// Verify user is excluded from can_read_safe (wildcard ban)
	t.Run("user excluded from can_read_safe via wildcard ban", func(t *testing.T) {
		ok, err := checker.Check(ctx, user, melange.Relation("can_read_safe"), repo)
		require.NoError(t, err)
		assert.False(t, ok, "user should be excluded from can_read_safe due to wildcard ban")
	})

	// Verify ListObjects excludes repo (parity with Check)
	t.Run("list_accessible_objects excludes repo", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, user, melange.Relation("can_read_safe"), authz.TypeRepository)
		require.NoError(t, err)
		assert.NotContains(t, ids, idStr(repoID), "list_accessible_objects should exclude repo with wildcard ban")
	})

	// Verify ListSubjects returns empty (all users banned)
	t.Run("list_accessible_subjects returns empty", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, repo, melange.Relation("can_read_safe"), authz.TypeUser)
		require.NoError(t, err)
		assert.Empty(t, ids, "list_accessible_subjects should return empty when all users banned")
	})
}

// TestParentLevelExclusions verifies exclusions inherited from parent objects.
// Finding 1.5: list_accessible_subjects must check exclusions at the permission source level.
//
// Schema: pull_request.banned = can_admin from repo
//
//	pull_request.can_review_strict = can_read from repo but not banned
//
// A repo admin should be excluded from can_review_strict on PRs in that repo
// because they are "banned" (inherited from repo.can_admin).
func TestParentLevelExclusions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, repoID, prID, adminID, readerID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parent_admin') RETURNING id`).Scan(&adminID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parent_reader') RETURNING id`).Scan(&readerID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('parent_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// NOTE: Don't add users as org members to avoid org inheritance path complexity.
	// Users get their access directly from repository_collaborators.

	// Create repository
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'parent_repo') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	// Add admin as repository admin (implies can_admin, which implies banned on PRs)
	// Admin also gets can_read via admin -> maintainer -> writer -> reader
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'admin')`, repoID, adminID)
	require.NoError(t, err)

	// Add reader as repository reader (so they have can_read but not can_admin)
	_, err = db.ExecContext(ctx, `INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, 'reader')`, repoID, readerID)
	require.NoError(t, err)

	// Create pull request
	err = db.QueryRowContext(ctx, `
		INSERT INTO pull_requests (repository_id, author_id, title, source_branch)
		VALUES ($1, $2, 'Parent Test PR', 'feature')
		RETURNING id
	`, repoID, readerID).Scan(&prID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	pr := authz.PullRequest(prID)
	admin := authz.User(adminID)
	reader := authz.User(readerID)

	// Verify admin has can_read on PR (via repo)
	t.Run("admin has can_read on PR", func(t *testing.T) {
		ok, err := checker.Check(ctx, admin, authz.RelCanRead, pr)
		require.NoError(t, err)
		assert.True(t, ok, "admin should have can_read on PR")
	})

	// Verify admin is excluded from can_review_strict (banned via parent)
	t.Run("admin excluded from can_review_strict via parent-level ban", func(t *testing.T) {
		ok, err := checker.Check(ctx, admin, melange.Relation("can_review_strict"), pr)
		require.NoError(t, err)
		assert.False(t, ok, "repo admin should be excluded from can_review_strict (banned inherited from repo.can_admin)")
	})

	// Verify reader can review_strict (not banned)
	t.Run("reader can review_strict", func(t *testing.T) {
		ok, err := checker.Check(ctx, reader, melange.Relation("can_review_strict"), pr)
		require.NoError(t, err)
		assert.True(t, ok, "reader should be able to review_strict (not banned)")
	})

	// Verify ListObjects excludes PR for admin (parity with Check)
	t.Run("list_accessible_objects excludes PR for admin", func(t *testing.T) {
		ids, err := checker.ListObjects(ctx, admin, melange.Relation("can_review_strict"), authz.TypePullRequest)
		require.NoError(t, err)
		assert.NotContains(t, ids, idStr(prID), "list_accessible_objects should exclude PR for banned admin")
	})

	// Verify ListSubjects excludes admin (parity with Check)
	// Regression test: list_accessible_subjects must correctly exclude subjects when the
	// exclusion has parent inheritance (e.g., banned: can_admin from repo). This requires
	// the recursive CTE to expand via the closure table for satisfying relations.
	t.Run("list_accessible_subjects excludes admin", func(t *testing.T) {
		ids, err := checker.ListSubjects(ctx, pr, melange.Relation("can_review_strict"), authz.TypeUser)
		require.NoError(t, err)

		// Reader should be included (not banned)
		assert.Contains(t, ids, idStr(readerID), "list_accessible_subjects should include reader")

		// Admin should be excluded (banned via parent inheritance)
		assert.NotContains(t, ids, idStr(adminID), "list_accessible_subjects should exclude banned admin")

		// Double-check parity with Check
		adminAllowed, err := checker.Check(ctx, admin, melange.Relation("can_review_strict"), pr)
		require.NoError(t, err)
		assert.False(t, adminAllowed, "check should also exclude banned admin")
	})
}

// TestListCheckParityProperty is a property-style test ensuring that every subject
// returned by ListSubjects passes Check, and every object returned by ListObjects passes Check.
// Finding 1.6: Semantic parity between list and check functions.
func TestListCheckParityProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create a small graph with various roles and exclusions
	var orgID, repo1ID, repo2ID int64
	var user1ID, user2ID, user3ID int64

	// Create users
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_user1') RETURNING id`).Scan(&user1ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_user2') RETURNING id`).Scan(&user2ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_user3') RETURNING id`).Scan(&user3ID)
	require.NoError(t, err)

	// Create organization
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('parity_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	// User1: org admin (has can_admin on org, can_deploy on repos)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, user1ID)
	require.NoError(t, err)

	// User2: org member (has can_read on org and repos)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, user2ID)
	require.NoError(t, err)

	// User3: org member
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, user3ID)
	require.NoError(t, err)

	// Create repos
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, owner_id, name) VALUES ($1, $2, 'parity_repo1') RETURNING id`, orgID, user2ID).Scan(&repo1ID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'parity_repo2') RETURNING id`, orgID).Scan(&repo2ID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)

	// Test relations with exclusions
	relations := []string{"can_review", "can_read_safe", "can_deploy"}
	users := []int64{user1ID, user2ID, user3ID}
	repos := []int64{repo1ID, repo2ID}

	// For each relation, verify ListObjects/Check parity
	for _, rel := range relations {
		t.Run(fmt.Sprintf("ListObjects/%s", rel), func(t *testing.T) {
			for _, uid := range users {
				user := authz.User(uid)
				ids, err := checker.ListObjects(ctx, user, melange.Relation(rel), authz.TypeRepository)
				require.NoError(t, err)

				// Every returned object must pass Check
				for _, objID := range ids {
					obj := melange.Object{Type: authz.TypeRepository, ID: objID}
					ok, err := checker.Check(ctx, user, melange.Relation(rel), obj)
					require.NoError(t, err)
					assert.True(t, ok, "ListObjects returned %s but Check failed for user %d", objID, uid)
				}
			}
		})
	}

	// For each relation, verify ListSubjects/Check parity
	for _, rel := range relations {
		t.Run(fmt.Sprintf("ListSubjects/%s", rel), func(t *testing.T) {
			for _, rid := range repos {
				repo := authz.Repository(rid)
				ids, err := checker.ListSubjects(ctx, repo, melange.Relation(rel), authz.TypeUser)
				require.NoError(t, err)

				// Every returned subject must pass Check
				for _, subjID := range ids {
					subj := melange.Object{Type: authz.TypeUser, ID: subjID}
					ok, err := checker.Check(ctx, subj, melange.Relation(rel), repo)
					require.NoError(t, err)
					assert.True(t, ok, "ListSubjects returned %s but Check failed for repo %d", subjID, rid)
				}
			}
		})
	}
}
