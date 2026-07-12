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

// widenVersionColumnsDDL returns a guarded statement that widens the version
// columns to TEXT only when they are still bounded VARCHAR. Module
// pseudo-versions (e.g. "v0.8.4-0.20260701080815-8f95d5d39b90" from go-install
// builds) are 38+ characters and overflowed the original codegen_version
// VARCHAR(32), failing every migrate with "value too long".
//
// ALTER TYPE has no IF syntax and PostgreSQL rejects even a no-op
// VARCHAR->TEXT/TEXT->TEXT change when a view or rule depends on the column, so
// the widen must be conditional: once the columns are TEXT
// (character_maximum_length IS NULL) the ALTERs are skipped, leaving dependent
// views intact. The guard runs server-side in a DO block so the same statement
// is correct on both the direct apply path and the dry-run/file output path.
func widenVersionColumnsDDL(databaseSchema string) string {
	table := sqldsl.PrefixIdent("melange_migrations", databaseSchema)
	schema := sqldsl.PostgresSchemaExpr(databaseSchema)

	return fmt.Sprintf(`
DO $mig$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'melange_migrations'
          AND column_name = 'codegen_version'
          AND table_schema = %[2]s
          AND character_maximum_length IS NOT NULL
    ) THEN
        ALTER TABLE %[1]s ALTER COLUMN codegen_version TYPE TEXT;
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'melange_migrations'
          AND column_name = 'melange_version'
          AND table_schema = %[2]s
          AND character_maximum_length IS NOT NULL
    ) THEN
        ALTER TABLE %[1]s ALTER COLUMN melange_version TYPE TEXT;
    END IF;
END
$mig$;
`, table, schema)
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
		widenVersionColumnsDDL(databaseSchema),
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
