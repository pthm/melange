package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/lib/doctor"
	"github.com/pthm/melange/test/testutil"
)

// TestDoctor_FullHealthy verifies that a fully set up database passes all checks,
// including performance checks. The template DB has expression indexes from migrations.
func TestDoctor_FullHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	d := doctor.New(db, "testutil/testdata")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	// Performance checks should be present (melange_tuples is a view)
	perfChecks := filterCategory(report, "Performance")
	assert.NotEmpty(t, perfChecks, "should have performance checks")

	// view_parsed should pass
	assertCheck(t, perfChecks, "view_parsed", doctor.StatusPass)
	// union_all should pass (test view uses UNION ALL)
	assertCheck(t, perfChecks, "union_all", doctor.StatusPass)
	// source_tables should pass
	assertCheck(t, perfChecks, "source_tables", doctor.StatusPass)
}

// TestDoctor_MissingExpressionIndex drops expression indexes and verifies warnings,
// then recreates them and verifies warnings clear.
func TestDoctor_MissingExpressionIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Template DB has all expression indexes — simplify view to one branch
	// and drop its indexes to test the warning.
	_, err := db.ExecContext(ctx, `DROP VIEW melange_tuples`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT
			'user'::text AS subject_type,
			user_id::text AS subject_id,
			role AS relation,
			'organization'::text AS object_type,
			organization_id::text AS object_id
		FROM organization_members
	`)
	require.NoError(t, err)

	// Drop the expression indexes on organization_members
	_, err = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_org_members_obj_text`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_org_members_subj_text`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	hasWarning := false
	for _, c := range perfChecks {
		if c.Name == "expr_indexes" && c.Status != doctor.StatusPass {
			hasWarning = true
			assert.Contains(t, c.FixHint, "CREATE INDEX", "should provide CREATE INDEX fix hint")
			assert.Contains(t, c.FixHint, "::text", "fix hint should include ::text cast")
		}
	}
	assert.True(t, hasWarning, "should warn about missing expression index")

	// Now recreate expression indexes and verify warnings clear
	_, err = db.ExecContext(ctx, `CREATE INDEX idx_om_user_text ON organization_members ((user_id::text))`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE INDEX idx_om_org_text ON organization_members ((organization_id::text))`)
	require.NoError(t, err)

	d = doctor.New(db, "testutil/testdata")
	report, err = d.Run(ctx)
	require.NoError(t, err)

	perfChecks = filterCategory(report, "Performance")
	assertCheck(t, perfChecks, "expr_indexes", doctor.StatusPass)
}

// TestDoctor_UnionNotAll creates a view with bare UNION and verifies the warning.
func TestDoctor_UnionNotAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Drop and recreate with bare UNION to avoid column type mismatch
	_, err := db.ExecContext(ctx, `DROP VIEW melange_tuples`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT
			'user'::text AS subject_type,
			user_id::text AS subject_id,
			role::text AS relation,
			'organization'::text AS object_type,
			organization_id::text AS object_id
		FROM organization_members
		UNION
		SELECT
			'user'::text AS subject_type,
			user_id::text AS subject_id,
			role::text AS relation,
			'team'::text AS object_type,
			team_id::text AS object_id
		FROM team_members
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assertCheck(t, perfChecks, "union_all", doctor.StatusWarn)
}

// TestDoctor_SkipPerformance verifies that Options.SkipPerformance omits performance checks.
func TestDoctor_SkipPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	d := doctor.New(db, "testutil/testdata", doctor.Options{SkipPerformance: true})
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.Empty(t, perfChecks, "should have no performance checks when skipped")
}

// TestDoctor_NoView verifies performance checks are skipped when melange_tuples doesn't exist.
func TestDoctor_NoView(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	d := doctor.New(db, "testutil/testdata")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.Empty(t, perfChecks, "should have no performance checks without melange_tuples")
}

// TestDoctor_TableNotView verifies performance checks are skipped when melange_tuples is a table.
func TestDoctor_TableNotView(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Create melange_tuples as a table (not a view)
	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text,
			subject_id text,
			relation text,
			object_type text,
			object_id text
		)
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.Empty(t, perfChecks, "should have no performance checks when melange_tuples is a table")
}

// filterCategory returns all checks in a given category.
func filterCategory(report *doctor.Report, category string) []doctor.CheckResult {
	var result []doctor.CheckResult
	for _, c := range report.Checks {
		if c.Category == category {
			result = append(result, c)
		}
	}
	return result
}

// assertCheck asserts that a check with the given name exists with the expected status.
func assertCheck(t *testing.T, checks []doctor.CheckResult, name string, expectedStatus doctor.Status) {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			assert.Equal(t, expectedStatus, c.Status, "check %q: expected status %v, got %v: %s", name, expectedStatus, c.Status, c.Message)
			return
		}
	}
	t.Errorf("check %q not found in results", name)
}
