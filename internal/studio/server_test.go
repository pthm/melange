package studio

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"

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

func TestEachRelationKeepsOrderAndErrors(t *testing.T) {
	rels := []string{"a", "b", "c", "d", "e", "f"}
	got := make([]string, len(rels))
	errs := eachRelation(context.Background(), rels, func(_ context.Context, i int, rel string) error {
		got[i] = rel
		if rel == "c" {
			return errors.New("boom")
		}
		return nil
	})
	if !slices.Equal(got, rels) {
		t.Fatalf("got %v, want %v", got, rels)
	}
	for i, err := range errs {
		if (err != nil) != (rels[i] == "c") {
			t.Fatalf("errs[%d] = %v", i, err)
		}
	}
}

func TestPlanTimes(t *testing.T) {
	plan := `Result  (cost=0.00..0.26 rows=1 width=1) (actual time=205.301..205.302 rows=1 loops=1)
  Buffers: shared hit=20696
Planning Time: 0.298 ms
Execution Time: 205.324 ms`
	exec, planning := planTimes(plan)
	if exec != 205.324 || planning != 0.298 {
		t.Fatalf("planTimes = %v, %v; want 205.324, 0.298", exec, planning)
	}
	if e, p := planTimes("Result"); e != 0 || p != 0 {
		t.Fatalf("planTimes on a plan without times = %v, %v; want 0, 0", e, p)
	}
}

func TestFuncDeltaSubtractsEarlierCalls(t *testing.T) {
	before := map[string]funcTime{"a": {"a", 2, 10, 10}, "idle": {"idle", 1, 5, 5}}
	after := map[string]funcTime{
		"a":    {"a", 3, 13, 13},
		"b":    {"b", 1, 20, 4},
		"idle": {"idle", 1, 5, 5},
	}
	got := funcDelta(before, after)
	want := []funcTime{{"b", 1, 20, 4}, {"a", 1, 3, 3}}
	if !slices.Equal(got, want) {
		t.Fatalf("funcDelta = %v; want %v", got, want)
	}
}

func TestFailedAfterWordsDepthLimitLikeTheChecker(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &pq.Error{Code: "M2002", Message: "resolution too complex"})
	if got, want := failedAfter(err, 412*time.Millisecond), "Failed after 412.0 ms: resolution too complex: depth limit exceeded"; got != want {
		t.Fatalf("failedAfter = %q; want %q", got, want)
	}
	if got := msf(0.997); got != "1.0 ms" {
		t.Fatalf("msf(0.997) = %q; want 1.0 ms", got)
	}
}

func TestHighlightSQL(t *testing.T) {
	src := "CREATE FUNCTION check_doc(p text) RETURNS integer AS $function$\n" +
		"-- direct\nSELECT 1 FROM \"melange\".melange_tuples WHERE relation = 'it''s' AND check_x (p) = 1;\n$function$"
	var got []string
	var text strings.Builder
	for _, tok := range highlightSQL(src) {
		text.WriteString(tok.Text)
		if tok.Class != "" && tok.Text != "" {
			got = append(got, tok.Class+":"+tok.Text)
		}
	}
	if text.String() != src {
		t.Fatalf("tokens do not reassemble the source:\n%s", text.String())
	}
	want := []string{
		"kw:CREATE", "kw:FUNCTION", "name:check_doc", "kw:text", "kw:RETURNS", "kw:integer", "kw:AS", "punct:$function$",
		"comment:-- direct", "kw:SELECT", "num:1", "kw:FROM", `name:"melange"`, "kw:WHERE", "ref:'it''s'", "kw:AND",
		"name:check_x", "num:1", "punct:$function$",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tokens:\n got  %v\n want %v", got, want)
	}
}

func TestServeAssets(t *testing.T) {
	h := (&Server{}).Handler() // routing only; no handler here touches the database
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		r.Host = "127.0.0.1:7070"
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Current version: immutable, gzipped, typed from the file, hardened.
	w := get(asset("studio.css"))
	if w.Code != http.StatusOK {
		t.Fatalf("studio.css: status %d", w.Code)
	}
	for k, want := range map[string]string{
		"Cache-Control":           "public, max-age=31536000, immutable",
		"Content-Encoding":        "gzip",
		"Content-Type":            "text/css; charset=utf-8",
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("studio.css %s = %q, want %q", k, got, want)
		}
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	want, _ := fs.ReadFile(assets, "assets/studio.css")
	if !bytes.Equal(body, want) {
		t.Error("studio.css: decompressed body differs from the embedded file")
	}

	// Already-compressed fonts pass through; an old version revalidates.
	w = get(asset("npm/fontsource-space-mono/space-mono-latin-400-normal.woff2"))
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" {
		t.Errorf("woff2: status %d, Content-Encoding %q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if got := get("/assets/0ld/studio.css").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("old version Cache-Control = %q, want no-cache", got)
	}
	if got := get("/no-such-page").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("page Cache-Control = %q, want no-store", got)
	}
}

func TestGzipKeepsContentTypeAfterEarlyStatus(t *testing.T) {
	h := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing": // a status first, then a rendered page, like relation's 404
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<!DOCTYPE html><p>not found</p>"))
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/missing")
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Encoding") != "gzip" ||
		w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("/missing: %d, encoding %q, type %q", w.Code, w.Header().Get("Content-Encoding"), w.Header().Get("Content-Type"))
	}
	if w = get("/empty"); w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
		t.Errorf("/empty: %d, %d body bytes, encoding %q", w.Code, w.Body.Len(), w.Header().Get("Content-Encoding"))
	}
}

func TestNumberFormatting(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{num(0), "0"},
		{num(999), "999"},
		{num(1000), "1,000"},
		{num(198304), "198,304"},
		{num(-1234567), "-1,234,567"},
		{plural(1, "entity"), "1 entity"},
		{plural(20000, "entity"), "20,000 entities"},
		{plural(2, "key"), "2 keys"},
		{plural(1500, "relation"), "1,500 relations"},
		{countLabel(200, true), "200+"},
		{msf(6200.34), "6,200.3 ms"},
		{msf(12.44), "12.4 ms"},
		{msf(0.42), "0.42 ms"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
