package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBulkFixtures_CreateUsers(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	tests := []struct {
		name  string
		count int
	}{
		{"zero", 0},
		{"small", 10},
		{"medium", 1000},
		{"large", 10000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := bf.CreateUsers(tt.count)
			require.NoError(t, err)

			if tt.count == 0 {
				require.Nil(t, ids)
				return
			}

			require.Len(t, ids, tt.count)

			// Verify all IDs are unique
			seen := make(map[int64]bool)
			for _, id := range ids {
				require.False(t, seen[id], "duplicate ID: %d", id)
				seen[id] = true
			}

			// Verify count in database
			var count int
			err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
			require.NoError(t, err)
			require.Equal(t, tt.count, count)

			// Cleanup
			_, err = db.ExecContext(ctx, "TRUNCATE TABLE users CASCADE")
			require.NoError(t, err)
		})
	}
}

func TestBulkFixtures_CreateOrganizations(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	tests := []struct {
		name  string
		count int
	}{
		{"zero", 0},
		{"small", 5},
		{"medium", 100},
		{"large", 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := bf.CreateOrganizations(tt.count)
			require.NoError(t, err)

			if tt.count == 0 {
				require.Nil(t, ids)
				return
			}

			require.Len(t, ids, tt.count)

			// Verify all IDs are unique
			seen := make(map[int64]bool)
			for _, id := range ids {
				require.False(t, seen[id], "duplicate ID: %d", id)
				seen[id] = true
			}

			// Verify count in database
			var count int
			err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM organizations").Scan(&count)
			require.NoError(t, err)
			require.Equal(t, tt.count, count)

			// Cleanup
			_, err = db.ExecContext(ctx, "TRUNCATE TABLE organizations CASCADE")
			require.NoError(t, err)
		})
	}
}

func TestBulkFixtures_CreateRepositories(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	// Create an organization first
	orgs, err := bf.CreateOrganizations(1)
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	orgID := orgs[0]

	tests := []struct {
		name  string
		count int
	}{
		{"zero", 0},
		{"small", 10},
		{"medium", 500},
		{"large", 5000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := bf.CreateRepositories(orgID, tt.count)
			require.NoError(t, err)

			if tt.count == 0 {
				require.Nil(t, ids)
				return
			}

			require.Len(t, ids, tt.count)

			// Verify all IDs are unique
			seen := make(map[int64]bool)
			for _, id := range ids {
				require.False(t, seen[id], "duplicate ID: %d", id)
				seen[id] = true
			}

			// Verify count in database
			var count int
			err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM repositories WHERE organization_id = $1", orgID).Scan(&count)
			require.NoError(t, err)
			require.Equal(t, tt.count, count)

			// Cleanup repos only
			_, err = db.ExecContext(ctx, "DELETE FROM repositories WHERE organization_id = $1", orgID)
			require.NoError(t, err)
		})
	}
}

func TestBulkFixtures_AddOrganizationMembers(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	// Create users and organization
	users, err := bf.CreateUsers(100)
	require.NoError(t, err)
	require.Len(t, users, 100)

	orgs, err := bf.CreateOrganizations(1)
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	orgID := orgs[0]

	tests := []struct {
		name     string
		userIDs  []int64
		role     string
		expected int
	}{
		{"zero", []int64{}, "member", 0},
		{"single", users[:1], "owner", 1},
		{"small", users[:10], "member", 10},
		{"medium", users[:50], "admin", 50},
		{"all", users, "member", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := bf.AddOrganizationMembers(orgID, tt.userIDs, tt.role)
			require.NoError(t, err)

			// Verify count in database
			var count int
			err = db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM organization_members WHERE organization_id = $1",
				orgID).Scan(&count)
			require.NoError(t, err)
			require.Equal(t, tt.expected, count)

			// Cleanup members only
			_, err = db.ExecContext(ctx, "DELETE FROM organization_members WHERE organization_id = $1", orgID)
			require.NoError(t, err)
		})
	}
}

func TestBulkFixtures_CreatePullRequests(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	// Create users, org, and repo
	users, err := bf.CreateUsers(10)
	require.NoError(t, err)

	orgs, err := bf.CreateOrganizations(1)
	require.NoError(t, err)
	orgID := orgs[0]

	repos, err := bf.CreateRepositories(orgID, 1)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	repoID := repos[0]

	tests := []struct {
		name  string
		count int
	}{
		{"zero", 0},
		{"small", 10},
		{"medium", 500},
		{"large", 5000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := bf.CreatePullRequests(repoID, users, tt.count)
			require.NoError(t, err)

			if tt.count == 0 {
				require.Nil(t, ids)
				return
			}

			require.Len(t, ids, tt.count)

			// Verify all IDs are unique
			seen := make(map[int64]bool)
			for _, id := range ids {
				require.False(t, seen[id], "duplicate ID: %d", id)
				seen[id] = true
			}

			// Verify count in database
			var count int
			err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pull_requests WHERE repository_id = $1", repoID).Scan(&count)
			require.NoError(t, err)
			require.Equal(t, tt.count, count)

			// Cleanup PRs only
			_, err = db.ExecContext(ctx, "DELETE FROM pull_requests WHERE repository_id = $1", repoID)
			require.NoError(t, err)
		})
	}
}

func TestBulkFixtures_MatchesFixtures(t *testing.T) {
	// Verify that BulkFixtures produces same tuple counts as Fixtures
	// Use same database but different data to compare counts
	db := DB(t)
	ctx := context.Background()

	// Method 1: Regular Fixtures - create some data
	f := NewFixtures(ctx, db)
	users1, err := f.CreateUsers(100)
	require.NoError(t, err)
	require.Len(t, users1, 100)

	orgs1, err := f.CreateOrganizations(5)
	require.NoError(t, err)
	require.Len(t, orgs1, 5)

	count1, err := f.TupleCount()
	require.NoError(t, err)

	// Clean up
	_, err = db.ExecContext(ctx, "TRUNCATE TABLE organization_members, organizations, users CASCADE")
	require.NoError(t, err)

	// Method 2: BulkFixtures - create same amount of data
	bf := NewBulkFixtures(ctx, db)
	users2, err := bf.CreateUsers(100)
	require.NoError(t, err)
	require.Len(t, users2, 100)

	orgs2, err := bf.CreateOrganizations(5)
	require.NoError(t, err)
	require.Len(t, orgs2, 5)

	count2, err := bf.TupleCount()
	require.NoError(t, err)

	// Verify tuple counts match (both should be 0 since we haven't added any organization members)
	require.Equal(t, count1, count2, "BulkFixtures should produce same tuple count as Fixtures")
	require.Equal(t, 0, count1, "No tuples expected without organization members")
	require.Equal(t, 0, count2, "No tuples expected without organization members")
}

func TestBulkFixtures_TupleCount(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	// Initially empty
	count, err := bf.TupleCount()
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Create some data
	users, err := bf.CreateUsers(50)
	require.NoError(t, err)

	orgs, err := bf.CreateOrganizations(3)
	require.NoError(t, err)

	// Add org members (creates tuples)
	err = bf.AddOrganizationMembers(orgs[0], users[:10], "member")
	require.NoError(t, err)

	// Count should be > 0 now
	count, err = bf.TupleCount()
	require.NoError(t, err)
	require.Greater(t, count, 0)
}

func TestBulkFixtures_EmptyInput(t *testing.T) {
	db := DB(t)
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	// Test all methods with empty/zero input
	users, err := bf.CreateUsers(0)
	require.NoError(t, err)
	require.Nil(t, users)

	orgs, err := bf.CreateOrganizations(0)
	require.NoError(t, err)
	require.Nil(t, orgs)

	repos, err := bf.CreateRepositories(1, 0)
	require.NoError(t, err)
	require.Nil(t, repos)

	err = bf.AddOrganizationMembers(1, []int64{}, "member")
	require.NoError(t, err)

	err = bf.AddOrganizationMembers(1, nil, "member")
	require.NoError(t, err)

	prs, err := bf.CreatePullRequests(1, []int64{1, 2, 3}, 0)
	require.NoError(t, err)
	require.Nil(t, prs)
}
