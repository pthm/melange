---
title: Project Structure
weight: 3
---

This guide explains Melange's codebase layout and the purpose of each component.

## Module Structure

Melange is organized as multiple Go modules for clean dependency isolation:

```
melange/
├── go.mod                 # Core module (github.com/pthm/melange)
├── tooling/
│   └── go.mod             # Tooling module (github.com/pthm/melange/tooling)
├── cmd/melange/
│   └── go.mod             # CLI module
└── test/
    └── go.mod             # Test module
```

| Module | Purpose | Dependencies |
|--------|---------|--------------|
| Core (`github.com/pthm/melange`) | Runtime checker, types, errors | stdlib only |
| Tooling (`github.com/pthm/melange/tooling`) | Schema parsing, code generation | OpenFGA parser |
| CLI (`github.com/pthm/melange/cmd/melange`) | Command-line tool | Tooling, pq driver |
| Test (`github.com/pthm/melange/test`) | Test suite | All of the above |

## Core Module

The core module has zero external dependencies and provides the runtime API:

```
melange.go         # Object, Relation, Querier interfaces
checker.go         # Checker implementation
cache.go           # Permission result caching
decision.go        # Decision overrides for testing
errors.go          # Sentinel errors and helpers
schema/            # Schema types, validation, codegen, migration
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

**`schema/`** - Schema handling:
- `TypeDefinition` - Object type with relations
- `RelationDefinition` - Individual relation rules
- `AuthzModel` - Database row representation
- Migrator, validation, and codegen helpers

**`cache.go`** - Caching:
- `Cache` interface
- `CacheImpl` - In-memory implementation with TTL

**`decision.go`** - Testing utilities:
- `Decision` type (Allow/Deny/Unset)
- Context-based decision propagation

## Tooling Module

The tooling module has external dependencies (OpenFGA parser) and is used for schema processing:

```
tooling/
├── go.mod
├── parser.go      # OpenFGA DSL parsing
├── migrate.go     # Convenience migration wrappers
└── generate.go    # Re-exports of generation config
```

### Key Files

**`parser.go`** - Schema parsing:
- `ParseSchema(path)` - Parse `.fga` file
- `ParseSchemaString(dsl)` - Parse DSL string
- Converts OpenFGA AST to `schema.TypeDefinition` slice

**`migrate.go`** - Migration helpers:
- `Migrate(ctx, db, dir)` - One-step migration
- `MigrateFromString(ctx, db, dsl)` - Migrate from string

## SQL Generation

SQL is generated from templates and applied by the migrator:

```
schema/
├── ddl.go         # melange_model + melange_relation_closure DDL
└── templates/     # check_permission, list_accessible_* and helpers
    ├── *.tpl.sql
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

The CLI provides commands for schema management:

```
cmd/melange/
├── go.mod
└── main.go
```

Commands:
- `validate` - Check schema syntax
- `generate` - Generate Go code
- `migrate` - Apply schema to database
- `status` - Check migration status

## Test Module

The test module contains integration tests and the OpenFGA compatibility suite:

```
test/
├── go.mod
├── checker_test.go     # Decision override tests
├── errors_test.go      # Error helper tests
├── generate_test.go    # Code generation tests
├── schema_test.go      # Schema helper tests
├── openfgatests/       # OpenFGA compatibility suite
│   ├── loader.go       # Test case loader
│   ├── client.go       # Test client (uses tooling parser)
│   ├── runner.go       # Test execution
│   └── *_test.go       # Test files
├── cmd/dumptest/       # Test case inspector
└── testutil/           # Test utilities
    └── testdata/       # Test schemas
```

### OpenFGA Test Suite

The `openfgatests` package runs OpenFGA's official test suite against Melange:

**`loader.go`** - Loads test cases from embedded YAML files

**`client.go`** - Test client:
- Uses `tooling.ConvertProtoModel()` for schema parsing
- Creates database schema
- Executes assertions

**`runner.go`** - Test execution:
- Category-based test runners
- Pattern matching
- Benchmark wrappers

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

1. **Core changes** go in the root module (`*.go`)
2. **Parser changes** go in `tooling/parser.go`
3. **SQL changes** go in `schema/templates/*.tpl.sql` or `schema/ddl.go`
4. **Tests** go in `test/` or the appropriate `*_test.go` file

### SQL Template Changes

When modifying `schema/templates/*.tpl.sql`:

1. Update the SQL function
2. Run `just test-openfga` to verify compatibility
3. Run `just bench-openfga` to check performance impact

### Adding Tests

For OpenFGA compatibility tests, the test cases come from the embedded OpenFGA test suite. To test custom scenarios:

```go
func TestCustomScenario(t *testing.T) {
    db := testutil.SetupDB(t)

    // Apply schema
    err := tooling.MigrateFromString(ctx, db, `
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
