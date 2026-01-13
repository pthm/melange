# migrator

Package `migrator` handles loading authorization schemas into PostgreSQL.

This package provides the primary API for applying OpenFGA schemas to your database, generating specialized SQL functions for permission checking. Migrations are idempotent and safe to run on every application startup.

## Package Responsibilities

- Parse and validate OpenFGA schemas
- Generate specialized SQL functions per relation
- Apply migrations atomically within transactions
- Track migration history for skip-if-unchanged optimization
- Clean up orphaned functions when schema changes

## Public API

### High-Level Functions

```go
// Migrate parses schema file and applies to database in one operation.
// Recommended for most applications.
func Migrate(ctx context.Context, db Execer, schemaPath string) error

// MigrateFromString parses schema content and applies to database.
// Useful for embedded schemas or testing.
func MigrateFromString(ctx context.Context, db Execer, content string) error

// MigrateWithOptions provides control over dry-run and skip behavior.
func MigrateWithOptions(ctx context.Context, db Execer, schemaPath string, opts MigrateOptions) (skipped bool, err error)
```

### Migrator Type

```go
// NewMigrator creates a new schema migrator.
func NewMigrator(db Execer, schemaPath string) *Migrator

// MigrateWithTypes performs migration using pre-parsed type definitions.
func (m *Migrator) MigrateWithTypes(ctx context.Context, types []TypeDefinition) error

// MigrateWithTypesAndOptions performs migration with full options control.
func (m *Migrator) MigrateWithTypesAndOptions(ctx context.Context, types []TypeDefinition, opts InternalMigrateOptions) error

// GetStatus returns the current migration status.
func (m *Migrator) GetStatus(ctx context.Context) (*Status, error)

// GetLastMigration returns the most recent migration record.
func (m *Migrator) GetLastMigration(ctx context.Context) (*MigrationRecord, error)

// HasSchema returns true if the schema file exists.
func (m *Migrator) HasSchema() bool
```

### Types

```go
// MigrateOptions controls migration behavior.
type MigrateOptions struct {
    DryRun  io.Writer // Output SQL without applying; nil = apply normally
    Force   bool      // Re-run even if schema unchanged
    Version string    // Melange version for traceability
}

// Status represents the current migration state.
type Status struct {
    SchemaExists bool // Schema file exists on disk
    TuplesExists bool // melange_tuples view exists in database
}

// MigrationRecord represents a row in melange_migrations table.
type MigrationRecord struct {
    MelangeVersion string
    SchemaChecksum string
    CodegenVersion string
    FunctionNames  []string
}
```

## Usage Examples

### Basic Migration (Recommended)

```go
import "github.com/pthm/melange/pkg/migrator"

// Run on application startup
func main() {
    db, _ := sql.Open("postgres", connString)

    if err := migrator.Migrate(ctx, db, "schemas/schema.fga"); err != nil {
        log.Fatalf("Migration failed: %v", err)
    }

    // Application ready...
}
```

### Embedded Schema

```go
import (
    _ "embed"
    "github.com/pthm/melange/pkg/migrator"
)

//go:embed schema.fga
var embeddedSchema string

func main() {
    db, _ := sql.Open("postgres", connString)

    if err := migrator.MigrateFromString(ctx, db, embeddedSchema); err != nil {
        log.Fatalf("Migration failed: %v", err)
    }
}
```

### Dry-Run (Preview SQL)

```go
var buf bytes.Buffer

_, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", migrator.MigrateOptions{
    DryRun: &buf,
})
if err != nil {
    log.Fatal(err)
}

// Write SQL to file for review
os.WriteFile("migrations/001_authz.sql", buf.Bytes(), 0644)
```

### Force Re-Migration

```go
// Useful after manual schema corruption or template changes
skipped, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", migrator.MigrateOptions{
    Force: true,
})
if err != nil {
    log.Fatal(err)
}
// skipped is always false when Force=true
```

### Check Migration Status

```go
m := migrator.NewMigrator(db, "schemas/schema.fga")

status, err := m.GetStatus(ctx)
if err != nil {
    log.Fatal(err)
}

if !status.SchemaExists {
    log.Println("No schema file found")
}

if !status.TuplesExists {
    log.Println("melange_tuples view needs to be created")
}
```

### With Pre-Parsed Types

```go
import (
    "github.com/pthm/melange/pkg/parser"
    "github.com/pthm/melange/pkg/migrator"
)

// Parse once, migrate multiple databases
types, err := parser.ParseSchema("schemas/schema.fga")
if err != nil {
    log.Fatal(err)
}

for _, db := range databases {
    m := migrator.NewMigrator(db, "schemas/schema.fga")
    if err := m.MigrateWithTypes(ctx, types); err != nil {
        log.Printf("Migration failed for %s: %v", db, err)
    }
}
```

## Migration Workflow

When you run migration, the following steps occur:

1. **Parse** - Read and parse the OpenFGA schema
2. **Validate** - Check for cycles and invalid references
3. **Compute** - Generate transitive closure for role hierarchies
4. **Analyze** - Classify relations and determine SQL patterns
5. **Generate** - Create specialized SQL functions per relation
6. **Apply** - Execute SQL atomically in a transaction
7. **Track** - Record migration in `melange_migrations` table
8. **Cleanup** - Drop orphaned functions from previous schema

## Skip-If-Unchanged Optimization

Migrations are skipped when both conditions are met:
- Schema content hash matches last migration
- Codegen version matches last migration

This avoids redundant function regeneration on every restart. Use `Force: true` to bypass.

## Transaction Support

When using `*sql.DB`, migrations are applied atomically:

```go
// All-or-nothing: either all functions are created or none
err := migrator.Migrate(ctx, db, "schema.fga")
```

For `*sql.Tx` or `*sql.Conn`, functions are applied individually (caller manages transaction).

## Error Handling

```go
err := migrator.Migrate(ctx, db, "schema.fga")
if err != nil {
    // Check specific error types
    if schema.IsCyclicSchemaErr(err) {
        log.Fatal("Schema has cyclic relations")
    }
    if errors.Is(err, melange.ErrInvalidSchema) {
        log.Fatal("Schema syntax error")
    }
    log.Fatalf("Migration failed: %v", err)
}
```

## Database Requirements

The migrator requires:

1. **PostgreSQL** - Generates PL/pgSQL functions
2. **Schema permissions** - Ability to CREATE/REPLACE FUNCTION
3. **melange_tuples view** - User-defined view over domain tables (checked by `GetStatus`)

## Dependency Information

This package imports:

- `github.com/lib/pq` - PostgreSQL driver for array handling
- `github.com/pthm/melange/pkg/parser` - Schema parsing
- `github.com/pthm/melange/pkg/schema` - Schema types
- `github.com/pthm/melange/internal/sqlgen` - SQL generation

## Execer Interface

The migrator accepts any type implementing `Execer`:

```go
type Execer interface {
    ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
    QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
    QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
```

Compatible types: `*sql.DB`, `*sql.Tx`, `*sql.Conn`
