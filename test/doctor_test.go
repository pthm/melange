package test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
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
			// A relabel cast (text-like role) is served by a plain index; the
			// bigint id columns need an expression index.
			if strings.HasPrefix(c.Message, "Missing expression index") {
				assert.Contains(t, c.FixHint, "::text", "expression index hint should include ::text cast")
			} else {
				assert.NotContains(t, c.FixHint, "::text", "plain index hint for a relabel cast")
			}
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
// The exact count comes from sqlgen.RecommendIndexes() driven by the test
// schema; today that schema has wildcard grants so three recommendations are
// expected (object-keyed, subject-keyed, wildcard partial).
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

	// Every recommendation should surface as a missing-index warning.
	warnings := 0
	for _, c := range perfChecks {
		if c.Name == "table_indexes" && c.Status != doctor.StatusPass {
			warnings++
			assert.Contains(t, c.FixHint, "CREATE INDEX", "should provide CREATE INDEX fix hint")
			assert.Contains(t, c.FixHint, "melange_tuples", "fix hint should reference melange_tuples")
		}
	}
	assert.GreaterOrEqual(t, warnings, 2,
		"should warn about at least the two universal recommendations (object-keyed + subject-keyed)")
}

// TestDoctor_TableWithIndexes verifies that a melange_tuples table carrying
// every index sqlgen.RecommendIndexes() produces for the test schema passes
// the performance check. The DDLs are pulled directly from the recommender so
// the test stays in sync if the recommendations evolve.
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

	for _, ddl := range testSchemaRecommendedDDLs(t) {
		_, err := db.ExecContext(ctx, ddl)
		require.NoError(t, err, "creating recommended index: %s", ddl)
	}

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assertCheck(t, perfChecks, "table_indexes", doctor.StatusPass)
}

// TestDoctor_TablePartialIndexCoverage verifies that having only the
// object-keyed full index produces warnings for the remaining recommendations
// (subject-keyed plus, on a wildcard-using schema, the wildcard partial).
// The covered one passes silently.
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

	// Only create the object-keyed index; omit the subject-keyed one.
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_obj_keyed
		ON melange_tuples (object_type, object_id, relation, subject_type, subject_id)
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assert.NotEmpty(t, perfChecks, "should have performance checks")

	// At least one warning should reference the subject-keyed columns (since
	// that's the recommendation the existing index doesn't cover).
	sawSubjectKeyed := false
	for _, c := range perfChecks {
		if c.Name == "table_indexes" && c.Status != doctor.StatusPass {
			if strings.Contains(c.FixHint, "(subject_type, subject_id, relation, object_type, object_id)") {
				sawSubjectKeyed = true
			}
		}
	}
	assert.True(t, sawSubjectKeyed,
		"should warn about the missing subject-keyed index when only the object-keyed one exists")
}

// TestDoctor_TableBroaderIndexSatisfiesRecommendation verifies that an index
// with extra trailing columns still satisfies a narrower recommendation. The
// existing indexes carry the recommended columns as their leading prefix
// plus an extra trailing column; PG can use them for the same access patterns
// as the bare recommendation.
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

	// Build broader-than-recommended indexes by appending a trailing column
	// to each recommendation. The partial wildcard recommendation needs its
	// WHERE clause preserved.
	for _, ddl := range testSchemaRecommendedDDLs(t) {
		broader := addTrailingColumn(ddl, "extra_col")
		_, err := db.ExecContext(ctx, broader)
		require.NoError(t, err, "creating broader index: %s", broader)
	}

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	assertCheck(t, perfChecks, "table_indexes", doctor.StatusPass)
}

// testSchemaRecommendedDDLs returns the index DDL that the recommender emits
// for testutil/testdata/schema.fga. Tests use these to build matching
// indexes without hard-coding column lists that drift from the recommender.
func testSchemaRecommendedDDLs(t *testing.T) []string {
	t.Helper()
	types, err := parser.ParseSchema("testutil/testdata/schema.fga")
	require.NoError(t, err)
	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inline := compiler.BuildInlineSQLData(closureRows, analyses)
	gen, err := compiler.GenerateSQL(analyses, inline, "")
	require.NoError(t, err)

	ddls := make([]string, 0, len(gen.IndexRecommendations))
	for _, rec := range gen.IndexRecommendations {
		ddls = append(ddls, rec.DDL)
	}
	return ddls
}

// addTrailingColumn rewrites a `CREATE INDEX ... (cols)[ WHERE pred]` DDL to
// add `extra` as the last column inside the column list, preserving any
// WHERE clause. Used to build broader-than-recommended indexes in tests.
func addTrailingColumn(ddl, extra string) string {
	// Split off any WHERE clause so its parens don't confuse the column-list
	// surgery (matches splitIndexKeysAndPredicate's contract).
	keys, pred := ddl, ""
	for _, sep := range []string{") WHERE ", ") where "} {
		if i := strings.Index(ddl, sep); i != -1 {
			keys = ddl[:i+1]
			pred = ddl[i+len(sep):]
			break
		}
	}
	closeIdx := strings.LastIndex(keys, ")")
	if closeIdx == -1 {
		return ddl
	}
	out := keys[:closeIdx] + ", " + extra + ")"
	if pred != "" {
		out += " WHERE " + pred
	}
	return out
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

// findCheck returns the first check matching the given name, or nil.
func findCheck(checks []doctor.CheckResult, name string) *doctor.CheckResult {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

// TestDoctor_DeployedModelChecks verifies the "Deployed Model" checks: the model
// is recorded, and a pending local change is classified additive vs breaking.
func TestDoctor_DeployedModelChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()

	const docBase = `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define viewer: [user] or owner
`
	const docAdditive = `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define editor: [user]
    define viewer: [user] or owner
`
	const docBreaking = `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
`

	m := migrator.NewMigrator(db, "")
	migrateSchema(t, ctx, m, docBase, migrator.InternalMigrateOptions{Version: "v0.9.0", SchemaFormat: "single"})

	run := func(content string) []doctor.CheckResult {
		path := filepath.Join(t.TempDir(), "schema.fga")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		d := doctor.New(db, path, doctor.Options{SkipPerformance: true})
		report, err := d.Run(ctx)
		require.NoError(t, err)
		return filterCategory(report, "Deployed Model")
	}

	// In sync: model recorded, no drift advisory.
	inSync := run(docBase)
	assertCheck(t, inSync, "recorded", doctor.StatusPass)
	assert.Nil(t, findCheck(inSync, "drift_safety"), "no drift advisory when in sync")

	// Additive local change: drift advisory passes.
	additive := run(docAdditive)
	assertCheck(t, additive, "recorded", doctor.StatusPass)
	assertCheck(t, additive, "drift_safety", doctor.StatusPass)

	// Breaking local change: drift advisory warns and names the change.
	breaking := run(docBreaking)
	assertCheck(t, breaking, "drift_safety", doctor.StatusWarn)
	if c := findCheck(breaking, "drift_safety"); assert.NotNil(t, c) {
		assert.Contains(t, c.Details, "viewer", "breaking detail should name the removed relation")
	}
}

// TestDoctor_DeployedModelCorruptDegradesToWarning verifies that a corrupt
// stored model_json produces a warning rather than aborting the whole run —
// doctor must still diagnose the broken deployment.
func TestDoctor_DeployedModelCorruptDegradesToWarning(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	// A release build, which the phase 1 skip applies to.
	stampVersion(t, "v0.9.0")
	db := testutil.EmptyDB(t)
	ctx := context.Background()

	const doc = `model
  schema 1.1
type user
type document
  relations
    define viewer: [user]
`
	m := migrator.NewMigrator(db, "")
	migrateSchema(t, ctx, m, doc, migrator.InternalMigrateOptions{Version: "v0.9.0", SchemaFormat: "single"})

	// Corrupt the stored model: valid JSON (the column is jsonb) but the wrong
	// shape for UnmarshalModel, which expects an array of type definitions.
	_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET model_json = '{"corrupt": true}'`)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "schema.fga")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))
	d := doctor.New(db, path, doctor.Options{SkipPerformance: true})
	report, err := d.Run(ctx)
	require.NoError(t, err, "doctor must not abort on a corrupt deployed model")

	dm := filterCategory(report, "Deployed Model")
	c := findCheck(dm, "recorded")
	require.NotNil(t, c)
	assert.Equal(t, doctor.StatusWarn, c.Status)
	assert.Contains(t, c.Message, "could not be decoded")

	// Subsequent checks still ran — the run was not aborted.
	assert.NotEmpty(t, filterCategory(report, "Generated Functions"), "later checks should still run")

	// The phase 1 skip does not read model_json: only --force rewrites it,
	// as the hint says.
	assert.Contains(t, c.FixHint, "melange migrate --force")
	stale := func() string {
		d := doctor.New(db, path, doctor.Options{SkipPerformance: true})
		report, err := d.Run(ctx)
		require.NoError(t, err)
		return requireHint(t, report, "Deployed Model", "recorded").Message
	}
	opts := migrator.InternalMigrateOptions{Version: "v0.9.0", SchemaFormat: "single"}
	migrateSchema(t, ctx, m, doc, opts)
	assert.Contains(t, stale(), "could not be decoded", "a plain migrate skips")
	opts.Force = true
	migrateSchema(t, ctx, m, doc, opts)
	assert.NotContains(t, stale(), "could not be decoded", "--force rewrites the model")
}

// TestDoctor_DeployedModelDriftFromDSLFallback verifies the drift advisory still
// works when a record has schema_dsl but no usable model_json (an older/partial
// record): the deployed model is reconstructed by parsing the stored DSL.
func TestDoctor_DeployedModelDriftFromDSLFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()

	const withViewer = `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define viewer: [user] or owner
`
	const withoutViewer = `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
`
	m := migrator.NewMigrator(db, "")
	migrateSchema(t, ctx, m, withViewer, migrator.InternalMigrateOptions{Version: "v0.9.0", SchemaFormat: "single"})

	// Blank out the JSON model, leaving schema_dsl — forces the DSL fallback.
	_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET model_json = 'null'`)
	require.NoError(t, err)

	// Local schema removes viewer: a breaking change that must still be caught.
	path := filepath.Join(t.TempDir(), "schema.fga")
	require.NoError(t, os.WriteFile(path, []byte(withoutViewer), 0o644))
	d := doctor.New(db, path, doctor.Options{SkipPerformance: true})
	report, err := d.Run(ctx)
	require.NoError(t, err)

	dm := filterCategory(report, "Deployed Model")
	assertCheck(t, dm, "recorded", doctor.StatusPass)
	assertCheck(t, dm, "drift_safety", doctor.StatusWarn)
}

// TestDoctor_ModularSchemaInSyncAfterMigrate verifies that doctor hashes a
// modular schema the way migrate does (manifest plus modules), so it reports
// the schema in sync right after migrating it.
func TestDoctor_ModularSchemaInSyncAfterMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	manifest := filepath.Join(dir, "fga.mod")
	require.NoError(t, os.WriteFile(manifest, []byte("schema: '1.2'\ncontents:\n  - core.fga\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "core.fga"), []byte(`module core

type user

type doc
  relations
    define owner: [user]
`), 0o644))

	require.NoError(t, migrator.Migrate(ctx, db, manifest))

	report, err := doctor.New(db, manifest, doctor.Options{SkipPerformance: true}).Run(ctx)
	require.NoError(t, err)
	assertCheck(t, filterCategory(report, "Migration State"), "schema_sync", doctor.StatusPass)
}

// TestDoctor_CodegenVersionIgnoresVPrefix verifies that a recorded codegen
// version differing only by its "v" prefix (release binaries omit it, go
// install builds include it) is not reported as a version change.
func TestDoctor_CodegenVersionIgnoresVPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "schema.fga")
	require.NoError(t, os.WriteFile(path, []byte("model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define owner: [user]\n"), 0o644))
	require.NoError(t, migrator.Migrate(ctx, db, path))

	_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version =
		CASE WHEN codegen_version LIKE 'v%' THEN substr(codegen_version, 2) ELSE 'v' || codegen_version END`)
	require.NoError(t, err)

	report, err := doctor.New(db, path, doctor.Options{SkipPerformance: true}).Run(ctx)
	require.NoError(t, err)
	assertCheck(t, filterCategory(report, "Migration State"), "schema_sync", doctor.StatusPass)
}

// TestDoctor_NoSchemaPathSkipsSchemaFile verifies that doctor without a local
// schema (studio run with none configured) skips the schema file checks
// rather than failing them.
func TestDoctor_NoSchemaPathSkipsSchemaFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.DB(t)
	report, err := doctor.New(db, "", doctor.Options{SkipPerformance: true}).Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, filterCategory(report, "Schema File"))
	assert.Zero(t, report.Errors)
}

// TestDoctor_ExpressionIndexesFollowCastType verifies that the expression
// index check accounts for the column type: casting varchar to text is a
// relabel, so a plain index serves col::text = $1 and no expression index is
// needed, while char(n) and uuid casts convert the value and still need one.
func TestDoctor_ExpressionIndexesFollowCastType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		CREATE TABLE r (v varchar NOT NULL, w varchar NOT NULL, k char(27) NOT NULL, u uuid NOT NULL);
		CREATE INDEX r_v ON r (v); CREATE INDEX r_k ON r (k); CREATE INDEX r_u ON r (u);
		CREATE TABLE s (v varchar NOT NULL, name text NOT NULL);
		CREATE INDEX s_name ON s (name);
		CREATE VIEW melange_tuples AS
		  SELECT 'user'::text AS subject_type, k::text AS subject_id, 'owner'::text AS relation, 'doc'::text AS object_type, v::text AS object_id FROM r
		  UNION ALL
		  SELECT 'user'::text, w::text, 'owner'::text, 'doc'::text, u::text FROM r
		  UNION ALL
		  -- A join: name is s's, k is r's. Neither should be reported on the
		  -- other table, and s.name (text) is served by its plain index.
		  SELECT 'team'::text, r.k::text, 'member'::text, 'doc'::text, s.name::text FROM r JOIN s ON s.v = r.v`)
	require.NoError(t, err)

	report, err := doctor.New(db, "").Run(ctx)
	require.NoError(t, err)
	msgs := map[string]doctor.CheckResult{}
	for _, c := range filterCategory(report, "Performance") {
		if c.Name == "expr_indexes" && c.Status != doctor.StatusPass {
			msg, _, _ := strings.Cut(c.Message, " (") // drop the size note
			msgs[msg] = c
		}
	}
	assert.Len(t, msgs, 3, "k, u and w warn; v and s.name are served by plain indexes, and joined columns aren't charged to the other table: %v", msgs)
	for _, col := range []string{"k", "u"} {
		c, ok := msgs["Missing expression index on r."+col+"::text"]
		if assert.True(t, ok, col) {
			assert.Contains(t, c.FixHint, "(("+col+"::text))")
		}
	}
	if c, ok := msgs["Missing index on r.w"]; assert.True(t, ok, "varchar without any index") {
		assert.Contains(t, c.FixHint, "ON r (w);", "a plain index, not an expression index")
	}
}

// TestDoctor_SubjectLookupIndexes verifies that a branch's subject column
// needs an index leading with it, for the subject_id = ANY(ARRAY(...))
// lookups of list_accessible_objects, including in joined branches, and that
// cast columns with no index at all are left to the expr_indexes check.
func TestDoctor_SubjectLookupIndexes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		CREATE TABLE members (org_id text NOT NULL, user_id text NOT NULL);
		CREATE INDEX members_org_user ON members (org_id, user_id);
		CREATE TABLE grants (doc_id text NOT NULL, grantee uuid NOT NULL);
		CREATE INDEX grants_doc_grantee ON grants (doc_id, (grantee::text));
		CREATE TABLE docs (id text PRIMARY KEY, folder_id text NOT NULL, code text NOT NULL);
		CREATE INDEX docs_code ON docs (code);
		CREATE TABLE folders (id text PRIMARY KEY, code text NOT NULL);
		CREATE TABLE owners (doc_id text NOT NULL, owner uuid NOT NULL);
		CREATE SCHEMA other;
		CREATE TABLE other.shares (doc_id text NOT NULL, grantee uuid NOT NULL);
		CREATE INDEX shares_grantee_text ON other.shares ((grantee::text));
		CREATE VIEW melange_tuples AS
		  -- user_id is indexed only behind org_id: reported.
		  SELECT 'user'::text AS subject_type, user_id AS subject_id, 'member'::text AS relation, 'org'::text AS object_type, org_id AS object_id FROM members
		  UNION ALL
		  -- grantee::text is indexed only behind doc_id: expr_indexes passes it, this check reports it.
		  SELECT 'user'::text, grantee::text, 'viewer'::text, 'doc'::text, doc_id FROM grants
		  UNION ALL
		  -- A join where both tables have code: the alias names folders, whose code has no index: reported.
		  SELECT 'folder'::text, f.code, 'parent'::text, 'doc'::text, d.id FROM docs d JOIN folders f ON f.id = d.folder_id
		  UNION ALL
		  -- The same join without aliases, subject docs.code by table name: indexed.
		  SELECT 'doc'::text, docs.code, 'self'::text, 'doc'::text, folders.id FROM docs JOIN folders ON folders.id = docs.folder_id
		  UNION ALL
		  -- owner has no index at all: left to expr_indexes.
		  SELECT 'user'::text, owner::text, 'owner'::text, 'doc'::text, doc_id FROM owners
		  UNION ALL
		  -- A table in another schema, indexed there: neither check reports it.
		  SELECT 'user'::text, grantee::text, 'editor'::text, 'doc'::text, doc_id FROM other.shares`)
	require.NoError(t, err)

	report, err := doctor.New(db, "").Run(ctx)
	require.NoError(t, err)
	reported := map[string]doctor.CheckResult{}
	for _, c := range filterCategory(report, "Performance") {
		if c.Name == "expr_indexes" && c.Status != doctor.StatusPass {
			assert.NotContains(t, c.Message, "other.shares", "the index is found in the table's own schema")
		}
		if c.Name == "subject_lookup_indexes" && c.Status != doctor.StatusPass {
			msg, _, _ := strings.Cut(c.Message, " for subject lookups")
			reported[msg] = c
		}
	}
	assert.Len(t, reported, 3, "members.user_id, grants.grantee and folders.code: %v", reported)
	if c, ok := reported["Missing index on folders (code)"]; assert.True(t, ok, "the alias resolves the joined owner") {
		assert.Contains(t, c.FixHint, "ON folders (code);")
	}
	if c, ok := reported["Missing index on members (user_id)"]; assert.True(t, ok) {
		assert.Contains(t, c.FixHint, "ON members (user_id);")
	}
	if c, ok := reported["Missing index on grants ((grantee::text))"]; assert.True(t, ok) {
		assert.Contains(t, c.FixHint, "ON grants ((grantee::text));")
	}

	_, err = db.ExecContext(ctx, `
		CREATE INDEX members_user ON members (user_id);
		CREATE INDEX grants_grantee_text ON grants ((grantee::text));
		CREATE INDEX folders_code ON folders (code);`)
	require.NoError(t, err)
	report, err = doctor.New(db, "").Run(ctx)
	require.NoError(t, err)
	assertCheck(t, filterCategory(report, "Performance"), "subject_lookup_indexes", doctor.StatusPass)
}

// fixHintDB is a database migrated with concurrencySchema by a release
// build, with check_document_viewer dropped by hand, and the schema file.
func fixHintDB(t *testing.T) (db *sql.DB, path string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	stampVersion(t, "v0.10.0")
	db = newTuplesDB(t)
	path = writeSchema(t, concurrencySchema)
	_, err := migrator.MigrateWithOptions(context.Background(), db, path, migrator.MigrateOptions{Version: "v0.10.0"})
	require.NoError(t, err)
	dropGeneratedFunction(t, db, "check_document_viewer")
	return db, path
}

// doctorReport runs doctor against path with the local exposure.
func doctorReport(t *testing.T, db *sql.DB, path string, exposure *migrator.Exposure) *doctor.Report {
	t.Helper()
	report, err := doctor.New(db, path, doctor.Options{SkipPerformance: true, Exposure: exposure}).Run(context.Background())
	require.NoError(t, err)
	return report
}

// requireHint returns the named check of category, which must exist.
func requireHint(t *testing.T, report *doctor.Report, category, name string) *doctor.CheckResult {
	t.Helper()
	c := findCheck(filterCategory(report, category), name)
	require.NotNil(t, c, "%s/%s not reported", category, name)
	return c
}

// plainMigrate runs an unforced migration of path.
func plainMigrate(t *testing.T, db *sql.DB, path string, force bool) (bool, error) {
	t.Helper()
	return migrator.MigrateWithOptions(context.Background(), db, path, migrator.MigrateOptions{Version: "v0.10.0", Force: force})
}

// Doctor's fix hints say what would actually fix the database: each subtest
// checks the hint, then that a plain migrate does (or does not) fix it.
func TestDoctor_FixHintsFollowMigrate(t *testing.T) {
	ctx := context.Background()
	const dropped = "check_document_viewer"

	t.Run("dropped function with nothing pending needs --force", func(t *testing.T) {
		db, path := fixHintDB(t)
		c := requireHint(t, doctorReport(t, db, path, nil), "Generated Functions", "missing")
		assert.Contains(t, c.FixHint, "melange migrate --force")
		assert.Contains(t, c.FixHint, "skips")

		skipped, err := plainMigrate(t, db, path, false)
		require.NoError(t, err)
		assert.True(t, skipped, "the premise: a plain migrate skips")
		assert.False(t, functionExists(t, ctx, db, dropped), "the premise: a plain migrate leaves it missing")

		_, err = plainMigrate(t, db, path, true)
		require.NoError(t, err)
		assertCheck(t, filterCategory(doctorReport(t, db, path, nil), "Generated Functions"), "complete", doctor.StatusPass)
	})

	t.Run("dropped dispatcher needs --force", func(t *testing.T) {
		db, path := fixHintDB(t)
		_, err := plainMigrate(t, db, path, true)
		require.NoError(t, err)
		dropGeneratedFunction(t, db, "check_permission")
		c := requireHint(t, doctorReport(t, db, path, nil), "Generated Functions", "dispatchers")
		assert.Equal(t, doctor.StatusFail, c.Status)
		assert.Contains(t, c.FixHint, "melange migrate --force")

		skipped, err := plainMigrate(t, db, path, false)
		require.NoError(t, err)
		assert.True(t, skipped)
		assert.False(t, functionExists(t, ctx, db, "check_permission"))
		_, err = plainMigrate(t, db, path, true)
		require.NoError(t, err)
		assertCheck(t, filterCategory(doctorReport(t, db, path, nil), "Generated Functions"), "dispatchers", doctor.StatusPass)
	})

	t.Run("comment-only edit records only", func(t *testing.T) {
		db, _ := fixHintDB(t)
		path := writeSchema(t, concurrencySchema+"\n# a comment changes the checksum, not the SQL\n")
		report := doctorReport(t, db, path, nil)
		sync := requireHint(t, report, "Migration State", "schema_sync")
		assert.Equal(t, doctor.StatusWarn, sync.Status)
		assert.Equal(t, "Run 'melange migrate' to apply changes", sync.FixHint)
		c := requireHint(t, report, "Generated Functions", "missing")
		assert.Contains(t, c.FixHint, "melange migrate --force")
		assert.Contains(t, c.FixHint, "only records")

		skipped, err := plainMigrate(t, db, path, false)
		require.NoError(t, err)
		assert.False(t, skipped)
		assert.False(t, functionExists(t, ctx, db, dropped), "the premise: recording leaves it missing")
		assertCheck(t, filterCategory(doctorReport(t, db, path, nil), "Migration State"), "schema_sync", doctor.StatusPass)
	})

	t.Run("schema change applies", func(t *testing.T) {
		db, _ := fixHintDB(t)
		path := writeSchema(t, concurrencySchema+"    define auditor: [user]\n")
		c := requireHint(t, doctorReport(t, db, path, nil), "Generated Functions", "missing")
		assert.Contains(t, c.FixHint, "Run 'melange migrate' to create functions")
		assert.Contains(t, c.FixHint, "pending changes")
		assert.NotContains(t, c.FixHint, "--force")

		_, err := plainMigrate(t, db, path, false)
		require.NoError(t, err)
		report := doctorReport(t, db, path, nil)
		assertCheck(t, filterCategory(report, "Generated Functions"), "complete", doctor.StatusPass)
		assertCheck(t, filterCategory(report, "Migration State"), "schema_sync", doctor.StatusPass)
	})

	t.Run("database ahead", func(t *testing.T) {
		db, path := fixHintDB(t)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.11.0'`)
		require.NoError(t, err)
		local := &migrator.Exposure{Relations: compiler.RelationPatterns{Include: []string{"document#editor"}}}
		report := doctorReport(t, db, path, local)
		sync := requireHint(t, report, "Migration State", "schema_sync")
		assert.Contains(t, sync.Message, "newer melange (v0.11.0)")
		for _, name := range []string{"exposure_sync", "schema_sync"} {
			assert.Contains(t, requireHint(t, report, "Migration State", name).FixHint, "Use melange v0.11.0 or later")
		}
		missing := requireHint(t, report, "Generated Functions", "missing")
		assert.Contains(t, missing.FixHint, "Use melange v0.11.0 or later")
		assert.Contains(t, missing.Details, "melange v0.11.0 may generate others")
		for _, c := range report.Checks {
			assert.NotContains(t, c.FixHint, "--force", "%s/%s", c.Category, c.Name)
		}

		_, err = plainMigrate(t, db, path, false)
		var ahead *migrator.DatabaseAheadError
		require.ErrorAs(t, err, &ahead, "the premise: this melange's migrate refuses")
	})

	t.Run("older codegen with the same SQL records only", func(t *testing.T) {
		db, path := fixHintDB(t)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.9.0'`)
		require.NoError(t, err)
		report := doctorReport(t, db, path, nil)
		sync := requireHint(t, report, "Migration State", "schema_sync")
		assert.Contains(t, sync.Message, "the generated SQL is unchanged")
		assert.Equal(t, "Run 'melange migrate' to record this codegen version", sync.FixHint)
		assert.Contains(t, requireHint(t, report, "Generated Functions", "missing").FixHint, "melange migrate --force")

		skipped, err := plainMigrate(t, db, path, false)
		require.NoError(t, err)
		assert.False(t, skipped)
		assert.False(t, functionExists(t, ctx, db, dropped), "the premise: recording leaves it missing")
		assertCheck(t, filterCategory(doctorReport(t, db, path, nil), "Migration State"), "schema_sync", doctor.StatusPass)
	})

	t.Run("older codegen with other SQL applies", func(t *testing.T) {
		db, path := fixHintDB(t)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.9.0',
			function_checksums = jsonb_set(function_checksums, '{check_document_owner}', '"generated by v0.9.0"')`)
		require.NoError(t, err)
		report := doctorReport(t, db, path, nil)
		assert.Equal(t, "Codegen version has changed", requireHint(t, report, "Migration State", "schema_sync").Message)
		c := requireHint(t, report, "Generated Functions", "missing")
		assert.Contains(t, c.FixHint, "Run 'melange migrate' to create functions")
		assert.NotContains(t, c.FixHint, "--force")

		_, err = plainMigrate(t, db, path, false)
		require.NoError(t, err)
		assert.True(t, functionExists(t, ctx, db, dropped), "applying re-creates it")
	})
}

// Orphans that other objects depend on are named with their dependents,
// since a plain migrate refuses to drop them.
func TestDoctor_OrphanHintNamesDependents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	stampVersion(t, "v0.10.0")
	ctx := context.Background()
	db := newTuplesDB(t)
	_, err := plainMigrate(t, db, writeSchema(t, concurrencySchema), false)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`)
	require.NoError(t, err)
	path := writeSchema(t, planSchemaWithoutViewer)
	dropGeneratedFunction(t, db, "check_document_owner")

	report := doctorReport(t, db, path, nil)
	c := requireHint(t, report, "Generated Functions", "orphans")
	assert.Contains(t, c.Details, "check_document_viewer: view alice_viewer")
	assert.Contains(t, c.FixHint, "refuses to drop them")
	// The migrate that would re-create the dropped function is refused too.
	assert.Contains(t, requireHint(t, report, "Generated Functions", "missing").FixHint, "first change or drop view alice_viewer")
	assert.Contains(t, requireHint(t, report, "Migration State", "schema_sync").FixHint, "first change or drop view alice_viewer")

	_, err = plainMigrate(t, db, path, false)
	var depErr *migrator.DependentObjectsError
	require.ErrorAs(t, err, &depErr, "the premise: a plain migrate refuses")
}

// After a melange upgrade the codegen hint goes through the plan too: the
// applying migrate drops a recorded function an application view depends
// on, so it is refused, and the hint names the view.
func TestDoctor_CodegenChangeHintNamesDependents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	stampVersion(t, "v0.10.0")
	ctx := context.Background()
	db := newTuplesDB(t)
	path := writeSchema(t, concurrencySchema)
	_, err := plainMigrate(t, db, path, false)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE FUNCTION check_document_retired(text, text, text) RETURNS integer LANGUAGE sql AS 'SELECT 0'`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE VIEW retired_access AS SELECT check_document_retired('user', 'alice', '1') AS allowed`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.9.0', function_names = function_names || 'check_document_retired'::text`)
	require.NoError(t, err)

	c := requireHint(t, doctorReport(t, db, path, nil), "Migration State", "schema_sync")
	assert.Equal(t, "Codegen version has changed", c.Message)
	assert.Contains(t, c.FixHint, "Run 'melange migrate' to regenerate functions")
	assert.Contains(t, c.FixHint, "first change or drop view retired_access")

	_, err = plainMigrate(t, db, path, false)
	var depErr *migrator.DependentObjectsError
	require.ErrorAs(t, err, &depErr, "the premise: a plain migrate refuses")
}

// The Generated Client category warns when the client names what the
// database does not install, fails with Strict, and is absent without a
// client.
func TestDoctor_ClientSkew(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := newTuplesDB(t)
	path := writeSchema(t, clientCheckSchema)
	noParentKey := migrator.Exposure{Functions: compiler.FunctionFamilies{ParentKey: new(bool)}}
	_, err := migrateExposure(t, db, path, &noParentKey, false)
	require.NoError(t, err)

	run := func(client *doctor.ClientCheck) []doctor.CheckResult {
		t.Helper()
		local := migrator.Exposure{}
		report, err := doctor.New(db, path, doctor.Options{SkipPerformance: true, Exposure: &local, Client: client}).Run(context.Background())
		require.NoError(t, err)
		return filterCategory(report, "Generated Client")
	}
	client := func(strict bool) *doctor.ClientCheck {
		return &doctor.ClientCheck{Runtime: "go", FilesSkipped: "not configured here", Names: clientNames(t, migrator.Exposure{}), Strict: strict}
	}

	checks := run(client(false))
	require.Len(t, checks, 2, "no client_file check without the files")
	c := findCheck(checks, "client_functions")
	require.NotNil(t, c)
	assert.Equal(t, doctor.StatusWarn, c.Status)
	assert.Contains(t, c.Details, "accessible_document_can_view_by_parent")
	assert.Contains(t, c.FixHint, "Run 'melange migrate' to apply")
	assertCheck(t, checks, "client_relations", doctor.StatusPass)

	assertCheck(t, run(client(true)), "client_functions", doctor.StatusFail)
	assert.Empty(t, run(nil))
}
