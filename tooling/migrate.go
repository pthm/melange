package tooling

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pthm/melange/tooling/schema"
)

// MigrateOptions controls migration behavior.
type MigrateOptions struct {
	// DryRun outputs SQL to the provided writer without applying changes to the database.
	// Use this to preview migrations, generate migration scripts, or validate schema changes.
	// If nil, migration proceeds normally and executes against the database.
	DryRun io.Writer

	// Force re-runs migration even if schema checksum and codegen version are unchanged.
	// By default, migrations are skipped when the schema.fga content and CodegenVersion
	// match the last successful migration. Set Force to true when manually fixing
	// corrupted state or testing migration logic.
	Force bool
}

// Migrate parses an OpenFGA schema file and applies it to the database in one operation.
// This is the recommended high-level API for most applications.
//
// The function is idempotent - safe to call on every application startup. It validates
// the schema, generates specialized SQL functions per relation, and applies everything
// atomically within a transaction (when db supports BeginTx).
//
// Migration workflow:
//  1. Reads schemasDir/schema.fga
//  2. Parses OpenFGA DSL using the official parser
//  3. Validates schema (cycle detection, referential integrity)
//  4. Generates specialized check_permission and list_accessible functions
//  5. Applies generated SQL atomically via transaction
//
// The schemasDir should contain a single schema.fga file in OpenFGA DSL format:
//
//	schemas/
//	  schema.fga
//
// Example usage on application startup:
//
//	if err := tooling.Migrate(ctx, db, "schemas"); err != nil {
//	    log.Fatalf("migration failed: %v", err)
//	}
//
// For embedded schemas (no file I/O), use MigrateFromString.
// For fine-grained control (dry-run, skip optimization), use MigrateWithOptions.
// For programmatic use with pre-parsed types, use schema.Migrator directly:
//
//	types, _ := tooling.ParseSchema("schemas/schema.fga")
//	migrator := schema.NewMigrator(db, "schemas")
//	err := migrator.MigrateWithTypes(ctx, types)
func Migrate(ctx context.Context, db schema.Execer, schemasDir string) error {
	migrator := schema.NewMigrator(db, schemasDir)

	if !migrator.HasSchema() {
		return fmt.Errorf("no schema found at %s", migrator.SchemaPath())
	}

	types, err := ParseSchema(migrator.SchemaPath())
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}

	return migrator.MigrateWithTypes(ctx, types)
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
//	err := tooling.MigrateFromString(ctx, db, embeddedSchema)
//
// The migration is idempotent and transactional (when using *sql.DB).
func MigrateFromString(ctx context.Context, db schema.Execer, content string) error {
	types, err := ParseSchemaString(content)
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}

	migrator := schema.NewMigrator(db, "")
	return migrator.MigrateWithTypes(ctx, types)
}

// MigrateWithOptions performs migration with control over dry-run and skip behavior.
// Use this when you need to preview migrations, force re-application, or detect skips.
//
// The skip-if-unchanged optimization compares the schema.fga content hash and codegen
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
//	_, err := tooling.MigrateWithOptions(ctx, db, "schemas", tooling.MigrateOptions{
//	    DryRun: &buf,
//	})
//	os.WriteFile("migrations/001_authz.sql", buf.Bytes(), 0644)
//
// Example: Force re-migration (e.g., after manual schema corruption)
//
//	skipped, err := tooling.MigrateWithOptions(ctx, db, "schemas", tooling.MigrateOptions{
//	    Force: true,
//	})
func MigrateWithOptions(ctx context.Context, db schema.Execer, schemasDir string, opts MigrateOptions) (skipped bool, err error) {
	migrator := schema.NewMigrator(db, schemasDir)

	if !migrator.HasSchema() {
		return false, fmt.Errorf("no schema found at %s", migrator.SchemaPath())
	}

	// Read schema content for checksum
	schemaContent, err := os.ReadFile(migrator.SchemaPath())
	if err != nil {
		return false, fmt.Errorf("reading schema file: %w", err)
	}

	types, err := ParseSchemaString(string(schemaContent))
	if err != nil {
		return false, fmt.Errorf("parsing schema: %w", err)
	}

	// Convert to schema.MigrateOptions
	schemaOpts := schema.MigrateOptions{
		DryRun:        opts.DryRun,
		Force:         opts.Force,
		SchemaContent: string(schemaContent),
	}

	// Check if we should skip (only if not dry-run and not force)
	if !opts.Force && opts.DryRun == nil {
		checksum := schema.ComputeSchemaChecksum(string(schemaContent))
		lastMigration, err := migrator.GetLastMigration(ctx)
		if err != nil {
			return false, fmt.Errorf("checking last migration: %w", err)
		}
		if lastMigration != nil &&
			lastMigration.SchemaChecksum == checksum &&
			lastMigration.CodegenVersion == schema.CodegenVersion {
			return true, nil // Skipped
		}
	}

	err = migrator.MigrateWithTypesAndOptions(ctx, types, schemaOpts)
	return false, err
}
