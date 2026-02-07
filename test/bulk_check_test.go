package test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/authz"
	"github.com/pthm/melange/test/testutil"
)

// TestBulkCheck_MixedResults tests a batch where some checks are allowed
// and some denied.
func TestBulkCheck_MixedResults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create test data
	var orgID, memberID, outsiderID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_member') RETURNING id`).Scan(&memberID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_outsider') RETURNING id`).Scan(&outsiderID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, memberID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	org := authz.Organization(orgID)
	member := authz.User(memberID)
	outsider := authz.User(outsiderID)

	res, err := checker.NewBulkCheck(ctx).
		Add(member, authz.RelCanRead, org).
		Add(outsider, authz.RelCanRead, org).
		Execute()
	require.NoError(t, err)

	assert.Equal(t, 2, res.Len())
	assert.False(t, res.All(), "not all should be allowed")
	assert.True(t, res.Any(), "at least one should be allowed")
	assert.False(t, res.None(), "not none")

	assert.True(t, res.Get(0).IsAllowed(), "member should be allowed")
	assert.False(t, res.Get(1).IsAllowed(), "outsider should be denied")

	assert.Len(t, res.Allowed(), 1)
	assert.Len(t, res.Denied(), 1)
}

// TestBulkCheck_MultipleRelations checks the same user for different
// relations on the same object.
func TestBulkCheck_MultipleRelations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, adminID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_admin') RETURNING id`).Scan(&adminID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_rel_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, adminID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	org := authz.Organization(orgID)
	admin := authz.User(adminID)

	res, err := checker.NewBulkCheck(ctx).
		Add(admin, authz.RelCanRead, org).
		Add(admin, authz.RelCanAdmin, org).
		Add(admin, authz.RelCanDelete, org).
		Execute()
	require.NoError(t, err)

	assert.Equal(t, 3, res.Len())
	assert.True(t, res.Get(0).IsAllowed(), "admin should have can_read")
	assert.True(t, res.Get(1).IsAllowed(), "admin should have can_admin")
	assert.False(t, res.Get(2).IsAllowed(), "admin should NOT have can_delete")
}

// TestBulkCheck_MultipleObjectTypes checks across different object types
// in a single batch.
func TestBulkCheck_MultipleObjectTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, repoID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_multi_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_multi_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'bulk_multi_repo') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	user := authz.User(userID)
	org := authz.Organization(orgID)
	repo := authz.Repository(repoID)

	res, err := checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Add(user, authz.RelCanRead, repo).
		Execute()
	require.NoError(t, err)

	assert.Equal(t, 2, res.Len())
	assert.True(t, res.Get(0).IsAllowed(), "user should read org")
	assert.True(t, res.Get(1).IsAllowed(), "user should read repo via inheritance")
}

// TestBulkCheck_AddWithID_Integration tests custom IDs with real SQL.
func TestBulkCheck_AddWithID_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_id_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_id_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	user := authz.User(userID)
	org := authz.Organization(orgID)

	res, err := checker.NewBulkCheck(ctx).
		AddWithID("org-read", user, authz.RelCanRead, org).
		AddWithID("org-admin", user, authz.RelCanAdmin, org).
		Execute()
	require.NoError(t, err)

	read := res.GetByID("org-read")
	require.NotNil(t, read)
	assert.True(t, read.IsAllowed(), "member should have can_read")

	admin := res.GetByID("org-admin")
	require.NotNil(t, admin)
	assert.False(t, admin.IsAllowed(), "member should NOT have can_admin")
}

// TestBulkCheck_AddMany_Integration tests AddMany across multiple repos.
func TestBulkCheck_AddMany_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_many_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_many_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	var repos []melange.ObjectLike
	for i := range 5 {
		var repoID int64
		err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, $2) RETURNING id`,
			orgID, fmt.Sprintf("bulk_many_repo_%d", i)).Scan(&repoID)
		require.NoError(t, err)
		repos = append(repos, authz.Repository(repoID))
	}

	checker := melange.NewChecker(db)
	user := authz.User(userID)

	res, err := checker.NewBulkCheck(ctx).
		AddMany(user, authz.RelCanRead, repos...).
		Execute()
	require.NoError(t, err)

	assert.Equal(t, 5, res.Len())
	assert.True(t, res.All(), "member should have can_read on all repos via org inheritance")
}

// TestBulkCheck_Deduplication tests that duplicate checks all get the same result.
func TestBulkCheck_Deduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_dedup_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_dedup_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	user := authz.User(userID)
	org := authz.Organization(orgID)

	// Add the same check 3 times
	res, err := checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Add(user, authz.RelCanRead, org).
		Add(user, authz.RelCanRead, org).
		Execute()
	require.NoError(t, err)

	assert.Equal(t, 3, res.Len())
	for i := range 3 {
		assert.True(t, res.Get(i).IsAllowed(), "duplicate check %d should be allowed", i)
	}
}

// TestBulkCheck_ParityWithCheck verifies bulk results match individual Check calls.
func TestBulkCheck_ParityWithCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Create a diverse permission graph
	var orgID, repoID int64
	var ownerID, adminID, memberID, outsiderID int64

	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_owner') RETURNING id`).Scan(&ownerID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_admin') RETURNING id`).Scan(&adminID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_member') RETURNING id`).Scan(&memberID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('parity_outsider') RETURNING id`).Scan(&outsiderID)
	require.NoError(t, err)

	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('parity_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, ownerID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, adminID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, memberID)
	require.NoError(t, err)

	err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, 'parity_repo') RETURNING id`, orgID).Scan(&repoID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	org := authz.Organization(orgID)
	repo := authz.Repository(repoID)

	// Build a diverse set of checks
	type checkSpec struct {
		subject  melange.Object
		relation melange.Relation
		object   melange.Object
	}

	checks := []checkSpec{
		{authz.User(ownerID), authz.RelCanRead, org},
		{authz.User(ownerID), authz.RelCanDelete, org},
		{authz.User(adminID), authz.RelCanAdmin, org},
		{authz.User(adminID), authz.RelCanDelete, org},
		{authz.User(memberID), authz.RelCanRead, org},
		{authz.User(memberID), authz.RelCanAdmin, org},
		{authz.User(outsiderID), authz.RelCanRead, org},
		{authz.User(memberID), authz.RelCanRead, repo},
		{authz.User(outsiderID), authz.RelCanRead, repo},
		{authz.User(ownerID), authz.RelCanRead, repo},
	}

	b := checker.NewBulkCheck(ctx)
	for _, c := range checks {
		b.Add(c.subject, c.relation, c.object)
	}
	res, err := b.Execute()
	require.NoError(t, err)

	// Verify each bulk result matches individual Check
	for i, c := range checks {
		expected, err := checker.Check(ctx, c.subject, c.relation, c.object)
		require.NoError(t, err)
		actual := res.Get(i).IsAllowed()
		assert.Equal(t, expected, actual,
			"check %d (%s %s %s): bulk=%v, individual=%v",
			i, c.subject, c.relation, c.object, actual, expected)
	}
}

// TestBulkCheck_AllOrError_Integration tests AllOrError with real mixed results.
func TestBulkCheck_AllOrError_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, memberID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_aoe_user') RETURNING id`).Scan(&memberID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_aoe_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, memberID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	user := authz.User(memberID)
	org := authz.Organization(orgID)

	// member has can_read but NOT can_admin
	res, err := checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Add(user, authz.RelCanAdmin, org).
		Execute()
	require.NoError(t, err)

	aoeErr := res.AllOrError()
	require.Error(t, aoeErr)
	assert.True(t, errors.Is(aoeErr, melange.ErrBulkCheckDenied))

	var denied *melange.BulkCheckDeniedError
	require.True(t, errors.As(aoeErr, &denied))
	assert.Equal(t, 1, denied.Total)
	assert.Equal(t, 1, denied.Index) // second check was denied
}

// TestBulkCheck_WithCache verifies cache is populated by bulk and subsequent
// Check() calls hit the cache.
func TestBulkCheck_WithCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_cache_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_cache_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	cache := melange.NewCache()
	checker := melange.NewChecker(db, melange.WithCache(cache))
	user := authz.User(userID)
	org := authz.Organization(orgID)

	// Cache should be empty initially
	assert.Equal(t, 0, cache.Size())

	// Execute bulk check
	res, err := checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Add(user, authz.RelCanAdmin, org).
		Execute()
	require.NoError(t, err)
	assert.True(t, res.Get(0).IsAllowed())
	assert.False(t, res.Get(1).IsAllowed())

	// Cache should now have entries
	assert.Greater(t, cache.Size(), 0, "cache should be populated after bulk check")

	// Subsequent Check() should use cache (we can verify by removing the membership
	// and checking the same thing - if cached, it will still return the cached result)
	_, err = db.ExecContext(ctx, `DELETE FROM organization_members WHERE user_id = $1`, userID)
	require.NoError(t, err)

	// This should still return true from cache
	ok, err := checker.Check(ctx, user, authz.RelCanRead, org)
	require.NoError(t, err)
	assert.True(t, ok, "should return cached result even after membership removed")
}

// TestBulkCheck_ResultOrdering verifies results match insertion order.
func TestBulkCheck_ResultOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_order_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_order_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, orgID, userID)
	require.NoError(t, err)

	var repoIDs []int64
	for i := range 5 {
		var repoID int64
		err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, $2) RETURNING id`,
			orgID, fmt.Sprintf("bulk_order_repo_%d", i)).Scan(&repoID)
		require.NoError(t, err)
		repoIDs = append(repoIDs, repoID)
	}

	checker := melange.NewChecker(db)
	user := authz.User(userID)

	b := checker.NewBulkCheck(ctx)
	for _, rid := range repoIDs {
		b.Add(user, authz.RelCanRead, authz.Repository(rid))
	}
	res, err := b.Execute()
	require.NoError(t, err)

	for i, rid := range repoIDs {
		r := res.Get(i)
		assert.Equal(t, i, r.Index(), "result %d index mismatch", i)
		assert.Equal(t, idStr(rid), r.Object().ID, "result %d object ID mismatch", i)
	}
}

// TestBulkCheck_SingleCheck tests a batch of 1.
func TestBulkCheck_SingleCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_single_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_single_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	user := authz.User(userID)
	org := authz.Organization(orgID)

	res, err := checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Execute()
	require.NoError(t, err)

	assert.Equal(t, 1, res.Len())
	assert.True(t, res.Get(0).IsAllowed())
	assert.True(t, res.All())
}

// TestBulkCheck_LargeBatch tests 100 checks in a single batch.
func TestBulkCheck_LargeBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, userID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_large_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_large_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	checker := melange.NewChecker(db)
	user := authz.User(userID)

	// Create 100 repos and collect IDs
	var repoIDs []int64
	for i := range 100 {
		var repoID int64
		err = db.QueryRowContext(ctx, `INSERT INTO repositories (organization_id, name) VALUES ($1, $2) RETURNING id`,
			orgID, fmt.Sprintf("bulk_large_repo_%03d", i)).Scan(&repoID)
		require.NoError(t, err)
		repoIDs = append(repoIDs, repoID)
	}

	b := checker.NewBulkCheck(ctx)
	for _, rid := range repoIDs {
		b.Add(user, authz.RelCanRead, authz.Repository(rid))
	}
	res, err := b.Execute()
	require.NoError(t, err)

	assert.Equal(t, 100, res.Len())
	assert.True(t, res.All(), "member should have can_read on all 100 repos via org")
}

// TestBulkCheck_DirectSQL calls check_permission_bulk directly to verify
// the SQL function returns (idx, allowed) rows.
func TestBulkCheck_DirectSQL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var orgID, memberID, outsiderID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_sql_member') RETURNING id`).Scan(&memberID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_sql_outsider') RETURNING id`).Scan(&outsiderID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_sql_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, memberID)
	require.NoError(t, err)

	orgIDStr := idStr(orgID)
	memberIDStr := idStr(memberID)
	outsiderIDStr := idStr(outsiderID)

	rows, err := db.QueryContext(ctx,
		`SELECT idx, allowed FROM check_permission_bulk($1, $2, $3, $4, $5)`,
		`{user,user}`,                                         // subject_types
		fmt.Sprintf("{%s,%s}", memberIDStr, outsiderIDStr),    // subject_ids
		`{can_read,can_read}`,                                 // relations
		`{organization,organization}`,                         // object_types
		fmt.Sprintf("{%s,%s}", orgIDStr, orgIDStr),            // object_ids
	)
	require.NoError(t, err)
	defer rows.Close()

	type sqlResult struct {
		idx     int
		allowed int
	}
	var results []sqlResult
	for rows.Next() {
		var r sqlResult
		require.NoError(t, rows.Scan(&r.idx, &r.allowed))
		results = append(results, r)
	}
	require.NoError(t, rows.Err())

	assert.Len(t, results, 2)
	// Results are ordered by idx (1-based from WITH ORDINALITY)
	assert.Equal(t, 1, results[0].idx)
	assert.Equal(t, 1, results[0].allowed, "member should be allowed")
	assert.Equal(t, 2, results[1].idx)
	assert.Equal(t, 0, results[1].allowed, "outsider should be denied")
}

// TestBulkCheck_InTransaction verifies bulk check sees uncommitted data.
func TestBulkCheck_InTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	var userID, orgID int64
	err := db.QueryRowContext(ctx, `INSERT INTO users (username) VALUES ('bulk_tx_user') RETURNING id`).Scan(&userID)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('bulk_tx_org') RETURNING id`).Scan(&orgID)
	require.NoError(t, err)

	user := authz.User(userID)
	org := authz.Organization(orgID)

	// Start transaction
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	// Before adding membership
	checker := melange.NewChecker(tx)
	res, err := checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Execute()
	require.NoError(t, err)
	assert.False(t, res.Get(0).IsAllowed(), "user should NOT have access before membership")

	// Add membership within transaction
	_, err = tx.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'member')`, orgID, userID)
	require.NoError(t, err)

	// After adding membership (uncommitted)
	res, err = checker.NewBulkCheck(ctx).
		Add(user, authz.RelCanRead, org).
		Execute()
	require.NoError(t, err)
	assert.True(t, res.Get(0).IsAllowed(), "user should have access after uncommitted insert")
}
