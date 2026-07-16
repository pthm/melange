package sqldsl

import "testing"

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo", `"foo"`},
		{`has"quote`, `"has""quote"`},
		{"", `""`},
	}
	for _, tt := range tests {
		if got := QuoteIdent(tt.input); got != tt.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPrefixIdent(t *testing.T) {
	tests := []struct {
		ident  string
		schema string
		want   string
	}{
		{"table", "", "table"},
		{"table", "myschema", `"myschema"."table"`},
		{"func_name", "public", `"public"."func_name"`},
	}
	for _, tt := range tests {
		if got := PrefixIdent(tt.ident, tt.schema); got != tt.want {
			t.Errorf("PrefixIdent(%q, %q) = %q, want %q", tt.ident, tt.schema, got, tt.want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"it's", "'it''s'"},
		{`back\slash`, ` E'back\\slash'`},
		{`both'and\`, ` E'both''and\\'`},
		{"", "''"},
	}
	for _, tt := range tests {
		if got := QuoteLiteral(tt.input); got != tt.want {
			t.Errorf("QuoteLiteral(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPostgresSchemaExpr(t *testing.T) {
	tests := []struct {
		schema string
		want   string
	}{
		{"", "current_schema()"},
		{"public", "'public'"},
		{"my_schema", "'my_schema'"},
	}
	for _, tt := range tests {
		if got := PostgresSchemaExpr(tt.schema); got != tt.want {
			t.Errorf("PostgresSchemaExpr(%q) = %q, want %q", tt.schema, got, tt.want)
		}
	}
}
