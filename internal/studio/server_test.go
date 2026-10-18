package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

func TestReadOnlyDSN(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://u:p@h/db?sslmode=disable": "postgres://u:p@h/db?default_transaction_read_only=on&sslmode=disable",
		"host=h dbname=db":                    "host=h dbname=db default_transaction_read_only=on",
	} {
		got, err := ReadOnlyDSN(in)
		if err != nil || got != want {
			t.Errorf("ReadOnlyDSN(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestLocalhostOnly(t *testing.T) {
	h := localhostOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for host, want := range map[string]int{
		"127.0.0.1:7070": http.StatusOK,
		"localhost:7070": http.StatusOK,
		"[::1]:7070":     http.StatusOK,
		"evil.example":   http.StatusForbidden,
		"10.0.0.1:7070":  http.StatusForbidden,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, w.Code, want)
		}
	}
}

func TestParsePlanExpressions(t *testing.T) {
	for expr, want := range map[string]string{
		"'user'::text":      "user",
		"'it''s'::text":     "it's",
		"'org'":             "org",
		"(t.user_id)::text": "",
	} {
		got, ok := parseLiteral(expr)
		if ok != (want != "") || got != want {
			t.Errorf("parseLiteral(%q) = %q, %v; want %q", expr, got, ok, want)
		}
	}
	for expr, want := range map[string][2]string{
		"(organization_members.user_id)::text": {"organization_members", "user_id"},
		"issues_1.id":                          {"issues_1", "id"},
		`("Odd Table"."My Col")::text`:         {"Odd Table", "My Col"},
	} {
		alias, col, ok := parseColumn(expr)
		if !ok || alias != want[0] || col != want[1] {
			t.Errorf("parseColumn(%q) = %q, %q, %v; want %v", expr, alias, col, ok, want)
		}
	}
	for _, expr := range []string{
		"CASE WHEN b.all THEN '*'::text ELSE (b.user_id)::text END",
		"(t.subject_type)::text || 'x'::text",
	} {
		if _, _, ok := parseColumn(expr); ok {
			t.Errorf("parseColumn(%q) should not match", expr)
		}
	}
}

func TestGroupByType(t *testing.T) {
	got := groupByType([]accessRow{
		{Relation: "member", Type: "organization", IDs: []string{"1", "2"}},
		{Relation: "repo_admin", Type: "repository", IDs: []string{"9"}},
		{Relation: "owner", Type: "organization", IDs: []string{"1"}},
	})
	if len(got) != 2 || got[0].Type != "organization" || got[1].Type != "repository" {
		t.Fatalf("type order: %+v", got)
	}
	org := got[0].Entities
	if len(org) != 2 || org[0].ID != "1" || len(org[0].Relations) != 2 || org[0].Relations[1] != "owner" || org[1].ID != "2" {
		t.Errorf("organization entities: %+v", org)
	}
}

func TestSharedGroupOutlivesOneWaiterButNotAll(t *testing.T) {
	var s Server
	release := make(chan struct{})
	canceled := make(chan struct{})
	compute := func(ctx context.Context) typeGroup {
		select {
		case <-release:
			return typeGroup{Type: "doc"}
		case <-ctx.Done():
			close(canceled)
			return typeGroup{Type: "doc", Errs: []string{ctx.Err().Error()}}
		}
	}

	// Two waiters; the first leaves, the second still gets the result.
	ctx1, leave1 := context.WithCancel(context.Background())
	got := make(chan typeGroup, 2)
	go func() { got <- s.shared(ctx1, "k", "doc", compute) }()
	go func() { got <- s.shared(context.Background(), "k", "doc", compute) }()
	for {
		s.groupsMu.Lock()
		n := 0
		if c := s.groups["k"]; c != nil {
			n = c.waiters
		}
		s.groupsMu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	leave1()
	if g := <-got; len(g.Errs) == 0 {
		t.Fatal("the waiter that left should get its context error")
	}
	close(release)
	if g := <-got; len(g.Errs) != 0 {
		t.Fatalf("the remaining waiter got errors %v", g.Errs)
	}

	// The only waiter leaves: the computation is canceled, not cached.
	ctx2, leave2 := context.WithCancel(context.Background())
	release = make(chan struct{})
	done := make(chan struct{})
	go func() { s.shared(ctx2, "k2", "doc", compute); close(done) }()
	time.Sleep(10 * time.Millisecond)
	leave2()
	<-done
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("computation kept running with no waiters")
	}
	if _, ok := s.groups["k2"]; ok {
		t.Fatal("a canceled computation stayed in the cache")
	}
}

func TestListRelationsSharesOneLimit(t *testing.T) {
	var running, peak atomic.Int32
	list := func(ctx context.Context, _ string) ([]string, error) {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		return []string{"1"}, ctx.Err()
	}
	rels := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	var wg sync.WaitGroup
	for range 3 { // three groups at once, as on an entity page
		wg.Go(func() { listRelations(context.Background(), rels, "doc", list) })
	}
	wg.Wait()
	if p := peak.Load(); p > listParallel {
		t.Fatalf("%d list calls ran at once; the server-wide limit is %d", p, listParallel)
	}
}

func TestModelOrder(t *testing.T) {
	m := &migrator.DeployedModel{
		DSL: `model
  schema 1.1
type user
type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define can_read: admin
`,
		Types: []schema.TypeDefinition{
			{Name: "user"},
			{Name: "organization", Relations: []schema.RelationDefinition{{Name: "admin"}, {Name: "can_read"}, {Name: "owner"}}},
		},
	}
	o := newModelOrder(m)
	o.sortRelations(m)
	got := make([]string, 0, len(m.Types[1].Relations))
	for _, r := range m.Types[1].Relations {
		got = append(got, r.Name)
	}
	if strings.Join(got, ",") != "owner,admin,can_read" {
		t.Errorf("relations = %v, want DSL order owner,admin,can_read", got)
	}
	types := []string{"zebra", "organization", "user"}
	slices.SortStableFunc(types, o.compareTypes)
	if strings.Join(types, ",") != "user,organization,zebra" {
		t.Errorf("types = %v, want model order with unknowns last", types)
	}
}

func TestNaturalCompare(t *testing.T) {
	ids := []string{"10", "b", "2", "*", "a", "010", "1"}
	slices.SortStableFunc(ids, naturalCompare)
	if got := strings.Join(ids, ","); got != "1,2,10,010,*,a,b" {
		t.Errorf("natural order = %s", got)
	}
}

func TestHighlightFGA(t *testing.T) {
	var got []string
	for _, tok := range highlightFGA("model\n  schema 1.1\ntype doc # c\n  relations\n    define viewer: [user, group#member, user:*] or owner from parent") {
		if tok.Class != "" && tok.Text != "" {
			got = append(got, tok.Class+":"+tok.Text)
		}
	}
	want := []string{
		"kw:model", "kw:schema", "num:1.1", "kw:type", "name:doc", "comment:# c", "kw:relations",
		"kw:define", "name:viewer", "punct::", "punct:[", "punct:,", "ref:#member", "punct:,", "ref::*", "punct:]",
		"op:or", "op:from",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tokens:\n got  %v\n want %v", got, want)
	}
}
