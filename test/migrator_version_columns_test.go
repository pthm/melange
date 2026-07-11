package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/lib/version"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/test/testutil"
)

// Module pseudo-versions from go-install builds (e.g.
// "v0.8.4-0.20260701080815-8f95d5d39b90", 38+ chars) overflowed the original
// codegen_version VARCHAR(32), failing every migrate with "value too long".
// This covers both the fresh-install DDL (TEXT columns) and the upgrade path
// (widenVersionColumns on a table created with the original VARCHAR DDL).
func TestMigrate_PseudoVersionFitsVersionColumns(t *testing.T) {
	prev := version.Version
	version.Version = "v0.8.4-0.20260701080815-8f95d5d39b90+dirty"
	t.Cleanup(func() { version.Version = prev })

	schemaPath := filepath.Join(t.TempDir(), "schema.fga")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
`), 0o644))

	ctx := context.Background()

	t.Run("fresh install", func(t *testing.T) {
		db := testutil.EmptyDB(t)
		_, err := db.ExecContext(ctx, `
			CREATE TABLE melange_tuples (
				subject_type TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				relation TEXT NOT NULL,
				object_type TEXT NOT NULL,
				object_id TEXT NOT NULL
			)`)
		require.NoError(t, err)

		_, err = migrator.MigrateWithOptions(ctx, db, schemaPath, migrator.MigrateOptions{Version: version.Version})
		require.NoError(t, err, "migrate with pseudo-version must succeed on a fresh install")

		var got string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT codegen_version FROM melange_migrations ORDER BY id DESC LIMIT 1`).Scan(&got))
		require.Equal(t, version.Version, got)
	})

	t.Run("upgrade widens legacy varchar columns in named schema", func(t *testing.T) {
		db := testutil.EmptyDB(t)
		_, err := db.ExecContext(ctx, `CREATE SCHEMA authz`)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
			CREATE TABLE authz.melange_tuples (
				subject_type TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				relation TEXT NOT NULL,
				object_type TEXT NOT NULL,
				object_id TEXT NOT NULL
			)`)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
			CREATE TABLE authz.melange_migrations (
				id SERIAL PRIMARY KEY,
				migrated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				melange_version VARCHAR(64) NOT NULL DEFAULT '',
				schema_checksum VARCHAR(64) NOT NULL,
				codegen_version VARCHAR(32) NOT NULL,
				function_names TEXT[] NOT NULL,
				function_checksums JSONB NOT NULL DEFAULT '{}'
			)`)
		require.NoError(t, err)

		_, err = migrator.MigrateWithOptions(ctx, db, schemaPath, migrator.MigrateOptions{
			Version:        version.Version,
			DatabaseSchema: "authz",
		})
		require.NoError(t, err, "migrate must widen legacy varchar columns in a named schema")

		var dataType string
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT data_type FROM information_schema.columns
			WHERE table_schema = 'authz' AND table_name = 'melange_migrations' AND column_name = 'codegen_version'`).Scan(&dataType))
		require.Equal(t, "text", dataType)
	})

	t.Run("dry-run output includes column migrations", func(t *testing.T) {
		db := testutil.EmptyDB(t)
		var out strings.Builder
		_, err := migrator.MigrateWithOptions(ctx, db, schemaPath, migrator.MigrateOptions{
			Version: version.Version,
			DryRun:  &out,
		})
		require.NoError(t, err)
		sql := out.String()
		require.Contains(t, sql, "ALTER COLUMN codegen_version TYPE TEXT",
			"file-based migrations must widen legacy tables too")
		require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS function_checksums",
			"file-based migrations must include column additions for legacy tables")
	})

	t.Run("upgrade widens legacy varchar columns", func(t *testing.T) {
		db := testutil.EmptyDB(t)
		_, err := db.ExecContext(ctx, `
			CREATE TABLE melange_tuples (
				subject_type TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				relation TEXT NOT NULL,
				object_type TEXT NOT NULL,
				object_id TEXT NOT NULL
			)`)
		require.NoError(t, err)

		// Recreate the original (pre-widening) tracking table shape.
		_, err = db.ExecContext(ctx, `
			CREATE TABLE melange_migrations (
				id SERIAL PRIMARY KEY,
				migrated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				melange_version VARCHAR(64) NOT NULL DEFAULT '',
				schema_checksum VARCHAR(64) NOT NULL,
				codegen_version VARCHAR(32) NOT NULL,
				function_names TEXT[] NOT NULL,
				function_checksums JSONB NOT NULL DEFAULT '{}'
			)`)
		require.NoError(t, err)

		_, err = migrator.MigrateWithOptions(ctx, db, schemaPath, migrator.MigrateOptions{Version: version.Version})
		require.NoError(t, err, "migrate with pseudo-version must widen legacy varchar columns and succeed")

		var dataType string
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT data_type FROM information_schema.columns
			WHERE table_name = 'melange_migrations' AND column_name = 'codegen_version'`).Scan(&dataType))
		require.Equal(t, "text", dataType)

		var got string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT codegen_version FROM melange_migrations ORDER BY id DESC LIMIT 1`).Scan(&got))
		require.Equal(t, version.Version, got)
	})
}
