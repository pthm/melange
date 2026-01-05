package tooling

import (
	"context"
	"fmt"

	"github.com/pthm/melange/schema"
)

// Migrate is a convenience function that parses a schema file and applies it
// to the database. This combines ParseSchema + schema.ToAuthzModels +
// schema.MigrateWithTypes into a single operation.
//
// The migration process:
//  1. Reads schemasDir/schema.fga
//  2. Parses OpenFGA DSL using the official parser
//  3. Validates schema (detects cycles)
//  4. Applies DDL (creates tables and functions)
//  5. Converts to authorization models and loads into PostgreSQL
//
// For more control over the migration process, use the individual functions:
//
//	types, err := tooling.ParseSchema(path)
//	models := schema.ToAuthzModels(types)
//	migrator := schema.NewMigrator(db, schemasDir)
//	err = migrator.MigrateWithTypes(ctx, types)
//
// The migration is idempotent and transactional (when using *sql.DB).
// Safe to run on application startup.
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
