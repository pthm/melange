package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/test/testutil"
)

// functionsDBSchema is the database schema melange functions is checked
// in, so names and example calls are qualified.
const functionsDBSchema = "fn_api"

// functionsDB returns an empty database with a melange_tuples table in
// functionsDBSchema, and its DSN.
func functionsDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := testutil.EmptyDBWithDSN(t)
	s := pq.QuoteIdentifier(functionsDBSchema)
	_, err := db.ExecContext(context.Background(), `CREATE SCHEMA `+s+`;
		CREATE TABLE `+s+`.melange_tuples (
			subject_type TEXT NOT NULL, subject_id TEXT NOT NULL, relation TEXT NOT NULL,
			object_type TEXT NOT NULL, object_id TEXT NOT NULL)`)
	require.NoError(t, err)
	return db, dsn
}

// functionsProject writes a project directory with the schema at path and
// a melange.yaml holding sqlSection, and migrates it into db with the CLI.
func functionsProject(t *testing.T, cli melangeCLI, dsn, path, sqlSection string) string {
	t.Helper()
	dir := t.TempDir()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.fga"), content, 0o644))
	writeConfigFile(t, dir, "schema: schema.fga\n"+sqlSection)
	cli.runIn(t, dir, "migrate", "--db", dsn, "--db-schema", functionsDBSchema)
	return dir
}

// functionsReport runs melange functions with args and --format json.
func functionsReport(t *testing.T, cli melangeCLI, dir string, args ...string) *sqlapi.Report {
	t.Helper()
	out := cli.runIn(t, dir, append(append([]string{"functions"}, args...), "--format", "json")...)
	var r sqlapi.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r), out)
	return &r
}

// reportNames returns the names the report expects, once each.
func reportNames(r *sqlapi.Report) []string {
	var names []string
	for _, rel := range r.Relations {
		for _, f := range rel.Functions {
			names = append(names, f.Name)
		}
	}
	for _, d := range r.Dispatchers {
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// installedFunctions returns the distinct generated function names in
// functionsDBSchema.
func installedFunctions(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var names []string
	require.NoError(t, queryStrings(db, `SELECT DISTINCT p.proname::text FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = '`+functionsDBSchema+`' AND p.proname ~ '^(check|list|expand|explain|accessible|accessiblesk)_' ORDER BY 1`, &names))
	return names
}

// After melange migrate, melange functions --db reports every function it
// installed and nothing missing, under the default and a restricted
// exposure in a non-default schema; file mode names the same functions; and
// every example call runs, which checks signatures and argument order.
func TestFunctions_ReportsWhatMigrateInstalled(t *testing.T) {
	cli := buildMelangeCLI(t)
	exposures := []struct{ label, config string }{
		{"default", ""},
		{"can_*, explain and list_subjects off", "sql:\n  relations:\n    include: [\"can_*\"]\n  functions:\n    explain: false\n    list_subjects: false\n"},
	}
	for _, path := range []string{"testutil/testdata/schema.fga", "testutil/testdata/kitchen_sink_schema.fga"} {
		for _, e := range exposures {
			t.Run(filepath.Base(path)+"/"+e.label, func(t *testing.T) {
				db, dsn := functionsDB(t)
				dir := functionsProject(t, cli, dsn, path, e.config)

				r := functionsReport(t, cli, dir, "--db", dsn, "--db-schema", functionsDBSchema)
				require.Equal(t, "database", r.Source)
				require.True(t, r.Checked && r.BodiesChecked)
				require.False(t, r.CodegenSkew, "migrated by the same melange")
				require.Falsef(t, r.Summary.Drift(), "a fresh migration has no drift: %+v", r.Summary)
				require.Equal(t, r.Summary.Functions, r.Summary.Installed)
				require.Empty(t, r.Unmanaged)
				require.Equal(t, e.label == "default", r.Exposure.IsDefault())

				// Exactly what is installed, and what file mode names.
				require.Equal(t, installedFunctions(t, db), reportNames(r))
				file := functionsReport(t, cli, dir, "--db-schema", functionsDBSchema)
				require.Equal(t, "file", file.Source)
				require.Equal(t, reportNames(r), reportNames(file))

				runExamples(t, db, r)

				// The text output says so too.
				out := cli.runIn(t, dir, "functions", "--db", dsn, "--db-schema", functionsDBSchema)
				assert.Contains(t, out, "Status:    all ")
				assert.Contains(t, out, " installed\n")
			})
		}
	}
}

// runExamples runs every example call in the report as written, on a
// session whose search_path does not hold the database schema. The examples
// whose note asks for the schema on the search_path (inlined accessible
// functions can read melange_tuples unqualified) run once the path is set;
// some fail without it, which is why the note is there.
func runExamples(t *testing.T, db *sql.DB, r *sqlapi.Report) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	query := func(f sqlapi.Function) error {
		rows, err := conn.QueryContext(ctx, f.Example)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		return rows.Close()
	}
	var fns, needPath []sqlapi.Function
	for _, rel := range r.Relations {
		fns = append(fns, rel.Functions...)
	}
	require.NotEmpty(t, fns)
	for _, f := range append(fns, r.Dispatchers...) {
		if f.Example == "" {
			continue
		}
		require.Truef(t, strings.Contains(f.Example, pq.QuoteIdentifier(functionsDBSchema)+"."+f.Name+"("), "%s: example %s is not qualified", f.Name, f.Example)
		if strings.Contains(f.Note, "search_path") {
			needPath = append(needPath, f)
			continue
		}
		require.NoError(t, query(f), f.Example)
	}
	failed := 0
	for _, f := range needPath {
		if query(f) != nil {
			failed++
		}
	}
	require.NotZero(t, failed, "every example whose note asks for the search_path runs without it")
	_, err = conn.ExecContext(ctx, `SET search_path TO `+pq.QuoteIdentifier(functionsDBSchema))
	require.NoError(t, err)
	for _, f := range needPath {
		require.NoError(t, query(f), f.Example)
	}
}

// Drift is reported as what it is: a dropped function, a changed body, a
// stale overload, an orphan the record lists, and an application's own
// function with a generated prefix, which is not an orphan for melange
// functions, doctor or studio.
func TestFunctions_ReportsDrift(t *testing.T) {
	cli := buildMelangeCLI(t)
	db, dsn := functionsDB(t)
	dir := functionsProject(t, cli, dsn, "testutil/testdata/schema.fga", "")
	ctx := context.Background()
	s := pq.QuoteIdentifier(functionsDBSchema)
	for _, stmt := range []string{
		`DROP FUNCTION ` + s + `.check_issue_assignee(text, text, text, text[])`,
		`CREATE OR REPLACE FUNCTION ` + s + `.check_issue_author(p_subject_type text, p_subject_id text, p_object_id text, p_visited text[] DEFAULT ARRAY[]::text[])
			RETURNS integer LANGUAGE plpgsql AS $$BEGIN RETURN 0; END$$`,
		`CREATE FUNCTION ` + s + `.accessible_issue_author(p_subject_type text, p_subject_id text) RETURNS TABLE(object_id text) LANGUAGE sql AS 'SELECT NULL::text WHERE false'`,
		`CREATE FUNCTION ` + s + `.check_invoice(p_id text) RETURNS integer LANGUAGE sql AS 'SELECT 1'`,
		`CREATE FUNCTION ` + s + `.accessiblesk_old(p_subject_type text) RETURNS integer LANGUAGE sql AS 'SELECT 1'`,
		`UPDATE ` + s + `.melange_migrations SET function_names = array_append(function_names, 'accessiblesk_old')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	r := functionsReport(t, cli, dir, "--db", dsn, "--db-schema", functionsDBSchema)
	status := func(name string) sqlapi.Function {
		for _, rel := range r.Relations {
			for _, f := range rel.Functions {
				if f.Name == name {
					return f
				}
			}
		}
		t.Fatalf("%s is not in the report", name)
		return sqlapi.Function{}
	}
	assert.Equal(t, sqlapi.StatusMissing, status("check_issue_assignee").Status)
	assert.Equal(t, sqlapi.StatusBodyChanged, status("check_issue_author").Status)
	author := status("accessible_issue_author")
	assert.Equal(t, sqlapi.StatusInstalled, author.Status)
	assert.Equal(t, []string{"p_subject_type text, p_subject_id text"}, author.StaleOverloads)
	assert.Equal(t, []sqlapi.InstalledFunction{{Name: "accessiblesk_old", Arguments: "p_subject_type text"}}, r.Orphans)
	assert.Equal(t, []sqlapi.InstalledFunction{{Name: "check_invoice", Arguments: "p_id text"}}, r.Unmanaged)
	assert.Equal(t, sqlapi.Summary{
		Relations: r.Summary.Relations, Exposed: r.Summary.Exposed, Functions: r.Summary.Functions,
		Installed: r.Summary.Functions - 2, Missing: 1, BodyChanged: 1, StaleOverloads: 1, Orphans: 1, Unmanaged: 1,
	}, r.Summary)

	out := cli.runIn(t, dir, "functions", "--db", dsn, "--db-schema", functionsDBSchema, "--type", "issue")
	for _, want := range []string{"1 missing, 1 with a changed body, 1 stale overloads, 1 orphans", "check_issue_assignee", "body changed", "+1 stale overload", "accessiblesk_old(p_subject_type text)", "check_invoice(p_id text)"} {
		assert.Contains(t, out, want)
	}
	show := cli.runIn(t, dir, "functions", "show", "issue#author", "--db", dsn, "--db-schema", functionsDBSchema)
	assert.Contains(t, show, "overloads:  p_subject_type text, p_subject_id text")
	assert.Contains(t, show, "the installed body is not the one melange migrate generated")

	// Doctor reports the recorded orphan and not the application's function.
	d := doctor.New(db, filepath.Join(dir, "schema.fga"))
	d.SetDatabaseSchema(functionsDBSchema)
	report, err := d.Run(ctx)
	require.NoError(t, err)
	var orphanDetails string
	for _, c := range report.Checks {
		if c.Category == "Generated Functions" && c.Name == "orphans" {
			orphanDetails = c.Details
		}
	}
	assert.Contains(t, orphanDetails, "accessiblesk_old")
	assert.NotContains(t, orphanDetails, "check_invoice")
	// The record now names a function migrate no longer generates, so a
	// plain migrate applies, re-creating the dropped function: doctor says
	// so rather than --force. The CLI that migrated may be a newer build
	// than this test binary, whose migrate then refuses.
	want := "Run 'melange migrate' to create functions"
	if recorded := lastRecordIn(t, db, functionsDBSchema).CodegenVersion; migrator.CodegenVersionAhead(recorded) {
		want = "Use melange " + recorded + " or later"
	}
	for _, c := range report.Checks {
		if c.Category == "Generated Functions" && c.Name == "missing" {
			assert.Contains(t, c.Details, "check_issue_assignee")
			assert.Contains(t, c.FixHint, want)
			assert.NotContains(t, c.FixHint, "--force")
		}
	}

	// Studio shows the same.
	c := newStudio(t, openStudioDB(t, dsn), functionsDBSchema)
	page := html.UnescapeString(c.get("/sql"))
	for _, want := range []string{"check_issue_assignee", "body changed", "accessiblesk_old(p_subject_type text)", "check_invoice(p_id text)", "Stale overloads: "} {
		assert.Contains(t, page, want)
	}
	issue := html.UnescapeString(c.get("/sql?type=issue&kind=accessible"))
	assert.Contains(t, issue, "accessible_issue_author")
	assert.Contains(t, issue, "stale overloads")
	assert.NotContains(t, issue, "check_issue_author", "the kind filter keeps only accessible functions")

	// Another codegen version recorded the functions: said up front.
	_, err = db.ExecContext(ctx, `UPDATE `+s+`.melange_migrations SET codegen_version = 'v0.0.1'`)
	require.NoError(t, err)
	r = functionsReport(t, cli, dir, "--db", dsn, "--db-schema", functionsDBSchema)
	assert.True(t, r.CodegenSkew)
	assert.Equal(t, "v0.0.1", r.DeployedCodegenVersion)
	assert.True(t, slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "generated by melange v0.0.1") }), r.Notes)

	// A recorded model this melange cannot read is reported as such, not as
	// a catalog of dispatchers with every recorded function an orphan.
	_, err = db.ExecContext(ctx, `UPDATE `+s+`.melange_migrations SET model_json = '{}', schema_dsl = 'not a schema'`)
	require.NoError(t, err)
	page = html.UnescapeString(c.get("/sql"))
	assert.Contains(t, page, "recorded a model this melange cannot read")
	assert.Contains(t, page, "Re-run `melange migrate` with this melange", "an older codegen version's record is replaced by a plain migrate")
	assert.NotContains(t, page, "check_issue_assignee")
	assert.NotContains(t, html.UnescapeString(c.get("/sql?type=issue")), "not a type in the deployed model")

	// Without a record, the local schema is compared under the local
	// exposure, and nothing can be called an orphan.
	_, err = db.ExecContext(ctx, `DROP TABLE `+s+`.melange_migrations`)
	require.NoError(t, err)
	_, stderr, err := cli.exec(t.TempDir(), "functions", "--db", dsn, "--db-schema", functionsDBSchema)
	require.Error(t, err)
	assert.Contains(t, stderr, "no melange migration found")
	assert.Contains(t, stderr, "pass --schema")
	r = functionsReport(t, cli, dir, "--db", dsn, "--db-schema", functionsDBSchema)
	assert.Equal(t, "schema.fga", r.SchemaFile)
	assert.Empty(t, r.Orphans)
	assert.Equal(t, 1, r.Summary.Missing)
	assert.True(t, slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "No migration is recorded") }), r.Notes)

	// Doctor agrees: without a record, migrate drops nothing, so nothing is
	// an orphan.
	d = doctor.New(db, filepath.Join(dir, "schema.fga"))
	d.SetDatabaseSchema(functionsDBSchema)
	report, err = d.Run(ctx)
	require.NoError(t, err)
	for _, c := range report.Checks {
		assert.False(t, c.Category == "Generated Functions" && c.Name == "orphans", "orphans without a record: %s", c.Details)
	}
}

// lastRecordIn returns the last migration record in databaseSchema.
func lastRecordIn(t *testing.T, db *sql.DB, databaseSchema string) *migrator.MigrationRecord {
	t.Helper()
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)
	rec, err := m.GetLastMigration(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rec)
	return rec
}

// A database built from melange generate migration files has no record, so
// melange functions compares the local schema under the local exposure,
// which the files were generated with: nothing is missing or changed.
func TestFunctions_GeneratedMigrationWithoutRecord(t *testing.T) {
	cli := buildMelangeCLI(t)
	db, dsn := functionsDB(t)
	dir := t.TempDir()
	content, err := os.ReadFile("testutil/testdata/schema.fga")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.fga"), content, 0o644))
	writeConfigFile(t, dir, "schema: schema.fga\nsql:\n  relations:\n    include: [\"can_*\"]\n  functions:\n    explain: false\n")
	up := cli.runIn(t, dir, "generate", "migration", "--up", "--db-schema", functionsDBSchema)
	_, err = db.ExecContext(context.Background(), up)
	require.NoError(t, err)

	r := functionsReport(t, cli, dir, "--db", dsn, "--db-schema", functionsDBSchema)
	assert.Equal(t, []string{"can_*"}, r.Exposure.Relations.Include)
	assert.True(t, r.BodiesChecked)
	assert.Equal(t, r.Summary.Functions, r.Summary.Installed, "every function the files installed is expected: %+v", r.Summary)
	assert.False(t, r.Summary.Drift(), "%+v", r.Summary)
	assert.Empty(t, r.Unmanaged, "check_permission_unexposed is expected under the local exposure")
	assert.Equal(t, reportNames(r), installedFunctions(t, db))
	assert.True(t, slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "under the local exposure") }), r.Notes)
}
