package test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/test/testutil"
)

const rlsModel = `model
  schema 1.1
type user
type document
  relations
    define writer: [user]
    define viewer: [user] or writer
`

// The two SELECT policy shapes documented in guides/row-level-security.md.
var rlsSelectPolicies = map[string]string{
	"check_permission": `check_permission('user', current_setting('app.user_id', true), 'viewer', 'document', id) = 1`,
	"list_accessible_objects": `id IN (SELECT object_id FROM list_accessible_objects(
		'user', current_setting('app.user_id', true), 'viewer', 'document', NULL, NULL))`,
}

// rlsFixture protects documents with RLS policies built on melange functions,
// where melange_tuples is a view over the protected table itself. It returns
// the admin connection and one for a role that is neither superuser nor table
// owner, since both bypass RLS.
func rlsFixture(t *testing.T, selectPolicy string) (admin, db *sql.DB) {
	t.Helper()
	admin = testutil.EmptyDB(t)
	isolationExec(t, admin, `
		CREATE TABLE documents (id text PRIMARY KEY, owner_id text NOT NULL, body text);
		CREATE TABLE document_viewers (document_id text, user_id text);
		INSERT INTO documents VALUES ('d1', 'alice', 'one'), ('d2', 'bob', 'two'), ('d3', 'carol', 'three');
		INSERT INTO document_viewers VALUES ('d1', 'bob');
		CREATE VIEW melange_tuples AS
			SELECT 'user'::text AS subject_type, owner_id AS subject_id, 'writer'::text AS relation,
				'document'::text AS object_type, id AS object_id
			FROM documents
			UNION ALL
			SELECT 'user', user_id, 'viewer', 'document', document_id FROM document_viewers;`)
	types, err := parser.ParseSchemaString(rlsModel)
	require.NoError(t, err)
	require.NoError(t, migrator.NewMigrator(admin, "").MigrateWithTypes(context.Background(), types))
	// Policies reference melange functions, so they must follow the migration.
	isolationExec(t, admin, `
		ALTER TABLE documents ENABLE ROW LEVEL SECURITY;
		CREATE POLICY doc_select ON documents FOR SELECT USING (`+selectPolicy+`);
		CREATE POLICY doc_insert ON documents FOR INSERT WITH CHECK (
			owner_id = current_setting('app.user_id', true));
		CREATE POLICY doc_update ON documents FOR UPDATE USING (
			check_permission('user', current_setting('app.user_id', true), 'writer', 'document', id) = 1);
		CREATE POLICY doc_delete ON documents FOR DELETE USING (
			check_permission('user', current_setting('app.user_id', true), 'writer', 'document', id) = 1);`)
	role := isolationRole(t, admin, "public")
	quoted := sqldsl.QuoteIdent(role)
	isolationExec(t, admin, "GRANT INSERT, UPDATE, DELETE ON documents TO "+quoted)
	isolationExec(t, admin, "GRANT INSERT ON document_viewers TO "+quoted)
	return admin, isolationPool(t, admin, role, nil)
}

type rlsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func rlsVisible(t *testing.T, q rlsQuerier) []string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), "SELECT id FROM documents ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func rlsAffected(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(), query)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	return n
}

func rlsAs(t *testing.T, db *sql.DB, user string) {
	t.Helper()
	isolationExec(t, db, "SELECT set_config('app.user_id', '"+user+"', false)")
}

func TestRLSPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for name, policy := range rlsSelectPolicies {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			admin, db := rlsFixture(t, policy)

			assert.Equal(t, []string{"d1", "d2", "d3"}, rlsVisible(t, admin), "owner bypasses RLS")
			assert.Empty(t, rlsVisible(t, db), "no user set")
			rlsAs(t, db, "alice")
			assert.Equal(t, []string{"d1"}, rlsVisible(t, db))
			rlsAs(t, db, "bob")
			assert.Equal(t, []string{"d1", "d2"}, rlsVisible(t, db), "direct viewer plus writer -> viewer")
			rlsAs(t, db, "mallory")
			assert.Empty(t, rlsVisible(t, db))

			// bob can view d1 but not write it.
			rlsAs(t, db, "bob")
			assert.Zero(t, rlsAffected(t, db, "UPDATE documents SET body = 'x' WHERE id = 'd1'"))
			assert.EqualValues(t, 1, rlsAffected(t, db, "UPDATE documents SET body = 'x' WHERE id = 'd2'"))
			assert.Zero(t, rlsAffected(t, db, "DELETE FROM documents WHERE id = 'd1'"))
			_, err := db.ExecContext(ctx, "INSERT INTO documents VALUES ('d4', 'alice', 'four')")
			require.ErrorContains(t, err, "row-level security")
			assert.EqualValues(t, 1, rlsAffected(t, db, "INSERT INTO documents VALUES ('d4', 'bob', 'four')"))
			assert.Equal(t, []string{"d1", "d2", "d4"}, rlsVisible(t, db), "new row grants writer")

			// Uncommitted tuple sources are visible to policies in their own
			// transaction only.
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, "INSERT INTO document_viewers VALUES ('d3', 'bob')")
			require.NoError(t, err)
			assert.Equal(t, []string{"d1", "d2", "d3", "d4"}, rlsVisible(t, tx))
			require.NoError(t, tx.Rollback())
			assert.Equal(t, []string{"d1", "d2", "d4"}, rlsVisible(t, db))
		})
	}
}

// A check_permission WITH CHECK cannot authorize a row through tuples derived
// from that same row: the view does not see the row while it is being checked.
func TestRLSPolicy_InsertCannotSeeOwnRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	admin, db := rlsFixture(t, rlsSelectPolicies["list_accessible_objects"])
	isolationExec(t, admin, `DROP POLICY doc_insert ON documents;
		CREATE POLICY doc_insert ON documents FOR INSERT WITH CHECK (
			check_permission('user', current_setting('app.user_id', true), 'writer', 'document', id) = 1)`)
	rlsAs(t, db, "bob")
	_, err := db.ExecContext(context.Background(), "INSERT INTO documents VALUES ('d4', 'bob', 'four')")
	require.ErrorContains(t, err, "row-level security")
}

// With security_invoker the view applies the caller's RLS, so the policy
// re-enters itself through melange_tuples.
func TestRLSPolicy_SecurityInvokerViewRecurses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	admin, db := rlsFixture(t, rlsSelectPolicies["check_permission"])
	isolationExec(t, admin, "ALTER VIEW melange_tuples SET (security_invoker = true)")
	rlsAs(t, db, "bob")
	_, err := db.ExecContext(context.Background(), "SELECT count(*) FROM documents")
	require.ErrorContains(t, err, "stack depth limit exceeded")
}

// Re-migrating with a changed model must keep policies that call the
// dispatchers intact.
func TestRLSPolicy_SurvivesRemigration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for name, policy := range rlsSelectPolicies {
		t.Run(name, func(t *testing.T) {
			admin, db := rlsFixture(t, policy)
			types, err := parser.ParseSchemaString(rlsModel + "    define commenter: [user] or viewer\n")
			require.NoError(t, err)
			require.NoError(t, migrator.NewMigrator(admin, "").MigrateWithTypes(context.Background(), types))
			var policies int
			require.NoError(t, admin.QueryRow(`SELECT count(*) FROM pg_policies WHERE tablename = 'documents'`).Scan(&policies))
			assert.Equal(t, 4, policies)
			rlsAs(t, db, "bob")
			assert.Equal(t, []string{"d1", "d2"}, rlsVisible(t, db))
		})
	}
}
