/**
 * Postgres identifier helpers.
 */

/**
 * Safely quotes a PostgreSQL identifier for use in SQL.
 * Double quotes are escaped by doubling them. This is needed when dynamically
 * constructing schema-qualified relation names in UNION queries.
 */
export function quoteIdent(ident: string) {
  return '"' + ident.replaceAll('"', '""') + '"'
}

/**
 * Prefixes the identifier with the schema if it is not empty.
 * Both the schema and identifier are quoted for consistency.
 */
export function prefixIdent(identifier: string, schema: string): string {
  if (!schema) {
    return identifier
  }

  return quoteIdent(schema) + '.' + quoteIdent(identifier)
}
