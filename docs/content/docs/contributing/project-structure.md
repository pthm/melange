---
title: Project Structure
weight: 3
---

This guide explains Melange's codebase layout and the purpose of each component.

## Module Structure

Melange is organized as multiple Go modules for clean dependency isolation:

```
melange/
├── go.mod                 # Root module (github.com/pthm/melange)
├── melange/
│   └── go.mod             # Runtime module (github.com/pthm/melange/melange)
├── clients/
│   └── typescript/        # TypeScript client (future)
├── pkg/                   # Public packages
├── internal/              # Internal packages
├── cmd/melange/           # CLI (part of root module)
└── test/                  # Tests (part of root module)
```

| Module | Purpose | Dependencies |
|--------|---------|--------------|
| Root (`github.com/pthm/melange`) | CLI, schema parsing, code generation | OpenFGA parser, pq driver |
| Runtime (`github.com/pthm/melange/melange`) | Checker, cache, types, errors | stdlib only |

## Runtime Module (`melange/`)

The runtime module has zero external dependencies and provides the permission checking API:

```
melange/
├── go.mod
├── melange.go         # Object, Relation, Querier interfaces
├── checker.go         # Checker implementation
├── cache.go           # Permission result caching
├── decision.go        # Decision overrides for testing
├── errors.go          # Sentinel errors and helpers
└── validator.go       # Input validation
```

### Key Files

**`melange.go`** - Core types:
- `Object` - Type + ID resource identifier
- `ObjectLike` / `SubjectLike` - Interfaces for domain models
- `Relation` / `RelationLike` - Relation types
- `Querier` - Interface for DB, Tx, Conn

**`checker.go`** - Permission checking:
- `Checker` - Main API for permission checks
- `Check()` - Single permission check
- `ListObjects()` / `ListSubjects()` - List operations
- `Must()` - Panic on failure

**`cache.go`** - Caching:
- `Cache` interface
- `CacheImpl` - In-memory implementation with TTL

**`decision.go`** - Testing utilities:
- `Decision` type (Allow/Deny/Unset)
- Context-based decision propagation

## Root Module Packages

### Public Packages (`pkg/`)

```
pkg/
├── schema/            # Schema types, validation, closure computation
├── parser/            # OpenFGA DSL parsing
├── migrator/          # Database migration APIs
├── compiler/          # SQL code generation APIs
└── clientgen/         # Client code generation APIs
```

**`pkg/schema`** - Schema handling:
- `TypeDefinition` - Object type with relations
- `RelationDefinition` - Individual relation rules
- `AuthzModel` - Database row representation
- Validation and cycle detection

**`pkg/parser`** - Schema parsing:
- `ParseSchema(path)` - Parse `.fga` file
- `ParseSchemaString(dsl)` - Parse DSL string
- Wraps the official OpenFGA language parser
- Converts OpenFGA AST to `schema.TypeDefinition` slice
- Only package that imports the OpenFGA parser dependency

**`pkg/migrator`** - Migration:
- `Migrate(ctx, db, dir)` - One-step migration
- `MigrateFromString(ctx, db, dsl)` - Migrate from string

**`pkg/clientgen`** - Client code generation:
- `Generate(runtime, types, cfg)` - Generate client code
- `ListRuntimes()` - Available generators

### Internal Packages (`internal/`)

```
internal/
├── clientgen/         # Generator registry and implementations
│   ├── generator.go   # Generator interface
│   ├── go/            # Go generator
│   └── typescript/    # TypeScript generator (stub)
├── sqlgen/            # SQL DSL, query builders, code generation internals
└── doctor/            # CLI health check logic
```

## SQL Generation

SQL is generated from the internal sqlgen package:

```
internal/sqlgen/
├── sql.go             # SQL DSL core
├── expr.go            # Expression types
├── query.go           # Query builders
├── check_queries.go   # Permission check queries
├── list_queries.go    # List operation queries
└── *.go               # Additional helpers
```

### Key Functions

**`check_permission()`** - Core permission check:
- Takes subject, relation, object
- Returns 1 (allowed) or 0 (denied)
- Handles direct tuples, implied relations, parent inheritance, exclusions

**`list_accessible_objects()`** - Find accessible objects:
- Returns object IDs the subject can access
- Uses recursive CTE

**`list_accessible_subjects()`** - Find subjects with access:
- Returns subject IDs that have access to object
- Uses recursive CTE

## CLI

The CLI is part of the root module:

```
cmd/melange/
└── main.go
```

Commands:
- `validate` - Check schema syntax
- `generate client` - Generate type-safe client code
- `migrate` - Apply schema to database
- `status` - Check migration status
- `doctor` - Health check

## Test Module

Tests are part of the root module:

```
test/
├── benchmark_test.go       # Performance benchmarks
├── integration_test.go     # Integration tests
├── openfgatests/           # OpenFGA compatibility suite
│   ├── loader.go           # Test case loader
│   ├── client.go           # Test client
│   ├── runner.go           # Test execution
│   └── *_test.go           # Test files
├── cmd/dumptest/           # Test case inspector
├── cmd/dumpsql/            # SQL dump tool
└── testutil/               # Test utilities
    └── testdata/           # Test schemas
```

### OpenFGA Test Suite

The `openfgatests` package runs OpenFGA's official test suite against Melange:

**`loader.go`** - Loads test cases from embedded YAML files

**`client.go`** - Test client:
- Uses `pkg/parser` for schema parsing
- Creates database schema
- Executes assertions

**`runner.go`** - Test execution:
- Category-based test runners
- Pattern matching
- Benchmark wrappers

## Language Clients

```
clients/
├── README.md           # Overview and contribution guide
└── typescript/         # TypeScript client
    ├── package.json
    ├── tsconfig.json
    └── src/
```

The Go runtime lives at `melange/` for clean imports:

```go
import "github.com/pthm/melange/melange"
```

Other language clients live under `clients/` as their package managers don't have Go's path constraints.

## Documentation

```
docs/
├── hugo.yaml           # Hugo configuration
├── content/
│   ├── _index.md       # Landing page
│   └── docs/           # Documentation pages
├── layouts/            # Custom layouts
├── assets/             # CSS/JS assets
└── themes/hextra/      # Hugo theme (submodule)
```

## Making Changes

### Adding a Feature

1. **Runtime changes** go in `melange/`
2. **Parser changes** go in `pkg/parser/`
3. **SQL generation** changes go in `internal/sqlgen/`
4. **Tests** go in `test/` or the appropriate `*_test.go` file

### SQL Generation Changes

When modifying SQL generation in `internal/sqlgen/`:

1. Update the query builder
2. Run `just test-openfga` to verify compatibility
3. Run `just bench-openfga` to check performance impact

### Adding a Language Generator

1. Create `internal/clientgen/<language>/generate.go`
2. Implement the `Generator` interface
3. Register in `init()` function
4. Import in `pkg/clientgen/api.go`

### Adding Tests

For OpenFGA compatibility tests, the test cases come from the embedded OpenFGA test suite. To test custom scenarios:

```go
func TestCustomScenario(t *testing.T) {
    db := testutil.SetupDB(t)

    // Apply schema
    err := migrator.MigrateFromString(ctx, db, `
model
  schema 1.1
type user
type doc
  relations
    define owner: [user]
`)
    require.NoError(t, err)

    // Create tuples view
    // Test assertions
}
```
