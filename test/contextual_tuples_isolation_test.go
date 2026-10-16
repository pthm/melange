package test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/test/testutil"
)

const contextualIsolationModel = `model
  schema 1.1
type user
type document
  relations
    define writer: [user]
`

func isolationExec(t *testing.T, q melange.Execer, query string) {
	t.Helper()
	_, err := q.ExecContext(context.Background(), query)
	require.NoError(t, err)
}

func isolationFixture(t *testing.T, schema string) *sql.DB {
	t.Helper()
	db := testutil.EmptyDB(t)
	quoted := sqldsl.QuoteIdent(schema)
	isolationExec(t, db, "CREATE SCHEMA IF NOT EXISTS "+quoted)
	isolationExec(t, db, "CREATE TABLE "+quoted+`.melange_tuples (
		subject_type text, subject_id text, relation text, object_type text, object_id text
	)`)
	types, err := parser.ParseSchemaString(contextualIsolationModel)
	require.NoError(t, err)
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(schema)
	require.NoError(t, m.MigrateWithTypes(context.Background(), types))
	return db
}

// Each pool has a single independent backend. SET ROLE through a startup
// parameter exercises real non-superuser permission checks, including pg_temp.
func isolationPool(t *testing.T, admin *sql.DB, role string, tracer pgx.QueryTracer) *sql.DB {
	t.Helper()
	conn, err := admin.Conn(context.Background())
	require.NoError(t, err)
	var cfg *pgx.ConnConfig
	err = conn.Raw(func(raw any) error {
		cfg = raw.(*stdlib.Conn).Conn().Config().Copy()
		return nil
	})
	require.NoError(t, conn.Close())
	require.NoError(t, err)
	if role != "" {
		cfg.RuntimeParams["role"] = role
	}
	cfg.Tracer = tracer
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	if role != "" {
		var privileged bool
		require.NoError(t, db.QueryRow(`SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls
			FROM pg_roles WHERE rolname = current_user`).Scan(&privileged))
		require.False(t, privileged)
	}
	return db
}

func isolationRole(t *testing.T, db *sql.DB, schemas ...string) string {
	t.Helper()
	role := fmt.Sprintf("contextual_%d", time.Now().UnixNano())
	quoted := sqldsl.QuoteIdent(role)
	isolationExec(t, db, "CREATE ROLE "+quoted+" NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS")
	t.Cleanup(func() {
		isolationExec(t, db, "DROP OWNED BY "+quoted)
		isolationExec(t, db, "DROP ROLE "+quoted)
	})
	for _, schema := range schemas {
		name := sqldsl.QuoteIdent(schema)
		isolationExec(t, db, "GRANT USAGE ON SCHEMA "+name+" TO "+quoted)
		isolationExec(t, db, "GRANT SELECT ON ALL TABLES IN SCHEMA "+name+" TO "+quoted)
	}
	return role
}

func isolationFacts(subject string) []melange.ContextualTuple {
	return []melange.ContextualTuple{{
		Subject: melange.Object{Type: "user", ID: subject}, Relation: "writer",
		Object: melange.Object{Type: "document", ID: "doc"},
	}}
}

func isolationCheck(t *testing.T, c *melange.Checker, subject string, facts []melange.ContextualTuple, want bool) {
	t.Helper()
	ok, err := c.CheckWithContextualTuples(context.Background(),
		melange.Object{Type: "user", ID: subject}, melange.Relation("writer"), melange.Object{Type: "document", ID: "doc"}, facts)
	require.NoError(t, err)
	assert.Equal(t, want, ok, "subject %s", subject)
}

func TestContextualTuples_PermanentSource(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for _, schema := range []string{"public", "z_authz", `z_auth'z`} {
		t.Run(schema, func(t *testing.T) {
			admin := isolationFixture(t, schema)
			isolationExec(t, admin, `CREATE SCHEMA a_other_dim;
				CREATE TABLE a_other_dim.melange_tuples (
					subject_type text, subject_id text, relation text, object_type text, object_id text);
				INSERT INTO a_other_dim.melange_tuples VALUES ('user', 'alice', 'writer', 'document', 'doc')`)
			isolationExec(t, admin, "INSERT INTO "+sqldsl.QuoteIdent(schema)+`.melange_tuples
				VALUES ('user', 'carol', 'writer', 'document', 'doc')`)
			role := isolationRole(t, admin, schema, "a_other_dim")
			db := isolationPool(t, admin, role, nil)
			// The configured source must win even when another source is first
			// alphabetically and on the connection's search path.
			isolationExec(t, db, "SET search_path = a_other_dim, "+sqldsl.QuoteIdent(schema))
			c := melange.NewChecker(db, melange.WithDatabaseSchema(schema))
			isolationCheck(t, c, "alice", nil, false)
			isolationCheck(t, c, "alice", isolationFacts("bob"), false)
			isolationCheck(t, c, "carol", isolationFacts("bob"), true)
			isolationCheck(t, c, "bob", isolationFacts("bob"), true)
			isolationCheck(t, c, "bob", nil, false)

			// A missing configured source must not fall back to another schema.
			isolationExec(t, admin, "DROP TABLE "+sqldsl.QuoteIdent(schema)+".melange_tuples")
			c = melange.NewChecker(db, melange.WithDatabaseSchema(schema))
			ok, err := c.CheckWithContextualTuples(context.Background(),
				melange.Object{Type: "user", ID: "alice"}, melange.Relation("writer"), melange.Object{Type: "document", ID: "doc"}, isolationFacts("bob"))
			assert.False(t, ok)
			require.ErrorIs(t, err, melange.ErrNoTuplesTable)
		})
	}
}

func TestContextualTuples_SearchPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	admin := isolationFixture(t, "public")
	isolationExec(t, admin, `CREATE SCHEMA z_authz;
		CREATE TABLE z_authz.melange_tuples (LIKE public.melange_tuples);
		INSERT INTO z_authz.melange_tuples VALUES ('user', 'alice', 'writer', 'document', 'doc');
		CREATE SCHEMA a_off_path;
		CREATE TABLE a_off_path.melange_tuples (LIKE public.melange_tuples);
		INSERT INTO a_off_path.melange_tuples VALUES ('user', 'alice', 'writer', 'document', 'doc')`)
	types, err := parser.ParseSchemaString(contextualIsolationModel)
	require.NoError(t, err)
	m := migrator.NewMigrator(admin, "")
	m.SetDatabaseSchema("z_authz")
	require.NoError(t, m.MigrateWithTypes(context.Background(), types))
	role := isolationRole(t, admin, "public", "z_authz", "a_off_path")
	db := isolationPool(t, admin, role, nil)
	c := melange.NewChecker(db, melange.WithDatabaseSchema(""))
	// Keep the same checker while changing the path: source discovery must
	// neither scan off-path schemas nor cache a connection-dependent result.
	isolationExec(t, db, "SET search_path = public, z_authz")
	isolationCheck(t, c, "alice", nil, false)
	isolationCheck(t, c, "alice", isolationFacts("bob"), false)
	isolationExec(t, db, "SET search_path = z_authz, public")
	isolationCheck(t, c, "alice", nil, true)
	isolationCheck(t, c, "alice", isolationFacts("bob"), true)
	isolationExec(t, db, "SET search_path = public")
	isolationCheck(t, c, "alice", isolationFacts("bob"), false)
	isolationExec(t, admin, "DROP TABLE public.melange_tuples")
	ok, err := c.CheckWithContextualTuples(context.Background(),
		melange.Object{Type: "user", ID: "alice"}, melange.Relation("writer"), melange.Object{Type: "document", ID: "doc"}, isolationFacts("bob"))
	assert.False(t, ok)
	require.ErrorIs(t, err, melange.ErrNoTuplesTable)
}

func TestContextualTuples_RejectTemporarySource(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	admin := isolationFixture(t, "public")
	role := isolationRole(t, admin, "public")
	db := isolationPool(t, admin, role, nil)
	isolationExec(t, admin, "DROP TABLE public.melange_tuples")
	for _, configured := range []bool{false, true} {
		isolationExec(t, db, `CREATE TEMP VIEW melange_tuples AS
			SELECT 'user'::text AS subject_type, 'alice'::text AS subject_id,
			'writer'::text AS relation, 'document'::text AS object_type, 'doc'::text AS object_id`)
		var tempSchema string
		require.NoError(t, db.QueryRow("SELECT nspname FROM pg_namespace WHERE oid = pg_my_temp_schema()").Scan(&tempSchema))
		// Even an explicitly named temp schema must not supply base authority.
		schema := ""
		if configured {
			schema = tempSchema
		}
		isolationExec(t, db, "SET search_path = "+sqldsl.QuoteIdent(tempSchema)+", public")
		c := melange.NewChecker(db, melange.WithDatabaseSchema(schema))
		ok, err := c.CheckWithContextualTuples(context.Background(),
			melange.Object{Type: "user", ID: "alice"}, melange.Relation("writer"), melange.Object{Type: "document", ID: "doc"}, isolationFacts("bob"))
		assert.False(t, ok)
		require.ErrorIs(t, err, melange.ErrNoTuplesTable)
	}
}

// Hold A immediately after PostgreSQL creates its real contextual view. B then
// performs discovery and evaluation while A's view is visible in pg_class.
type contextualViewBarrier struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

type contextualQueryKey struct{}

func (b *contextualViewBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, contextualQueryKey{}, data.SQL)
}

func (b *contextualViewBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	query, _ := ctx.Value(contextualQueryKey{}).(string)
	if strings.HasPrefix(query, "CREATE TEMP VIEW melange_tuples AS") && data.Err == nil {
		b.once.Do(func() { close(b.reached) })
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
}

func TestContextualTuples_ConcurrentIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for _, privileged := range []bool{false, true} {
		for _, schema := range []string{"public", ""} {
			t.Run(fmt.Sprintf("superuser=%v/schema=%q", privileged, schema), func(t *testing.T) {
				admin := isolationFixture(t, "public")
				role := ""
				if !privileged {
					role = isolationRole(t, admin, "public")
				}
				barrier := &contextualViewBarrier{reached: make(chan struct{}), release: make(chan struct{})}
				a := isolationPool(t, admin, role, barrier)
				b := isolationPool(t, admin, role, nil)
				ca := melange.NewChecker(a, melange.WithDatabaseSchema(schema))
				cb := melange.NewChecker(b, melange.WithDatabaseSchema(schema))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				type result struct {
					allowed bool
					err     error
				}
				done := make(chan result, 1)
				go func() {
					ok, err := ca.CheckWithContextualTuples(ctx,
						melange.Object{Type: "user", ID: "alice"}, melange.Relation("writer"), melange.Object{Type: "document", ID: "doc"}, isolationFacts("alice"))
					done <- result{ok, err}
				}()
				// Always release and join A before pool cleanup, even on failure.
				defer func() {
					close(barrier.release)
					first := <-done
					assert.NoError(t, first.err)
					assert.True(t, first.allowed, "A must retain its own contextual grant")
				}()
				select {
				case <-barrier.reached:
				case <-ctx.Done():
					t.Fatal("A did not establish its contextual view")
				}
				ok, err := cb.CheckWithContextualTuples(ctx,
					melange.Object{Type: "user", ID: "alice"}, melange.Relation("writer"), melange.Object{Type: "document", ID: "doc"}, isolationFacts("bob"))
				require.NoError(t, err, "B must not select A's temporary source")
				assert.False(t, ok, "B must not inherit A's authority")
				isolationCheck(t, cb, "bob", isolationFacts("bob"), true)
				isolationCheck(t, cb, "alice", nil, false)
				isolationCheck(t, cb, "bob", nil, false)
			})
		}
	}
}
