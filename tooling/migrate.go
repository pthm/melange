package tooling

import (
	"context"
	"fmt"

	"github.com/pthm/melange"
)

// Migrate is a convenience function that parses a schema file and applies it
// to the database. This combines ParseSchema + ToAuthzModels + MigrateWithTypes.
//
// For more control over the migration process, use the individual functions:
//
//	types, err := tooling.ParseSchema(path)
//	models := melange.ToAuthzModels(types)
//	migrator.MigrateWithTypes(ctx, types)
func Migrate(ctx context.Context, db melange.Execer, schemasDir string) error {
	migrator := melange.NewMigrator(db, schemasDir)

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
// Example:
//
//	//go:embed schema.fga
//	var embeddedSchema string
//
//	err := tooling.MigrateFromString(ctx, db, embeddedSchema)
func MigrateFromString(ctx context.Context, db melange.Execer, content string) error {
	types, err := ParseSchemaString(content)
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}

	migrator := melange.NewMigrator(db, "")
	return migrator.MigrateWithTypes(ctx, types)
}
