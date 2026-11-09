---
title: Running Migrations
description: "Two ways to install Melange's generated SQL: melange migrate directly, or SQL files for your existing migration tool."
weight: 2
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

- **Skip-if-unchanged**: Melange checksums the schema and skips the migration when neither it nor the [SQL API exposure](#sql-api-exposure) has changed. A Melange upgrade that changes the generated SQL is applied too. `--force` re-applies regardless and also drops the dependents of removed functions; see [Dependent objects](#dependent-objects). The skip check does not inspect the installed functions; `melange doctor`'s fix hints say when a plain migrate would skip, and `migrator.PlanWithOptions` (or `Migrator.Plan`) predicts a run from Go without changing the database.
- **Orphan cleanup**: Functions from removed relations, or outside a narrower exposure, are dropped automatically. A drop that views or policies depend on is refused unless forced; see [Dependent objects](#dependent-objects).
- **Dry-run**: Use `--dry-run` to preview the SQL without applying it.

See the [CLI Reference](../../reference/cli/#migrate) for the full flag list.

### SQL API exposure

The [`sql:` section](../../reference/configuration/#sql-api-settings) of
`melange.yaml` selects which relations the public SQL functions answer for and
which function families are generated. `melange migrate` applies it and
records it in the `exposure` column of `melange_migrations` (a JSON object;
`{}` is the default exposure).

- **Exposure-only changes re-apply.** The recorded exposure is part of the
  skip check, so editing the `sql:` section and migrating applies it even when
  the schema is unchanged. When the change produces identical SQL (excluding a
  relation the model does not define, say), nothing is re-applied but the new
  exposure is recorded.
- **The config is declarative.** Each `melange migrate` applies the exposure the
  config resolves to. Removing the section widens the database back to the
  default. Exposure flags such as `--include-relation` apply to one run and
  are not remembered, so the next migrate without them applies the config
  again.
- **The generated client follows the config, not the flags.** `melange
  generate client` names the accessible functions and relations of the `sql:`
  section. A migrate with exposure flags that narrow it installs less than the
  client names; when `generate.client.runtime` is set, migrate warns.
  `melange generate client --check --db` and doctor's Generated Client checks
  report each name the database does not answer for.
- **Status and doctor follow it.** `melange status` reports `exposure_drift`
  and `melange doctor` warns (`exposure_sync`) when the local exposure differs
  from the recorded one. Doctor expects the functions of the recorded
  exposure, so a restricted database does not report missing functions.
- **`--if-deployed-checksum` compares only the schema checksum.** A database at
  the expected checksum but with a different recorded exposure passes the
  guard, and the migration applies the local exposure.
- **Changing the exposure needs a recorded migration.** `Migrator.MigrateWithTypes`,
  and `Migrator.MigrateWithTypesAndOptions` without `SchemaContent`, record
  nothing, so they could neither drop the previous exposure's functions nor
  record the new exposure. They generate under the recorded exposure, or the
  default when none is recorded, and return
  `*migrator.UnrecordedExposureChangeError` for any other, applying nothing.
  A database deployed from restricted `melange generate migration` files has
  no record, so these calls refuse to restate that restriction too.

A Go program that migrates through `pkg/migrator` without setting
`MigrateOptions.Exposure` keeps the recorded exposure instead, so an
application that migrates on startup does not undo a restriction applied by
`melange migrate`. To change the exposure from Go, use
`migrator.MigrateWithOptions` with `MigrateOptions.Exposure`, or
`Migrator.MigrateWithTypesAndOptions` with `SchemaContent` (for an embedded
schema, `SchemaFormat: migrator.FormatSingle`) and `Exposure`.

### Dependent objects

Views, materialized views, row-level security policies, rules and
`BEGIN ATOMIC` SQL functions that call a generated function depend on it.
When a migration would drop such a function (a removed relation or a narrower
exposure), `melange migrate` refuses before applying anything and lists the
dependents:

```
Error: migration refused (rerun with --force to drop the dependents): migration would drop functions that other database objects depend on; nothing was applied (remove or change the dependents, or force the migration to drop them with CASCADE):
  policy docs_read on table docs depends on accessible_document_viewer
```

Change the dependents to use a function the new schema still generates (an
exposed relation, or a public dispatcher), or drop them, then migrate again.
`--force` drops the functions with `CASCADE`, which drops the dependents with
them. Dropping a policy widens access to its table, so read the list before
forcing.

Without `--force` the drop has no `CASCADE`, so a dependent created by another
session after the check makes PostgreSQL refuse the drop (SQLSTATE `2BP01`).
The migration is then rolled back and not recorded, and the error lists the
dependent.

### Applying a dry-run script

`melange migrate --dry-run` prints the SQL the migration would run, orphan
drops included. Unless `--force` is given, the script starts with a guard block
that re-checks for dependents when the script runs and fails if there are any,
and it drops orphans without `CASCADE`. The script has no `BEGIN`/`COMMIT`, so
it can be wrapped by a migration tool. To apply it by hand, run it in one
transaction that stops at the first error, so a failed guard or drop leaves
the database unchanged:

```bash
melange migrate --dry-run > migration.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction -f migration.sql
```

The migration record is inserted last, once every orphan is gone.

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

Reads the most recent `melange_migrations` record from the database to determine which functions existed before, what their checksums were, and which SQL API exposure they were generated with. The UP migration includes only:
- Functions whose SQL body has changed
- Newly added functions
- `DROP` statements for orphaned functions (relations removed from the schema, or outside a narrower exposure)
- Dispatchers (always included since they reference all relations)

The SQL is generated under the local exposure (the `sql:` section and any
exposure flags), so a database migrated with a restricted exposure is widened
when the config no longer restricts it, as `melange migrate` would.

This is the most precise mode but requires database access at generation time.

### Git comparison (`--git-ref`)

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --git-ref main
```

Compiles the schema from a git ref (branch, tag, or commit) through the full pipeline and compares the result against the current schema. No database access required, making it suitable for CI pipelines.

The previous exposure is read from the config file discovery finds at the
same ref (or the `--config` file, read at that ref): its `sql:` section, with
the current `MELANGE_SQL_*` variables applied. Deleting a restricted
`melange.yaml` is detected as a widening. Commit the config change together
with the schema change so the two stay in step. When the config file cannot
be read at the ref, or discovery finds a different file there from the one in
use now, see [Unknown previous exposure](#unknown-previous-exposure).

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

A file holds no record of the exposure, so this mode always emits every
function; see [Unknown previous exposure](#unknown-previous-exposure).

{{< callout type="info" >}}
The three comparison flags (`--db`, `--git-ref`, `--previous-schema`) are mutually exclusive. Specify at most one.
{{< /callout >}}

## How Change Detection Works

When a comparison mode is active, Melange computes a SHA-256 checksum of each function's SQL body for both the current schema and the previous state. Only functions with a different (or missing) checksum appear in the UP migration. Dispatcher functions (`check_permission`, `list_accessible_objects`, etc.) are always included because they reference every relation and must be regenerated whenever any relation changes.

Functions that existed in the previous state but are absent from the current schema are treated as **orphans** and receive `DROP FUNCTION IF EXISTS ...` statements in the UP migration. The drops have no `CASCADE`, so applying the migration fails (SQLSTATE `2BP01`) when a view, RLS policy or function calls one of them, as an unforced `melange migrate` refuses. Change or drop those [dependent objects](#dependent-objects) first. The DOWN migration still drops every function with `CASCADE`.

### Unknown previous exposure

Change detection compares each function with its previous body, which needs
the exposure the previous functions were generated with: a function the
previous exposure did not install has no previous body, and skipping it would
leave the database without it. When the previous exposure is unknown
(`--previous-schema`, or `--git-ref` when the config file is untracked or
invalid at that ref, or is not the one discovery finds there), the UP
migration includes every function, whatever the current exposure, and its
header says so:

```sql
-- Previous exposure unknown: every function is included
```

Orphans are still dropped: the previous schema is compiled under the default
exposure, which installs every function any other exposure does except the
`check_permission_unexposed` helper, and the helper is added to the drops
when the current exposure does not install it. The drops cover whatever the
previous deployment may have installed (`DROP FUNCTION IF EXISTS`).

## Migration Metadata

Each generated UP migration includes a comment header with metadata:

```sql
-- Melange Migration (UP)
-- Melange version: v0.7.3
-- Schema checksum: a1b2c3d4...
-- Codegen version: 1
-- Exposure: {"relations":{"include":["document#can_*"]},"functions":{"expand":false}}
-- Previous state: git:main
-- Changed functions: 3 of 12
```

The `Exposure` line appears only for a restricted exposure. This helps you understand at a glance what changed and why the migration was generated.

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
    IF array_length(p_visited, 1) >= 26 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;
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
$$ LANGUAGE plpgsql STABLE PARALLEL RESTRICTED COST 1000;  -- SET search_path and planner settings omitted

-- Explain, Expand, List Objects and List Subjects sections follow
-- (explain_document_viewer, expand_document_viewer, list_document_viewer_obj,
-- list_document_viewer_sub), omitted. A relation that can match a wildcard
-- also gets a check_<type>_<relation>_nw variant.

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
    IF array_length(p_visited, 1) >= 26 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;
    IF (p_object_type = 'document') THEN
        IF (p_relation = 'viewer') THEN
            RETURN check_document_viewer(p_subject_type, p_subject_id, p_object_id, p_visited);
        END IF;
    END IF;
    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE PARALLEL RESTRICTED COST 1000;

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

-- ... check_permission_nw, check_permission_bulk, explain_permission,
--     expand_permission, list_accessible_objects, list_accessible_subjects,
--     and the accessible functions (accessible_document_viewer, accessible_objects) omitted ...
```

{{< callout type="info" >}}
Each relation in your schema produces specialized functions (e.g., `check_document_viewer`). The dispatchers (`check_permission`, `list_accessible_objects`, etc.) are always regenerated because they route every relation.
{{< /callout >}}

### DOWN migration

The DOWN migration drops all functions installed by the UP migration. To restore a previous version, apply that version's UP migration after rolling back.

```sql
-- Melange Migration (DOWN)
-- To restore a previous version, apply that version's UP migration.

-- Drop specialized functions
DROP FUNCTION IF EXISTS accessible_document_viewer CASCADE;
DROP FUNCTION IF EXISTS check_document_viewer CASCADE;
DROP FUNCTION IF EXISTS expand_document_viewer CASCADE;
DROP FUNCTION IF EXISTS explain_document_viewer CASCADE;
DROP FUNCTION IF EXISTS list_document_viewer_obj CASCADE;
DROP FUNCTION IF EXISTS list_document_viewer_sub CASCADE;

-- Drop dispatchers
DROP FUNCTION IF EXISTS check_permission CASCADE;
DROP FUNCTION IF EXISTS check_permission_internal CASCADE;
DROP FUNCTION IF EXISTS check_permission_nw CASCADE;
DROP FUNCTION IF EXISTS check_permission_nw_internal CASCADE;
DROP FUNCTION IF EXISTS check_permission_bulk CASCADE;
DROP FUNCTION IF EXISTS explain_permission CASCADE;
DROP FUNCTION IF EXISTS explain_permission_internal CASCADE;
DROP FUNCTION IF EXISTS expand_permission CASCADE;
DROP FUNCTION IF EXISTS expand_permission_internal CASCADE;
DROP FUNCTION IF EXISTS accessible_objects CASCADE;
DROP FUNCTION IF EXISTS list_accessible_objects CASCADE;
DROP FUNCTION IF EXISTS list_accessible_subjects CASCADE;
DROP FUNCTION IF EXISTS list_accessible_subjects_internal CASCADE;
```

### Comparison mode example

When using `--git-ref` or `--db`, only changed functions appear. For example, if you add an `editor` relation to `document`, the UP migration includes only the new functions and any modified dispatchers:

```sql
-- Melange Migration (UP)
-- Melange version: v0.7.3
-- Schema checksum: b7e4...
-- Codegen version: 1
-- Previous state: git:main
-- Changed functions: 6 of 13

-- ============================================================
-- Changed Functions (6 functions)
-- ============================================================

CREATE OR REPLACE FUNCTION check_document_editor(
    -- ... new function for the added relation ...
) RETURNS INTEGER AS $$ /* ... */ $$ LANGUAGE plpgsql STABLE PARALLEL RESTRICTED COST 1000;

-- ... expand_document_editor, explain_document_editor, list_document_editor_obj,
--     list_document_editor_sub and accessible_document_editor omitted ...

-- ============================================================
-- Check Dispatchers
-- ============================================================

-- Dispatchers are always regenerated to include the new routing branch
CREATE OR REPLACE FUNCTION check_permission_internal(
    /* ... */
) RETURNS INTEGER AS $$
BEGIN
    /* ... */
    IF (p_object_type = 'document') THEN
        IF (p_relation = 'editor') THEN
            RETURN check_document_editor(p_subject_type, p_subject_id, p_object_id, p_visited);
        END IF;
        IF (p_relation = 'viewer') THEN
            RETURN check_document_viewer(p_subject_type, p_subject_id, p_object_id, p_visited);
        END IF;
    END IF;
    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE PARALLEL RESTRICTED COST 1000;

-- ... remaining dispatchers omitted ...
```

If you **remove** a relation, the comparison mode also emits `DROP` statements for the orphaned functions, right after the header and without `CASCADE`:

```sql
-- ============================================================
-- Drop removed functions
-- ============================================================

-- These fail if other database objects depend on them; change or drop those first.
DROP FUNCTION IF EXISTS accessible_document_editor;
DROP FUNCTION IF EXISTS check_document_editor;
DROP FUNCTION IF EXISTS expand_document_editor;
DROP FUNCTION IF EXISTS explain_document_editor;
DROP FUNCTION IF EXISTS list_document_editor_obj;
DROP FUNCTION IF EXISTS list_document_editor_sub;
```

The list holds the functions the previous exposure installed, sorted by name.

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
# Validate the schema
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
        run: go install github.com/pthm/melange@latest
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
2. Run `melange validate` to catch syntax errors and undefined relations.
3. Generate a migration with `--git-ref main` to see only the delta.
4. Review the generated SQL.
5. Update application code and tuples view if needed.
6. Deploy the migration, application code, and view changes together.

## Next Steps

- [CLI Reference: generate migration](../../reference/cli/#generate-migration): full flag reference and examples
- [Configuration: generate.migration](../../reference/configuration/#generate-migration-settings): config file and environment variables
- [How It Works](../../concepts/how-it-works/): what the generated SQL functions do
- [Troubleshooting](../troubleshooting/): diagnosing migration issues
