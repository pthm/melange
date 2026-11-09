package testutil

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// saasSchemaFGA is the multi-tenant SaaS schema of the SaaS scale benchmark:
// roles per product area in tiers, partner orgs reaching client orgs, support
// staff reading every production tenant, and area readers as intersections
// over those routes. See the schema's header.
//
//go:embed testdata/saas_schema.fga
var saasSchemaFGA string

// saasTablesSQL creates the SaaS domain tables and the melange_tuples view
// over them: 27 branches with joins, a cross join, an anti-join and a
// CASE-typed object type.
//
//go:embed testdata/saas_tables.sql
var saasTablesSQL string

// SaaSScale parameterises the deterministic SaaS data generator. Most tuples
// are contacts, spread over ContactOrgs orgs by a Zipf distribution, so one
// org holds about a quarter of them and the median org a fraction of a
// percent. Per-org counts of the other resources vary by up to 2.5x across
// orgs.
type SaaSScale struct {
	Name              string
	Orgs              int
	Partners          int // orgs 1..Partners are partner orgs with clients
	ClientsPerPartner int // average clients per partner; each client has one partner
	UsersPerOrg       int // members per org, about 7 of 17 roles each
	Staff             int // support staff, reading every production org (35%)
	Contacts          int
	ContactOrgs       int
	DealsPerOrg       int
	QueuesPerOrg      int
	TicketsPerOrg     int
	InvoicesPerOrg    int
	EmployeesPerOrg   int
	TeamsPerOrg       int
	FoldersPerOrg     int // a ternary folder tree per org
	DocsPerOrg        int
}

// SaaSScaleSmall is sized for the differential correctness test.
var SaaSScaleSmall = SaaSScale{
	Name: "small", Orgs: 16, Partners: 3, ClientsPerPartner: 3, UsersPerOrg: 5, Staff: 2,
	Contacts: 600, ContactOrgs: 6, DealsPerOrg: 6, QueuesPerOrg: 3, TicketsPerOrg: 8,
	InvoicesPerOrg: 5, EmployeesPerOrg: 5, TeamsPerOrg: 3, FoldersPerOrg: 7, DocsPerOrg: 10,
}

// SaaSScales are the benchmark sizes, named by their approximate tuple count.
// The 1M and 5M scales have production's tenant count; 5M has its contact
// volume too and needs MELANGE_BENCH_LARGE_SCALE=1.
var SaaSScales = []SaaSScale{
	{
		Name: "100K", Orgs: 80, Partners: 8, ClientsPerPartner: 5, UsersPerOrg: 6, Staff: 6,
		Contacts: 75_000, ContactOrgs: 20, DealsPerOrg: 60, QueuesPerOrg: 4, TicketsPerOrg: 80,
		InvoicesPerOrg: 40, EmployeesPerOrg: 30, TeamsPerOrg: 3, FoldersPerOrg: 13, DocsPerOrg: 60,
	},
	{
		Name: "1M", Orgs: 190, Partners: 24, ClientsPerPartner: 5, UsersPerOrg: 6, Staff: 24,
		Contacts: 900_000, ContactOrgs: 62, DealsPerOrg: 150, QueuesPerOrg: 4, TicketsPerOrg: 200,
		InvoicesPerOrg: 80, EmployeesPerOrg: 60, TeamsPerOrg: 3, FoldersPerOrg: 40, DocsPerOrg: 150,
	},
	{
		Name: "5M", Orgs: 190, Partners: 24, ClientsPerPartner: 5, UsersPerOrg: 6, Staff: 24,
		Contacts: 4_600_000, ContactOrgs: 62, DealsPerOrg: 150, QueuesPerOrg: 4, TicketsPerOrg: 200,
		InvoicesPerOrg: 80, EmployeesPerOrg: 60, TeamsPerOrg: 3, FoldersPerOrg: 40, DocsPerOrg: 150,
	},
}

// Large reports whether the scale needs MELANGE_BENCH_LARGE_SCALE=1.
func (s SaaSScale) Large() bool { return s.Contacts > 1_000_000 }

var saasOrgRoles = []string{
	"admin", "org_user", "sso_admin",
	"crm_manager", "crm_agent", "crm_viewer",
	"helpdesk_manager", "helpdesk_agent", "helpdesk_viewer",
	"billing_manager", "billing_clerk", "billing_viewer",
	"people_manager", "people_viewer",
	"docs_manager", "docs_editor", "docs_viewer",
}

var saasPartnerRoles = saasOrgRoles[3:]

// SaaSData is a loaded SaaS database and the subjects and objects the
// benchmarks use.
type SaaSData struct {
	DB  *sql.DB
	DSN string // for opening fresh sessions

	Staff       string // support staff: reads every production org without membership
	BigMember   string // a member of the largest org with a 7-role bundle
	PartnerUser string // a partner org member with roles over its clients, the largest org among them
	SmallMember string // a member of one small org

	BigOrg   string // the org with the most contacts
	Contact  string // objects in BigOrg
	Incident string
	Deal     string
	Invoice  string
	Document string // in the deepest folder of BigOrg's tree
}

// SetupSaaSDB returns a fresh database with the SaaS schema migrated and the
// scale's data loaded and analyzed.
func SetupSaaSDB(tb testing.TB, s SaaSScale) *SaaSData {
	tb.Helper()
	db, dsn := EmptyDBWithDSN(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	_, err := db.ExecContext(ctx, saasTablesSQL)
	require.NoError(tb, err, "create SaaS tables and view")
	require.NoError(tb, generateSaaSData(ctx, db, s), "generate SaaS data at scale %s", s.Name)

	types, err := parser.ParseSchemaString(saasSchemaFGA)
	require.NoError(tb, err, "parse SaaS schema")
	require.NoError(tb, migrator.NewMigrator(db, "").MigrateWithTypes(ctx, types), "migrate SaaS schema")

	one := func(q string, args ...any) string {
		tb.Helper()
		var v string
		require.NoError(tb, db.QueryRowContext(ctx, q, args...).Scan(&v), "query %s", q)
		return v
	}
	d := &SaaSData{DB: db, DSN: dsn}
	d.BigOrg = one(`SELECT org_id FROM contacts GROUP BY org_id ORDER BY count(*) DESC LIMIT 1`)
	d.Staff = one(`SELECT id FROM users WHERE is_staff ORDER BY id LIMIT 1`)
	d.BigMember = one(`SELECT u.id FROM memberships m JOIN users u ON u.id = m.user_id AND u.active
		WHERE m.org_id = $1 ORDER BY (SELECT count(*) FROM user_roles r WHERE r.org_id = m.org_id AND r.user_id = u.id) DESC, u.id LIMIT 1`, d.BigOrg)
	d.PartnerUser = one(`SELECT r.user_id FROM partner_clients pc JOIN partner_roles r ON r.partner_org_id = pc.partner_org_id
		JOIN users u ON u.id = r.user_id AND u.active
		WHERE pc.client_org_id = $1 GROUP BY r.user_id ORDER BY count(*) DESC, r.user_id LIMIT 1`, d.BigOrg)
	d.SmallMember = one(`SELECT u.id FROM orgs o JOIN memberships m ON m.org_id = o.id JOIN users u ON u.id = m.user_id AND u.active
		WHERE NOT EXISTS (SELECT 1 FROM partner_clients pc WHERE pc.client_org_id = o.id OR pc.partner_org_id = o.id)
		ORDER BY o.n DESC, u.id LIMIT 1`)
	d.Contact = one(`SELECT id FROM contacts WHERE org_id = $1 ORDER BY id LIMIT 1`, d.BigOrg)
	d.Incident = one(`SELECT id FROM tickets WHERE org_id = $1 AND kind = 'incident' ORDER BY id LIMIT 1`, d.BigOrg)
	d.Deal = one(`SELECT id FROM deals WHERE org_id = $1 ORDER BY id LIMIT 1`, d.BigOrg)
	d.Invoice = one(`SELECT id FROM invoices WHERE org_id = $1 ORDER BY id LIMIT 1`, d.BigOrg)
	d.Document = one(`WITH RECURSIVE depth AS (
			SELECT id, 0 AS level FROM folders WHERE org_id = $1 AND parent_id IS NULL
			UNION ALL SELECT f.id, d.level + 1 FROM folders f JOIN depth d ON f.parent_id = d.id)
		SELECT doc.id FROM documents doc JOIN depth ON depth.id = doc.folder_id
		ORDER BY depth.level DESC, doc.id LIMIT 1`, d.BigOrg)
	return d
}

// generateSaaSData fills the SaaS tables. Every choice is arithmetic on
// ordinals, so a scale always produces the same data. Resource IDs are
// hashes, so ID order is unrelated to org, as with random or time-ordered IDs.
func generateSaaSData(ctx context.Context, db *sql.DB, s SaaSScale) error {
	clients, partners := saasPartnerClients(s)
	contactOrgs, contactCounts := saasContactCounts(s)
	upo := s.UsersPerOrg
	member := func(expr string) string { return fmt.Sprintf("'u' || (o.n * %d + (%s) %% %d)", upo, expr, upo) }
	rid := func(kind, expr string) string {
		return fmt.Sprintf("left(md5(o.id || ':%s:' || (%s)), 24)", kind, expr)
	}
	// Per-org counts vary between 0.5x and 2x the base.
	perOrg := func(base int) string { return fmt.Sprintf("generate_series(0, %d * (1 + o.n %% 4) / 2 - 1)", base) }

	stmts := []struct {
		sql  string
		args []any
	}{
		{fmt.Sprintf(`INSERT INTO orgs
			SELECT 'org' || lpad(n::text, 4, '0'), n,
			       CASE WHEN n = 0 OR n %% 20 < 7 THEN 'production' WHEN n %% 20 < 18 THEN 'testing' ELSE 'demo' END
			FROM generate_series(0, %d) n`, s.Orgs-1), nil},
		{fmt.Sprintf(`INSERT INTO users SELECT 'u' || i, false, i %% 17 <> 16 FROM generate_series(0, %d) i`, s.Orgs*upo-1), nil},
		{fmt.Sprintf(`INSERT INTO users SELECT 'staff' || i, true, true FROM generate_series(0, %d) i`, s.Staff-1), nil},
		{fmt.Sprintf(`INSERT INTO memberships SELECT o.id, %s FROM orgs o, generate_series(0, %d) k`, member("k"), upo-1), nil},
		// Each org's first member also belongs to the next org, with two roles.
		{fmt.Sprintf(`INSERT INTO memberships SELECT o2.id, 'u' || (o.n * %d)
			FROM orgs o JOIN orgs o2 ON o2.n = (o.n + 1) %% %d ON CONFLICT DO NOTHING`, upo, s.Orgs), nil},
		{fmt.Sprintf(`INSERT INTO user_roles SELECT o2.id, r, 'u' || (o.n * %d)
			FROM orgs o JOIN orgs o2 ON o2.n = (o.n + 1) %% %d, unnest(ARRAY['org_user', 'crm_viewer']) r
			ON CONFLICT DO NOTHING`, upo, s.Orgs), nil},
		{fmt.Sprintf(`INSERT INTO user_roles SELECT o.id, r.role, %s
			FROM orgs o, generate_series(0, %d) k, unnest($1::text[]) WITH ORDINALITY r(role, j)
			WHERE ((j - 1) * 7 + k * 3 + o.n) %% 10 < 4`, member("k"), upo-1), []any{saasOrgRoles}},
		{fmt.Sprintf(`INSERT INTO user_roles SELECT o.id, 'suspended', %s
			FROM orgs o, generate_series(0, %d) k WHERE k %% 13 = 5`, member("k"), upo-1), nil},
		{`INSERT INTO partner_clients SELECT c.id, p.id
			FROM unnest($1::int[], $2::int[]) AS pc(client, partner)
			JOIN orgs c ON c.n = pc.client JOIN orgs p ON p.n = pc.partner`, []any{clients, partners}},
		{
			fmt.Sprintf(`INSERT INTO partner_roles SELECT o.id, r.role, %s
			FROM orgs o, generate_series(0, %d) k, unnest($1::text[]) WITH ORDINALITY r(role, j)
			WHERE o.n BETWEEN 1 AND %d AND ((j - 1) * 7 + k * 3 + o.n) %% 10 < 4`, member("k"), upo-1, s.Partners),
			[]any{saasPartnerRoles},
		},
		{
			fmt.Sprintf(`INSERT INTO contacts
			SELECT %s, o.id, timestamptz '2024-01-01' + (g * 37 %% 1000000) * interval '1 minute'
			FROM unnest($1::int[], $2::int[]) AS c(n, cnt) JOIN orgs o ON o.n = c.n, generate_series(1, c.cnt) g`, rid("c", "g")),
			[]any{contactOrgs, contactCounts},
		},
		{fmt.Sprintf(`INSERT INTO deals SELECT %s, o.id, CASE WHEN g %% 6 = 0 THEN NULL ELSE %s END
			FROM orgs o, %s g`, rid("d", "g"), member("g"), perOrg(s.DealsPerOrg)), nil},
		{fmt.Sprintf(`INSERT INTO queues SELECT %s, o.id FROM orgs o, generate_series(0, %d) q`,
			rid("q", "q"), s.QueuesPerOrg-1), nil},
		{fmt.Sprintf(`INSERT INTO tickets
			SELECT %s, CASE WHEN g %% 3 = 0 THEN 'request' ELSE 'incident' END, o.id, %s,
			       CASE WHEN g %% 5 = 0 THEN NULL ELSE %s END
			FROM orgs o, %s g`, rid("t", "g"), rid("q", fmt.Sprintf("g %% %d", s.QueuesPerOrg)), member("g * 2"),
			perOrg(s.TicketsPerOrg)), nil},
		{fmt.Sprintf(`INSERT INTO invoices SELECT %s, o.id, CASE WHEN g %% 7 = 0 THEN NULL ELSE %s END
			FROM orgs o, %s g`, rid("i", "g"), member("g * 3"), perOrg(s.InvoicesPerOrg)), nil},
		{fmt.Sprintf(`INSERT INTO employees SELECT %s, o.id, CASE WHEN g %% 4 = 0 THEN NULL ELSE %s END
			FROM orgs o, %s g`, rid("e", "g"), member("g"), perOrg(s.EmployeesPerOrg)), nil},
		{fmt.Sprintf(`INSERT INTO teams SELECT %s, o.id FROM orgs o, generate_series(0, %d) t`,
			rid("tm", "t"), s.TeamsPerOrg-1), nil},
		{fmt.Sprintf(`INSERT INTO team_members SELECT %s, %s FROM orgs o, generate_series(0, %d) k`,
			rid("tm", fmt.Sprintf("k %% %d", s.TeamsPerOrg)), member("k"), upo-1), nil},
		{fmt.Sprintf(`INSERT INTO folders SELECT %s, o.id, CASE WHEN f = 0 THEN NULL ELSE %s END
			FROM orgs o, generate_series(0, %d) f`, rid("f", "f"), rid("f", "(f - 1) / 3"), s.FoldersPerOrg-1), nil},
		{fmt.Sprintf(`INSERT INTO folder_shares
			SELECT %s, CASE WHEN f %% 4 = 1 THEN %s END, CASE WHEN f %% 4 = 2 THEN %s END
			FROM orgs o, generate_series(0, %d) f WHERE f %% 4 IN (1, 2)`,
			rid("f", "f"), member("f"), rid("tm", fmt.Sprintf("f %% %d", s.TeamsPerOrg)), s.FoldersPerOrg-1), nil},
		{fmt.Sprintf(`INSERT INTO documents SELECT %s, o.id, %s, CASE WHEN g %% 9 = 0 THEN NULL ELSE %s END
			FROM orgs o, %s g`, rid("doc", "g"), rid("f", fmt.Sprintf("g %% %d", s.FoldersPerOrg)), member("g"),
			perOrg(s.DocsPerOrg)), nil},
	}
	for _, st := range stmts {
		if _, err := db.ExecContext(ctx, st.sql, st.args...); err != nil {
			return fmt.Errorf("%w\n%s", err, strings.TrimSpace(st.sql))
		}
	}
	_, err := db.ExecContext(ctx, "ANALYZE")
	return err
}

// saasPartnerClients assigns client orgs to partners: partner p gets 1 to
// 2*ClientsPerPartner clients in turn from the orgs after the partners, and
// partner 1 also has org 0, the largest. It returns parallel client and
// partner ordinals.
func saasPartnerClients(s SaaSScale) (clients, partners []int64) {
	clients, partners = []int64{0}, []int64{1}
	next := s.Partners + 1
	for p := 1; p <= s.Partners; p++ {
		for range 1 + (p*7)%(2*s.ClientsPerPartner) {
			if next >= s.Orgs {
				return clients, partners
			}
			clients, partners = append(clients, int64(next)), append(partners, int64(p))
			next++
		}
	}
	return clients, partners
}

// saasContactCounts spreads s.Contacts over s.ContactOrgs orgs with Zipf
// weights r^-1.1: org 0 first, then the orgs after the partners. It returns
// parallel org ordinals and counts.
func saasContactCounts(s SaaSScale) (orgs, counts []int64) {
	weight := func(r int) float64 { return math.Pow(float64(r), -1.1) }
	var h float64
	for r := 1; r <= s.ContactOrgs; r++ {
		h += weight(r)
	}
	for r := 1; r <= s.ContactOrgs; r++ {
		n := 0
		if r > 1 {
			n = s.Partners + r - 1
		}
		if n >= s.Orgs {
			break
		}
		orgs = append(orgs, int64(n))
		counts = append(counts, int64(math.Max(1, math.Round(float64(s.Contacts)*weight(r)/h))))
	}
	return orgs, counts
}
