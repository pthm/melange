---
title: Running Migrations
weight: 10
---

Melange compiles your `.fga` schema into SQL functions that must be installed in PostgreSQL before permission checks can run. This page explains the two ways to get those functions into your database and helps you choose between them.

## Two Migration Strategies

| | Built-in (`melange migrate`) | External (`melange generate migration`) |
|---|---|---|
| **How it works** | Connects to PostgreSQL and applies SQL directly | Produces versioned `.sql` files you apply yourself |
| **Best for** | Solo projects, rapid prototyping, simple deployments | Teams using migration frameworks (golang-migrate, Atlas, Flyway, etc.) |
| **Change tracking** | `melange_migrations` table in the database | Your migration framework's own tracking |
| **Requires database access** | Yes, at migration time | Only when using `--db` comparison mode |
| **Integrates with CI review** | Via `--dry-run` | Natively: SQL files are committed and reviewed like any other migration |

{{< callout type="warning" >}}
Pick one strategy per database. Mixing both is discouraged. `melange migrate` will warn if it detects that `generate migration` has been configured, and vice versa.
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

See the [CLI Reference](../reference/cli/#migrate) for the full flag list.

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

By default, `generate migration` emits **every** function as a full migration. This is correct for the first migration or when you want a complete snapshot. For subsequent migrations, you usually want only the functions that changed. Melange supports three comparison modes to achieve this:

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

Compiles the schema from a git ref (branch, tag, or commit) through the full pipeline and compares the result against the current schema. No database access required, making it suitable for CI pipelines.

Common patterns:
- `--git-ref main` - compare against the main branch
- `--git-ref HEAD~1` - compare against the previous commit
- `--git-ref v1.2.0` - compare against a release tag

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

When a comparison mode is active, Melange computes a SHA-256 checksum of each function's SQL body for both the current schema and the previous state. Only functions with a different (or missing) checksum appear in the UP migration. Dispatcher functions (`check_permission`, `list_accessible_objects`, etc.) are always included because they reference every relation and must be regenerated whenever any relation changes.

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

## Example Output

To make the generated migrations concrete, here's what the UP and DOWN files look like for a simple schema:

```fga
model
  schema 1.1
type user

type document
  relations
    define viewer: [user]
```

### UP migration

The UP migration creates specialized check functions for each relation, then installs dispatchers that route `check_permission(...)` calls to the right function.

```sql
-- Melange Migration (UP)
-- Melange version: v0.7.3
-- Schema checksum: 9f3a...
-- Codegen version: 1

-- ============================================================
-- Check Functions (1 functions)
-- ============================================================

CREATE OR REPLACE FUNCTION check_document_viewer(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_object_id TEXT,
    p_visited TEXT [] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
BEGIN
    -- ... userset subject handling omitted for brevity ...

    IF EXISTS (
        SELECT 1
        FROM melange_tuples
        WHERE object_type = 'document'
          AND relation IN ('viewer')
          AND object_id = p_object_id
          AND subject_type = p_subject_type
          AND subject_id = p_subject_id
          AND NOT (subject_id = '*')
        LIMIT 1
    ) THEN
        RETURN 1;
    ELSE
        RETURN 0;
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;

-- ============================================================
-- No-Wildcard Check Functions (1 functions)
-- ============================================================

-- Same as above but excludes wildcard (type:*) matches
CREATE OR REPLACE FUNCTION check_document_viewer_no_wildcard(
    -- ... same signature and body, omitted for brevity ...
) RETURNS INTEGER AS $$ /* ... */ $$ LANGUAGE plpgsql STABLE;

-- ============================================================
-- Check Dispatchers
-- ============================================================

CREATE OR REPLACE FUNCTION check_permission_internal(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT [] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
BEGIN
    IF array_length(p_visited, 1) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;
    IF (p_object_type = 'document' AND p_relation = 'viewer') THEN
        RETURN check_document_viewer(p_subject_type, p_subject_id, p_object_id, p_visited);
    END IF;
    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;

-- Public entry point - this is what your application calls
CREATE OR REPLACE FUNCTION check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT check_permission_internal(
        p_subject_type, p_subject_id, p_relation,
        p_object_type, p_object_id, ARRAY[]::TEXT[]);
$$ LANGUAGE sql STABLE;

-- ... check_permission_no_wildcard, check_permission_bulk,
--     list_accessible_objects, list_accessible_subjects omitted ...
```

{{< callout type="info" >}}
Each relation in your schema produces a specialized function (e.g., `check_document_viewer`). The dispatchers (`check_permission`, `list_accessible_objects`, etc.) are always regenerated because they contain a `CASE` branch for every relation.
{{< /callout >}}

### DOWN migration

The DOWN migration drops all functions installed by the UP migration. To restore a previous version, apply that version's UP migration after rolling back.

```sql
-- Melange Migration (DOWN)
-- To restore a previous version, apply that version's UP migration.

-- Drop specialized functions
DROP FUNCTION IF EXISTS check_document_viewer CASCADE;
DROP FUNCTION IF EXISTS check_document_viewer_no_wildcard CASCADE;
DROP FUNCTION IF EXISTS list_document_viewer_objects CASCADE;
DROP FUNCTION IF EXISTS list_document_viewer_subjects CASCADE;

-- Drop dispatchers
DROP FUNCTION IF EXISTS check_permission CASCADE;
DROP FUNCTION IF EXISTS check_permission_internal CASCADE;
DROP FUNCTION IF EXISTS check_permission_no_wildcard CASCADE;
DROP FUNCTION IF EXISTS check_permission_no_wildcard_internal CASCADE;
DROP FUNCTION IF EXISTS check_permission_bulk CASCADE;
DROP FUNCTION IF EXISTS list_accessible_objects CASCADE;
DROP FUNCTION IF EXISTS list_accessible_subjects CASCADE;
```

### Comparison mode example

When using `--git-ref` or `--db`, only changed functions appear. For example, if you add an `editor` relation to `document`, the UP migration includes only the new functions and any modified dispatchers:

```sql
-- Melange Migration (UP)
-- Melange version: v0.7.3
-- Schema checksum: b7e4...
-- Codegen version: 1
-- Previous state: git:main
-- Changed functions: 2 of 4

-- ============================================================
-- Changed Functions (2 functions)
-- ============================================================

CREATE OR REPLACE FUNCTION check_document_editor(
    -- ... new function for the added relation ...
) RETURNS INTEGER AS $$ /* ... */ $$ LANGUAGE plpgsql STABLE;

CREATE OR REPLACE FUNCTION check_document_editor_no_wildcard(
    -- ... no-wildcard variant ...
) RETURNS INTEGER AS $$ /* ... */ $$ LANGUAGE plpgsql STABLE;

-- ============================================================
-- Check Dispatchers
-- ============================================================

-- Dispatchers are always regenerated to include the new routing branch
CREATE OR REPLACE FUNCTION check_permission_internal(
    /* ... */
) RETURNS INTEGER AS $$
BEGIN
    /* ... */
    IF (p_object_type = 'document' AND p_relation = 'editor') THEN
        RETURN check_document_editor(p_subject_type, p_subject_id, p_object_id, p_visited);
    END IF;
    IF (p_object_type = 'document' AND p_relation = 'viewer') THEN
        RETURN check_document_viewer(p_subject_type, p_subject_id, p_object_id, p_visited);
    END IF;
    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;

-- ... remaining dispatchers omitted ...
```

If you **remove** a relation, the comparison mode also emits `DROP` statements for the orphaned functions:

```sql
-- ============================================================
-- Drop removed functions
-- ============================================================

DROP FUNCTION IF EXISTS check_document_editor CASCADE;
DROP FUNCTION IF EXISTS check_document_editor_no_wildcard CASCADE;
```

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

## CI/CD Integration

### Validate Schema in CI

Add schema validation as an early CI step. It runs without database access and fails fast on syntax errors:

```bash
melange validate
```

### Generate and Review Migrations in PRs

Compare against the main branch to generate only the delta:

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --git-ref main
```

Commit the generated SQL alongside the schema change. Reviewers see both the intent (`.fga` diff) and the effect (`.sql` diff).

### GitHub Actions Example

```yaml
name: Melange
on: [push, pull_request]
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install Melange
        run: go install github.com/pthm/melange/cmd/melange@latest
      - name: Validate schema
        run: melange validate
      - name: Check migration drift
        run: |
          melange generate migration \
            --schema schemas/schema.fga \
            --git-ref origin/main \
            --up > /tmp/expected.sql
          # Compare against committed migration or just validate it generates cleanly

  deploy:
    runs-on: ubuntu-latest
    needs: validate
    if: github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v4
      - name: Apply migrations
        env:
          MELANGE_DATABASE_URL: ${{ secrets.DATABASE_URL }}
        run: melange migrate
```

### GitLab CI Example

```yaml
stages:
  - validate
  - deploy

validate-schema:
  stage: validate
  script:
    - melange validate

deploy-migrations:
  stage: deploy
  only:
    - main
  script:
    - melange migrate
  variables:
    MELANGE_DATABASE_URL: $DATABASE_URL
```

### Dry-Run in Staging

Preview the migration without applying:

```bash
melange migrate --dry-run --db "$STAGING_DATABASE_URL"
```

## Schema Evolution

### Safe Changes

These changes can be applied without coordination:

- **Adding a new type**. No existing functions are affected.
- **Adding a new relation**. New functions are generated; existing functions and dispatchers are updated.
- **Adding a union member** (e.g., `define viewer: [user]` to `define viewer: [user] or editor`). The check function is regenerated to include the new path.

### Changes That Require Planning

- **Removing a relation**. The specialized functions are dropped. Application code referencing the relation will get `ErrMissingFunction` until updated. Deploy the application change and migration together.
- **Removing a type**. All functions for that type are dropped. Same coordination as removing a relation.
- **Renaming a type or relation**. Melange treats this as a removal + addition. The old functions are dropped and new ones created. Update your `melange_tuples` view and application code in the same deployment.
- **Changing relation semantics** (e.g., from direct assignment to implied). The function SQL changes. Test thoroughly since permission results may differ.

### Recommended Workflow

1. Make the schema change in your `.fga` file.
2. Run `melange validate` to catch syntax errors.
3. Generate a migration with `--git-ref main` to see only the delta.
4. Review the generated SQL.
5. Update application code and tuples view if needed.
6. Deploy the migration, application code, and view changes together.

## Next Steps

- [CLI Reference: generate migration](../../reference/cli/#generate-migration): full flag reference and examples
- [Configuration: generate.migration](../../reference/configuration/#generate-migration-settings): config file and environment variables
- [How It Works](../../concepts/how-it-works/): what the generated SQL functions do
- [Troubleshooting](../troubleshooting/): diagnosing migration issues
