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

// QuoteLiteral safely quotes a PostgreSQL string literal.
// Single quotes are escaped by doubling; backslashes are doubled and the
// result is prefixed with E (C-style escape syntax). This mirrors the
// behavior of github.com/lib/pq.QuoteLiteral without the external dependency.
func QuoteLiteral(literal string) string {
	literal = strings.ReplaceAll(literal, `'`, `''`)
	if strings.Contains(literal, `\`) {
		literal = strings.ReplaceAll(literal, `\`, `\\`)
		return ` E'` + literal + `'`
	}
	return `'` + literal + `'`
}

// PostgresSchemaExpr returns a SQL expression for the schema name.
// If schema is empty, it returns "current_schema()" (a SQL function call).
// Otherwise, it returns the schema name as a quoted literal.
func PostgresSchemaExpr(schema string) string {
	if schema == "" {
		return "current_schema()"
	}
	return QuoteLiteral(schema)
}
