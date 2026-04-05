package melange

import "testing"

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
