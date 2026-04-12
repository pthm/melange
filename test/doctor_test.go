package test

import (
	"context"
	"database/sql"
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

	d := doctor.New(db, "testutil/testdata/schema.fga")
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

	// Restore view and indexes when done (important for shared remote DB in CI).
	t.Cleanup(func() {
		restoreView(t, db)
		restoreIndexes(t, db)
	})

	// Template DB has all expression indexes — simplify view to one branch
	// and drop its indexes to test the warning.
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
	`)
	require.NoError(t, err)

	// Drop the expression indexes on organization_members
	_, err = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_org_members_obj_text`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_org_members_subj_text`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
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
	_, err = db.ExecContext(ctx, `CREATE INDEX idx_om_role_text ON organization_members ((role::text))`)
	require.NoError(t, err)

	d = doctor.New(db, "testutil/testdata/schema.fga")
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

	// Restore view when done (important for shared remote DB in CI).
	t.Cleanup(func() { restoreView(t, db) })

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

	d := doctor.New(db, "testutil/testdata/schema.fga")
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

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.Empty(t, perfChecks, "should have no performance checks without melange_tuples")
}

// TestDoctor_TableNoIndexes verifies that a melange_tuples table without indexes
// produces warnings recommending the indexes needed for melange query patterns.
func TestDoctor_TableNoIndexes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Create melange_tuples as a table with no indexes.
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

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.NotEmpty(t, perfChecks, "should have performance checks for table")

	// Should warn about both missing indexes.
	warnings := 0
	for _, c := range perfChecks {
		if c.Name == "table_indexes" && c.Status != doctor.StatusPass {
			warnings++
			assert.Contains(t, c.FixHint, "CREATE INDEX", "should provide CREATE INDEX fix hint")
			assert.Contains(t, c.FixHint, "melange_tuples", "fix hint should reference melange_tuples")
		}
	}
	assert.Equal(t, 2, warnings, "should warn about both check_lookup and list_objects indexes")
}

// TestDoctor_TableWithIndexes verifies that a melange_tuples table with the
// recommended indexes passes the performance check.
func TestDoctor_TableWithIndexes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

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

	// Create both recommended indexes.
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_melange_tuples_check_lookup
		ON melange_tuples (object_type, object_id, relation, subject_type, subject_id)
	`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_melange_tuples_list_objects
		ON melange_tuples (object_type, relation, subject_type, subject_id, object_id)
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assertCheck(t, perfChecks, "table_indexes", doctor.StatusPass)
}

// TestDoctor_TablePartialIndexCoverage verifies that having only one of the two
// recommended indexes produces a warning for the missing one while the covered
// one passes silently.
func TestDoctor_TablePartialIndexCoverage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

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

	// Only create the check_lookup index, omit list_objects.
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_melange_tuples_check_lookup
		ON melange_tuples (object_type, object_id, relation, subject_type, subject_id)
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.NotEmpty(t, perfChecks, "should have performance checks")

	// Should warn about exactly one missing index (list_objects).
	warnings := 0
	for _, c := range perfChecks {
		if c.Name == "table_indexes" && c.Status != doctor.StatusPass {
			warnings++
			assert.Contains(t, c.Message, "list_objects", "warning should be about the missing list_objects index")
		}
	}
	assert.Equal(t, 1, warnings, "should warn about exactly one missing index")
}

// TestDoctor_TableBroaderIndexSatisfiesRecommendation verifies that an index
// with extra trailing columns still satisfies a narrower recommendation.
func TestDoctor_TableBroaderIndexSatisfiesRecommendation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text,
			subject_id text,
			relation text,
			object_type text,
			object_id text,
			extra_col text
		)
	`)
	require.NoError(t, err)

	// Create indexes with an extra trailing column beyond the recommendation.
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_broad_check
		ON melange_tuples (object_type, object_id, relation, subject_type, subject_id, extra_col)
	`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_broad_list
		ON melange_tuples (object_type, relation, subject_type, subject_id, object_id, extra_col)
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assertCheck(t, perfChecks, "table_indexes", doctor.StatusPass)
}

// TestDoctor_OrphanedTuples_UnknownObjectType verifies that tuples with an
// object_type not in the schema produce a warning with affected tuple count.
func TestDoctor_OrphanedTuples_UnknownObjectType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text, subject_id text, relation text,
			object_type text, object_id text
		)
	`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO melange_tuples VALUES
		('user', '1', 'owner', 'organization', 'org1'),
		('user', '2', 'viewer', 'widget', 'w1'),
		('user', '3', 'viewer', 'widget', 'w2')
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	checks := filterCategory(report, "Data Health")
	found := false
	for _, c := range checks {
		if c.Name == "unknown_object_types" {
			found = true
			assert.Equal(t, doctor.StatusWarn, c.Status)
			assert.Contains(t, c.Details, "widget")
			assert.Contains(t, c.Details, "2 tuples")
		}
	}
	assert.True(t, found, "should report unknown object types")
}

// TestDoctor_OrphanedTuples_UnknownRelation verifies that tuples with a relation
// not defined on their object type produce a warning.
func TestDoctor_OrphanedTuples_UnknownRelation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text, subject_id text, relation text,
			object_type text, object_id text
		)
	`)
	require.NoError(t, err)

	// "organization" is valid but "superadmin" is not a relation on it.
	_, err = db.ExecContext(ctx, `
		INSERT INTO melange_tuples VALUES
		('user', '1', 'owner', 'organization', 'org1'),
		('user', '2', 'superadmin', 'organization', 'org1')
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	checks := filterCategory(report, "Data Health")
	found := false
	for _, c := range checks {
		if c.Name == "unknown_relations" {
			found = true
			assert.Equal(t, doctor.StatusWarn, c.Status)
			assert.Contains(t, c.Details, "organization:superadmin")
		}
	}
	assert.True(t, found, "should report unknown relations")
}

// TestDoctor_OrphanedTuples_UnknownSubjectType verifies that tuples with a
// subject_type not defined in the schema produce a warning.
func TestDoctor_OrphanedTuples_UnknownSubjectType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text, subject_id text, relation text,
			object_type text, object_id text
		)
	`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO melange_tuples VALUES
		('user', '1', 'owner', 'organization', 'org1'),
		('device', 'd1', 'member', 'organization', 'org1')
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	checks := filterCategory(report, "Data Health")
	found := false
	for _, c := range checks {
		if c.Name == "unknown_subject_types" {
			found = true
			assert.Equal(t, doctor.StatusWarn, c.Status)
			assert.Contains(t, c.Details, "device")
		}
	}
	assert.True(t, found, "should report unknown subject types")
}

// TestDoctor_OrphanedTuples_InvalidSubjectType verifies that tuples with a
// subject_type not allowed by the relation definition produce a warning.
func TestDoctor_OrphanedTuples_InvalidSubjectType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text, subject_id text, relation text,
			object_type text, object_id text
		)
	`)
	require.NoError(t, err)

	// "organization:owner" only allows [user] as subject type per the test schema.
	_, err = db.ExecContext(ctx, `
		INSERT INTO melange_tuples VALUES
		('user', '1', 'owner', 'organization', 'org1'),
		('organization', 'org2', 'owner', 'organization', 'org1')
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	checks := filterCategory(report, "Data Health")
	found := false
	for _, c := range checks {
		if c.Name == "invalid_subject_types" {
			found = true
			assert.Equal(t, doctor.StatusWarn, c.Status)
			assert.Contains(t, c.Details, "organization:owner")
			assert.Contains(t, c.Details, "subject_type=organization")
		}
	}
	assert.True(t, found, "should report invalid subject type assignments")
}

// TestDoctor_OrphanedTuples_Clean verifies that the fully migrated test database
// with valid tuples passes all data health checks.
func TestDoctor_OrphanedTuples_Clean(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type text, subject_id text, relation text,
			object_type text, object_id text
		)
	`)
	require.NoError(t, err)

	// Insert only tuples that are valid per the test schema.
	_, err = db.ExecContext(ctx, `
		INSERT INTO melange_tuples VALUES
		('user', '1', 'owner', 'organization', 'org1'),
		('user', '2', 'member', 'organization', 'org1'),
		('organization', 'org1', 'org', 'repository', 'repo1')
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	checks := filterCategory(report, "Data Health")
	assertCheck(t, checks, "valid", doctor.StatusPass)
}

// restoreView drops and re-creates the melange_tuples view from the canonical SQL.
// This ensures doctor tests that modify the view don't break subsequent tests
// when running against a shared database (CI remote DB mode).
func restoreView(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DROP VIEW IF EXISTS melange_tuples`)
	_, err := db.ExecContext(ctx, testutil.TuplesViewSQL(""))
	if err != nil {
		t.Logf("warning: failed to restore melange_tuples view: %v", err)
	}
}

// TestDoctor_CustomSchema verifies that all doctor checks work when melange objects
// live in a custom schema instead of public.
func TestDoctor_CustomSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	databaseSchema := "melange"
	db := testutil.DBWithDatabaseSchema(t, databaseSchema)
	ctx := context.Background()

	d := doctor.New(db, "testutil/testdata/schema.fga")
	d.SetDatabaseSchema(databaseSchema)
	report, err := d.Run(ctx)
	require.NoError(t, err)

	// Schema file checks
	schemaFileChecks := filterCategory(report, "Schema File")
	assert.NotEmpty(t, schemaFileChecks, "should have schema file checks")
	assertCheck(t, schemaFileChecks, "exists", doctor.StatusPass)
	assertCheck(t, schemaFileChecks, "valid", doctor.StatusPass)

	// Generated functions should be found in custom schema
	funcChecks := filterCategory(report, "Generated Functions")
	assert.NotEmpty(t, funcChecks, "should have function checks")
	assertCheck(t, funcChecks, "dispatchers", doctor.StatusPass)
	assertCheck(t, funcChecks, "complete", doctor.StatusPass)

	// Tuples source should be found in custom schema
	tuplesChecks := filterCategory(report, "Tuples Source")
	assert.NotEmpty(t, tuplesChecks, "should have tuples source checks")
	assertCheck(t, tuplesChecks, "exists", doctor.StatusPass)
	assertCheck(t, tuplesChecks, "columns", doctor.StatusPass)

	// Performance checks should work with custom schema —
	// view_parsed proves pg_get_viewdef works with the schema-qualified regclass
	perfChecks := filterCategory(report, "Performance")
	assert.NotEmpty(t, perfChecks, "should have performance checks")
	assertCheck(t, perfChecks, "view_parsed", doctor.StatusPass)
	assertCheck(t, perfChecks, "union_all", doctor.StatusPass)
	assertCheck(t, perfChecks, "source_tables", doctor.StatusPass)
}

// restoreIndexes re-creates expression indexes that doctor tests may have dropped.
func restoreIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_org_members_obj_text ON organization_members ((organization_id::TEXT), (user_id::TEXT))`,
		`CREATE INDEX IF NOT EXISTS idx_org_members_subj_text ON organization_members ((user_id::TEXT), (organization_id::TEXT))`,
	}
	for _, ddl := range indexes {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Logf("warning: failed to restore index: %v", err)
		}
	}
	// Clean up any temporary indexes created by the test
	_, _ = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_om_user_text`)
	_, _ = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_om_org_text`)
	_, _ = db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_om_role_text`)
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
