package test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// exposureSchema: exposing document#editor reaches editor and owner only, so
// viewer's and auditor's functions are not installed.
const exposureSchema = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
    define auditor: [user]
`

// editorOnly is the restricted exposure the tests narrow to.
func editorOnly() *migrator.Exposure {
	return &migrator.Exposure{Relations: compiler.RelationPatterns{Include: []string{"document#editor"}}}
}

// deExposed are functions the default exposure installs and editorOnly does not.
var deExposed = map[string]string{
	"check_document_viewer":      "viewer is outside the exposed closure",
	"list_document_viewer_obj":   "viewer is outside the exposed closure",
	"accessible_document_viewer": "viewer is outside the exposed closure",
	"check_document_auditor":     "auditor is outside the exposed closure",
}

// restrictedInstalls are functions editorOnly installs.
var restrictedInstalls = map[string]string{
	"check_document_editor":        "editor is exposed",
	"check_document_owner":         "owner is in editor's closure",
	compiler.UnexposedFunctionName: "a restricted exposure installs the raising helper",
	"check_permission":             "dispatchers are always installed",
}

// exposureDB is an empty database with melange_tuples, and the schema file.
func exposureDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	return newTuplesDB(t), writeSchema(t, exposureSchema)
}

// migrateExposure runs MigrateWithOptions with the given exposure.
func migrateExposure(t *testing.T, db *sql.DB, path string, exposure *migrator.Exposure, force bool) (bool, error) {
	t.Helper()
	return migrator.MigrateWithOptions(context.Background(), db, path, migrator.MigrateOptions{
		Version: "v0.10.0", Exposure: exposure, Force: force,
	})
}

// lastRecord returns the last migration record.
func lastRecord(t *testing.T, db *sql.DB) *migrator.MigrationRecord {
	t.Helper()
	rec, err := migrator.NewMigrator(db, "").GetLastMigration(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rec)
	return rec
}

// requireNotExposed asserts that the public check dispatcher raises M2003 for
// relation and answers for exposed.
func requireNotExposed(t *testing.T, db *sql.DB, relation, exposed string) {
	t.Helper()
	ctx := context.Background()
	var allowed int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT check_permission('user', 'alice', $1, 'document', '1')`, exposed).Scan(&allowed))
	err := db.QueryRowContext(ctx,
		`SELECT check_permission('user', 'alice', $1, 'document', '1')`, relation).Scan(&allowed)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, compiler.NotExposedErrCode, pgErr.Code)
}

// An exposure-only change re-applies (phase 1 must not skip it, for released
// and dev builds alike), drops the de-exposed functions and records the new
// exposure; the same exposure again skips.
func TestMigrateExposure_ExposureOnlyChangeReapplies(t *testing.T) {
	for _, v := range []string{"v0.10.0", "dev"} {
		t.Run(v, func(t *testing.T) {
			stampVersion(t, v)
			db, path := exposureDB(t)
			ctx := context.Background()

			_, err := migrateExposure(t, db, path, nil, false)
			require.NoError(t, err)
			assertFunctions(t, ctx, db, deExposed)
			records := migrationRecordCount(t, ctx, db)

			skipped, err := migrateExposure(t, db, path, editorOnly(), false)
			require.NoError(t, err)
			assert.False(t, skipped, "an exposure-only change must be applied")
			assertFunctionsDropped(t, ctx, db, deExposed)
			assertFunctions(t, ctx, db, restrictedInstalls)
			requireNotExposed(t, db, "viewer", "editor")
			rec := lastRecord(t, db)
			assert.True(t, migrator.SameExposure(*editorOnly(), rec.Exposure), "the new exposure is recorded")
			assert.Equal(t, records+1, migrationRecordCount(t, ctx, db))
			assert.NotContains(t, rec.FunctionNames, "check_document_viewer")

			skipped, err = migrateExposure(t, db, path, editorOnly(), false)
			require.NoError(t, err)
			assert.True(t, skipped, "the same exposure again is a no-op")
			assert.Equal(t, records+1, migrationRecordCount(t, ctx, db))

			// An equivalent spelling of the same exposure is the same skip key.
			same := editorOnly()
			same.Relations.Include = append(same.Relations.Include, "document#editor")
			skipped, err = migrateExposure(t, db, path, same, false)
			require.NoError(t, err)
			assert.True(t, skipped, "an equivalent exposure is a no-op")
		})
	}
}

// A caller that passes no exposure (Migrate, MigrateFromString, MigrateWithTypes,
// MigrateWithOptions without one) keeps the recorded restricted exposure
// instead of widening it, so app-startup migrations and `melange migrate` do
// not flap. An explicit default exposure widens it again.
func TestMigrateExposure_NilKeepsRecordedExplicitDefaultWidens(t *testing.T) {
	stampVersion(t, "v0.10.0")
	db, path := exposureDB(t)
	ctx := context.Background()

	_, err := migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)
	records := migrationRecordCount(t, ctx, db)

	skipped, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)
	assert.True(t, skipped, "nil keeps the recorded exposure: nothing to do")

	require.NoError(t, migrator.Migrate(ctx, db, path))
	require.NoError(t, migrator.MigrateFromString(ctx, db, exposureSchema))
	types, err := parser.ParseSchemaString(exposureSchema)
	require.NoError(t, err)
	require.NoError(t, migrator.NewMigrator(db, "").MigrateWithTypes(ctx, types))

	assertFunctionsDropped(t, ctx, db, deExposed)
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure), "the recorded exposure is unchanged")

	// Force re-applies, still under the recorded exposure.
	_, err = migrateExposure(t, db, path, nil, true)
	require.NoError(t, err)
	assertFunctionsDropped(t, ctx, db, deExposed)
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure))
	records = migrationRecordCount(t, ctx, db)

	// SetExposure applies to MigrateWithTypes, which records nothing.
	m := migrator.NewMigrator(db, "")
	m.SetExposure(&migrator.Exposure{})
	require.NoError(t, m.MigrateWithTypes(ctx, types))
	assertFunctions(t, ctx, db, deExposed)
	assert.Equal(t, records, migrationRecordCount(t, ctx, db))

	// An explicit default replaces the recorded exposure and drops the helper.
	skipped, err = migrateExposure(t, db, path, &migrator.Exposure{}, false)
	require.NoError(t, err)
	assert.False(t, skipped)
	assertFunctions(t, ctx, db, deExposed)
	assert.False(t, functionExists(t, ctx, db, compiler.UnexposedFunctionName), "the default exposure has no raising helper")
	assert.True(t, lastRecord(t, db).Exposure.IsDefault())
	var allowed int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT check_permission('user', 'alice', 'viewer', 'document', '1')`).Scan(&allowed))
}

// A run that keeps the recorded exposure and waits for the lock while another
// migration records a different exposure starts over under the new one,
// rather than reverting it.
func TestMigrateExposure_KeptExposureChangedUnderLock(t *testing.T) {
	stampVersion(t, "v0.10.0")
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)

	changed := writeSchema(t, exposureSchema+"    define reader: [user] or editor\n")
	restricted := `'{"relations":{"include":["document#editor"]},"functions":{}}'`
	skipped, err := migrateBehindLock(t, db, changed, migrator.MigrateOptions{Version: "v0.10.0"},
		copyLastRecord(map[string]string{"exposure": restricted}))
	require.NoError(t, err)
	assert.False(t, skipped)
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure),
		"the exposure recorded while waiting is kept")
	assertFunctionsDropped(t, ctx, db, deExposed)
	assert.False(t, functionExists(t, ctx, db, "check_document_reader"), "reader is outside the exposed closure")
}

// Dropping orphaned functions refuses when user objects depend on them,
// listing the dependents and applying nothing, unless forced.
func TestMigrateExposure_OrphanDropRefusesDependents(t *testing.T) {
	stampVersion(t, "v0.10.0")
	for _, tc := range []struct {
		name, ddl, dependent string
	}{
		{
			name:      "view",
			ddl:       `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`,
			dependent: "view alice_viewer",
		},
		{
			// WITH NO DATA: populating it runs under a restricted
			// search_path, where the body's melange_tuples does not resolve.
			name:      "materialized view",
			ddl:       `CREATE MATERIALIZED VIEW alice_viewer_mv AS SELECT check_document_viewer('user', 'alice', '1') AS allowed WITH NO DATA`,
			dependent: "materialized view alice_viewer_mv",
		},
		{
			name: "rls policy",
			ddl: `CREATE TABLE docs (id TEXT PRIMARY KEY);
				ALTER TABLE docs ENABLE ROW LEVEL SECURITY;
				CREATE POLICY docs_read ON docs USING (id IN (SELECT object_id FROM accessible_document_viewer('user', current_user)))`,
			dependent: "policy docs_read on table docs",
		},
		{
			// A SQL-standard body records its callees in pg_depend; it is a
			// pg_proc dependent like the orphans themselves, which the
			// lookup leaves out.
			name: "sql-standard function",
			ddl: `CREATE FUNCTION alice_can_view() RETURNS INTEGER LANGUAGE sql
				BEGIN ATOMIC SELECT check_document_viewer('user', 'alice', '1'); END`,
			dependent: "function alice_can_view()",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := exposureDB(t)
			ctx := context.Background()
			_, err := migrateExposure(t, db, path, nil, false)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, tc.ddl)
			require.NoError(t, err)
			records := migrationRecordCount(t, ctx, db)

			_, err = migrateExposure(t, db, path, editorOnly(), false)
			var depErr *migrator.DependentObjectsError
			require.ErrorAs(t, err, &depErr)
			assert.Contains(t, err.Error(), tc.dependent)
			assertFunctions(t, ctx, db, deExposed)
			assert.False(t, functionExists(t, ctx, db, compiler.UnexposedFunctionName), "a refused migration applies nothing")
			assert.Equal(t, records, migrationRecordCount(t, ctx, db), "a refused migration records nothing")
			assert.True(t, dependentExists(t, db, tc.name))

			// The dry-run preview shows the drop it would refuse.
			var buf bytes.Buffer
			_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{DryRun: &buf, Exposure: editorOnly()})
			require.NoError(t, err)
			assert.Contains(t, buf.String(), "--   "+tc.dependent)
			_, err = db.ExecContext(ctx, buf.String())
			require.Error(t, err, "applying the preview refuses the drop too")
			assert.Contains(t, err.Error(), tc.dependent)
			assert.True(t, dependentExists(t, db, tc.name))

			// The forced preview names the dependents CASCADE would drop.
			buf.Reset()
			_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{DryRun: &buf, Exposure: editorOnly(), Force: true})
			require.NoError(t, err)
			assert.Contains(t, buf.String(), "-- WARNING: CASCADE also drops these dependents:")
			assert.Contains(t, buf.String(), "--   "+tc.dependent)
			assert.True(t, dependentExists(t, db, tc.name), "a dry run changes nothing")

			_, err = migrateExposure(t, db, path, editorOnly(), true)
			require.NoError(t, err, "force drops the dependents with the functions")
			assertFunctionsDropped(t, ctx, db, deExposed)
			assert.False(t, dependentExists(t, db, tc.name), "CASCADE removed the dependent")
		})
	}
}

// dependentExists reports whether the dependent created for case name exists.
func dependentExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	query := map[string]string{
		"view":                  `SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'alice_viewer')`,
		"materialized view":     `SELECT EXISTS (SELECT 1 FROM pg_matviews WHERE matviewname = 'alice_viewer_mv')`,
		"rls policy":            `SELECT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'docs_read')`,
		"sql-standard function": `SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'alice_can_view')`,
	}[name]
	require.NotEmpty(t, query, "unknown dependent %q", name)
	var exists bool
	require.NoError(t, db.QueryRowContext(context.Background(), query).Scan(&exists))
	return exists
}

// A dependent created after migrate's up-front check, before it drops the
// orphans, makes the drop fail instead of being dropped with CASCADE: an
// unforced drop is a plain DROP, which the database refuses.
func TestMigrateExposure_OrphanDropRefusesDependentsCreatedDuringMigrate(t *testing.T) {
	stampVersion(t, "v0.10.0")
	const createView = `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`

	// Another session commits the view just before the first drop. On an
	// Execer without BeginTx, migrate runs without a transaction of its own.
	t.Run("concurrent session", func(t *testing.T) {
		db, path := exposureDB(t)
		ctx := context.Background()
		_, err := migrateExposure(t, db, path, nil, false)
		require.NoError(t, err)
		records := migrationRecordCount(t, ctx, db)

		racing := &dependentBeforeDrop{Execer: db, create: func() error {
			_, err := db.ExecContext(ctx, createView)
			return err
		}}
		_, err = migrator.MigrateWithOptions(ctx, racing, path, migrator.MigrateOptions{Version: "v0.10.0", Exposure: editorOnly()})
		var depErr *migrator.DependentObjectsError
		require.ErrorAs(t, err, &depErr)
		require.Error(t, depErr.Err, "the drop refused, not the up-front check")
		assert.Contains(t, err.Error(), "view alice_viewer depends on check_document_viewer")
		assert.True(t, dependentExists(t, db, "view"), "the dependent survives")
		assert.True(t, functionExists(t, ctx, db, "check_document_viewer"), "its function survives")
		assert.Equal(t, records, migrationRecordCount(t, ctx, db), "a refused migration records nothing")
	})

	// An event trigger creates the view inside migrate's own transaction at
	// the first drop, which rolls the whole migration back. On a pool of one
	// connection, the dependents must be read after that rollback: a lookup
	// while the transaction holds the connection waits for it forever.
	t.Run("own transaction", func(t *testing.T) {
		db, path := exposureDB(t)
		db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := migrateExposure(t, db, path, nil, false)
		require.NoError(t, err)
		records := migrationRecordCount(t, ctx, db)
		_, err = db.ExecContext(ctx, `
			CREATE FUNCTION create_alice_viewer() RETURNS event_trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF to_regclass('alice_viewer') IS NULL THEN
					`+createView+`;
				END IF;
			END $$;
			CREATE EVENT TRIGGER create_alice_viewer ON ddl_command_start
				WHEN TAG IN ('DROP FUNCTION') EXECUTE FUNCTION create_alice_viewer()`)
		require.NoError(t, err)

		_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{Version: "v0.10.0", Exposure: editorOnly()})
		require.NoError(t, ctx.Err(), "the refusal must not wait on the connection its transaction holds")
		var depErr *migrator.DependentObjectsError
		require.ErrorAs(t, err, &depErr)
		require.Error(t, depErr.Err, "the drop refused, not the up-front check")
		assert.Contains(t, err.Error(), "2BP01")
		assertFunctions(t, ctx, db, deExposed)
		assert.False(t, functionExists(t, ctx, db, compiler.UnexposedFunctionName), "the migration rolled back")
		assert.Equal(t, records, migrationRecordCount(t, ctx, db), "a refused migration records nothing")
	})
}

// dependentBeforeDrop is an Execer that runs create once, before the first
// DROP FUNCTION it forwards.
type dependentBeforeDrop struct {
	migrator.Execer
	create  func() error
	created bool
}

func (e *dependentBeforeDrop) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if !e.created && strings.HasPrefix(query, "DROP FUNCTION") {
		e.created = true
		if err := e.create(); err != nil {
			return nil, err
		}
	}
	return e.Execer.ExecContext(ctx, query, args...)
}

// A database whose melange_migrations predates the exposure column reads as
// the default exposure, skips a rerun, and gains the column on the next
// migration that records one.
func TestMigrateExposure_LegacyTableWithoutColumn(t *testing.T) {
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `ALTER TABLE melange_migrations DROP COLUMN exposure`)
	require.NoError(t, err)

	rec := lastRecord(t, db)
	assert.True(t, rec.Exposure.IsDefault(), "a legacy record is the default exposure")
	history, err := migrator.NewMigrator(db, "").GetMigrationHistory(ctx, 5)
	require.NoError(t, err)
	require.NotEmpty(t, history)

	skipped, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)
	assert.True(t, skipped, "nothing changed against a legacy record")

	_, err = migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure))
	history, err = migrator.NewMigrator(db, "").GetMigrationHistory(ctx, 5)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.False(t, history[0].Exposure.IsDefault(), "history reads the new exposure")
	assert.True(t, history[1].Exposure.IsDefault(), "the legacy row reads as the default")

	model, err := migrator.NewMigrator(db, "").GetDeployedModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, model)
	assert.True(t, migrator.SameExposure(*editorOnly(), model.Exposure))
}

// Dry-run prints the orphan drops a narrowing migration makes without
// changing the database, and the script, applied separately, does what
// migrate does: drops them and records the exposure.
func TestMigrateExposure_DryRunShowsOrphanDrops(t *testing.T) {
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{DryRun: &buf, Exposure: editorOnly()})
	require.NoError(t, err)
	script := buf.String()
	for fn := range deExposed {
		assert.Contains(t, script, "DROP FUNCTION IF EXISTS "+fn+";")
	}
	assert.Contains(t, script, "-- Exposure: ")
	assertFunctions(t, ctx, db, deExposed)

	_, err = db.ExecContext(ctx, script)
	require.NoError(t, err, "the dry-run script applies cleanly")
	assertFunctionsDropped(t, ctx, db, deExposed)
	assertFunctions(t, ctx, db, restrictedInstalls)
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure))

	skipped, err := migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)
	assert.True(t, skipped, "migrate sees the script's record as the current state")
}

// The dry-run script checks for dependents when it is applied, not only when
// it was generated: a view created on an orphan after the preview makes the
// script refuse instead of dropping the view with the function.
func TestMigrateExposure_DryRunRefusesDependentsCreatedLater(t *testing.T) {
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{DryRun: &buf, Exposure: editorOnly()})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "WARNING: migrate refuses", "no dependents when the preview ran")

	_, err = db.ExecContext(ctx, `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`)
	require.NoError(t, err)
	records := migrationRecordCount(t, ctx, db)

	_, err = db.ExecContext(ctx, buf.String())
	require.Error(t, err, "the script refuses the drop")
	assert.Contains(t, err.Error(), "view alice_viewer depends on check_document_viewer")
	assert.True(t, dependentExists(t, db, "view"), "the dependent survives")
	assertFunctions(t, ctx, db, deExposed)
	assert.False(t, functionExists(t, ctx, db, compiler.UnexposedFunctionName), "a refused script applies nothing")
	assert.Equal(t, records, migrationRecordCount(t, ctx, db))

	// A client that carries on after an error (psql without ON_ERROR_STOP)
	// applies the new functions past the guard and the refused drop, but
	// records nothing, so the next migrate does not skip the unfinished one.
	carriedOn := dropStatements.ReplaceAllString(guardBlock.ReplaceAllString(buf.String(), ""), "-- refused: $0")
	_, err = db.ExecContext(ctx, carriedOn)
	require.NoError(t, err)
	assert.True(t, functionExists(t, ctx, db, compiler.UnexposedFunctionName), "the new functions were applied")
	assert.True(t, functionExists(t, ctx, db, "check_document_viewer"), "the refused orphan stays")
	assert.Equal(t, records, migrationRecordCount(t, ctx, db), "an unfinished script records nothing")

	// A forced preview drops the dependent, as forced migrate does.
	buf.Reset()
	_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{DryRun: &buf, Exposure: editorOnly(), Force: true})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, buf.String())
	require.NoError(t, err)
	assert.False(t, dependentExists(t, db, "view"))
	assertFunctionsDropped(t, ctx, db, deExposed)
}

var (
	guardBlock     = regexp.MustCompile(`(?s)DO \$melange_guard\$.*?\$melange_guard\$;`)
	dropStatements = regexp.MustCompile(`(?m)^DROP FUNCTION IF EXISTS .*;$`)
)

// `generate migration` output under a restricted exposure applies cleanly
// and installs exactly what migrate installs.
func TestMigrateExposure_StaticMigrationMatchesMigrate(t *testing.T) {
	staticDB, path := exposureDB(t)
	migrateDB := newTuplesDB(t)
	ctx := context.Background()

	types, err := parser.ParseSchemaString(exposureSchema)
	require.NoError(t, err)
	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, closureRows))
	inline := compiler.BuildInlineSQLData(closureRows, analyses)
	opts := compiler.GenerateSQLOptions{Exposure: *editorOnly()}
	gen, err := compiler.GenerateSQLWithOptions(analyses, inline, "", opts)
	require.NoError(t, err)
	list, err := compiler.GenerateListSQLWithOptions(analyses, inline, "", opts)
	require.NoError(t, err)
	names := compiler.CollectFunctionNamesWithOptions(analyses, opts)
	result := compiler.GenerateMigrationSQL(gen, list, names, compiler.MigrationOptions{
		Exposure:       *editorOnly(),
		NamedFunctions: compiler.CollectNamedFunctionsWithOptions(gen, list, analyses, opts),
	})
	assert.Contains(t, result.Up, "-- Exposure: ")
	applyMigrationUp(t, ctx, staticDB, result)

	_, err = migrateExposure(t, migrateDB, path, editorOnly(), false)
	require.NoError(t, err)

	assert.Equal(t, functionBodies(t, migrateDB), functionBodies(t, staticDB),
		"static migration and migrate install the same functions")
	assert.ElementsMatch(t, lastRecord(t, migrateDB).FunctionNames, getFunctionNames(t, ctx, staticDB))

	applyMigrationDown(t, ctx, staticDB, result)
	assert.Empty(t, getFunctionNames(t, ctx, staticDB), "DOWN removes everything UP installed")
}

// functionBodies maps each generated function to its source.
func functionBodies(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	ctx := context.Background()
	bodies := make(map[string]string)
	for _, name := range getFunctionNames(t, ctx, db) {
		var src string
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT string_agg(p.prosrc, E'\n' ORDER BY p.oid) FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = current_schema() AND p.proname = $1`, name).Scan(&src))
		bodies[name] = src
	}
	return bodies
}

// Doctor follows the recorded exposure: no false missing functions or
// orphans under a restricted one, and drift against a different local one.
func TestMigrateExposure_Doctor(t *testing.T) {
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)

	run := func(local *migrator.Exposure) *doctor.Report {
		report, err := doctor.New(db, path, doctor.Options{SkipPerformance: true, Exposure: local}).Run(ctx)
		require.NoError(t, err)
		return report
	}

	report := run(nil)
	fns := filterCategory(report, "Generated Functions")
	assertCheck(t, fns, "dispatchers", doctor.StatusPass)
	assertCheck(t, fns, "complete", doctor.StatusPass)
	assert.Nil(t, findCheck(fns, "missing"), "no false missing functions under a restricted exposure")
	assert.Nil(t, findCheck(fns, "orphans"), "no false orphans under a restricted exposure")
	assert.Nil(t, findCheck(filterCategory(report, "Migration State"), "exposure_sync"),
		"no local exposure: nothing to drift from")

	assertCheck(t, filterCategory(run(editorOnly()), "Migration State"), "exposure_sync", doctor.StatusPass)

	drift := filterCategory(run(&migrator.Exposure{}), "Migration State")
	assertCheck(t, drift, "exposure_sync", doctor.StatusWarn)
	assertCheck(t, drift, "schema_sync", doctor.StatusPass)

	typo := &migrator.Exposure{Relations: compiler.RelationPatterns{Include: []string{"document#editor", "document#vewer"}}}
	unmatched := filterCategory(run(typo), "Generated Functions")
	assertCheck(t, unmatched, "exposure_patterns", doctor.StatusWarn)
	if c := findCheck(unmatched, "exposure_patterns"); assert.NotNil(t, c) {
		assert.Contains(t, c.Details, "document#vewer")
		assert.NotContains(t, c.Details, "document#editor")
	}

	// After widening, the helper is gone and nothing is reported missing.
	_, err = migrateExposure(t, db, path, &migrator.Exposure{}, false)
	require.NoError(t, err)
	fns = filterCategory(run(nil), "Generated Functions")
	assertCheck(t, fns, "complete", doctor.StatusPass)
	assert.Nil(t, findCheck(fns, "orphans"))
}

// `melange status` reports the recorded exposure and, given no local one,
// stays in sync after an exposure-only change, as migrate keeps the recorded
// exposure when given none.
func TestMigrateExposure_Status(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	path := writeSchema(t, exposureSchema)
	cli := buildMelangeCLI(t)
	status := func() (report struct {
		Sync     string `json:"sync"`
		Deployed *struct {
			Exposure *migrator.Exposure `json:"exposure"`
		} `json:"deployed"`
	}) {
		t.Helper()
		out := cli.run(t, "status", "--db", dsn, "--schema", path, "--format", "json")
		require.NoError(t, json.Unmarshal([]byte(out), &report), out)
		require.NotNil(t, report.Deployed, out)
		return report
	}

	_, err := migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)
	report := status()
	assert.Equal(t, "in_sync", report.Sync)
	if assert.NotNil(t, report.Deployed.Exposure, "a restricted exposure is reported") {
		assert.True(t, migrator.SameExposure(*editorOnly(), *report.Deployed.Exposure))
	}

	_, err = migrateExposure(t, db, path, &migrator.Exposure{}, false)
	require.NoError(t, err)
	report = status()
	assert.Equal(t, "in_sync", report.Sync)
	assert.Nil(t, report.Deployed.Exposure, "the default exposure is omitted")
}

// `melange generate migration --db` generates under the exposure the database
// records and compares against it, so its UP keeps a restricted database
// restricted instead of widening it or emitting every function.
func TestMigrateExposure_GenerateMigrationFromDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	path := writeSchema(t, exposureSchema)
	ctx := context.Background()
	// Migrate into "public" explicitly, as `melange migrate` does, so the
	// functions compare equal to the CLI's.
	types, err := parser.ParseSchemaString(exposureSchema)
	require.NoError(t, err)
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema("public")
	require.NoError(t, m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		SchemaContent: exposureSchema, Exposure: editorOnly(),
	}))

	up := buildMelangeCLI(t).run(t, "generate", "migration", "--db", dsn, "--schema", path, "--up")
	assert.Contains(t, up, `-- Exposure: {"relations":{"include":["document#editor"]}`)
	assert.Contains(t, up, "-- Changed functions: 0 of ", "the recorded exposure is the previous one")
	assert.NotContains(t, up, "Previous exposure unknown")
	assert.NotContains(t, up, "FUNCTION check_document_viewer(", "viewer stays outside the closure")

	_, err = db.ExecContext(ctx, up)
	require.NoError(t, err)
	assertFunctionsDropped(t, ctx, db, deExposed)
	requireNotExposed(t, db, "viewer", "editor")
}

// melangeCLI is the melange binary built from the root module.
type melangeCLI string

// buildMelangeCLI builds the melange CLI into a temporary directory.
func buildMelangeCLI(t *testing.T) melangeCLI {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "melange")
	out, err := exec.Command("go", "build", "-C", root, "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, "building the CLI: %s", out)
	return melangeCLI(bin)
}

// run runs the CLI in an empty directory (no melange.yaml) and returns its
// stdout, failing the test on a non-zero exit.
func (c melangeCLI) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(string(c), append(args, "--no-update-check")...)
	cmd.Dir = t.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "melange %v: %s", args, stderr.String())
	return string(out)
}

// An exposure-only change that generates byte-identical SQL (here, excluding
// a relation the model does not define) takes the phase 2 path: nothing is
// re-applied, but the new exposure is recorded, so doctor, status and
// `generate migration --db` see it. The same exposure again records nothing.
func TestMigrateExposure_IdenticalSQLExposureChangeRecords(t *testing.T) {
	for _, v := range []string{"v0.10.0", "dev"} {
		t.Run(v, func(t *testing.T) {
			stampVersion(t, v)
			db, path := exposureDB(t)
			ctx := context.Background()
			_, err := migrateExposure(t, db, path, nil, false)
			require.NoError(t, err)
			before := functionBodies(t, db)
			records := migrationRecordCount(t, ctx, db)

			nothingLeftOut := &migrator.Exposure{Relations: compiler.RelationPatterns{Exclude: []string{"document#nonexistent"}}}
			skipped, err := migrateExposure(t, db, path, nothingLeftOut, false)
			require.NoError(t, err)
			assert.False(t, skipped, "an exposure-only change is recorded")
			assert.Equal(t, records+1, migrationRecordCount(t, ctx, db))
			assert.True(t, migrator.SameExposure(*nothingLeftOut, lastRecord(t, db).Exposure), "the new exposure is recorded")
			assert.Equal(t, before, functionBodies(t, db), "the functions are unchanged")

			skipped, err = migrateExposure(t, db, path, nothingLeftOut, false)
			require.NoError(t, err)
			assert.True(t, skipped, "the same exposure again is a no-op")
			assert.Equal(t, records+1, migrationRecordCount(t, ctx, db))
		})
	}
}

// An invalid exposure fails before anything is applied, also next to a
// pattern that matches everything (which normalizing drops it with).
func TestMigrateExposure_InvalidExposure(t *testing.T) {
	for _, include := range [][]string{{"a#b#c"}, {"*", "a#b#c"}} {
		db, path := exposureDB(t)
		_, err := migrateExposure(t, db, path, &migrator.Exposure{Relations: compiler.RelationPatterns{Include: include}}, false)
		require.Error(t, err, "include %q", include)
		assert.True(t, strings.Contains(err.Error(), "invalid exposure"), err.Error())
		assert.Empty(t, getFunctionNames(t, context.Background(), db), "nothing is applied")

		m := migrator.NewMigrator(db, "")
		m.SetExposure(&migrator.Exposure{Relations: compiler.RelationPatterns{Include: include}})
		types, err := parser.ParseSchemaString(exposureSchema)
		require.NoError(t, err)
		require.Error(t, m.MigrateWithTypes(context.Background(), types), "include %q", include)
	}
}
