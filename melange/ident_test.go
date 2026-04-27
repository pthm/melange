package melange

import (
	"context"
	"database/sql"
	"testing"
)

func TestPrefixIdent(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		schema     string
		want       string
	}{
		{"empty schema returns bare identifier", "check_permission", "", "check_permission"},
		{"schema qualifies and quotes both parts", "check_permission", "authz", `"authz"."check_permission"`},
		{"handles special characters in schema", `my"schema`, "check_permission", `"check_permission"."my""schema"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prefixIdent(tt.identifier, tt.schema)
			if got != tt.want {
				t.Errorf("prefixIdent(%q, %q) = %q, want %q", tt.identifier, tt.schema, got, tt.want)
			}
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", `"simple"`},
		{`has"quote`, `"has""quote"`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := quoteIdent(tt.input)
			if got != tt.want {
				t.Errorf("quoteIdent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "alice", `'alice'`},
		{"empty", "", `''`},
		{"single quote doubled", "O'Brien", `'O''Brien'`},
		{"backslash uses E-string and doubles slash", `path\to`, ` E'path\\to'`},
		{"backslash with quote uses E-string and escapes both", `O'\Brien`, ` E'O''\\Brien'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteLiteral(tt.input)
			if got != tt.want {
				t.Errorf("quoteLiteral(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// stubQuerier implements Querier for unit tests that don't need a real DB.
// Tests assert on the calls counter rather than scanned values; *sql.Row
// can't be constructed with a payload outside database/sql, so tests that
// need data exercise paths that bypass QueryRow.
type stubQuerier struct {
	calls int
}

func (s *stubQuerier) QueryRowContext(_ context.Context, _ string, _ ...any) *sql.Row {
	s.calls++
	return nil
}

func (s *stubQuerier) QueryContext(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	s.calls++
	return nil, nil //nolint:nilnil // stub: tests that use this don't iterate
}

// TestBaseTuplesSchema_CacheHit verifies the second call returns the cached
// schema without invoking the underlying Querier. The first call is
// short-circuited via the cached field directly so we don't need a working
// QueryRow stub.
func TestBaseTuplesSchema_CacheHit(t *testing.T) {
	c := &Checker{}

	// Seed the cache directly to simulate a successful first lookup.
	c.tuplesSchema = "public"

	q := &stubQuerier{}
	got, err := c.baseTuplesSchema(context.Background(), q)
	if err != nil {
		t.Fatalf("baseTuplesSchema returned error: %v", err)
	}
	if got != "public" {
		t.Errorf("got schema %q, want %q", got, "public")
	}
	if q.calls != 0 {
		t.Errorf("expected 0 querier calls on cache hit, got %d", q.calls)
	}
}
