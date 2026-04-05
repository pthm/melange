package sqldsl

import "strings"

// QuoteIdent safely quotes a PostgreSQL identifier for use in SQL.
func QuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// PrefixIdent prefixes the identifier with the quoted schema.
// Both the schema and identifier are quoted for consistency.
func PrefixIdent(identifier, schema string) string {
	if schema == "" {
		return identifier
	}

	return QuoteIdent(schema) + "." + QuoteIdent(identifier)
}
