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

// PlanWithOptions predicts what MigrateWithOptions would do with opts,
// unforced, without changing the database.
func PlanWithOptions(ctx context.Context, db Execer, schemaPath string, opts MigrateOptions) (*MigrationPlan, error)
```

### Migrator Type

```go
// NewMigrator creates a new schema migrator.
func NewMigrator(db Execer, schemaPath string) *Migrator

// MigrateWithTypes performs migration using pre-parsed type definitions.
// It records no migration, so it generates under the recorded exposure and
// returns *UnrecordedExposureChangeError when SetExposure asks for another.
func (m *Migrator) MigrateWithTypes(ctx context.Context, types []TypeDefinition) error

// MigrateWithTypesAndOptions performs migration with full options control.
// It records the migration when opts.SchemaContent holds the schema text;
// without it, it records nothing and refuses an exposure change like
// MigrateWithTypes.
func (m *Migrator) MigrateWithTypesAndOptions(ctx context.Context, types []TypeDefinition, opts InternalMigrateOptions) error

// Plan predicts what MigrateWithTypesAndOptions would do with opts, unforced.
// It reads the database and takes no lock; Force and DryRun are ignored.
func (m *Migrator) Plan(ctx context.Context, types []TypeDefinition, opts InternalMigrateOptions) (*MigrationPlan, error)

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
    DryRun             io.Writer // Output SQL without applying; nil = apply normally
    Force              bool      // Re-run even if unchanged; also drops orphans with CASCADE and overrides the downgrade guard
    Version            string    // Melange version for traceability
    DatabaseSchema     string    // PostgreSQL schema for melange objects
    IfDeployedChecksum *string   // Compare-and-swap: abort with *DeployedModelChangedError unless this checksum is deployed
    Exposure           *Exposure // SQL API to generate; nil keeps the recorded one (the default when none), a pointer to the zero value restores the default
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
    Exposure       Exposure // normalized exposure the functions were generated with
}

// MigrationPlan is what an unforced migration would do (Plan).
type MigrationPlan struct {
    Action   PlanAction         // PlanApply, PlanRecordOnly, PlanSkip or PlanRefusedAhead
    Record   *MigrationRecord   // last migration; nil when none is recorded
    Exposure Exposure           // the exposure the migration generates with
    Orphans  []OrphanedFunction // what an applying (or forced) run drops, with dependents
}
```

`PlanRecordOnly` means the generated SQL is the SQL the last migration
recorded: the migration records the new state and applies nothing, so a
function dropped or edited by hand stays that way. `PlanSkip` does nothing
at all. Both are fixed by `Force: true`, which also drops `Orphans` with
`CASCADE`, dependents included. `PlanRefusedAhead` means a newer melange
migrated the database and migrate returns `*DatabaseAheadError`.

## Usage Examples

### Basic Migration (Recommended)

```go
import "github.com/pthm/melange/pkg/migrator"

// Run on application startup. Concurrent replicas are serialized by an
// advisory lock, so every replica can migrate.
func main() {
    db, _ := sql.Open("postgres", connString)

    err := migrator.Migrate(ctx, db, "schemas/schema.fga")
    var ahead *migrator.DatabaseAheadError
    switch {
    case errors.As(err, &ahead):
        // A replica running a newer melange already upgraded the database
        // (e.g. mid rolling deploy). Its functions keep the same SQL API.
        log.Printf("Skipping migration: %v", err)
    case err != nil:
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

### Predict a Migration

```go
// Readiness check: the last migration recorded exactly what this schema,
// exposure and codegen version generate. Plan reads the migration record,
// not the installed functions, so PlanSkip does not detect a function
// dropped or edited by hand; `melange doctor` and `melange functions` check
// those.
plan, err := migrator.PlanWithOptions(ctx, db, "schemas/schema.fga", migrator.MigrateOptions{})
if err != nil {
    log.Fatal(err)
}
if plan.Action != migrator.PlanSkip {
    log.Printf("migration pending: %s", plan.Action)
}
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

`MigrateWithTypes` records no migration, so it cannot change the SQL API
exposure: it generates under the recorded one (the default when none is
recorded), and an exposure set with `SetExposure` must be that same one. To
change it, record the migration by passing the schema text:

```go
m := migrator.NewMigrator(db, "")
err := m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
    SchemaContent: schemaContent,
    SchemaFormat:  migrator.FormatSingle,
    Exposure:      &exposure,
})
var unrecorded *migrator.UnrecordedExposureChangeError
if errors.As(err, &unrecorded) {
    // Only a migration without SchemaContent returns this; nothing was applied.
    log.Printf("exposure %v needs a recorded migration", unrecorded.Requested)
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
7. **Cleanup** - Drop functions a previous migration installed that this one no longer generates. When other database objects (for example a view or RLS policy) depend on one, an unforced migration fails with `*DependentObjectsError` before applying anything; `Force: true` drops them with `CASCADE`, dependents included
8. **Track** - Record the migration (schema, codegen version, function checksums and SQL API exposure) in `melange_migrations`

## Skip-If-Unchanged Optimization

Migrations are skipped when all of these match the last migration:

- Schema content hash
- Codegen version
- SQL API exposure

When one differs, the SQL is generated and compared with the function
checksums the last migration recorded; identical SQL is recorded without
being applied. Neither phase looks at the installed functions, so one
dropped or edited by hand is restored only by `Force: true`. This avoids
redundant function regeneration on every restart. `Plan` predicts the
outcome without migrating.

## Transaction Support

When the Execer can begin a transaction (`*sql.DB`, `*sql.Conn`), migrations are applied atomically in the migrator's own transaction:

```go
// All-or-nothing: either all functions are created or none
err := migrator.Migrate(ctx, db, "schema.fga")
```

With a `*sql.Tx`, functions are applied on the caller's transaction, which the caller commits or rolls back.

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
