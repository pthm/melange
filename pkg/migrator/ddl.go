package migrator

import (
	"fmt"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// migrationsDDL returns a query that defines the melange_migrations table for
// tracking migration state.
func migrationsDDL(databaseSchema string) string {
	table := sqldsl.PrefixIdent("melange_migrations", databaseSchema)

	return fmt.Sprintf(`-- Melange migrations tracking table
-- Stores migration history for change detection and orphan cleanup.
--
-- Each row represents a completed migration:
-- - melange_version: Version of the melange CLI/library (e.g., "v0.4.3")
-- - schema_checksum: SHA256 of the schema.fga content
-- - codegen_version: Version of the SQL generation logic
-- - function_names: All generated function names (for orphan detection)
--
-- The migrator checks the most recent record to determine if re-migration
-- is needed. If both checksum and codegen_version match, migration is skipped
-- unless --force is specified.

CREATE TABLE IF NOT EXISTS %[1]s (
    id SERIAL PRIMARY KEY,
    migrated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    melange_version TEXT NOT NULL DEFAULT '',
    schema_checksum VARCHAR(64) NOT NULL,
    codegen_version TEXT NOT NULL,
    function_names TEXT[] NOT NULL
);

-- Lookup by checksum for change detection
CREATE INDEX IF NOT EXISTS idx_melange_migrations_checksum
ON %[1]s (schema_checksum, codegen_version);
`, table)
}

// addMelangeVersionColumn return a query to add the melange_version column
// to existing tables.
func addMelangeVersionColumn(databaseSchema string) string {
	table := sqldsl.PrefixIdent("melange_migrations", databaseSchema)

	return fmt.Sprintf(`
ALTER TABLE %s
ADD COLUMN IF NOT EXISTS melange_version TEXT NOT NULL DEFAULT '';
`, table)
}

// widenVersionColumns returns a query that widens the version columns of
// existing tables from their original VARCHAR types to TEXT. Module
// pseudo-versions (e.g. "v0.8.4-0.20260701080815-8f95d5d39b90" from
// go-install builds) are 38+ characters and overflowed the original
// codegen_version VARCHAR(32), failing every migrate with "value too long".
// Runs unconditionally like the sibling ADD COLUMN helpers: ALTER TYPE has
// no IF syntax, varchar→text is catalog-only (no table rewrite), and re-runs
// on an already-TEXT column are harmless no-op catalog updates on a table
// with one row per migration.
func widenVersionColumns(databaseSchema string) string {
	table := sqldsl.PrefixIdent("melange_migrations", databaseSchema)

	return fmt.Sprintf(`
ALTER TABLE %[1]s ALTER COLUMN codegen_version TYPE TEXT;
ALTER TABLE %[1]s ALTER COLUMN melange_version TYPE TEXT;
`, table)
}

// migrationsTableDDL returns every statement that brings the
// melange_migrations table to the current shape, in order: create if absent,
// then the column migrations for tables created by earlier versions. Both
// the direct apply path (applyMigrationsDDL) and the dry-run/file output
// (outputDryRun) must emit all of them — a statement missing from either
// path resurfaces the legacy-table bugs for that workflow.
func migrationsTableDDL(databaseSchema string) []string {
	return []string{
		migrationsDDL(databaseSchema),
		addMelangeVersionColumn(databaseSchema),
		addFunctionChecksumsColumn(databaseSchema),
		widenVersionColumns(databaseSchema),
	}
}

// addFunctionChecksumsColumn return a query to add function_checksums to existing melange_migrations
// tables. The column was not present in the original DDL, so it is applied
// separately via ADD COLUMN IF NOT EXISTS to preserve compatibility with databases
// migrated by earlier versions.
//
// The column stores function_name → SHA256(sql_body) pairs. The generate
// migration --db comparison mode reads this to emit only changed functions.
func addFunctionChecksumsColumn(databaseSchema string) string {
	table := sqldsl.PrefixIdent("melange_migrations", databaseSchema)

	return fmt.Sprintf(`
ALTER TABLE %s
ADD COLUMN IF NOT EXISTS function_checksums JSONB NOT NULL DEFAULT '{}';
`, table)
}
