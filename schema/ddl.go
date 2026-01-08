package schema

// migrationsDDL defines the melange_migrations table for tracking migration state.
const migrationsDDL = `-- Melange migrations tracking table
-- Stores migration history for change detection and orphan cleanup.
--
-- Each row represents a completed migration:
-- - schema_checksum: SHA256 of the schema.fga content
-- - codegen_version: Version of the SQL generation logic
-- - function_names: All generated function names (for orphan detection)
--
-- The migrator checks the most recent record to determine if re-migration
-- is needed. If both checksum and codegen_version match, migration is skipped
-- unless --force is specified.

CREATE TABLE IF NOT EXISTS melange_migrations (
    id SERIAL PRIMARY KEY,
    migrated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    schema_checksum VARCHAR(64) NOT NULL,
    codegen_version VARCHAR(32) NOT NULL,
    function_names TEXT[] NOT NULL
);

-- Lookup by checksum for change detection
CREATE INDEX IF NOT EXISTS idx_melange_migrations_checksum
ON melange_migrations (schema_checksum, codegen_version);
`
