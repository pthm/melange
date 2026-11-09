package test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
)

// Explain and expand on demand: a database migrated with sql.functions.explain
// and expand off answers explain and expand through functions compiled from
// the recorded model into pg_temp for one rolled-back transaction
// (internal/sqlapi/ondemand.go).

const kitchenSinkSchemaPath = "testutil/testdata/kitchen_sink_schema.fga"

// traceFamilies is the exposure with the explain and expand families on or
// off and every relation exposed.
func traceFamilies(on bool) *migrator.Exposure {
	if on {
		return &migrator.Exposure{}
	}
	return &migrator.Exposure{Functions: compiler.FunctionFamilies{Explain: new(bool), Expand: new(bool)}}
}

// kitchenSinkRecorded returns a database with the kitchen-sink model migrated
// into databaseSchema by MigrateWithOptions, so a migration record exists,
// and the small tuple set behind databaseSchema.melange_tuples, opened with
// lib/pq as the CLI and studio open it. migrate re-migrates it with the
// explain and expand families on or off.
func kitchenSinkRecorded(t *testing.T, databaseSchema string) (db *sql.DB, dsn string, migrate func(on bool)) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	admin, dsn := testutil.EmptyDBWithDSN(t)
	ctx := context.Background()
	s := pq.QuoteIdentifier(databaseSchema)
	_, err := admin.ExecContext(ctx, `
		CREATE SCHEMA IF NOT EXISTS `+s+`;
		CREATE TABLE kitchen_sink_tuples (
			subject_type TEXT NOT NULL, subject_id TEXT NOT NULL, relation TEXT NOT NULL,
			object_type TEXT NOT NULL, object_id TEXT NOT NULL);
		CREATE VIEW `+s+`.melange_tuples AS
			SELECT subject_type, subject_id, relation, object_type, object_id FROM public.kitchen_sink_tuples`)
	require.NoError(t, err)
	testutil.LoadKitchenSinkTuples(t, admin, testutil.GenerateKitchenSinkTuples(testutil.KitchenSinkScaleSmall))
	db, err = sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	migrate = func(on bool) {
		t.Helper()
		_, err := migrator.MigrateWithOptions(ctx, db, kitchenSinkSchemaPath, migrator.MigrateOptions{
			Version: "v0.10.0", DatabaseSchema: databaseSchema, Exposure: traceFamilies(on),
		})
		require.NoError(t, err)
		// The dispatchers are installed either way; the per-relation
		// functions follow the families.
		var installed int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1 AND p.proname IN ('explain_document_viewer', 'expand_document_viewer')`, databaseSchema).Scan(&installed))
		require.Equal(t, map[bool]int{true: 2, false: 0}[on], installed, "per-relation explain and expand functions")
	}
	migrate(true)
	return db, dsn, migrate
}

// traceSample is the explain and expand requests the comparison runs: three
// objects of every type with tuples, every relation of their type, and a few
// subjects of every kind.
type traceSample struct {
	objects  []melange.Object
	rels     map[melange.ObjectType][]string
	subjects []melange.Object
}

func kitchenSinkSample(t *testing.T, db *sql.DB) traceSample {
	t.Helper()
	ctx := context.Background()
	types, err := parser.ParseSchema(kitchenSinkSchemaPath)
	require.NoError(t, err)
	s := traceSample{rels: map[melange.ObjectType][]string{}}
	for _, typ := range types {
		for _, r := range typ.Relations {
			s.rels[melange.ObjectType(typ.Name)] = append(s.rels[melange.ObjectType(typ.Name)], r.Name)
		}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT object_type, object_id FROM (
			SELECT object_type, object_id, row_number() OVER (PARTITION BY object_type ORDER BY object_id) AS n
			FROM (SELECT DISTINCT object_type, object_id FROM kitchen_sink_tuples) d) o
		WHERE n <= 3 ORDER BY 1, 2`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var o melange.Object
		require.NoError(t, rows.Scan(&o.Type, &o.ID))
		s.objects = append(s.objects, o)
	}
	require.NoError(t, rows.Err())
	var usersetSubject string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT subject_id FROM kitchen_sink_tuples
		WHERE subject_type = 'group' AND subject_id LIKE '%#member' ORDER BY 1 LIMIT 1`).Scan(&usersetSubject))
	s.subjects = []melange.Object{
		{Type: "user", ID: "u0"},
		{Type: "user", ID: "u1"},
		{Type: "user", ID: "u7"},
		{Type: "service_account", ID: "sa0"},
		{Type: "group", ID: usersetSubject},
	}
	require.Greater(t, len(s.objects), 20)
	return s
}

// traceResults is every answer for a sample, each a JSON document or an error.
type traceResults map[string]string

func answer(v any, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// runSample asks every explain (when explain) and expand (when expand)
// request of s through one Checker.
func runSample(ctx context.Context, c *melange.Checker, s traceSample, explain, expand bool, out traceResults) {
	for _, o := range s.objects {
		for _, rel := range s.rels[o.Type] {
			r := melange.Relation(rel)
			if explain {
				for _, subj := range s.subjects {
					trace, err := c.Explain(ctx, subj, r, o)
					out[fmt.Sprintf("explain %s %s %s", subj, rel, o)] = answer(trace, err)
				}
			}
			if expand {
				tree, err := c.Expand(ctx, o, r)
				out[fmt.Sprintf("expand %s %s", o, rel)] = answer(tree, err)
				users, err := c.ExpandRecursive(ctx, o, r)
				out[fmt.Sprintf("expand --recursive %s %s", o, rel)] = answer(users, err)
			}
		}
	}
}

// Explain, Expand and ExpandRecursive on demand give the answers the
// installed functions give, over the kitchen-sink model (implied, TTU,
// userset, intersection, exclusion and wildcard relations), in public and
// in another schema.
func TestOnDemand_MatchesInstalled(t *testing.T) {
	for _, databaseSchema := range []string{"public", "authz"} {
		t.Run(databaseSchema, func(t *testing.T) {
			db, _, migrate := kitchenSinkRecorded(t, databaseSchema)
			ctx := context.Background()
			sample := kitchenSinkSample(t, db)

			installed := traceResults{}
			require.False(t, sqlapi.Load(ctx, db, databaseSchema).OnDemand(sqlapi.Explain))
			runSample(ctx, melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema)), sample, true, true, installed)

			migrate(false)
			api := sqlapi.Load(ctx, db, databaseSchema)
			require.True(t, api.OnDemand(sqlapi.Explain))
			require.True(t, api.OnDemand(sqlapi.Expand))
			onDemand := traceResults{}
			// One transaction per family, holding only that family, as
			// melange expand --recursive runs every call in one.
			for _, f := range []sqlapi.Family{sqlapi.Explain, sqlapi.Expand} {
				require.NoError(t, api.Trace(ctx, db, databaseSchema, f, nil, func(c *melange.Checker) error {
					runSample(ctx, c, sample, f.Name() == "explain", f.Name() == "expand", onDemand)
					return nil
				}))
			}
			require.Equal(t, len(installed), len(onDemand))
			granted := 0
			for key, want := range installed {
				assert.Equal(t, want, onDemand[key], key)
				assert.NotContains(t, want, "error:", key)
				if strings.HasPrefix(key, "explain") && strings.Contains(want, `"result":true`) {
					granted++
				}
			}
			assert.Greater(t, granted, 50, "the sample grants some explains")
		})
	}
}

// studioTimes matches the timings studio pages show, which differ between
// any two runs.
var studioTimes = regexp.MustCompile(`\d[\d,]*\.\d+ ms`)

// Studio's explain, what-if explain, expand and expand drill-in on demand
// render what they render with the families installed, over the kitchen-sink
// model, in public and in another schema, on studio's read-only connection.
// What-if is the one on-demand call with setup: the explain functions in
// pg_temp must read the temporary melange_tuples view it creates after them
// as the installed ones do. Each what-if copies an object's tuples onto a new
// object, with every user given to user:wi, so user:wi reaches the copy
// through the same direct, implied, TTU, userset, wildcard, intersection and
// exclusion paths the original's users do.
func TestStudio_OnDemandMatchesInstalled(t *testing.T) {
	for _, databaseSchema := range []string{"public", "authz"} {
		t.Run(databaseSchema, func(t *testing.T) {
			db, dsn, migrate := kitchenSinkRecorded(t, databaseSchema)
			ctx := context.Background()
			sample := kitchenSinkSample(t, db)
			c := newStudio(t, openStudioDB(t, dsn), databaseSchema)

			type request struct {
				label string
				form  url.Values // posted to /query; nil for a GET of path
				path  string
			}
			var requests []request
			// Studio refuses a what-if tuple the model does not accept
			// directly, such as the fixtures' group nesting stored without
			// #member, which the model ignores.
			types, err := parser.ParseSchema(kitchenSinkSchemaPath)
			require.NoError(t, err)
			accepts := func(objectType melange.ObjectType, rel string, subject melange.Object) bool {
				id, usersetRel, _ := strings.Cut(subject.ID, "#")
				for _, typ := range types {
					for _, r := range typ.Relations {
						if typ.Name != string(objectType) || r.Name != rel {
							continue
						}
						for _, ref := range r.SubjectTypeRefs {
							if ref.Type == string(subject.Type) && ref.Relation == usersetRel && (id == "*") == ref.Wildcard {
								return true
							}
						}
					}
				}
				return false
			}
			seen := map[melange.ObjectType]bool{}
			for _, o := range sample.objects {
				if seen[o.Type] {
					continue
				}
				seen[o.Type] = true
				copied := melange.Object{Type: o.Type, ID: "wi-" + o.ID}
				var whatIf []string
				rows, err := db.QueryContext(ctx, `SELECT subject_type, subject_id, relation FROM kitchen_sink_tuples
					WHERE object_type = $1 AND object_id = $2 ORDER BY 1, 2, 3`, o.Type, o.ID)
				require.NoError(t, err)
				for rows.Next() {
					var subject melange.Object
					var rel string
					require.NoError(t, rows.Scan(&subject.Type, &subject.ID, &rel))
					if !accepts(o.Type, rel, subject) {
						continue
					}
					if subject.Type == "user" && subject.ID != "*" {
						subject.ID = "wi"
					}
					whatIf = append(whatIf, fmt.Sprintf("%s %s %s", subject, rel, copied))
				}
				require.NoError(t, rows.Err())
				_ = rows.Close()
				for _, rel := range sample.rels[o.Type] {
					requests = append(requests,
						request{label: fmt.Sprintf("explain user:u0 %s %s", rel, o), form: url.Values{
							"mode": {"explain"}, "subject": {"user:u0"}, "relation": {rel}, "object": {o.String()},
						}},
						request{label: fmt.Sprintf("what-if user:wi %s %s", rel, copied), form: url.Values{
							"mode": {"explain"}, "subject": {"user:wi"}, "relation": {rel}, "object": {copied.String()},
							"whatif": {strings.Join(whatIf, "\n")},
						}},
						request{label: fmt.Sprintf("expand %s %s", o, rel), form: url.Values{
							"mode": {"expand"}, "relation": {rel}, "object": {o.String()},
						}})
				}
			}
			render := func() map[string]string {
				t.Helper()
				out := map[string]string{}
				for _, r := range requests {
					var body string
					if r.form != nil {
						body = c.query(r.form)
					} else {
						body = c.get(r.path)
					}
					out[r.label] = studioTimes.ReplaceAllString(flat(body), "_ ms")
				}
				return out
			}

			installed := render()
			// Drill into the first usersets the installed expand pages link.
			drillIn := regexp.MustCompile(`/expand\?userset=[^"&\s]+`)
			links := map[string]bool{}
			for _, r := range requests {
				for _, link := range drillIn.FindAllString(installed[r.label], -1) {
					if len(links) < 25 && !links[link] {
						links[link] = true
						requests = append(requests, request{label: "drill in " + link, path: link})
					}
				}
			}
			require.NotEmpty(t, links)
			installed = render()

			migrate(false)
			require.True(t, sqlapi.Load(ctx, db, databaseSchema).OnDemand(sqlapi.Explain))
			onDemand := render()

			granted := regexp.MustCompile(`badge fail">denied</span>.*?→.*?badge ok">allowed</span>`)
			changed := 0
			for _, r := range requests {
				want := installed[r.label]
				assert.Equal(t, want, onDemand[r.label], r.label)
				assert.NotContains(t, want, `callout error`, r.label)
				if strings.HasPrefix(r.label, "what-if") && granted.MatchString(want) {
					changed++
				}
			}
			assert.Greater(t, changed, 10, "what-if grants some relations")
		})
	}
}

// readOnlyBegin begins every transaction READ ONLY, as on a connection
// where BEGIN READ WRITE has no effect.
type readOnlyBegin struct{ *sql.DB }

func (d readOnlyBegin) BeginTx(ctx context.Context, _ *sql.TxOptions) (*sql.Tx, error) {
	return d.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

// An on-demand call works on a session that defaults to read-only (as
// studio's does), closes the backend it compiled the functions on instead of
// returning it to the pool, leaves nothing behind in a session it does not
// close (a *sql.Conn the caller holds), and refuses clearly
// where it cannot create the functions: in a read-only transaction and
// without the TEMP privilege.
func TestOnDemand_SessionAndRefusals(t *testing.T) {
	db, dsn, migrate := kitchenSinkRecorded(t, "public")
	migrate(false)
	ctx := context.Background()
	db.SetMaxOpenConns(1)
	api := sqlapi.Load(ctx, db, "public")
	doc := melange.Object{Type: "document", ID: "d0"}

	run := func(db sqlapi.DB, f sqlapi.Family) error {
		return api.Trace(ctx, db, "public", f, nil, func(c *melange.Checker) error {
			if f.Name() == "explain" {
				trace, err := c.Explain(ctx, melange.Object{Type: "user", ID: "u0"}, melange.Relation("viewer"), doc)
				if err == nil && trace.Root == nil {
					err = fmt.Errorf("empty trace")
				}
				return err
			}
			_, err := c.ExpandRecursive(ctx, doc, melange.Relation("viewer"))
			return err
		})
	}
	for _, f := range []sqlapi.Family{sqlapi.Explain, sqlapi.Expand} {
		// The pool's one connection, which Trace takes, defaults to
		// read-only.
		_, err := db.ExecContext(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY")
		require.NoError(t, err)
		var pid int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
		require.NoError(t, run(db, f), f.Name())
		// PL/pgSQL never frees its compiled copies of the rolled-back
		// functions, so the backend that ran them must not be reused.
		var pidAfter int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pidAfter))
		assert.NotEqual(t, pid, pidAfter, "%s: the backend went back to the pool", f.Name())
		assert.Eventually(t, func() bool {
			var n int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1", pid).Scan(&n))
			return n == 0
		}, 5*time.Second, 20*time.Millisecond, "%s: the backend is still running", f.Name())
	}

	// On a connection the caller holds, which Trace does not close, the
	// session it ran in afterwards has no function in pg_temp, is outside a
	// transaction, and still defaults to read-only.
	t.Run("same session", func(t *testing.T) {
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()
		_, err = conn.ExecContext(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY")
		require.NoError(t, err)
		for _, f := range []sqlapi.Family{sqlapi.Explain, sqlapi.Expand} {
			require.NoError(t, run(conn, f), f.Name())
			var temp int
			require.NoError(t, conn.QueryRowContext(ctx,
				`SELECT count(*) FROM pg_proc WHERE pronamespace = pg_my_temp_schema()`).Scan(&temp))
			assert.Zero(t, temp, "%s: functions left in pg_temp", f.Name())
			var inTx bool
			require.NoError(t, conn.QueryRowContext(ctx, "SELECT now() <> statement_timestamp()").Scan(&inTx))
			assert.False(t, inTx, "%s: session left inside a transaction", f.Name())
			var readOnly string
			require.NoError(t, conn.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly))
			assert.Equal(t, "on", readOnly, "%s: session no longer defaults to read-only", f.Name())
		}
	})

	t.Run("read-only transaction", func(t *testing.T) {
		err := api.Trace(ctx, readOnlyBegin{db}, "public", sqlapi.Explain, nil, func(*melange.Checker) error {
			t.Fatal("ran")
			return nil
		})
		msg := requireOnDemandRefusal(t, err)
		assert.Contains(t, msg, "cannot execute CREATE FUNCTION in a read-only transaction")
	})

	t.Run("no TEMP privilege", func(t *testing.T) {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		role, password := "no_temp_"+hex.EncodeToString(b), hex.EncodeToString(b)
		quoted := pq.QuoteIdentifier(role)
		// db's session is read-only.
		admin, err := sql.Open("postgres", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = admin.Close() })
		var dbName string
		require.NoError(t, admin.QueryRowContext(ctx, "SELECT current_database()").Scan(&dbName))
		for _, stmt := range []string{
			`CREATE ROLE ` + quoted + ` LOGIN PASSWORD ` + pq.QuoteLiteral(password),
			`REVOKE TEMPORARY ON DATABASE ` + pq.QuoteIdentifier(dbName) + ` FROM PUBLIC`,
			`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + quoted,
		} {
			_, err := admin.ExecContext(ctx, stmt)
			require.NoError(t, err, stmt)
		}
		t.Cleanup(func() {
			_, _ = admin.ExecContext(context.Background(), `DROP OWNED BY `+quoted)
			_, _ = admin.ExecContext(context.Background(), `DROP ROLE `+quoted)
		})
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		u.User = url.UserPassword(role, password)
		limited, err := sql.Open("postgres", u.String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = limited.Close() })
		limitedAPI := sqlapi.Load(ctx, limited, "public")
		require.True(t, limitedAPI.OnDemand(sqlapi.Expand), "the role reads the record")
		err = limitedAPI.Trace(ctx, limited, "public", sqlapi.Expand, nil, func(*melange.Checker) error {
			t.Fatal("ran")
			return nil
		})
		msg := requireOnDemandRefusal(t, err)
		assert.Contains(t, msg, "permission denied to create temporary tables")
	})
}

// requireOnDemandRefusal asserts err says on demand failed and what it
// needs, without the raw database error, and returns its message.
func requireOnDemandRefusal(t *testing.T, err error) string {
	t.Helper()
	u := sqlapi.FromError(sqlapi.Explain, "document", "viewer", err)
	require.NotNil(t, u, "%v", err)
	msg := u.Error()
	for _, w := range []string{"on demand failed", "pg_temp", "writable primary", "TEMP privilege", "melange migrate"} {
		assert.Contains(t, msg, w)
	}
	for _, raw := range []string{"pq:", "SQLSTATE"} {
		assert.NotContains(t, msg, raw)
	}
	return msg
}

// On demand is refused, saying why, when the record's codegen version is not
// this melange's or the record has no model. A database deployed with the
// families off but without a record (versioned migration files) looks
// installed; its dispatcher raises M2003, which is reported with how on
// demand would apply.
func TestOnDemand_RefusedWithoutAMatchingRecord(t *testing.T) {
	ctx := context.Background()
	call := func(db *sql.DB) error {
		api := sqlapi.Load(ctx, db, "public")
		if u := api.Unavailable(sqlapi.Explain, "document", "viewer"); u != nil {
			return u
		}
		return api.Trace(ctx, db, "public", sqlapi.Explain, nil, func(c *melange.Checker) error {
			_, err := c.Explain(ctx, melange.Object{Type: "user", ID: "u0"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "d0"})
			return err
		})
	}
	message := func(t *testing.T, err error, label string) string {
		t.Helper()
		u := sqlapi.FromError(sqlapi.Explain, "document", "viewer", err)
		require.NotNil(t, u, "%v", err)
		assert.Equal(t, label, u.Label())
		return u.Error()
	}

	t.Run("codegen version", func(t *testing.T) {
		db, _, migrate := kitchenSinkRecorded(t, "public")
		migrate(false)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET codegen_version = 'v0.0.1'`)
		require.NoError(t, err)
		msg := message(t, call(db), "not installed")
		for _, w := range []string{"cannot compile them on demand", "generated by melange v0.0.1", "this is melange " + migrator.CodegenVersion(), "use melange v0.0.1", "sql.functions.explain"} {
			assert.Contains(t, msg, w)
		}
	})
	t.Run("record without a model", func(t *testing.T) {
		db, _, migrate := kitchenSinkRecorded(t, "public")
		migrate(false)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET schema_dsl = '', model_json = '{}'`)
		require.NoError(t, err)
		msg := message(t, call(db), "not installed")
		assert.Contains(t, msg, "recorded no model")
		assert.Contains(t, msg, "run melange migrate to record it")
	})
	t.Run("record with an unreadable model", func(t *testing.T) {
		db, _, migrate := kitchenSinkRecorded(t, "public")
		migrate(false)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET schema_dsl = 'not a model', model_json = '{}'`)
		require.NoError(t, err)
		msg := message(t, call(db), "not installed")
		assert.Contains(t, msg, "recorded cannot be read")
		assert.Contains(t, msg, "melange migrate --force")
		assert.NotContains(t, msg, "recorded no model")
	})
	t.Run("record with only the DSL", func(t *testing.T) {
		db, _, migrate := kitchenSinkRecorded(t, "public")
		migrate(false)
		_, err := db.ExecContext(ctx, `UPDATE melange_migrations SET model_json = '{}'`)
		require.NoError(t, err)
		require.NoError(t, call(db), "the recorded DSL is parsed instead")
	})
	t.Run("record with a name the schema language does not allow", func(t *testing.T) {
		db, _, migrate := kitchenSinkRecorded(t, "public")
		migrate(false)
		// A relation name that would end the comment line naming its
		// explain function and commit statements of its own.
		types, err := parser.ParseSchema(kitchenSinkSchemaPath)
		require.NoError(t, err)
		types = append(types, schema.TypeDefinition{Name: "probe", Relations: []schema.RelationDefinition{{
			Name:            "r\nCOMMIT; BEGIN READ WRITE; CREATE TABLE public.on_demand_injected (); COMMIT; BEGIN; --",
			SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}},
		}}})
		model, err := schema.MarshalModel(types)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE melange_migrations SET model_json = $1::jsonb`, string(model))
		require.NoError(t, err)
		err = call(db)
		var injected bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.on_demand_injected') IS NOT NULL`).Scan(&injected))
		assert.False(t, injected, "the script compiled from the record committed a statement of its own")
		assert.Contains(t, message(t, err, "not installed"), "a name the schema language does not allow")
	})
	t.Run("no record", func(t *testing.T) {
		db, _ := newTuplesDBWithDSN(t)
		types, err := parser.ParseSchema(kitchenSinkSchemaPath)
		require.NoError(t, err)
		// A migration that records nothing cannot turn the families off.
		requireExposureChangeRefused(t, migrator.NewMigrator(db, "").MigrateWithTypesAndOptions(ctx, types,
			migrator.InternalMigrateOptions{Exposure: traceFamilies(false)}), false)
		// Stand in for a database deployed without a record (versioned
		// migration files): migrate with one, then drop it.
		_, err = migrator.MigrateWithOptions(ctx, db, kitchenSinkSchemaPath, migrator.MigrateOptions{Exposure: traceFamilies(false)})
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `DROP TABLE melange_migrations`)
		require.NoError(t, err)
		require.True(t, sqlapi.Load(ctx, db, "public").Installed(sqlapi.Explain), "no record: the default exposure")
		msg := message(t, call(db), "not exposed")
		assert.Contains(t, msg, "with sql.functions.explain on")
		assert.Contains(t, msg, "melange migrate records the model melange compiles the explain functions from on demand")
	})
}

// melange explain and expand against the kitchen-sink model in another
// schema print the same on demand as with the families installed.
func TestCLI_OnDemandKitchenSink(t *testing.T) {
	db, dsn, migrate := kitchenSinkRecorded(t, "authz")
	cli := buildMelangeCLI(t)
	dir := t.TempDir()
	ctx := context.Background()
	var doc, folder, comment string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
		(SELECT min(object_id) FROM kitchen_sink_tuples WHERE object_type = 'document'),
		(SELECT min(object_id) FROM kitchen_sink_tuples WHERE object_type = 'folder'),
		(SELECT min(object_id) FROM kitchen_sink_tuples WHERE object_type = 'comment')`).Scan(&doc, &folder, &comment))
	subjects := []string{"user:u0", "user:u1", "service_account:sa0"}
	explains := [][2]string{
		{"can_view", "document:" + doc},
		{"can_comment", "document:" + doc},
		{"elevated", "document:" + doc},
		{"visible", "document:" + doc},
		{"co_owner", "folder:" + folder},
		{"can_read", "comment:" + comment},
	}
	expands := [][2]string{{"document:" + doc, "can_view"}, {"folder:" + folder, "viewer"}, {"comment:" + comment, "can_edit"}}
	calls := make([][]string, 0, len(subjects)*len(explains)+2*len(expands))
	for _, subject := range subjects {
		for _, q := range explains {
			calls = append(calls, []string{"explain", subject, q[0], q[1], "--format", "json"})
		}
	}
	for _, q := range expands {
		calls = append(calls,
			[]string{"expand", q[0], q[1], "--format", "json"},
			[]string{"expand", q[0], q[1], "--recursive"})
	}
	outputs := func() []string {
		t.Helper()
		out := make([]string, 0, len(calls))
		for _, args := range calls {
			stdout, stderr, err := cli.exec(dir, append(args, "--db", dsn, "--db-schema", "authz")...)
			require.NoError(t, err, "melange %v: %s", args, stderr)
			out = append(out, stdout)
		}
		return out
	}
	installed := outputs()
	assert.Contains(t, strings.Join(installed, ""), `"result": true`)
	migrate(false)
	_, err := db.ExecContext(ctx, `UPDATE authz.melange_migrations SET codegen_version = $1`, cliCodegenVersion(t, cli))
	require.NoError(t, err)
	onDemand := outputs()
	for i := range calls {
		assert.Equal(t, installed[i], onDemand[i], "melange %v", calls[i])
	}
}
