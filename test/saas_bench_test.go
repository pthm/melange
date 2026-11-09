package test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/pthm/melange/test/testutil"
)

// saasQuerier is what the SaaS benchmark operations run on: the pool for warm
// calls, or one session for the session benchmarks.
type saasQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// saasOp is one benchmarked call, run for a viewer.
type saasOp struct {
	name  string
	query string
	args  func(d *testutil.SaaSData, viewer string) []any
}

func saasCheck(name, relation, objectType string, object func(*testutil.SaaSData) string) saasOp {
	return saasOp{name, `SELECT check_permission('user', $1, $2, $3, $4)`, func(d *testutil.SaaSData, v string) []any {
		return []any{v, relation, objectType, object(d)}
	}}
}

func saasList(name, relation, objectType string, limit int) saasOp {
	return saasOp{name, `SELECT object_id FROM list_accessible_objects('user', $1, $2, $3, $4, NULL)`, func(_ *testutil.SaaSData, v string) []any {
		var l any
		if limit > 0 {
			l = limit
		}
		return []any{v, relation, objectType, l}
	}}
}

func saasApp(name, query string) saasOp {
	return saasOp{name, query, func(_ *testutil.SaaSData, v string) []any { return []any{v} }}
}

var saasChecks = []saasOp{
	saasCheck("OrgCanRead", "can_read", "organization", func(d *testutil.SaaSData) string { return d.BigOrg }),
	saasCheck("OrgCanReadSettings", "can_read_settings", "organization", func(d *testutil.SaaSData) string { return d.BigOrg }),
	saasCheck("ContactCanRead", "can_read", "contact", func(d *testutil.SaaSData) string { return d.Contact }),
	saasCheck("ContactCanReadPII", "can_read_pii", "contact", func(d *testutil.SaaSData) string { return d.Contact }),
	saasCheck("IncidentCanComment", "can_comment", "incident", func(d *testutil.SaaSData) string { return d.Incident }),
	saasCheck("IncidentCanBeMentioned", "can_be_mentioned", "incident", func(d *testutil.SaaSData) string { return d.Incident }),
	saasCheck("DealCanEdit", "can_edit", "deal", func(d *testutil.SaaSData) string { return d.Deal }),
	saasCheck("InvoiceCanApprove", "can_approve", "invoice", func(d *testutil.SaaSData) string { return d.Invoice }),
	saasCheck("DocumentCanRead", "can_read", "document", func(d *testutil.SaaSData) string { return d.Document }),
}

var saasLists = []saasOp{
	saasList("OrgsCanRead", "can_read", "organization", 0),
	saasList("OrgsCanReadSettings", "can_read_settings", "organization", 0),
	saasList("OrgsContactReader", "contact_reader", "organization", 0),
	saasList("OrgsCanSwitchTo", "can_switch_to", "organization", 0),
	saasList("IncidentsCanRead", "can_read", "incident", 0),
	saasList("ContactsCanRead_Page50", "can_read", "contact", 50),
	saasList("DocumentsCanRead_Page50", "can_read", "document", 50),
}

// saasAppQueries are the application's own queries over the contacts table,
// filtered by org through the parent-key function: a count, and the newest
// page sorted after the filter or merged from each org's newest (the form the
// SQL API reference recommends).
var saasAppQueries = []saasOp{
	saasApp("ContactsCountByOrg", `SELECT count(*) FROM contacts
		WHERE org_id IN (SELECT * FROM accessible_contact_can_read_by_org('user', $1))`),
	saasApp("ContactsNewestByOrg_Page50", `SELECT id FROM contacts
		WHERE org_id IN (SELECT * FROM accessible_contact_can_read_by_org('user', $1))
		ORDER BY created_at DESC LIMIT 50`),
	saasApp("ContactsNewestPerOrg_Page50", `SELECT c.id FROM accessible_contact_can_read_by_org('user', $1) AS o(org_id),
		LATERAL (SELECT id, created_at FROM contacts WHERE org_id = o.org_id ORDER BY created_at DESC LIMIT 50) c
		ORDER BY c.created_at DESC LIMIT 50`),
}

// saasSessionOps are measured on fresh sessions too: the calls a service
// makes first on a new connection.
var saasSessionOps = []saasOp{
	saasChecks[0], // OrgCanRead
	saasChecks[2], // ContactCanRead
	saasLists[1],  // OrgsCanReadSettings
	saasLists[5],  // ContactsCanRead_Page50
}

// BenchmarkSaaS measures the SaaS model (saas_schema.fga) over its 27-branch
// view, for four viewers: support staff, a member of the largest org, a
// partner user reaching it through a client role, and a member of a small
// org. Warm calls run on the pool. FirstCall and Calls2to5 run on fresh
// sessions: the first call plans the generated functions and the view, and
// calls 2 to 5 run before a plan cache's custom-plan count is used up.
//
//	just bench-saas 1M
//
// The 5M scale, with production's contact volume, needs
// MELANGE_BENCH_LARGE_SCALE=1.
func BenchmarkSaaS(b *testing.B) {
	for _, scale := range testutil.SaaSScales {
		b.Run(scale.Name, func(b *testing.B) {
			if scale.Large() && os.Getenv("MELANGE_BENCH_LARGE_SCALE") != "1" {
				b.Skipf("SaaS scale %s requires MELANGE_BENCH_LARGE_SCALE=1", scale.Name)
			}
			d := testutil.SetupSaaSDB(b, scale)
			testutil.EnableServerStats(d.DB)
			viewers := []struct{ name, id string }{
				{"Staff", d.Staff}, {"BigMember", d.BigMember}, {"Partner", d.PartnerUser}, {"SmallMember", d.SmallMember},
			}

			for _, group := range []struct {
				name string
				ops  []saasOp
			}{{"Check", saasChecks}, {"List", saasLists}, {"App", saasAppQueries}} {
				for _, op := range group.ops {
					for _, v := range viewers {
						b.Run(group.name+"/"+op.name+"/"+v.name, func(b *testing.B) {
							args := op.args(d, v.id)
							testutil.BenchLoop(b, d.DB, func() { runSaaSOp(b, d.DB, op.query, args) })
						})
					}
				}
			}

			b.Run("ListSubjects/BigOrgContactReader", func(b *testing.B) {
				testutil.BenchLoop(b, d.DB, func() {
					runSaaSOp(b, d.DB, `SELECT subject_id FROM list_accessible_subjects('organization', $1, 'contact_reader', 'user')`, []any{d.BigOrg})
				})
			})
			b.Run("ListSubjects/IncidentCanBeMentioned", func(b *testing.B) {
				testutil.BenchLoop(b, d.DB, func() {
					runSaaSOp(b, d.DB, `SELECT subject_id FROM list_accessible_subjects('incident', $1, 'can_be_mentioned', 'user')`, []any{d.Incident})
				})
			})

			for _, op := range saasSessionOps {
				for _, v := range viewers[:2] {
					args := op.args(d, v.id)
					b.Run("FirstCall/"+op.name+"/"+v.name, func(b *testing.B) {
						benchSaaSSession(b, d.DSN, 0, 1, op.query, args)
					})
					b.Run("Calls2to5/"+op.name+"/"+v.name, func(b *testing.B) {
						benchSaaSSession(b, d.DSN, 1, 4, op.query, args)
					})
				}
			}
		})
	}
}

// runSaaSOp runs query and reads every row.
func runSaaSOp(b *testing.B, q saasQuerier, query string, args []any) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var v sql.RawBytes
	for rows.Next() {
		if err := rows.Scan(&v); err != nil {
			b.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
}

// benchSaaSSession times calls of query on a fresh session per iteration,
// after skip untimed calls. Connecting is not timed. ns/call is the time per
// timed call.
func benchSaaSSession(b *testing.B, dsn string, skip, calls int, query string, args []any) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxIdleConns(0) // a released session is closed, so each Conn is new
	ctx := context.Background()

	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if err := conn.PingContext(ctx); err != nil {
			b.Fatal(err)
		}
		for range skip {
			runSaaSOp(b, conn, query, args)
		}
		b.StartTimer()
		for range calls {
			runSaaSOp(b, conn, query, args)
		}
		b.StopTimer()
		_ = conn.Close()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*calls), "ns/call")
}
