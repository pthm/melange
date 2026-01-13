package migrator

import (
	"context"
	"fmt"
	"os"

	"github.com/pthm/melange/pkg/parser"
)

// Migrate parses an OpenFGA schema file and applies it to the database in one operation.
// This is the recommended high-level API for most applications.
//
// The function is idempotent - safe to call on every application startup. It validates
// the schema, generates specialized SQL functions per relation, and applies everything
// atomically within a transaction (when db supports BeginTx).
//
// Migration workflow:
//  1. Reads the schema file at schemaPath
//  2. Parses OpenFGA DSL using the official parser
//  3. Validates schema (cycle detection, referential integrity)
//  4. Generates specialized check_permission and list_accessible functions
//  5. Applies generated SQL atomically via transaction
//
// Example usage on application startup:
//
//	if err := migrator.Migrate(ctx, db, "schemas/schema.fga"); err != nil {
//	    log.Fatalf("migration failed: %v", err)
//	}
//
// For embedded schemas (no file I/O), use MigrateFromString.
// For fine-grained control (dry-run, skip optimization), use MigrateWithOptions.
// For programmatic use with pre-parsed types, use Migrator directly:
//
//	types, _ := parser.ParseSchema("schemas/schema.fga")
//	m := migrator.NewMigrator(db, "schemas/schema.fga")
//	err := m.MigrateWithTypes(ctx, types)
func Migrate(ctx context.Context, db Execer, schemaPath string) error {
	m := NewMigrator(db, schemaPath)

	if !m.HasSchema() {
		return fmt.Errorf("no schema found at %s", m.SchemaPath())
	}

	types, err := parser.ParseSchema(m.SchemaPath())
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}

	return m.MigrateWithTypes(ctx, types)
}

// MigrateFromString parses schema content and applies it to the database.
// Useful for testing or when schema is embedded in the application binary.
//
// This allows bundling the authorization schema with the application rather
// than reading from disk, which simplifies deployment and versioning.
//
// Example:
//
//	//go:embed schema.fga
//	var embeddedSchema string
//
//	err := migrator.MigrateFromString(ctx, db, embeddedSchema)
//
// The migration is idempotent and transactional (when using *sql.DB).
func MigrateFromString(ctx context.Context, db Execer, content string) error {
	types, err := parser.ParseSchemaString(content)
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}

	m := NewMigrator(db, "")
	return m.MigrateWithTypes(ctx, types)
}

// MigrateWithOptions performs migration with control over dry-run and skip behavior.
// Use this when you need to preview migrations, force re-application, or detect skips.
//
// The skip-if-unchanged optimization compares the schema content hash and codegen
// version against the last successful migration. If both match and Force is false,
// the migration is skipped (skipped=true). This avoids redundant function regeneration
// on every application restart when schemas are stable.
//
// Returns (skipped, error):
//   - skipped=true if migration was skipped due to unchanged schema (only when Force=false and DryRun=nil)
//   - error is non-nil if migration failed (parse error, validation error, DB error)
//
// Example: Generate migration script without applying
//
//	var buf bytes.Buffer
//	_, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", migrator.MigrateOptions{
//	    DryRun: &buf,
//	})
//	os.WriteFile("migrations/001_authz.sql", buf.Bytes(), 0644)
//
// Example: Force re-migration (e.g., after manual schema corruption)
//
//	skipped, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", migrator.MigrateOptions{
//	    Force: true,
//	})
func MigrateWithOptions(ctx context.Context, db Execer, schemaPath string, opts MigrateOptions) (skipped bool, err error) {
	m := NewMigrator(db, schemaPath)

	if !m.HasSchema() {
		return false, fmt.Errorf("no schema found at %s", m.SchemaPath())
	}

	// Read schema content for checksum
	schemaContent, err := os.ReadFile(m.SchemaPath())
	if err != nil {
		return false, fmt.Errorf("reading schema file: %w", err)
	}

	types, err := parser.ParseSchemaString(string(schemaContent))
	if err != nil {
		return false, fmt.Errorf("parsing schema: %w", err)
	}

	// Convert to internal MigrateOptions
	internalOpts := InternalMigrateOptions{
		DryRun:        opts.DryRun,
		Force:         opts.Force,
		Version:       opts.Version,
		SchemaContent: string(schemaContent),
	}

	// Check if we should skip (only if not dry-run and not force)
	if !opts.Force && opts.DryRun == nil {
		checksum := ComputeSchemaChecksum(string(schemaContent))
		lastMigration, err := m.GetLastMigration(ctx)
		if err != nil {
			return false, fmt.Errorf("checking last migration: %w", err)
		}
		if lastMigration != nil &&
			lastMigration.SchemaChecksum == checksum &&
			lastMigration.CodegenVersion == CodegenVersion {
			return true, nil // Skipped
		}
	}

	err = m.MigrateWithTypesAndOptions(ctx, types, internalOpts)
	return false, err
}
