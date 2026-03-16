---
title: CLI Reference
weight: 1
---

The Melange CLI provides commands for validating schemas, generating client code, and applying migrations to your database. Built on Cobra/Viper, it supports [configuration files](configuration.md), environment variables, and command-line flags with consistent precedence.

## Installation

```bash
go install github.com/pthm/melange/cmd/melange@latest
```

## Global Flags

These flags are available on all commands:

| Flag                | Description                                                                                         |
| ------------------- | --------------------------------------------------------------------------------------------------- |
| `--config`          | Path to config file (default: auto-discover `melange.yaml`). See [Configuration](configuration.md). |
| `-v`, `--verbose`   | Increase verbosity (can be repeated: `-vv`, `-vvv`)                                                 |
| `-q`, `--quiet`     | Suppress non-error output                                                                           |
| `--no-update-check` | Disable automatic update checking                                                                   |

### Update Notifications

By default, Melange automatically checks for new versions in the background and displays a notification if an update is available. The check is:

- **Non-blocking**: Runs in a background goroutine with a 5-second timeout
- **Cached**: Results are cached for 24 hours in `~/.cache/melange/update-check.json`
- **Fast**: Uses a 1-second wait time for displaying results
- **Respectful**: Automatically disabled in CI environments (when `CI` env var is set)

**Example notification:**

```
$ melange migrate
Migration completed successfully

* A new version of melange is available: v1.2.3 (current: v1.2.0)
  brew upgrade melange  or  go install github.com/pthm/melange/cmd/melange@latest
```

**To disable update checks:**

```bash
melange --no-update-check migrate
```

**To clear the cache:**

```bash
rm -rf ~/.cache/melange/update-check.json
```

The cache respects the `XDG_CACHE_HOME` environment variable if set.

## Command Groups

Commands are organized into logical groups:

**Schema Commands:** `validate`, `migrate`, `status`, `doctor`
**Client Commands:** `generate`
**Utility Commands:** `init`, `config`, `version`, `license`

---

## Schema Commands

### validate

Check `.fga` schema syntax without database access.

```bash
melange validate --schema schemas/schema.fga
```

**Output:**

```
Schema is valid. Found 3 types:
  - user (0 relations)
  - organization (3 relations)
  - repository (5 relations)
```

This command parses the schema using the OpenFGA parser and reports any syntax errors. It does not require database access.

**Flags:**

| Flag       | Default              | Description             |
| ---------- | -------------------- | ----------------------- |
| `--schema` | `schemas/schema.fga` | Path to schema.fga file |

The schema path can also be set via configuration file or environment variable. See [Configuration](#configuration).

### migrate

Apply the schema to your PostgreSQL database.

```bash
melange migrate \
  --db postgres://localhost/mydb \
  --schema schemas/schema.fga
```

**Flags:**

| Flag        | Default              | Description                                   |
| ----------- | -------------------- | --------------------------------------------- |
| `--db`      | (from config)        | PostgreSQL connection string                  |
| `--schema`  | `schemas/schema.fga` | Path to schema.fga file                       |
| `--dry-run` | `false`              | Output SQL to stdout without applying changes |
| `--force`   | `false`              | Force migration even if schema is unchanged   |

This command:

1. Checks if the schema has changed since the last migration
2. Installs generated SQL functions (`check_permission`, `list_accessible_objects`, etc.)
3. Cleans up orphaned functions from removed relations
4. Records the migration in `melange_migrations` table

**Skip-if-unchanged behavior:**

Melange tracks schema changes using a SHA256 checksum. If you run `migrate` and the schema hasn't changed since the last migration, it will be skipped automatically:

```
Schema unchanged, migration skipped.
Use --force to re-apply.
```

Use `--force` to re-apply the migration anyway (useful after updating Melange itself).

**Dry-run mode:**

Preview the migration SQL without applying it:

```bash
melange migrate --db postgres://localhost/mydb --dry-run
```

This outputs the complete SQL that would be executed, including:

- DDL for the migrations tracking table
- All generated check functions
- All generated list functions
- Dispatcher functions
- The migration record insert

Dry-run output goes to stdout, so you can redirect it:

```bash
melange migrate --db postgres://localhost/mydb --dry-run > migration.sql
```

**Orphan cleanup:**

When you remove a relation from your schema, Melange automatically drops the orphaned SQL functions during migration. For example, if you remove the `editor` relation from `document`, the next migration will drop `check_document_editor`, `list_document_editor_objects`, etc.

**melange_tuples warning:**

After migration, if the `melange_tuples` view doesn't exist, you'll see a warning:

```
WARNING: melange_tuples view/table does not exist.
         Permission checks will fail until you create it.
```

See [Tuples View](../concepts/tuples-view.md) for setup instructions.

### status

Check the current migration status.

```bash
melange status \
  --db postgres://localhost/mydb \
  --schema schemas/schema.fga
```

**Flags:**

| Flag       | Default              | Description                  |
| ---------- | -------------------- | ---------------------------- |
| `--db`     | (from config)        | PostgreSQL connection string |
| `--schema` | `schemas/schema.fga` | Path to schema.fga file      |

**Output:**

```
Schema file:  present
Tuples view:  present
```

This helps you verify that:

- Your schema file exists
- The tuples view exists in the database

### doctor

Run comprehensive health checks on your authorization infrastructure.

```bash
melange doctor \
  --db postgres://localhost/mydb \
  --schema schemas/schema.fga
```

**Flags:**

| Flag                 | Default              | Description                                  |
| -------------------- | -------------------- | -------------------------------------------- |
| `--db`               | (from config)        | PostgreSQL connection string                 |
| `--schema`           | `schemas/schema.fga` | Path to schema.fga file                      |
| `--verbose`          | `false`              | Show detailed output with additional context |
| `--skip-performance` | `false`              | Skip performance checks (view analysis)      |

**Output:**

```
melange doctor - Health Check

Schema File
  ✓ Schema file exists at schemas/schema.fga
  ✓ Schema is valid (3 types, 8 relations)
  ✓ No cyclic dependencies detected

Migration State
  ✓ melange_migrations table exists
  ✓ Schema migrated (24 functions tracked)
  ✓ Schema is in sync with database

Generated Functions
  ✓ All dispatcher functions present
  ✓ All 24 expected functions present

Tuples Source
  ✓ melange_tuples exists (view)
  ✓ All required columns present

Data Health
  ✓ melange_tuples contains 1523 tuples
  ✓ All sampled tuples reference valid types and relations

Performance
  ✓ View definition parsed (3 branches)
  ✓ View uses UNION ALL (no unnecessary deduplication)
  ✓ Source tables: users, organizations, repositories
  ✓ All ::text cast columns have expression indexes

Summary: 15 passed, 0 warnings, 0 errors
```

The doctor command performs the following checks:

**Schema File:**

- Verifies the schema file exists
- Parses and validates schema syntax
- Detects cyclic dependencies in implied-by relationships

**Migration State:**

- Checks if the `melange_migrations` tracking table exists
- Verifies a migration has been applied
- Compares schema checksum to detect if schema has changed since last migration
- Checks if codegen version has changed (indicating Melange was updated)

**Generated Functions:**

- Verifies all dispatcher functions exist (`check_permission`, `list_accessible_objects`, etc.)
- Compares expected functions from schema against actual functions in database
- Identifies orphan functions from previous schema versions

**Tuples Source:**

- Verifies `melange_tuples` view/table exists
- Checks required columns: `object_type`, `object_id`, `relation`, `subject_type`, `subject_id`
- Warns if using a materialized view (requires manual refresh)

**Data Health:**

- Reports tuple count
- Validates that tuples reference valid types and relations defined in the schema

**Performance** (view-based `melange_tuples` only):

Performance checks run automatically when `melange_tuples` is a view (not a table or materialized view). They can be disabled with `--skip-performance`.

- Parses the view definition via `pg_get_viewdef()` and reports the number of UNION branches and source tables
- Detects bare `UNION` (warns to use `UNION ALL` instead, since `UNION` adds deduplication overhead)
- Checks for missing expression indexes on columns cast with `::text` in the view definition. Missing indexes cause sequential scans on the source tables:
  - **Warning** if the source table has fewer than 10,000 rows (recommended for future scaling)
  - **Failure** if the source table has 10,000+ rows (critical at current scale)
  - Provides exact `CREATE INDEX` statements as fix hints

**Verbose mode:**

Use `--verbose` to see additional details for each check:

```bash
melange doctor --db postgres://localhost/mydb --verbose
```

This shows:

- Exact file paths and checksums
- Lists of missing or orphan functions
- Specific unknown types or relations found in data

**Common issues and fixes:**

| Issue                    | Fix                                                   |
| ------------------------ | ----------------------------------------------------- |
| Schema file not found    | Create `schemas/schema.fga`                           |
| Schema has syntax errors | Run `fga model validate` for detailed errors          |
| Schema out of sync       | Run `melange migrate`                                 |
| Missing functions        | Run `melange migrate`                                 |
| Orphan functions         | Run `melange migrate` (cleanup is automatic)          |
| melange_tuples missing   | Create a view over your domain tables                 |
| Missing columns          | Update melange_tuples to include all required columns |
| Unknown types in tuples  | Update tuples view or schema to match                 |
| UNION instead of UNION ALL | Replace `UNION` with `UNION ALL` in view definition |
| Missing expression index | Run the `CREATE INDEX` command shown in the fix hint  |

---

## Client Commands

### generate client

Generate type-safe client code from your schema.

```bash
melange generate client \
  --runtime go \
  --schema schemas/schema.fga \
  --output internal/authz \
  --package authz
```

**Flags:**

| Flag        | Default              | Description                                               |
| ----------- | -------------------- | --------------------------------------------------------- |
| `--runtime` | (required)           | Target runtime: `go`, `typescript`                        |
| `--schema`  | `schemas/schema.fga` | Path to schema.fga file                                   |
| `--output`  | stdout               | Output directory for generated code                       |
| `--package` | `authz`              | Package name for generated code                           |
| `--id-type` | `string`             | ID type for constructors (`string`, `int64`, `uuid.UUID`) |
| `--filter`  | `""`                 | Only generate relations with this prefix (e.g., `can_`)   |

**Example with all options:**

```bash
melange generate client \
  --runtime go \
  --schema schemas/schema.fga \
  --output internal/authz \
  --package authz \
  --id-type int64 \
  --filter can_
```

**Output to stdout:**

```bash
melange generate client --runtime go --schema schemas/schema.fga
```

**Generated code example:**

```go
// schema_gen.go
package authz

import "github.com/pthm/melange/melange"

// Object types
const (
    TypeUser         melange.ObjectType = "user"
    TypeOrganization melange.ObjectType = "organization"
    TypeRepository   melange.ObjectType = "repository"
)

// Relation constants (filtered by prefix "can_")
const (
    RelCanRead   melange.Relation = "can_read"
    RelCanWrite  melange.Relation = "can_write"
    RelCanDelete melange.Relation = "can_delete"
)

// Type-safe constructors
func User(id int64) melange.Object {
    return melange.Object{Type: TypeUser, ID: fmt.Sprint(id)}
}

func Repository(id int64) melange.Object {
    return melange.Object{Type: TypeRepository, ID: fmt.Sprint(id)}
}

// Wildcard constructors
func AnyUser() melange.Object {
    return melange.Object{Type: TypeUser, ID: "*"}
}
```

**Supported runtimes:**

| Runtime      | Status      | Description                                       |
| ------------ | ----------- | ------------------------------------------------- |
| `go`         | Implemented | Type-safe Go code with constants and constructors |
| `typescript` | Planned     | TypeScript types and factory functions            |

---

## Utility Commands

### init

Initialize a new Melange project with an interactive wizard. Detects your project type (Go or TypeScript) and scaffolds a config file, starter schema, and runtime dependency.

```bash
melange init
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-y`, `--yes` | `false` | Accept all defaults without prompting |
| `--no-install` | `false` | Skip installing runtime dependencies |
| `--schema` | `melange/schema.fga` | Schema file path |
| `--db` | `postgres://localhost:5432/mydb` | Database URL |
| `--template` | `org-rbac` | Starter model: `org-rbac`, `doc-sharing`, `minimal`, `none` |
| `--runtime` | (auto-detected) | Client runtime: `go`, `typescript` |
| `--output` | `internal/authz` (Go) / `src/authz` (TS) | Client output directory |
| `--package` | `authz` | Client package name (Go only) |
| `--id-type` | `string` | Client ID type: `string`, `int64`, `uuid.UUID` |

**Project detection:**

Melange inspects the current directory for project files:

| File | Detected Runtime | Default Output |
|------|-----------------|----------------|
| `go.mod` | Go | `internal/authz` |
| `package.json` | TypeScript | `src/authz` |

When a project is detected, client code generation is enabled by default and the runtime dependency is installed automatically (`go get github.com/pthm/melange/melange` for Go, `npm install @pthm/melange` for TypeScript). If both `go.mod` and `package.json` exist, Go takes precedence.

For TypeScript projects, the package manager is auto-detected from lock files: `bun.lockb`/`bun.lock` → bun, `pnpm-lock.yaml` → pnpm, `yarn.lock` → yarn, otherwise npm.

**Starter models:**

| Template | Description |
|----------|-------------|
| `org-rbac` | Organization with role hierarchy and repository permissions (default) |
| `doc-sharing` | Google Docs-style owner/editor/viewer model |
| `minimal` | Bare-bones model with just `type user` |
| `none` | Skip schema file creation |

**Directory convention:**

By default, `init` creates a `melange/` directory containing both the config and schema:

```
myproject/
├── melange/
│   ├── config.yaml
│   └── schema.fga
└── ...
```

If the schema path is outside the `melange/` directory (e.g., `--schema schemas/auth.fga`), the config is placed at the project root as `melange.yaml` instead.

**Safety:**

- Refuses to overwrite an existing config file in `--yes` mode (use interactive mode to confirm)
- Skips schema creation if the file already exists in `--yes` mode

**Examples:**

```bash
# Interactive wizard
melange init

# Accept all defaults (in a Go project)
melange init -y

# Custom schema and database, skip dependency install
melange init -y --schema melange/auth.fga --db postgres://prod:5432/app --no-install

# Minimal schema with no client generation
melange init -y --template minimal
```

### config show

Display the effective configuration after merging defaults, config file, and environment variables.

```bash
melange config show
```

**Flags:**

| Flag       | Default | Description                          |
| ---------- | ------- | ------------------------------------ |
| `--source` | `false` | Show the config file path being used |

**Example with source:**

```bash
melange config show --source
```

**Output:**

```
Config file: /path/to/project/melange.yaml

schema: schemas/schema.fga
database:
  url: postgres://localhost/mydb
  host: ""
  port: 5432
  ...
```

This is useful for debugging configuration issues and understanding which values are in effect.

### version

Print version information.

```bash
melange version
```

**Output:**

```
melange v1.0.0 (commit: abc1234, built: 2024-01-15)
```

### license

Print license and third-party notices.

```bash
melange license
```

This displays the Melange license and attribution for all embedded third-party dependencies.

---

## Exit Codes

| Code | Meaning                                                              |
| ---- | -------------------------------------------------------------------- |
| 0    | Success                                                              |
| 1    | General error (validation, runtime, IO)                              |
| 2    | Configuration error (invalid config file, missing required settings) |
| 3    | Schema parse error                                                   |
| 4    | Database connection error                                            |

## Common Workflows

### Development Setup

**With `melange init` (recommended):**

```bash
melange init
```

This creates a config file and starter schema. Then run commands without flags:

```bash
# Validate schema
melange validate

# Apply to database
melange migrate

# Generate Go code
melange generate client
```

**Without configuration file:**

```bash
# 1. Validate schema syntax
melange validate --schema schemas/schema.fga

# 2. Apply to local database
melange migrate \
  --db postgres://localhost/myapp_dev \
  --schema schemas/schema.fga

# 3. Generate Go code
melange generate client \
  --runtime go \
  --schema schemas/schema.fga \
  --output internal/authz \
  --package authz
```

### CI/CD Pipeline

Use environment variables for credentials:

```bash
# Set database URL from CI secrets
export MELANGE_DATABASE_URL="$DATABASE_URL"

# Validate schema (fails fast if syntax error)
melange validate

# Preview migration (optional, for review)
melange migrate --dry-run

# Apply migrations
melange migrate

# Run health checks
melange doctor
```

For pipelines where you want to ensure migrations are always applied (e.g., after a Melange version update):

```bash
melange migrate --force
```

### Schema Updates

When you modify your `.fga` schema:

```bash
# 1. Validate changes
melange validate

# 2. Regenerate client code
melange generate client

# 3. Apply to database
melange migrate
```

Or as a single workflow with explicit flags:

```bash
melange validate --schema schemas/schema.fga && \
  melange generate client --runtime go --output internal/authz && \
  melange migrate --db "$DATABASE_URL"
```

### Troubleshooting

When permission checks aren't working as expected, use `doctor` to diagnose issues:

```bash
# Run comprehensive health checks
melange doctor

# With verbose output for more details
melange doctor --verbose

# Check effective configuration
melange config show --source
```

Common scenarios where `doctor` helps:

1. **Permission checks returning unexpected results** - Doctor validates that your schema, generated functions, and tuples are all in sync.

2. **After updating Melange** - Doctor detects if the codegen version has changed and functions need regenerating.

3. **New environment setup** - Doctor validates the complete authorization stack is properly configured.

4. **Data migration issues** - Doctor samples tuples and validates they reference valid types and relations.

5. **Slow permission checks** - Doctor detects missing expression indexes and inefficient view patterns that cause performance degradation at scale.

---

## Programmatic Alternative

For programmatic schema management without the CLI, use the Go API:

```go
import (
    "github.com/pthm/melange/pkg/parser"
    "github.com/pthm/melange/pkg/migrator"
)

// Parse schema
types, err := parser.ParseSchema("schemas/schema.fga")
if err != nil {
    log.Fatal(err)
}

// Create migrator and apply
m := migrator.NewMigrator(db, "schemas/schema.fga")
err = m.MigrateWithTypes(ctx, types)
```

**With options (dry-run, force, skip-if-unchanged):**

```go
import (
    "os"
    "github.com/pthm/melange/pkg/migrator"
)

// Dry-run: output SQL to stdout
opts := migrator.MigrateOptions{
    DryRun: os.Stdout,
}
skipped, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", opts)

// Force migration even if unchanged
opts := migrator.MigrateOptions{
    Force: true,
}
skipped, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", opts)

// Normal migration with skip detection
opts := migrator.MigrateOptions{}
skipped, err := migrator.MigrateWithOptions(ctx, db, "schemas/schema.fga", opts)
if skipped {
    log.Println("Schema unchanged, migration skipped")
}
```

**Running health checks programmatically:**

```go
import (
    "os"
    "github.com/pthm/melange/lib/doctor"
)

d := doctor.New(db, "schemas/schema.fga", doctor.Options{})
report, err := d.Run(ctx)
if err != nil {
    log.Fatal(err)
}

// Print report (verbose=true for detailed output)
report.Print(os.Stdout, true)

// Check for critical failures
if report.HasErrors() {
    os.Exit(1)
}

// Access individual check results
for _, check := range report.Checks {
    if check.Status == doctor.StatusFail {
        log.Printf("FAIL: %s - %s", check.Name, check.Message)
        if check.FixHint != "" {
            log.Printf("  Fix: %s", check.FixHint)
        }
    }
}
```

See [Checking Permissions](../guides/checking-permissions.md) for the full Go API reference.
