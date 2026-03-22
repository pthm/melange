---
title: Running Migrations
weight: 4
---

Melange compiles your `.fga` schema into SQL functions that must be installed in PostgreSQL before permission checks can run. This page explains the two ways to get those functions into your database and helps you choose between them.

## Two Migration Strategies

| | Built-in (`melange migrate`) | External (`melange generate migration`) |
|---|---|---|
| **How it works** | Connects to PostgreSQL and applies SQL directly | Produces versioned `.sql` files you apply yourself |
| **Best for** | Solo projects, rapid prototyping, simple deployments | Teams using migration frameworks (golang-migrate, Atlas, Flyway, etc.) |
| **Change tracking** | `melange_migrations` table in the database | Your migration framework's own tracking |
| **Requires database access** | Yes — at migration time | Only when using `--db` comparison mode |
| **Integrates with CI review** | Via `--dry-run` | Natively — SQL files are committed and reviewed like any other migration |

{{< callout type="warning" >}}
Pick one strategy per database. Mixing both is discouraged — `melange migrate` will warn if it detects that `generate migration` has been configured, and vice versa.
{{< /callout >}}

## Built-in Migrations

The simplest path. Point `melange migrate` at your database and schema:

```bash
melange migrate \
  --db postgres://localhost/mydb \
  --schema schemas/schema.fga
```

Melange connects, generates the SQL functions, and applies them in a single transaction. It records each migration in a `melange_migrations` table so it can skip unchanged schemas and clean up orphaned functions automatically.

### When to use

- You control the database directly (local dev, small teams)
- You don't need SQL migrations checked into version control
- You want the simplest possible workflow

### Key behaviors

- **Skip-if-unchanged**: Melange checksums the schema and skips the migration when nothing has changed. Use `--force` to override.
- **Orphan cleanup**: Functions from removed relations are dropped automatically.
- **Dry-run**: Use `--dry-run` to preview the SQL without applying it.

See the [CLI Reference](../../reference/cli/#migrate) for the full flag list.

## External Migration Frameworks

If your team already uses a migration framework, `melange generate migration` produces versioned SQL files that slot into your existing workflow:

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations
```

This writes timestamped migration files (e.g., `20260322143000_melange.up.sql` / `.down.sql`) containing the same SQL that `melange migrate` would apply, but as static files you commit, review, and apply through your framework.

### When to use

- Your team uses golang-migrate, Atlas, Flyway, Liquibase, or a similar tool
- You want migration SQL reviewed in pull requests before it reaches production
- You need to coordinate Melange schema changes with other database migrations
- You don't want Melange to have direct database access in CI

### Output formats

| Format | Flag | Files produced |
|--------|------|---------------|
| **Split** (default) | `--format split` | `TIMESTAMP_NAME.up.sql` and `TIMESTAMP_NAME.down.sql` |
| **Single** | `--format single` | `TIMESTAMP_NAME.sql` with both UP and DOWN sections |

You can also write to stdout with `--up` or `--down` for scripting.

## Comparison Modes

By default, `generate migration` emits **every** function — a full migration. This is correct for the first migration or when you want a complete snapshot. For subsequent migrations, you usually want only the functions that changed. Melange supports three comparison modes to achieve this:

### Full mode (default)

```bash
melange generate migration --schema schemas/schema.fga --output db/migrations
```

No comparison flag is specified. The UP migration includes all functions. Use this for:
- Your first migration
- Periodic full snapshots
- When you're unsure what changed

### Database comparison (`--db`)

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --db postgres://localhost/mydb
```

Reads the most recent `melange_migrations` record from the database to determine which functions existed before and what their checksums were. The UP migration includes only:
- Functions whose SQL body has changed
- Newly added functions
- `DROP` statements for orphaned functions (relations removed from the schema)
- Dispatchers (always included since they reference all relations)

This is the most precise mode but requires database access at generation time.

### Git comparison (`--git-ref`)

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --git-ref main
```

Compiles the schema from a git ref (branch, tag, or commit) through the full pipeline and compares the result against the current schema. No database access required — ideal for CI pipelines.

Common patterns:
- `--git-ref main` — compare against the main branch
- `--git-ref HEAD~1` — compare against the previous commit
- `--git-ref v1.2.0` — compare against a release tag

### File comparison (`--previous-schema`)

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --previous-schema schemas/schema.fga.bak
```

Compiles a previous schema from a local file and compares. Useful when you keep a copy of the last-deployed schema alongside your current one.

{{< callout type="info" >}}
The three comparison flags (`--db`, `--git-ref`, `--previous-schema`) are mutually exclusive. Specify at most one.
{{< /callout >}}

## How Change Detection Works

When a comparison mode is active, Melange computes a SHA-256 checksum of each function's SQL body — both for the current schema and the previous state. Only functions with a different (or missing) checksum appear in the UP migration. Dispatcher functions (`check_permission`, `list_accessible_objects`, etc.) are always included because they reference every relation and must be regenerated whenever any relation changes.

Functions that existed in the previous state but are absent from the current schema are treated as **orphans** and receive `DROP FUNCTION IF EXISTS` statements in the UP migration.

## Migration Metadata

Each generated UP migration includes a comment header with metadata:

```sql
-- Melange Migration (UP)
-- Melange version: v0.7.3
-- Schema checksum: a1b2c3d4...
-- Codegen version: 1
-- Previous state: git:main
-- Changed functions: 3 of 12
```

This helps you understand at a glance what changed and why the migration was generated.

## Typical Workflows

### First-time setup with golang-migrate

```bash
# Generate the initial full migration
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --name melange

# Apply with golang-migrate
migrate -path db/migrations -database "$DATABASE_URL" up
```

### Schema change in a PR

```bash
# Edit schemas/schema.fga, then generate a diff migration
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --git-ref main

# Commit the .fga change and the generated SQL together
git add schemas/schema.fga db/migrations/
git commit -m "Add editor relation to document type"
```

### CI pipeline

```bash
# Validate schema syntax
melange validate --schema schemas/schema.fga

# Verify no un-committed migration drift
# (generate to stdout and compare against committed files)
melange generate migration \
  --schema schemas/schema.fga \
  --git-ref main \
  --up
```

## Next Steps

- [CLI Reference — generate migration](../../reference/cli/#generate-migration) — full flag reference and examples
- [Configuration — generate.migration](../../reference/configuration/#generate-migration-settings) — config file and environment variables
- [How It Works](../how-it-works/) — understand what the generated SQL functions do
