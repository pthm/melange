---
title: CLI Reference
weight: 3
---

The Melange CLI provides commands for validating schemas, generating Go code, and applying migrations to your database.

## Installation

```bash
go install github.com/pthm/melange/cmd/melange@latest
```

## Commands

### validate

Check `.fga` schema syntax without database access.

```bash
melange validate --schemas-dir schemas
```

**Output:**
```
Schema is valid. Found 3 types:
  - user (0 relations)
  - organization (3 relations)
  - repository (5 relations)
```

This command parses the schema using the OpenFGA parser and reports any syntax errors. It does not require database access.

### generate

Generate type-safe Go code from your schema.

```bash
melange generate \
  --schemas-dir schemas \
  --generate-dir internal/authz \
  --generate-pkg authz
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--schemas-dir` | `schemas` | Directory containing `schema.fga` |
| `--generate-dir` | `authz` | Output directory for generated code |
| `--generate-pkg` | `authz` | Package name for generated code |
| `--id-type` | `string` | ID type for constructors (`string`, `int64`, `uuid.UUID`) |
| `--relation-prefix` | `""` | Only generate relations with this prefix (e.g., `can_`) |

**Example with all options:**
```bash
melange generate \
  --schemas-dir schemas \
  --generate-dir internal/authz \
  --generate-pkg authz \
  --id-type int64 \
  --relation-prefix can_
```

**Generated code example:**
```go
// schema_gen.go
package authz

import "github.com/pthm/melange"

// Object types
const (
    TypeUser         = "user"
    TypeOrganization = "organization"
    TypeRepository   = "repository"
)

// Relation constants (filtered by prefix "can_")
const (
    RelCanRead   = "can_read"
    RelCanWrite  = "can_write"
    RelCanDelete = "can_delete"
)

// Type-safe constructors
func User(id int64) melange.Object {
    return melange.Object{Type: TypeUser, ID: fmt.Sprint(id)}
}

func Repository(id int64) melange.Object {
    return melange.Object{Type: TypeRepository, ID: fmt.Sprint(id)}
}
```

### migrate

Apply the schema to your PostgreSQL database.

```bash
melange migrate \
  --db postgres://localhost/mydb \
  --schemas-dir schemas
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `$DATABASE_URL` | PostgreSQL connection string |
| `--schemas-dir` | `schemas` | Directory containing `schema.fga` |

This command:
1. Creates the `melange_model` table if it doesn't exist
2. Installs SQL functions (`check_permission`, `list_accessible_objects`, etc.)
3. Loads your schema into `melange_model`

The migration is idempotent - safe to run multiple times. Each run replaces the schema in `melange_model`.

### status

Check the current migration status.

```bash
melange status \
  --db postgres://localhost/mydb \
  --schemas-dir schemas
```

**Output:**
```
Schema file:  present
Model count:  15 rows
```

This helps you verify that:
- Your schema file exists
- The schema has been loaded into the database

## Global Flags

These flags apply to all commands:

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `$DATABASE_URL` | PostgreSQL connection string |
| `--schemas-dir` | `schemas` | Directory containing schema files |
| `--config` | `melange.yaml` | Config file (not yet implemented) |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | Default database connection string (used if `--db` not provided) |

## Common Workflows

### Development Setup

```bash
# 1. Validate schema syntax
melange validate --schemas-dir schemas

# 2. Apply to local database
melange migrate \
  --db postgres://localhost/myapp_dev \
  --schemas-dir schemas

# 3. Generate Go code
melange generate \
  --schemas-dir schemas \
  --generate-dir internal/authz \
  --generate-pkg authz
```

### CI/CD Pipeline

```bash
# Validate schema (fails fast if syntax error)
melange validate --schemas-dir schemas

# Apply migrations to staging/production
melange migrate \
  --db $DATABASE_URL \
  --schemas-dir schemas

# Verify migration succeeded
melange status --db $DATABASE_URL
```

### Schema Updates

When you modify your `.fga` schema:

```bash
# 1. Validate changes
melange validate --schemas-dir schemas

# 2. Regenerate Go code
melange generate \
  --schemas-dir schemas \
  --generate-dir internal/authz \
  --generate-pkg authz

# 3. Apply to database
melange migrate --db $DATABASE_URL --schemas-dir schemas
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error (schema parse error, database connection failure, etc.) |

## Programmatic Alternative

For programmatic schema management without the CLI, use the Go API:

```go
import (
    "github.com/pthm/melange/schema"
    "github.com/pthm/melange/tooling"
)

// Parse and migrate in one step
err := tooling.Migrate(ctx, db, "schemas")

// Or with more control
types, err := tooling.ParseSchema("schemas/schema.fga")
migrator := schema.NewMigrator(db, "schemas")
err = migrator.MigrateWithTypes(ctx, types)
```

See [Checking Permissions]({{< relref "checking-permissions" >}}) for the full Go API reference.
