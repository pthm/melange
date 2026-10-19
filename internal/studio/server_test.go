package studio

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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
