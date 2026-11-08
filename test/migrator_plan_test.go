package test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/migrator"
)

// planSchemaWithoutViewer is concurrencySchema without document#viewer, whose
// functions an applying migration then drops as orphans.
const planSchemaWithoutViewer = `model
  schema 1.1

type user

type group
  relations
    define member: [user, group#member]

type document
  relations
    define owner: [user]
    define editor: [user, group#member] or owner
`

// dropGeneratedFunction drops every overload of the named function, as an
// operator fixing the database by hand might.
func dropGeneratedFunction(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), fmt.Sprintf(`DO $$
		DECLARE f regprocedure;
		BEGIN
			FOR f IN SELECT p.oid FROM pg_proc p WHERE p.pronamespace = current_schema()::regnamespace AND p.proname = %s LOOP
				EXECUTE 'DROP FUNCTION ' || f;
			END LOOP;
		END $$`, pq.QuoteLiteral(name)))
	require.NoError(t, err)
}

// Plan predicts each outcome of an unforced migration: it is compared with
// the migration itself, run right after it. A function dropped by hand tells
// an applying migration (which re-creates it) from one that only records.
func TestPlanMatchesMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	stampVersion(t, "v0.10.0")
	ctx := context.Background()
	const dropped = "check_document_viewer"
	opts := migrator.MigrateOptions{Version: "v0.10.0"}

	for _, tc := range []struct {
		name string
		// setup migrates the database as needed and returns the schema the
		// planned migration uses.
		setup func(t *testing.T, db *sql.DB) string
		want  migrator.PlanAction
		check func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error)
	}{
		{
			name:  "no record applies",
			setup: func(t *testing.T, db *sql.DB) string { return writeSchema(t, concurrencySchema) },
			want:  migrator.PlanApply,
			check: func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error) {
				require.NoError(t, err)
				assert.False(t, skipped)
				assert.Nil(t, plan.Record)
				assert.True(t, functionExists(t, ctx, db, dropped))
				assert.Equal(t, 1, migrationRecordCount(t, ctx, db))
			},
		},
		{
			name: "unchanged schema skips",
			setup: func(t *testing.T, db *sql.DB) string {
				path := writeSchema(t, concurrencySchema)
				_, err := migrator.MigrateWithOptions(ctx, db, path, opts)
				require.NoError(t, err)
				dropGeneratedFunction(t, db, dropped)
				return path
			},
			want: migrator.PlanSkip,
			check: func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error) {
				require.NoError(t, err)
				assert.True(t, skipped)
				assert.False(t, functionExists(t, ctx, db, dropped), "a skip re-creates nothing")
				assert.Equal(t, records, migrationRecordCount(t, ctx, db))
			},
		},
		{
			name: "comment-only edit records only",
			setup: func(t *testing.T, db *sql.DB) string {
				_, err := migrator.MigrateWithOptions(ctx, db, writeSchema(t, concurrencySchema), opts)
				require.NoError(t, err)
				dropGeneratedFunction(t, db, dropped)
				return writeSchema(t, concurrencySchema+"\n# a comment changes the checksum, not the SQL\n")
			},
			want: migrator.PlanRecordOnly,
			check: func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error) {
				require.NoError(t, err)
				assert.False(t, skipped)
				assert.False(t, functionExists(t, ctx, db, dropped), "recording re-creates nothing")
				assert.Equal(t, records+1, migrationRecordCount(t, ctx, db))
			},
		},
		{
			name: "schema change applies",
			setup: func(t *testing.T, db *sql.DB) string {
				_, err := migrator.MigrateWithOptions(ctx, db, writeSchema(t, concurrencySchema), opts)
				require.NoError(t, err)
				dropGeneratedFunction(t, db, dropped)
				return writeSchema(t, concurrencySchema+"    define auditor: [user]\n")
			},
			want: migrator.PlanApply,
			check: func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error) {
				require.NoError(t, err)
				assert.False(t, skipped)
				assert.True(t, functionExists(t, ctx, db, dropped), "applying re-creates every function")
				assert.Equal(t, records+1, migrationRecordCount(t, ctx, db))
			},
		},
		{
			name: "database ahead is refused",
			setup: func(t *testing.T, db *sql.DB) string {
				path := writeSchema(t, concurrencySchema)
				_, err := migrator.MigrateWithOptions(ctx, db, path, opts)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.11.0'`)
				require.NoError(t, err)
				return path
			},
			want: migrator.PlanRefusedAhead,
			check: func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error) {
				var ahead *migrator.DatabaseAheadError
				require.ErrorAs(t, err, &ahead)
				assert.Empty(t, plan.Orphans)
			},
		},
		{
			name: "orphans with dependents apply, and are refused",
			setup: func(t *testing.T, db *sql.DB) string {
				_, err := migrator.MigrateWithOptions(ctx, db, writeSchema(t, concurrencySchema), opts)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `CREATE VIEW alice_viewer AS SELECT check_document_viewer('user', 'alice', '1') AS allowed`)
				require.NoError(t, err)
				return writeSchema(t, planSchemaWithoutViewer)
			},
			want: migrator.PlanApply,
			check: func(t *testing.T, db *sql.DB, plan *migrator.MigrationPlan, records int, skipped bool, err error) {
				var depErr *migrator.DependentObjectsError
				require.ErrorAs(t, err, &depErr)
				var planned []string
				for _, o := range plan.Orphans {
					if o.Name == dropped {
						planned = o.Dependents
					}
				}
				assert.Equal(t, []string{"view alice_viewer"}, planned, "the plan names the dependent migrate refuses")
				for _, d := range depErr.Dependents {
					assert.Equal(t, dropped, d.Function)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTuplesDB(t)
			path := tc.setup(t, db)
			count := func() int {
				var exists bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('melange_migrations') IS NOT NULL`).Scan(&exists))
				if !exists {
					return 0
				}
				return migrationRecordCount(t, ctx, db)
			}
			records := count()
			plan, err := migrator.PlanWithOptions(ctx, db, path, opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, plan.Action)
			assert.Equal(t, records, count(), "Plan records nothing")
			skipped, err := migrator.MigrateWithOptions(ctx, db, path, opts)
			tc.check(t, db, plan, records, skipped, err)
		})
	}
}
