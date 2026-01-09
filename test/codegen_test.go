package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/test/testutil"
)

// TestCodegen_SpecializedFunctionsExist verifies that specialized check functions
// are being generated and applied to the database during migration.
func TestCodegen_SpecializedFunctionsExist(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Query for all specialized check functions
	rows, err := db.QueryContext(ctx, `
		SELECT p.proname, pg_get_function_arguments(p.oid) as args
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE p.proname LIKE 'check_%_%'
		  AND n.nspname = current_schema()
		  AND p.proname NOT LIKE '%_generic'
		ORDER BY p.proname
	`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	functions := make([]string, 0, 16)
	for rows.Next() {
		var name, args string
		require.NoError(t, rows.Scan(&name, &args))
		functions = append(functions, name)
	}
	require.NoError(t, rows.Err())

	t.Logf("Found %d specialized check functions", len(functions))
	for _, fn := range functions {
		t.Logf("  - %s", fn)
	}

	// Verify at least some expected functions exist
	// These should be generatable based on the test schema
	expectedFunctions := []string{
		"check_organization_owner",
		"check_organization_admin",
		"check_organization_member",
		"check_organization_billing_manager",
		"check_team_maintainer",
		"check_team_member",
		"check_repository_owner",
		"check_repository_admin",
	}

	for _, expected := range expectedFunctions {
		found := false
		for _, fn := range functions {
			if fn == expected {
				found = true
				break
			}
		}
		assert.True(t, found, "expected function %s to exist", expected)
	}
}

// TestCodegen_DispatcherRouting verifies that the dispatcher routes to specialized
// functions instead of always calling generic.
func TestCodegen_DispatcherRouting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Get the internal dispatcher function definition (where routing happens)
	var internalDispatcherDef string
	err := db.QueryRowContext(ctx, `
		SELECT pg_get_functiondef(p.oid)
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE p.proname = 'check_permission_internal'
		  AND n.nspname = current_schema()
		LIMIT 1
	`).Scan(&internalDispatcherDef)
	require.NoError(t, err)

	t.Logf("Internal dispatcher definition:\n%s", internalDispatcherDef)

	// Verify the internal dispatcher contains specialized routing
	// It should contain CASE statements for routing to specialized functions
	assert.Contains(t, internalDispatcherDef, "check_organization_owner",
		"internal dispatcher should route to check_organization_owner")
	// Phase 5: Dispatcher returns 0 for unknown type/relation pairs (no generic fallback)
	assert.Contains(t, internalDispatcherDef, "ELSE 0",
		"internal dispatcher should return 0 for unknown type/relation pairs")

	// Also verify the public check_permission delegates to internal
	var publicDispatcherDef string
	err = db.QueryRowContext(ctx, `
		SELECT pg_get_functiondef(p.oid)
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE p.proname = 'check_permission'
		  AND n.nspname = current_schema()
		LIMIT 1
	`).Scan(&publicDispatcherDef)
	require.NoError(t, err)

	t.Logf("Public dispatcher definition:\n%s", publicDispatcherDef)

	assert.Contains(t, publicDispatcherDef, "check_permission_internal",
		"public dispatcher should delegate to internal dispatcher")
}

// TestCodegen_SpecializedFunctionCorrectness verifies that specialized functions
// produce correct results.
func TestCodegen_SpecializedFunctionCorrectness(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Insert test data using correct table schema
	_, err := db.ExecContext(ctx, `
		INSERT INTO users (id, username) VALUES (100, 'alice') ON CONFLICT DO NOTHING;
		INSERT INTO users (id, username) VALUES (999, 'nobody') ON CONFLICT DO NOTHING;
		INSERT INTO organizations (id, name) VALUES (1, 'testorg') ON CONFLICT DO NOTHING;
		INSERT INTO organization_members (organization_id, user_id, role)
		VALUES (1, 100, 'owner') ON CONFLICT DO NOTHING;
	`)
	require.NoError(t, err)

	// Test 1: Direct owner check should return 1
	var result int
	err = db.QueryRowContext(ctx,
		`SELECT check_permission('user', '100', 'owner', 'organization', '1')`).Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result, "owner should have owner permission")

	// Test 2: Owner should have admin (via implied relation)
	err = db.QueryRowContext(ctx,
		`SELECT check_permission('user', '100', 'admin', 'organization', '1')`).Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result, "owner should have admin permission via implied relation")

	// Test 3: Owner should have member (via implied relation chain)
	err = db.QueryRowContext(ctx,
		`SELECT check_permission('user', '100', 'member', 'organization', '1')`).Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result, "owner should have member permission via implied relation chain")

	// Test 4: Non-member should not have permissions
	err = db.QueryRowContext(ctx,
		`SELECT check_permission('user', '999', 'member', 'organization', '1')`).Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 0, result, "non-member should not have member permission")

	// Test 5: Call specialized function directly (should match dispatcher result)
	err = db.QueryRowContext(ctx,
		`SELECT check_organization_owner('user', '100', '1', ARRAY[]::TEXT[])`).Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result, "direct call to check_organization_owner should return 1")
}
