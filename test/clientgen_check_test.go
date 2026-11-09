package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
	fgaparser "github.com/pthm/melange/pkg/parser"
)

// clientCheckSchema has role relations the can_* permissions resolve
// through, and a parent-key function (document#can_view by parent).
const clientCheckSchema = `model
  schema 1.1

type user

type folder
  relations
    define viewer: [user]

type document
  relations
    define parent: [folder]
    define owner: [user]
    define editor: [user] or owner
    define can_view: viewer from parent
    define can_edit: editor
`

// clientNames returns the names a client generated under local emits.
func clientNames(t *testing.T, local sqlgen.Exposure) sqlapi.ClientNames {
	t.Helper()
	types, err := fgaparser.ParseSchemaString(clientCheckSchema)
	require.NoError(t, err)
	m, err := clientgen.Resolve(types, &clientgen.Config{Exposure: &local})
	require.NoError(t, err)
	return sqlapi.NewClientNames(m, types, clientCheckSchema)
}

// compareClient runs doctor's client comparison outside Run, as generate
// client --check --db does.
func compareClient(t *testing.T, db *sql.DB, path string, local sqlgen.Exposure) *sqlapi.ClientReport {
	t.Helper()
	cr, err := doctor.New(db, path, doctor.Options{Exposure: &local}).CompareClient(context.Background(), clientNames(t, local), local)
	require.NoError(t, err)
	return cr
}

// pgFunctions returns the generated functions installed in public.
func pgFunctions(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	var names []string
	require.NoError(t, queryStrings(db, `SELECT DISTINCT p.proname::text FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.proname ~ '^accessible_'`, &names))
	set := make(map[string]bool)
	for _, n := range names {
		set[n] = true
	}
	return set
}

// A client generated under the exposure the database was migrated with has
// no findings; one generated under the default names exactly what that
// exposure leaves out, and the database agrees: the functions reported not
// installed are not in pg_proc, and check_permission raises M2003 for the
// relations reported not exposed.
func TestClientCheck_DetectsExposureSkew(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	off := func() *bool { return new(bool) }
	canStar := sqlgen.RelationPatterns{Include: []string{"can_*"}}
	for _, tc := range []struct {
		name     string
		exposure sqlgen.Exposure
	}{
		{"default", sqlgen.Exposure{}},
		{"can_*", sqlgen.Exposure{Relations: canStar}},
		{"can_* with the list families off", sqlgen.Exposure{Relations: canStar, Functions: sqlgen.FunctionFamilies{ListObjects: off(), Accessible: off(), ParentKey: off()}}},
		{"parent_key off", sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{ParentKey: off()}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := newTuplesDB(t)
			path := writeSchema(t, clientCheckSchema)
			_, err := migrateExposure(t, db, path, &tc.exposure, false)
			require.NoError(t, err)

			assert.Empty(t, compareClient(t, db, path, tc.exposure).Findings, "a client generated under the database's exposure")

			cr := compareClient(t, db, path, sqlgen.Exposure{})
			installed := pgFunctions(t, db)
			narrow := clientNames(t, tc.exposure)
			var reported []string
			for _, f := range cr.Findings {
				if f.Code == "family_off" {
					continue // list_objects, which the client names no function of
				}
				require.NotEqual(t, sqlapi.SeverityInfo, f.Severity, "%+v", f)
				switch f.Code {
				case "function_not_installed":
					assert.False(t, installed[f.Function], "%s is reported not installed", f.Function)
					reported = append(reported, f.Function)
				case "function_internal":
					assert.True(t, installed[f.Function], "%s is reported internal", f.Function)
					reported = append(reported, f.Function)
				case "relation_not_exposed":
					var allowed int
					err := db.QueryRowContext(ctx, `SELECT check_permission('user', 'u', $1, $2, '1')`, f.Relation, f.Type).Scan(&allowed)
					var state interface{ SQLState() string }
					require.True(t, errors.As(err, &state), "%s#%s: %v", f.Type, f.Relation, err)
					assert.Equal(t, sqlgen.NotExposedErrCode, state.SQLState())
					reported = append(reported, f.Type+"#"+f.Relation)
				default:
					t.Errorf("unexpected finding %+v", f)
				}
			}
			// Exactly the names the default client has and the narrow one
			// does not.
			var omitted []string
			for _, f := range clientNames(t, sqlgen.Exposure{}).Functions {
				if !slices.ContainsFunc(narrow.Functions, func(n sqlgen.AccessibleFunction) bool { return n.Name == f.Name }) {
					omitted = append(omitted, f.Name)
				}
			}
			for _, p := range clientNames(t, sqlgen.Exposure{}).Pairs {
				if !slices.Contains(narrow.Pairs, p) {
					omitted = append(omitted, p.Type+"#"+p.Relation)
				}
			}
			slices.Sort(reported)
			slices.Sort(omitted)
			assert.Equal(t, omitted, reported)
			assert.Equal(t, len(omitted) == 0, cr.Errors == 0)
		})
	}
}

// A database built without a record (as from generate migration files) is
// compared under the local exposure, so the client matches it.
func TestClientCheck_NoRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := newTuplesDB(t)
	path := writeSchema(t, clientCheckSchema)
	local := sqlgen.Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"can_*"}}}
	_, err := migrateExposure(t, db, path, &local, false)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `DROP TABLE melange_migrations`)
	require.NoError(t, err)

	cr := compareClient(t, db, path, local)
	assert.Empty(t, cr.Findings)
	assert.True(t, slices.ContainsFunc(cr.Notes, func(n string) bool { return strings.Contains(n, "No migration is recorded") }), cr.Notes)

	// The default client names what the files did not install.
	cr = compareClient(t, db, path, sqlgen.Exposure{})
	assert.Positive(t, cr.Errors)

	// Doctor's own schema (--schema) may be another model; with no record
	// the database is described with the client's model, not doctor's.
	other := writeSchema(t, "model\n  schema 1.1\n\ntype user\n")
	cr, err = doctor.New(db, other, doctor.Options{Exposure: &local}).CompareClient(context.Background(), clientNames(t, local), local)
	require.NoError(t, err)
	assert.Empty(t, cr.Findings)
}

// After the database is narrowed, an accessible function the last migration
// recorded but this melange no longer installs for the model is an orphan
// the next migrate drops; a function of the same name an application
// created is unmanaged.
func TestClientCheck_OrphanAndUnmanaged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx := context.Background()
	const byParent = "accessible_document_can_view_by_parent"
	noParentKey := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{ParentKey: new(bool)}}

	t.Run("orphan", func(t *testing.T) {
		db := newTuplesDB(t)
		path := writeSchema(t, clientCheckSchema)
		_, err := migrateExposure(t, db, path, &migrator.Exposure{}, false)
		require.NoError(t, err)
		// The record says parent_key was off, as after a migration a later
		// one partly undid; the function stays recorded and installed.
		_, err = db.ExecContext(ctx, `UPDATE melange_migrations SET exposure = '{"functions":{"parent_key":false}}'`)
		require.NoError(t, err)
		cr := compareClient(t, db, path, sqlgen.Exposure{})
		require.Len(t, cr.Findings, 1, "%+v", cr.Findings)
		assert.Equal(t, "function_orphan", cr.Findings[0].Code)
		assert.Equal(t, byParent, cr.Findings[0].Function)
		assert.Contains(t, cr.Findings[0].Fix, "Run 'melange migrate'")
	})

	t.Run("unmanaged", func(t *testing.T) {
		db := newTuplesDB(t)
		path := writeSchema(t, clientCheckSchema)
		_, err := migrateExposure(t, db, path, &noParentKey, false)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `CREATE FUNCTION `+byParent+`(p_subject_type text, p_subject_id text) RETURNS SETOF text LANGUAGE sql AS $$ SELECT NULL::text WHERE false $$`)
		require.NoError(t, err)
		cr := compareClient(t, db, path, sqlgen.Exposure{})
		require.Len(t, cr.Findings, 1, "%+v", cr.Findings)
		assert.Equal(t, "function_unmanaged", cr.Findings[0].Code)
		assert.Equal(t, sqlapi.SeverityWarning, cr.Findings[0].Severity)
	})
}

// The CLI end to end: migrate's exposure flags warn about the configured
// client, generate client --check passes without a database and fails with
// one, doctor warns (fails with --strict-client), and a plain migrate,
// which applies melange.yaml, fixes it.
func TestClientCheck_CLI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	_, dsn := newTuplesDBWithDSN(t)
	cli := buildMelangeCLI(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.fga"), []byte(clientCheckSchema), 0o644))
	config := "schema: schema.fga\ndatabase:\n  url: " + dsn + "\ngenerate:\n  client:\n    runtime: go\n    output: authz\n"
	writeConfigFile(t, dir, config)

	cli.runIn(t, dir, "generate", "client")
	_, stderr, err := cli.exec(dir, "migrate", "--no-parent-key")
	require.NoError(t, err, stderr)
	assert.Contains(t, stderr, "the exposure flags differ from melange.yaml's sql section")

	cli.runIn(t, dir, "generate", "client", "--check")
	out, _, err := cli.exec(dir, "generate", "client", "--check", "--db", dsn)
	requireExit(t, err, 1)
	assert.Contains(t, out, "function_not_installed: accessible_document_can_view_by_parent is not installed: the database was migrated with sql.functions.parent_key off")
	assert.Contains(t, out, "Fix: Run 'melange migrate'")

	out, _, err = cli.exec(dir, "generate", "client", "--check", "--db", dsn, "--format", "json")
	requireExit(t, err, 1)
	var report struct {
		Files    []clientgen.FileDrift `json:"files"`
		Database *sqlapi.ClientReport  `json:"database"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report), out)
	assert.Empty(t, report.Files)
	require.NotNil(t, report.Database)
	assert.Equal(t, 1, report.Database.Errors)

	c := cli.doctorCheck(t, dir, "client_functions")
	require.NotNil(t, c)
	assert.Equal(t, "warn", c.Status)
	assert.Equal(t, "fail", cli.doctorCheck(t, dir, "client_functions", "--strict-client").Status)
	assert.Equal(t, "pass", cli.doctorCheck(t, dir, "client_file").Status)
	assert.Nil(t, cli.doctorCheck(t, dir, "client_functions", "--skip-client"))
	writeConfigFile(t, dir, config+"doctor:\n  strict_client: true\n")
	assert.Equal(t, "fail", cli.doctorCheck(t, dir, "client_functions").Status)
	writeConfigFile(t, dir, config)

	cli.runIn(t, dir, "migrate")
	cli.runIn(t, dir, "generate", "client", "--check", "--db", dsn)
	assert.Equal(t, "pass", cli.doctorCheck(t, dir, "client_functions").Status)

	// Without the sources, as in a production image, doctor skips the files
	// and still checks the names.
	require.NoError(t, os.Rename(filepath.Join(dir, "authz"), filepath.Join(dir, "authz.moved")))
	assert.Nil(t, cli.doctorCheck(t, dir, "client_file"))
	c = cli.doctorCheck(t, dir, "client_functions")
	require.NotNil(t, c)
	assert.Equal(t, "pass", c.Status)
	assert.Contains(t, c.Details, "Generated files not compared: authz does not exist here")

	// An output path that exists but is not a directory is an error, not a
	// production image.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "authz"), []byte("not a directory"), 0o644))
	c = cli.doctorCheck(t, dir, "client_file")
	require.NotNil(t, c)
	assert.Equal(t, "fail", c.Status)
}

// requireExit asserts the CLI exited with code.
func requireExit(t *testing.T, err error, code int) {
	t.Helper()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "exit error: %v", err)
	require.Equal(t, code, exit.ExitCode())
}

// Doctor's exposure flags (or --schema) change what its own migrate would
// do, not what migrating the client's configuration does: a hand-dropped
// accessible function the client names needs --force, since a plain migrate
// of melange.yaml skips, even though doctor's flagged migrate would apply.
func TestClientCheck_HintsFollowTheClientConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	stampVersion(t, "v0.10.0")
	db := newTuplesDB(t)
	path := writeSchema(t, clientCheckSchema)
	_, err := migrateExposure(t, db, path, &migrator.Exposure{}, false)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `DROP FUNCTION accessible_document_can_edit`)
	require.NoError(t, err)

	off := false
	flagged := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Explain: &off}}
	local := sqlgen.Exposure{}
	cr, err := doctor.New(db, path, doctor.Options{Exposure: &flagged}).CompareClient(context.Background(), clientNames(t, local), local)
	require.NoError(t, err)
	i := slices.IndexFunc(cr.Findings, func(f sqlapi.ClientFinding) bool { return f.Function == "accessible_document_can_edit" })
	require.GreaterOrEqual(t, i, 0, "findings: %+v", cr.Findings)
	assert.Contains(t, cr.Findings[i].Fix, "melange migrate --force")

	skipped, err := migrateExposure(t, db, path, &migrator.Exposure{}, false)
	require.NoError(t, err)
	assert.True(t, skipped, "the premise: a plain migrate of the client's configuration skips")
}
