package test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/studio"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/test/testutil"
)

// Studio integration tests drive the real HTTP handler against a migrated
// database (test/testutil/testdata), opened read-only exactly as the
// `melange studio` command does. Each runs under the public schema and a
// custom one.

// studioFixture is the seeded data: pageUsers users, all in org main
// (3 owners, 3 admins, the rest members); heavy (users[0]) is also a member
// of every org in extra, so it is the subject of exactly two pages of tuples
// (200 = 2*pageSize, the "more rows" boundary).
type studioFixture struct {
	dsn    string  // admin DSN of the test database
	admin  *sql.DB // superuser, read-write
	schema string
	users  []string // user ids, ascending
	even   []string // users whose username contains "even", ascending
	main   string   // org id
	extra  []string // org ids, ascending
}

const pageUsers = 250

func (f *studioFixture) heavy() string { return f.users[0] }

// studioRole is main's role for users[i].
func studioRole(i int) string {
	switch {
	case i < 3:
		return "owner"
	case i < 6:
		return "admin"
	}
	return "member"
}

// seedStudio returns a fresh migrated database for databaseSchema with the
// studio fixture loaded. It always uses the container (like
// TestDriverCompatibility), even when DATABASE_URL points at a shared
// database: studio needs the deployed model recorded, and recording it in a
// shared database would change what later doctor and status tests see.
func seedStudio(t *testing.T, databaseSchema string) *studioFixture {
	t.Helper()
	f := &studioFixture{schema: databaseSchema, dsn: testutil.DSNWithDatabaseSchema(t, databaseSchema)}
	db, err := sql.Open("postgres", f.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	f.admin = db
	ctx := context.Background()

	// testutil migrates without the DSL; studio reads the model from the
	// migration record, as `melange migrate` writes it.
	dsl, err := os.ReadFile("testutil/testdata/schema.fga")
	require.NoError(t, err)
	types, err := parser.ParseSchemaString(string(dsl))
	require.NoError(t, err)
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)
	require.NoError(t, m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{SchemaContent: string(dsl)}))

	_, err = f.admin.ExecContext(ctx, `
		INSERT INTO users (username)
		SELECT format('studio-%s-%s', CASE WHEN g % 2 = 0 THEN 'even' ELSE 'odd' END, g)
		FROM generate_series(1, $1) g`, pageUsers)
	require.NoError(t, err)
	rows, err := f.admin.QueryContext(ctx, `SELECT id::text, username FROM users WHERE username LIKE 'studio-%' ORDER BY users.id`)
	require.NoError(t, err)
	for rows.Next() {
		var id, name string
		require.NoError(t, rows.Scan(&id, &name))
		f.users = append(f.users, id)
		if strings.Contains(name, "even") {
			f.even = append(f.even, id)
		}
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, f.users, pageUsers)

	require.NoError(t, f.admin.QueryRowContext(ctx, `INSERT INTO organizations (name) VALUES ('studio-main') RETURNING id::text`).Scan(&f.main))
	for i, u := range f.users {
		_, err := f.admin.ExecContext(ctx, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)`, f.main, u, studioRole(i))
		require.NoError(t, err)
	}
	_, err = f.admin.ExecContext(ctx, `INSERT INTO organizations (name) SELECT 'studio-org-' || g FROM generate_series(1, 199) g`)
	require.NoError(t, err)
	_, err = f.admin.ExecContext(ctx, `
		INSERT INTO organization_members (organization_id, user_id, role)
		SELECT id, $1, 'member' FROM organizations WHERE name LIKE 'studio-org-%'`, f.heavy())
	require.NoError(t, err)
	require.NoError(t, queryStrings(f.admin, `SELECT id::text FROM organizations WHERE name LIKE 'studio-org-%' ORDER BY organizations.id`, &f.extra))
	return f
}

// wrapTuples renames the tuples view to melange_tuples_base and recreates
// melange_tuples as query, which selects from it (qualified with the
// fixture's schema). Call it before newStudio, which discovers labels from
// the view.
func (f *studioFixture) wrapTuples(t *testing.T, query string) {
	t.Helper()
	s := pq.QuoteIdentifier(f.schema)
	_, err := f.admin.Exec(`ALTER VIEW ` + s + `.melange_tuples RENAME TO melange_tuples_base`)
	require.NoError(t, err)
	_, err = f.admin.Exec(`CREATE VIEW ` + s + `.melange_tuples AS ` + query)
	require.NoError(t, err)
}

func queryStrings(db *sql.DB, query string, out *[]string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		*out = append(*out, s)
	}
	return rows.Err()
}

// openStudioDB opens dsn the way the studio command does: lib/pq, with the
// read-only runtime parameter, verified.
func openStudioDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	ro, err := studio.ReadOnlyDSN(dsn)
	require.NoError(t, err)
	db, err := sql.Open("postgres", ro)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, studio.VerifyReadOnly(context.Background(), db))
	return db
}

// studioClient talks to a studio handler over real HTTP.
type studioClient struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newStudio(t *testing.T, db *sql.DB, databaseSchema string) studioClient {
	t.Helper()
	srv := httptest.NewServer(studio.New(db, databaseSchema, "", "").Handler())
	t.Cleanup(srv.Close)
	return studioClient{t: t, base: srv.URL, c: srv.Client()}
}

func (c studioClient) do(req *http.Request) (resp *http.Response, body string) {
	c.t.Helper()
	resp, err := c.c.Do(req)
	require.NoError(c.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	return resp, string(b)
}

// on returns c reporting to t, for use inside a subtest.
func (c studioClient) on(t *testing.T) studioClient {
	c.t = t
	return c
}

// get fetches path and requires a 200.
func (c studioClient) get(path string) string {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.base+path, http.NoBody)
	require.NoError(c.t, err)
	resp, body := c.do(req)
	require.Equal(c.t, http.StatusOK, resp.StatusCode, "GET %s: %s", path, body)
	return body
}

// query posts the Model page's query form.
func (c studioClient) query(form url.Values) string {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.base+"/query", strings.NewReader(form.Encode()))
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, body := c.do(req)
	require.Equal(c.t, http.StatusOK, resp.StatusCode, body)
	return body
}

var moreRowsRe = regexp.MustCompile(`class="more-rows" hx-get="([^"]+)"`)

// entityLinks matches links to entities of one type, capturing the id.
func entityLinks(typ string) *regexp.Regexp {
	return regexp.MustCompile(`href="/entity\?id=([^&"]+)&amp;type=` + regexp.QuoteMeta(typ) + `"`)
}

// walk follows a list's infinite-scroll sentinels from path until a page
// has none, the way htmx would, returning every id in order and the number
// of pages fetched.
func (c studioClient) walk(path string, ids *regexp.Regexp) (got []string, pages int) {
	c.t.Helper()
	for path != "" {
		require.Less(c.t, pages, 50, "runaway pagination")
		body := c.get(path)
		pages++
		for _, m := range ids.FindAllStringSubmatch(body, -1) {
			id, err := url.QueryUnescape(m[1])
			require.NoError(c.t, err)
			got = append(got, id)
		}
		path = ""
		if m := moreRowsRe.FindAllStringSubmatch(body, -1); len(m) > 0 {
			require.Len(c.t, m, 1, "one sentinel per page")
			path = html.UnescapeString(m[0][1])
		}
	}
	return got, pages
}

func byNumber(ids []string) []string {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b string) int {
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x - y
	})
	return out
}

func TestStudio_Pagination(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		f := seedStudio(t, databaseSchema)
		// 100 pairs of team ids that pad to the same natural-order key
		// ("0001" and "1"), so browse order depends on its raw-id
		// tie-breaker, and 200 = 2*pageSize of them.
		teams := make([]string, 0, 200)
		for i := 1; i <= 100; i++ {
			teams = append(teams, fmt.Sprintf("%04d", i), strconv.Itoa(i))
		}
		f.wrapTuples(t, `SELECT * FROM `+pq.QuoteIdentifier(databaseSchema)+`.melange_tuples_base
			UNION ALL
			SELECT 'user', '`+f.users[1]+`', 'member', 'team', id
			FROM generate_series(1, 100) g, LATERAL (VALUES (lpad(g::text, 4, '0')), (g::text)) v(id)`)
		c := newStudio(t, openStudioDB(t, f.dsn), databaseSchema)
		users, orgs := entityLinks("user"), entityLinks("organization")

		t.Run("browse via sentinels", func(t *testing.T) {
			c := c.on(t)
			got, pages := c.walk("/browse/user", users)
			assert.Equal(t, f.users, got)
			assert.Equal(t, 3, pages)
			got, pages = c.walk("/browse/organization", orgs)
			assert.Equal(t, append([]string{f.main}, f.extra...), got)
			assert.Equal(t, 2, pages, "a full last page has no sentinel")
			got, pages = c.walk("/browse/team", entityLinks("team"))
			assert.Equal(t, teams, got, "ties on the natural key in raw-id order")
			assert.Equal(t, 2, pages)
		})
		t.Run("browse via offset", func(t *testing.T) {
			c := c.on(t)
			var got []string
			for offset := 0; offset < 1000; offset += 100 {
				body := c.get(fmt.Sprintf("/browse/user/list?offset=%d", offset))
				for _, m := range users.FindAllStringSubmatch(body, -1) {
					got = append(got, m[1])
				}
				if !moreRowsRe.MatchString(body) {
					break
				}
			}
			assert.Equal(t, f.users, got)
		})
		t.Run("browse filter", func(t *testing.T) {
			c := c.on(t)
			got, pages := c.walk("/browse/user/list?q=EVEN", users)
			assert.Equal(t, f.even, got, "q matches labels case-insensitively")
			assert.Equal(t, 2, pages)
		})
		t.Run("access group", func(t *testing.T) {
			c := c.on(t)
			path := "/entity/group?" + url.Values{"type": {"organization"}, "id": {f.main}, "tab": {"by"}, "group": {"user"}}.Encode()
			got, pages := c.walk(path, users)
			assert.Equal(t, byNumber(f.users), got)
			assert.Equal(t, 3, pages)
			first := c.get(path)
			assert.Contains(t, first, "250 user entities")
			// An owner holds every relation the model derives from owner,
			// merged into one row.
			owner := regexp.MustCompile(`(?s)id=` + f.users[0] + `&amp;type=user".*?</li>`).FindString(first)
			for _, rel := range []string{"owner", "admin", "member", "can_read", "can_admin", "can_delete"} {
				assert.Contains(t, owner, ">"+rel+"</button>")
			}

			got, pages = c.walk(path+"&q=even", users)
			assert.Equal(t, byNumber(f.even), got)
			assert.Equal(t, 2, pages)

			badge := c.get("/entity/count?" + url.Values{"type": {"organization"}, "id": {f.main}, "tab": {"by"}, "group": {"user"}}.Encode())
			assert.Contains(t, badge, ">250<")

			can := "/entity/group?" + url.Values{"type": {"user"}, "id": {f.heavy()}, "tab": {"can"}, "group": {"organization"}}.Encode()
			got, pages = c.walk(can, orgs)
			assert.Equal(t, byNumber(append([]string{f.main}, f.extra...)), got)
			assert.Equal(t, 2, pages)
		})
		t.Run("direct tuples", func(t *testing.T) {
			c := c.on(t)
			// Model order of relations (owner, admin, member), then the raw ids.
			rank := map[string]int{"owner": 0, "admin": 1, "member": 2}
			want := slices.Clone(f.users)
			slices.SortStableFunc(want, func(a, b string) int {
				ra, rb := rank[studioRole(slices.Index(f.users, a))], rank[studioRole(slices.Index(f.users, b))]
				if ra != rb {
					return ra - rb
				}
				return strings.Compare(a, b)
			})
			obj := "/entity/direct?" + url.Values{"type": {"organization"}, "id": {f.main}, "side": {"object"}}.Encode()
			got, pages := c.walk(obj, users)
			assert.Equal(t, want, got)
			assert.Equal(t, 3, pages)

			got, pages = c.walk(obj+"&q=member", users)
			assert.Equal(t, want[6:], got)
			assert.Equal(t, 3, pages)

			extra := slices.Clone(f.extra)
			slices.Sort(extra)
			sub := "/entity/direct?" + url.Values{"type": {"user"}, "id": {f.heavy()}, "side": {"subject"}}.Encode()
			got, pages = c.walk(sub, orgs)
			assert.Equal(t, append([]string{f.main}, extra...), got, "owner first, then members by raw id")
			assert.Equal(t, 2, pages, "a full last page has no sentinel")

			page := c.get("/entity?" + url.Values{"type": {"organization"}, "id": {f.main}, "tab": {"direct"}, "q": {"member"}}.Encode())
			assert.Contains(t, page, "244 of 250")
		})
	})
}

func TestStudio_QueriesAndPages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		f := seedStudio(t, databaseSchema)
		c := newStudio(t, openStudioDB(t, f.dsn), databaseSchema)
		owner, member := "user:"+f.users[0], "user:"+f.users[10]
		org := "organization:" + f.main

		t.Run("model page", func(t *testing.T) {
			c := c.on(t)
			body := c.get("/")
			assert.Contains(t, body, `hx-post="/query"`)
			assert.Contains(t, body, "pull_request", "deployed model types are listed")
			assert.Contains(t, body, `<meta name="htmx-config" content='{"allowEval":false,"includeIndicatorStyles":false}'>`,
				"htmx needs eval and inline styles off under the CSP")
		})
		t.Run("explain", func(t *testing.T) {
			c := c.on(t)
			body := c.query(url.Values{"mode": {"explain"}, "subject": {member}, "relation": {"can_read"}, "object": {org}})
			assert.Contains(t, body, `<span class="badge ok">allowed</span>`)
			body = c.query(url.Values{"mode": {"explain"}, "subject": {member}, "relation": {"can_delete"}, "object": {org}})
			assert.Contains(t, body, `<span class="badge fail">denied</span>`)
		})
		t.Run("matrix", func(t *testing.T) {
			c := c.on(t)
			body := c.query(url.Values{"mode": {"matrix"}, "subject": {owner}, "object": {org}})
			assert.Contains(t, body, "6 of 7 relations allowed", "all but billing_manager")
			body = c.query(url.Values{"mode": {"matrix"}, "subject": {member}, "object": {org}})
			assert.Contains(t, body, "2 of 7 relations allowed", "member and can_read")
		})
		t.Run("expand", func(t *testing.T) {
			c := c.on(t)
			body := c.query(url.Values{"mode": {"expand"}, "relation": {"owner"}, "object": {org}})
			assert.Contains(t, body, owner)
		})
		t.Run("analyze as superuser", func(t *testing.T) {
			c := c.on(t)
			body := c.query(url.Values{"mode": {"analyze"}, "subject": {member}, "relation": {"can_read"}, "object": {org}})
			assert.Contains(t, body, `<span class="badge ok">allowed</span>`)
			assert.Contains(t, body, "Execution Time")
			assert.Contains(t, body, `<a href="/relation?relation=can_read&amp;type=organization"><code>check_organization_can_read</code></a>`, "per-function times with track_functions")
		})
		t.Run("relation page", func(t *testing.T) {
			c := c.on(t)
			body := c.get("/relation?type=organization&relation=member")
			for _, fn := range []string{"check_organization_member", "list_organization_member_obj", "list_organization_member_sub", "explain_organization_member", "expand_organization_member"} {
				assert.Contains(t, body, fn)
			}
			assert.Contains(t, body, `<span class="f-kw">FUNCTION</span> <span class="f-name">`+f.schema+`.check_organization_member</span>`,
				"the deployed definition, from the configured schema's pg_proc")
		})
		t.Run("tuples index", func(t *testing.T) {
			c := c.on(t)
			body := c.get("/tuples")
			assert.Contains(t, body, "250 entities")
			assert.Contains(t, body, "200 entities")
		})
		t.Run("labels", func(t *testing.T) {
			c := c.on(t)
			body := c.get("/browse/organization")
			assert.Contains(t, body, "studio-main", "labels discovered through the tuples view")
		})
	})
}

func TestStudio_ReadOnlyAndWhatIf(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		f := seedStudio(t, databaseSchema)
		db := openStudioDB(t, f.dsn)
		db.SetMaxOpenConns(1) // every request and check below shares one backend
		c := newStudio(t, db, databaseSchema)
		ctx := context.Background()

		var pid int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
		assertReadOnly := func(t *testing.T) {
			t.Helper()
			_, err := db.ExecContext(ctx, `INSERT INTO users (username) VALUES ('studio-write')`)
			var pqErr *pq.Error
			require.ErrorAs(t, err, &pqErr)
			assert.Equal(t, "25006", string(pqErr.Code), "read_only_sql_transaction: %v", err)
			var v string
			require.NoError(t, db.QueryRowContext(ctx, "SHOW default_transaction_read_only").Scan(&v))
			assert.Equal(t, "on", v)
		}
		assertReadOnly(t)

		outsider := "user:" + f.users[10]
		org := "organization:" + f.extra[0]
		form := url.Values{"mode": {"explain"}, "subject": {outsider}, "relation": {"can_read"}, "object": {org}}
		before := c.query(form)
		assert.Contains(t, before, `<span class="badge fail">denied</span>`)

		form.Set("whatif", outsider+" member "+org)
		with := c.query(form)
		require.Contains(t, with, `<span class="badge accent">what-if</span>`, with)
		assert.Regexp(t, `(?s)badge fail">denied</span>.*?→.*?badge ok">allowed</span>`, with, "denied without, allowed with")

		// Same backend afterwards: nothing leaked from the what-if
		// transaction, and the connection is still read-only.
		var pidAfter int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pidAfter))
		require.Equal(t, pid, pidAfter, "the pool reused its one connection")
		var temp sql.NullString
		require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('pg_temp.melange_tuples')::text").Scan(&temp))
		assert.False(t, temp.Valid, "temp view %s outlived the what-if", temp.String)
		var inTx bool
		require.NoError(t, db.QueryRowContext(ctx, "SELECT now() <> statement_timestamp()").Scan(&inTx))
		assert.False(t, inTx, "connection left inside a transaction")
		assertReadOnly(t)
		form.Del("whatif")
		assert.Contains(t, c.query(form), `<span class="badge fail">denied</span>`, "what-if tuples gone")

		// Bad what-if input is rejected before anything runs.
		form.Set("whatif", outsider+" nonsense "+org)
		assert.Contains(t, c.query(form), "relation nonsense not defined on organization")

		// The check itself runs read-only: a tuples view that writes
		// whenever the what-if temp view is in place fails the what-if.
		// OFFSET 0 keeps the check's quals out of the view, so the probe
		// runs on every row, not just matching ones.
		s := pq.QuoteIdentifier(databaseSchema)
		_, err := f.admin.ExecContext(ctx, `
			CREATE TABLE `+s+`.studio_probe (n int);
			CREATE FUNCTION `+s+`.studio_probe() RETURNS boolean VOLATILE LANGUAGE plpgsql AS $$
			BEGIN
				IF to_regclass('pg_temp.melange_tuples') IS NOT NULL THEN
					INSERT INTO `+s+`.studio_probe VALUES (1);
				END IF;
				RETURN true;
			END $$`)
		require.NoError(t, err)
		f.wrapTuples(t, `SELECT * FROM `+s+`.melange_tuples_base WHERE `+s+`.studio_probe() OFFSET 0`)
		form.Set("whatif", outsider+" member "+org)
		assert.Contains(t, c.query(form), "cannot execute INSERT in a read-only transaction")
	})
}

// TestStudio_LimitedRole runs studio as a role with only what it needs:
//   - USAGE on the melange schema and SELECT on its tables and views
//     (melange_tuples and melange's model/migration tables);
//   - SELECT on the domain tables that hold labels (optional: without it
//     entities show by id);
//   - EXECUTE on the generated functions and TEMP on the database (for
//     what-if), both granted to PUBLIC by default.
//
// It cannot set track_functions, so Analyze returns a plan and verdict and
// says why per-function times are missing.
func TestStudio_LimitedRole(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	runTestWithSchema(t, func(t *testing.T, databaseSchema string) {
		f := seedStudio(t, databaseSchema)
		ctx := context.Background()
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		role, password := "studio_ro_"+hex.EncodeToString(b), hex.EncodeToString(b)
		quoted := pq.QuoteIdentifier(role)
		_, err := f.admin.ExecContext(ctx, `CREATE ROLE `+quoted+` LOGIN PASSWORD `+pq.QuoteLiteral(password))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = f.admin.ExecContext(context.Background(), `DROP OWNED BY `+quoted)
			_, _ = f.admin.ExecContext(context.Background(), `DROP ROLE `+quoted)
		})
		s := pq.QuoteIdentifier(databaseSchema)
		for _, grant := range []string{
			`GRANT USAGE ON SCHEMA ` + s + ` TO ` + quoted,
			`GRANT SELECT ON ALL TABLES IN SCHEMA ` + s + ` TO ` + quoted,
			`GRANT SELECT ON users, organizations TO ` + quoted,
		} {
			_, err := f.admin.ExecContext(ctx, grant)
			require.NoError(t, err, grant)
		}
		u, err := url.Parse(f.dsn)
		require.NoError(t, err)
		u.User = url.UserPassword(role, password)
		c := newStudio(t, openStudioDB(t, u.String()), databaseSchema)

		member, org := "user:"+f.users[10], "organization:"+f.main
		body := c.query(url.Values{"mode": {"analyze"}, "subject": {member}, "relation": {"can_read"}, "object": {org}})
		assert.Contains(t, body, `<span class="badge ok">allowed</span>`)
		assert.Contains(t, body, "Execution Time")
		assert.Contains(t, body, "Per-function times need <code>track_functions</code>")

		body = c.query(url.Values{"mode": {"explain"}, "subject": {member}, "relation": {"can_read"}, "object": {org}, "whatif": {member + " owner " + org}})
		assert.Contains(t, body, `<span class="badge accent">what-if</span>`, "what-if works with PUBLIC's TEMP")

		assert.Contains(t, c.get("/browse/organization"), "studio-main")
		assert.Contains(t, c.get("/"), "pull_request")
	})
}

func TestStudio_Headers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	f := seedStudio(t, "public")
	c := newStudio(t, openStudioDB(t, f.dsn), "public")

	req, err := http.NewRequest(http.MethodGet, c.base+"/", http.NoBody)
	require.NoError(t, err)
	resp, _ := c.do(req)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// The full policy: the in-package test compares against the constant.
	assert.Equal(t, "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
		"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "same-origin", resp.Header.Get("Cross-Origin-Opener-Policy"))
	assert.Equal(t, "same-origin", resp.Header.Get("Cross-Origin-Resource-Policy"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	req.Host = "studio.attacker.example"
	resp, _ = c.do(req)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "DNS-rebinding Host is refused")
}
