# Melange

<img align="right" width="300" src="assets/mascot.png">

Melange is a pure PostgreSQL + Go authorization library inspired by OpenFGA/Zanzibar
and the rover-app pgfga implementation: https://github.com/rover-app/pgfga

## Overview

Melange provides fine-grained authorization with:

- PostgreSQL functions for permission checks
- Zero tuple sync (permissions derived from a view over your tables)
- Optional code generation for type-safe constants
- Zero runtime dependencies (core library is pure stdlib)

## Module Structure

Melange is split into two modules for clean dependency isolation:

| Module                            | Purpose                                | Dependencies   |
| --------------------------------- | -------------------------------------- | -------------- |
| `github.com/pthm/melange`         | Core runtime (checker, types, errors)  | stdlib only    |
| `github.com/pthm/melange/tooling` | Schema parsing, CLI, migration helpers | OpenFGA parser |

**Most applications only import the core module at runtime.** The tooling module
is used during development (CLI, code generation) or if you need programmatic
schema parsing.

## Requirements

- PostgreSQL database
- A `.fga` schema file (parsed by CLI or tooling module)
- A `melange_tuples` view that maps your domain tables into tuples

## Quick Start

1. Create a schema file (`schema.fga`):

```fga
model
  schema 1.1

type user

type repository
  relations
    define owner: [user]
    define can_read: owner
```

2. Create a `melange_tuples` view:

```sql
CREATE OR REPLACE VIEW melange_tuples AS
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_owners;
```

3. Apply Melange infrastructure + schema:

```bash
melange migrate --db postgres://localhost/mydb --schemas-dir schemas
```

4. Generate type-safe Go constants:

```bash
melange generate --schemas-dir schemas --generate-dir internal/authz --generate-pkg authz
```

5. Check permissions in Go:

```go
checker := melange.NewChecker(db)
ok, err := checker.Check(ctx, authz.User("123"), authz.RelCanRead, authz.Repository("456"))
if err != nil {
    return err
}
if !ok {
    return ErrForbidden
}
```

## Core Concepts

- **Objects**: Both subjects and resources are modeled as objects.
  - `Object{Type: "user", ID: "123"}`
- **Relations**: Simple strings (generated constants are optional).
- **Wildcard**: Use `*` as a subject ID for public access (type:\*).

## Checker API

Melange works with `*sql.DB`, `*sql.Tx`, or `*sql.Conn`.

```go
checker := melange.NewChecker(db)
ok, err := checker.Check(ctx, subject, relation, object)
ids, err := checker.ListObjects(ctx, subject, relation, objectType)
```

## Caching

```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))
```

## Decision Overrides

For tests or admin tools:

```go
checker := melange.NewChecker(db, melange.WithDecision(melange.DecisionAllow))
```

## Error Handling

Sentinel errors:

- `melange.ErrNoTuplesTable` - melange_tuples view doesn't exist
- `melange.ErrMissingModel` - melange_model table doesn't exist
- `melange.ErrEmptyModel` - Model table exists but is empty
- `melange.ErrInvalidSchema` - Schema parsing failed
- `melange.ErrMissingFunction` - SQL functions not installed

Helpers:

- `melange.IsNoTuplesTableErr(err)`
- `melange.IsMissingModelErr(err)`
- `melange.IsEmptyModelErr(err)`
- `melange.IsInvalidSchemaErr(err)`
- `melange.IsMissingFunctionErr(err)`

## Schema Helpers

Query schema definitions to build dynamic UIs or introspect the model:

```go
types := []melange.TypeDefinition{...} // from tooling.ParseSchema

// Get all unique subject types across the schema
subjects := melange.SubjectTypes(types)
// e.g., ["user", "team", "organization"]

// Get subject types for a specific relation
allowed := melange.RelationSubjects(types, "repository", "owner")
// e.g., ["user"]  (only users can be owners)
```

## Programmatic Migration

For programmatic schema loading (without the CLI):

```go
import "github.com/pthm/melange/tooling"

// Parse and migrate in one step
err := tooling.Migrate(ctx, db, "schemas")

// Or with more control:
types, err := tooling.ParseSchema("schemas/schema.fga")
migrator := melange.NewMigrator(db, "schemas")
err = migrator.MigrateWithTypes(ctx, types)
```

The `Migrator` also supports individual steps:

```go
migrator := melange.NewMigrator(db, "schemas")

// Apply DDL only (tables + functions)
err := migrator.ApplyDDL(ctx)

// Load schema into model table
err := migrator.MigrateWithTypes(ctx, types)

// Check current status
status, err := migrator.GetStatus(ctx)
// status.SchemaExists, status.ModelCount
```

## CLI

```
melange [command] [flags]

Commands:
  migrate   Apply schema to database
  generate  Generate Go types from schema
  validate  Validate schema syntax
  status    Show current schema status
```
