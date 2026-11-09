package test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"net/http/httptest"
	"os"
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
	"github.com/pthm/melange/internal/studio"
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
func exposureDB(t *testing.T) (db *sql.DB, path string) {
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

// requireExposureChangeRefused asserts that err is an
// *UnrecordedExposureChangeError with the given HasRecord, and returns it.
func requireExposureChangeRefused(t *testing.T, err error, hasRecord bool) *migrator.UnrecordedExposureChangeError {
	t.Helper()
	var refused *migrator.UnrecordedExposureChangeError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, hasRecord, refused.HasRecord)
	return refused
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
	assert.Equal(t, records, migrationRecordCount(t, ctx, db), "a skipped migration records nothing")

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

	// MigrateWithTypes records nothing, so SetExposure may only restate the
	// recorded exposure: widening it is refused and applies nothing.
	m := migrator.NewMigrator(db, "")
	m.SetExposure(&migrator.Exposure{})
	requireExposureChangeRefused(t, m.MigrateWithTypes(ctx, types), true)
	assertFunctionsDropped(t, ctx, db, deExposed)
	assert.True(t, functionExists(t, ctx, db, compiler.UnexposedFunctionName), "the restricted exposure's helper stays")
	assert.Equal(t, records, migrationRecordCount(t, ctx, db))
	m.SetExposure(editorOnly())
	require.NoError(t, m.MigrateWithTypes(ctx, types))
	assertFunctionsDropped(t, ctx, db, deExposed)
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

// A migration that records nothing (MigrateWithTypes, MigrateWithTypesAndOptions
// without SchemaContent) cannot change the exposure: it could neither drop the
// previous exposure's functions nor record the new one. Any exposure other
// than the recorded one, or the default when none is recorded, is refused and
// applies nothing. Restating it, or passing none, works as before, and a
// dry-run records its migration, so it may preview a change.
func TestMigrateExposure_RecordlessRefusesExposureChange(t *testing.T) {
	stampVersion(t, "v0.10.0")
	ctx := context.Background()
	types, err := parser.ParseSchemaString(exposureSchema)
	require.NoError(t, err)
	trackingTableExists := func(t *testing.T, db *sql.DB) bool {
		t.Helper()
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT to_regclass('melange_migrations') IS NOT NULL`).Scan(&exists))
		return exists
	}

	t.Run("fresh database, SetExposure", func(t *testing.T) {
		db, _ := exposureDB(t)
		m := migrator.NewMigrator(db, "")
		m.SetExposure(editorOnly())
		requireExposureChangeRefused(t, m.MigrateWithTypes(ctx, types), false)
		assert.Empty(t, getFunctionNames(t, ctx, db), "a refusal applies nothing")
		assert.False(t, trackingTableExists(t, db), "a refusal creates no tracking table")
	})

	t.Run("fresh database, options without content", func(t *testing.T) {
		db, _ := exposureDB(t)
		for _, force := range []bool{false, true} {
			err := migrator.NewMigrator(db, "").MigrateWithTypesAndOptions(ctx, types,
				migrator.InternalMigrateOptions{Exposure: editorOnly(), Force: force})
			requireExposureChangeRefused(t, err, false)
		}
		assert.Empty(t, getFunctionNames(t, ctx, db))
		assert.False(t, trackingTableExists(t, db))
	})

	t.Run("recorded restriction, explicit default without content", func(t *testing.T) {
		db, path := exposureDB(t)
		_, err := migrateExposure(t, db, path, editorOnly(), false)
		require.NoError(t, err)
		records := migrationRecordCount(t, ctx, db)
		before := functionBodies(t, db)

		err = migrator.NewMigrator(db, "").MigrateWithTypesAndOptions(ctx, types,
			migrator.InternalMigrateOptions{Exposure: &migrator.Exposure{}})
		requireExposureChangeRefused(t, err, true)
		assert.Equal(t, before, functionBodies(t, db), "a refusal applies nothing")
		assert.Equal(t, records, migrationRecordCount(t, ctx, db))
		assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure))
	})

	t.Run("recorded restriction, restated or kept without content", func(t *testing.T) {
		db, path := exposureDB(t)
		_, err := migrateExposure(t, db, path, editorOnly(), false)
		require.NoError(t, err)
		records := migrationRecordCount(t, ctx, db)
		m := migrator.NewMigrator(db, "")
		require.NoError(t, m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{Exposure: editorOnly()}))
		require.NoError(t, m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{}))
		assertFunctionsDropped(t, ctx, db, deExposed)
		assertFunctions(t, ctx, db, restrictedInstalls)
		assert.Equal(t, records, migrationRecordCount(t, ctx, db))
	})

	t.Run("recorded explain, families off without content", func(t *testing.T) {
		db, path := exposureDB(t)
		_, err := migrateExposure(t, db, path, nil, false)
		require.NoError(t, err)
		require.True(t, functionExists(t, ctx, db, "explain_permission"))
		err = migrator.NewMigrator(db, "").MigrateWithTypesAndOptions(ctx, types,
			migrator.InternalMigrateOptions{Exposure: traceFamilies(false)})
		requireExposureChangeRefused(t, err, true)
		assert.True(t, functionExists(t, ctx, db, "explain_permission"), "a refusal drops nothing")
	})

	t.Run("dry-run without content may preview a change", func(t *testing.T) {
		db, path := exposureDB(t)
		_, err := migrateExposure(t, db, path, nil, false)
		require.NoError(t, err)
		var buf bytes.Buffer
		require.NoError(t, migrator.NewMigrator(db, "").MigrateWithTypesAndOptions(ctx, types,
			migrator.InternalMigrateOptions{DryRun: &buf, Exposure: editorOnly()}))
		assert.Contains(t, buf.String(), `"include":["document#editor"]`, "the script records the new exposure")
		assertFunctions(t, ctx, db, deExposed)
	})
}

// A migration that records nothing checks the exposure again under the lock:
// one it waited on may have recorded another exposure, which its own would
// then silently revert without a record.
func TestMigrateExposure_RecordlessRefusesExposureRecordedUnderLock(t *testing.T) {
	stampVersion(t, "v0.10.0")
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)
	types, err := parser.ParseSchemaString(exposureSchema)
	require.NoError(t, err)

	// The run restates the recorded restriction, so it passes the up-front
	// check; a default-exposure record lands while it waits.
	_, err = behindLock(t, db, func() (bool, error) {
		return false, migrator.NewMigrator(db, "").MigrateWithTypesAndOptions(ctx, types,
			migrator.InternalMigrateOptions{Exposure: editorOnly()})
	}, copyLastRecord(map[string]string{"exposure": "'{}'"}))
	refused := requireExposureChangeRefused(t, err, true)
	assert.True(t, refused.Recorded.IsDefault(), "the refusal names the exposure recorded while waiting")
	assertFunctionsDropped(t, ctx, db, deExposed)
	assertFunctions(t, ctx, db, restrictedInstalls)
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

// A static UP drops removed functions without CASCADE, so a view that calls
// one makes the migration fail instead of silently losing the view.
func TestMigrateExposure_StaticMigrationRefusesDependents(t *testing.T) {
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`)
	require.NoError(t, err)

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
	result := compiler.GenerateMigrationSQL(gen, list, compiler.CollectFunctionNamesWithOptions(analyses, opts), compiler.MigrationOptions{
		Exposure:              *editorOnly(),
		PreviousFunctionNames: lastRecord(t, db).FunctionNames,
		PreviousExposure:      &compiler.Exposure{},
	})
	require.Contains(t, result.Up, "DROP FUNCTION IF EXISTS check_document_viewer;")

	_, err = db.ExecContext(ctx, result.Up)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "2BP01", pgErr.Code)
	assert.True(t, dependentExists(t, db, "view"), "the dependent survives")
	assertFunctions(t, ctx, db, deExposed)
}

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

	// A newer melange migrated the database: this one's migrate refuses to
	// apply the drift, so the hint names the newer version, not migrate.
	stampVersion(t, "v0.10.0")
	_, err = db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.11.0'`)
	require.NoError(t, err)
	ahead := findCheck(filterCategory(run(editorOnly()), "Migration State"), "exposure_sync")
	require.NotNil(t, ahead)
	assert.Equal(t, doctor.StatusWarn, ahead.Status)
	assert.Contains(t, ahead.FixHint, "Use melange v0.11.0 or later")
	assert.NotContains(t, ahead.FixHint, "--force")
}

// Narrowing the exposure drops the functions it no longer exposes. Doctor's
// orphan check is compiled under the recorded exposure, so it does not list
// them; the exposure_sync hint names their dependents, since a plain migrate
// refuses to drop them.
func TestMigrateExposure_DoctorNamesDependentsOfNarrowing(t *testing.T) {
	stampVersion(t, "v0.10.0")
	db, path := exposureDB(t)
	ctx := context.Background()
	_, err := migrateExposure(t, db, path, nil, false)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`)
	require.NoError(t, err)

	report, err := doctor.New(db, path, doctor.Options{SkipPerformance: true, Exposure: editorOnly()}).Run(ctx)
	require.NoError(t, err)
	assert.Nil(t, findCheck(filterCategory(report, "Generated Functions"), "orphans"), "the premise: doctor lists no orphans")
	c := findCheck(filterCategory(report, "Migration State"), "exposure_sync")
	require.NotNil(t, c)
	assert.Equal(t, doctor.StatusWarn, c.Status)
	assert.Contains(t, c.FixHint, "Run 'melange migrate' to apply it")
	assert.Contains(t, c.FixHint, "first change or drop view alice_viewer")

	_, err = migrateExposure(t, db, path, editorOnly(), false)
	var depErr *migrator.DependentObjectsError
	require.ErrorAs(t, err, &depErr, "the premise: a plain migrate refuses")

	// Without the dependent, the hint is the plain migrate that applies it.
	_, err = db.ExecContext(ctx, `DROP VIEW alice_viewer`)
	require.NoError(t, err)
	report, err = doctor.New(db, path, doctor.Options{SkipPerformance: true, Exposure: editorOnly()}).Run(ctx)
	require.NoError(t, err)
	c = findCheck(filterCategory(report, "Migration State"), "exposure_sync")
	require.NotNil(t, c)
	assert.Equal(t, "Run 'melange migrate' to apply it", c.FixHint)
}

// `melange status` reports the recorded exposure and compares the local one
// (here from flags, with no melange.yaml) against it: an exposure-only
// difference is exposure_drift, since migrate would apply the local one.
func TestMigrateExposure_Status(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	path := writeSchema(t, exposureSchema)
	cli := buildMelangeCLI(t)
	status := func(flags ...string) statusJSON {
		t.Helper()
		return cli.status(t, t.TempDir(), append([]string{"--db", dsn, "--schema", path}, flags...)...)
	}

	_, err := migrateExposure(t, db, path, editorOnly(), false)
	require.NoError(t, err)
	report := status("--include-relation", "document#editor")
	assert.Equal(t, "in_sync", report.Sync)
	if assert.NotNil(t, report.Deployed.Exposure, "a restricted exposure is reported") {
		assert.True(t, migrator.SameExposure(*editorOnly(), *report.Deployed.Exposure))
	}
	assert.Equal(t, "exposure_drift", status().Sync, "the default local exposure differs from the recorded one")

	_, err = migrateExposure(t, db, path, &migrator.Exposure{}, false)
	require.NoError(t, err)
	report = status()
	assert.Equal(t, "in_sync", report.Sync)
	assert.Nil(t, report.Deployed.Exposure, "the default exposure is omitted")
}

// `melange generate migration --db` compares against the exposure the
// database records and generates under the local one: the same exposure
// changes nothing, and the default (no melange.yaml, no flags) widens the
// database back, as `melange migrate` would.
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
	cli := buildMelangeCLI(t)
	generate := func(flags ...string) string {
		return cli.run(t, append([]string{"generate", "migration", "--db", dsn, "--schema", path, "--up"}, flags...)...)
	}

	up := generate("--include-relation", "document#editor")
	assert.Contains(t, up, `-- Exposure: {"relations":{"include":["document#editor"]}`)
	assert.Contains(t, up, "-- Changed functions: 0 of ", "the recorded exposure is the previous one")
	assert.NotContains(t, up, "Previous exposure unknown")
	assert.NotContains(t, up, `FUNCTION "public"."check_document_viewer"(`, "viewer stays outside the closure")

	_, err = db.ExecContext(ctx, up)
	require.NoError(t, err)
	assertFunctionsDropped(t, ctx, db, deExposed)
	requireNotExposed(t, db, "viewer", "editor")

	up = generate()
	assert.NotContains(t, up, "-- Exposure:")
	assert.Contains(t, up, "-- Changed functions: ", "the recorded exposure is the previous one")
	assert.Contains(t, up, `FUNCTION "public"."check_document_viewer"(`, "widening creates the functions left out")
	assert.NotContains(t, up, `FUNCTION "public"."check_document_editor"(`, "functions both install are unchanged")
	_, err = db.ExecContext(ctx, up)
	require.NoError(t, err)
	var allowed int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT check_permission('user', 'alice', 'viewer', 'document', '1')`).Scan(&allowed))
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
	return c.runIn(t, t.TempDir(), args...)
}

// runIn is run in dir, which may hold a melange.yaml.
func (c melangeCLI) runIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := c.exec(dir, args...)
	require.NoError(t, err, "melange %v: %s", args, stderr)
	return out
}

// exec runs the CLI in dir and returns its stdout, stderr and exit error.
func (c melangeCLI) exec(dir string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(string(c), append(args, "--no-update-check")...)
	cmd.Dir = dir
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	return string(out), errBuf.String(), err
}

// statusJSON is the part of `melange status --format json` the tests read.
type statusJSON struct {
	Sync     string `json:"sync"`
	Deployed *struct {
		Exposure *migrator.Exposure `json:"exposure"`
	} `json:"deployed"`
}

// status runs `melange status --format json` in dir.
func (c melangeCLI) status(t *testing.T, dir string, args ...string) statusJSON {
	t.Helper()
	out := c.runIn(t, dir, append([]string{"status", "--format", "json"}, args...)...)
	var report statusJSON
	require.NoError(t, json.Unmarshal([]byte(out), &report), out)
	require.NotNil(t, report.Deployed, out)
	return report
}

// doctorCheckJSON is a check of `melange doctor --format json`.
type doctorCheckJSON struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass, warn or fail
	Details string `json:"details"`
}

// doctorCheck runs `melange doctor --format json` in dir and returns the
// named check, nil when doctor did not report it. Doctor exits non-zero on
// any failed check, which is not what these tests look at.
func (c melangeCLI) doctorCheck(t *testing.T, dir, name string, args ...string) *doctorCheckJSON {
	t.Helper()
	out, stderr, _ := c.exec(dir, append([]string{"doctor", "--format", "json", "--skip-performance"}, args...)...)
	var report struct {
		Checks []doctorCheckJSON `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report), "%s\n%s", out, stderr)
	for i := range report.Checks {
		if report.Checks[i].Name == name {
			return &report.Checks[i]
		}
	}
	return nil
}

// The sql section of melange.yaml drives migrate, status and doctor through
// its whole life: a restricted section is applied and recorded, an edit to
// the section alone is reported as drift until migrate applies it, and
// removing the section widens the database back to the default.
func TestCLI_ExposureFromConfigLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.fga"), []byte(exposureSchema), 0o644))
	writeConfig := func(sqlSection string) {
		t.Helper()
		content := "schema: schema.fga\ndatabase:\n  url: " + dsn + "\n" + sqlSection
		require.NoError(t, os.WriteFile(filepath.Join(dir, "melange.yaml"), []byte(content), 0o644))
	}
	cli := buildMelangeCLI(t)
	migrate := func() { t.Helper(); cli.runIn(t, dir, "migrate") }
	syncState := func() string { t.Helper(); return cli.status(t, dir).Sync }
	exposureSync := func() string {
		t.Helper()
		c := cli.doctorCheck(t, dir, "exposure_sync")
		require.NotNil(t, c, "doctor compares the local exposure")
		return c.Status
	}

	// A restricted section is applied and recorded; status and doctor agree.
	writeConfig("sql:\n  relations:\n    include: [document#editor]\n")
	migrate()
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure))
	requireNotExposed(t, db, "viewer", "editor")
	assert.Equal(t, "in_sync", syncState())
	assert.Equal(t, "pass", exposureSync())
	if c := cli.doctorCheck(t, dir, "missing"); c != nil {
		assert.NotEqual(t, "fail", c.Status, "no false missing functions: %s", c.Details)
	}

	// Changing only the section is drift until migrate applies it.
	viewerAndEditor := "sql:\n  relations:\n    include: [document#editor, document#viewer]\n  functions:\n    expand: false\n"
	writeConfig(viewerAndEditor)
	assert.Equal(t, "exposure_drift", syncState())
	assert.Equal(t, "warn", exposureSync())
	migrate()
	want := migrator.Exposure{
		Relations: compiler.RelationPatterns{Include: []string{"document#editor", "document#viewer"}},
		Functions: compiler.FunctionFamilies{Expand: new(bool)},
	}
	assert.True(t, migrator.SameExposure(want, lastRecord(t, db).Exposure), "%+v", lastRecord(t, db).Exposure)
	requireNotExposed(t, db, "auditor", "viewer")
	assert.Equal(t, "in_sync", syncState())
	assert.Equal(t, "pass", exposureSync())

	// A pattern that matches nothing is a doctor warning, not an error.
	writeConfig("sql:\n  relations:\n    include: [document#editor, document#vewer]\n")
	if c := cli.doctorCheck(t, dir, "exposure_patterns"); assert.NotNil(t, c) {
		assert.Equal(t, "warn", c.Status)
		assert.Contains(t, c.Details, "document#vewer")
	}

	// Flags apply to one run only: they do not persist.
	writeConfig(viewerAndEditor)
	cli.runIn(t, dir, "migrate", "--include-relation", "document#owner")
	assert.Equal(t, "exposure_drift", syncState(), "the file still asks for viewer and editor")
	assert.Equal(t, "in_sync", cli.status(t, dir, "--include-relation", "document#owner").Sync)

	// Removing the section widens the database back to the default.
	writeConfig("")
	assert.Equal(t, "exposure_drift", syncState())
	migrate()
	assert.True(t, lastRecord(t, db).Exposure.IsDefault(), "%+v", lastRecord(t, db).Exposure)
	assert.Equal(t, "in_sync", syncState())
	assert.Equal(t, "pass", exposureSync())
	var allowed int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT check_permission('user', 'alice', 'auditor', 'document', '1')`).Scan(&allowed))
	for name := range deExposed {
		assert.True(t, functionExists(t, context.Background(), db, name), "%s is installed again", name)
	}
}

// An invalid pattern in melange.yaml stops migrate before it connects, and
// a dependent object on a function the new exposure drops makes migrate
// refuse, naming the dependent and --force.
func TestCLI_ExposureRefusals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.fga"), []byte(exposureSchema), 0o644))
	cli := buildMelangeCLI(t)
	cfg := func(sqlSection string) {
		t.Helper()
		content := "schema: schema.fga\ndatabase:\n  url: " + dsn + "\n" + sqlSection
		require.NoError(t, os.WriteFile(filepath.Join(dir, "melange.yaml"), []byte(content), 0o644))
	}

	cfg("sql:\n  relations:\n    exclude: [\"a#b#c\"]\n")
	_, stderr, err := cli.exec(dir, "migrate")
	require.Error(t, err)
	assert.Contains(t, stderr, "sql.relations.exclude")
	assert.Empty(t, getFunctionNames(t, context.Background(), db), "nothing is applied")

	cfg("")
	cli.runIn(t, dir, "migrate")
	_, err = db.ExecContext(context.Background(), `CREATE TABLE docs (id TEXT PRIMARY KEY);
		ALTER TABLE docs ENABLE ROW LEVEL SECURITY;
		CREATE POLICY docs_read ON docs USING (id IN (SELECT object_id FROM accessible_document_viewer('user', current_user)))`)
	require.NoError(t, err)

	cfg("sql:\n  relations:\n    include: [document#editor]\n")
	_, stderr, err = cli.exec(dir, "migrate")
	require.Error(t, err)
	assert.Contains(t, stderr, "--force")
	assert.Contains(t, stderr, "policy docs_read on table docs")
	assert.True(t, lastRecord(t, db).Exposure.IsDefault(), "the refused exposure is not recorded")

	cli.runIn(t, dir, "migrate", "--force")
	assert.True(t, migrator.SameExposure(*editorOnly(), lastRecord(t, db).Exposure))
	assert.False(t, dependentExists(t, db, "rls policy"), "--force drops the dependent with CASCADE")
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
		// Validated before the record-less exposure guard, which compares
		// normalized exposures: ["*", "a#b#c"] normalizes to the default.
		err = m.MigrateWithTypes(context.Background(), types)
		require.Error(t, err, "include %q", include)
		assert.True(t, strings.Contains(err.Error(), "invalid exposure"), err.Error())
	}
}

// Studio's Health page compares the configured exposure with the recorded
// one, as melange doctor does; without one it keeps the recorded exposure.
func TestMigrateExposure_StudioHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	_, err := migrateExposure(t, db, writeSchema(t, exposureSchema), editorOnly(), false)
	require.NoError(t, err)

	health := func(exposure *migrator.Exposure) string {
		console := studio.New(openStudioDB(t, dsn), "public", "", "")
		console.SetExposure(exposure)
		srv := httptest.NewServer(console.Handler())
		t.Cleanup(srv.Close)
		c := studioClient{t: t, base: srv.URL, c: srv.Client()}
		return html.UnescapeString(c.get("/health"))
	}
	const drift = "SQL API exposure differs from the one the database was migrated with"
	assert.NotContains(t, health(nil), drift)
	assert.NotContains(t, health(editorOnly()), drift)
	assert.Contains(t, health(&migrator.Exposure{}), drift)
}
