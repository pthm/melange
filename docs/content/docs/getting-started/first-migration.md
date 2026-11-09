---
title: Your First Migration
description: "Validate your schema, apply your first Melange migration, and check the result with melange doctor."
weight: 5
prev: /docs/getting-started/tuples-view
next: /docs/concepts
---

With your schema and tuples view in place, the next step is compiling the authorization model into PostgreSQL functions.

## Validate Your Schema

Check that your schema is syntactically valid before migrating:

```bash
melange validate
```

This parses the `.fga` file and reports syntax errors or cyclic dependencies. It does not require database access.

## Apply the Migration

```bash
melange migrate
```

This connects to your database (using the URL from your config file or `--db` flag), compiles your schema, and installs the generated SQL functions.

You'll see output like:

```
Applying authz infrastructure...
Authz schema applied successfully.
```

Running it again with an unchanged schema prints `Schema unchanged, migration skipped.` and `Use --force to re-apply.`

### Preview Without Applying

To see the generated SQL without making any changes:

```bash
melange migrate --dry-run
```

This outputs the full SQL that would be executed, which is useful for review or debugging.

## Verify the Setup

### Check Migration Status

```bash
melange status
```

Reports whether a migration has been applied and if the schema has changed since the last migration.

### Run Health Checks

```bash
melange doctor
```

The doctor command runs these checks against your database:

1. **Schema File**: exists, parses correctly, no cyclic dependencies
2. **Migration State**: `melange_migrations` table exists, schema is in sync
3. **Deployed Model**: the last migration recorded its model, local changes against it are additive or breaking, and published security advisories cover the melange version that generated the functions
4. **Generated Functions**: the dispatchers and per-relation functions the recorded exposure installs are present, no orphans, and `sql.relations` patterns that match no relation
5. **Generated Client** (when `generate.client.runtime` is set): the generated files match what `melange.yaml` generates, and every name the client uses is installed and exposed (a warning, or a failure with `--strict-client`; `--skip-client` skips it)
6. **Tuples Source**: `melange_tuples` view exists with the correct columns
7. **Data Health**: tuple count, type/relation validation against schema
8. **Performance**: view analysis (UNION ALL usage, missing expression, source-table and subject-lookup indexes)

For detailed output:

```bash
melange doctor --verbose
```

## Test a Permission Check

Run a permission check to confirm the setup:

```sql
SELECT check_permission('user', 'alice', 'can_read', 'repository', '42');
-- Returns 1 (allowed) or 0 (denied)
```

If this returns `0` and you expect `1`, check that:
- The `melange_tuples` view returns the expected rows (`SELECT * FROM melange_tuples WHERE subject_id = 'alice'`)
- The relation names in your view match your schema exactly

## Generate Client Code (Optional)

If you configured client code generation during `melange init`, generate the type-safe helpers:

```bash
melange generate client
```

{{< tabs >}}

{{< tab name="Go" >}}
This creates files in your output directory (e.g., `internal/authz/`) with constants and constructors:

```go
import "myapp/internal/authz"

allowed, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("42"),
)
```

Regenerate after any schema change to keep the generated code in sync.
{{< /tab >}}

{{< tab name="TypeScript" >}}
This creates TypeScript types and factory functions in your output directory (e.g., `src/authz/`):

```typescript
import { User, Repository, RelCanRead } from './authz';

const user = User('alice');
const repo = Repository('42');
```
{{< /tab >}}

{{< /tabs >}}

## Using an External Migration Framework

If you chose the **versioned** migration strategy during init (or prefer to use golang-migrate, Atlas, Flyway, etc.), generate SQL files instead:

```bash
melange generate migration
```

This produces timestamped migration files in your output directory:

```
migrations/
├── 20240315120000_melange.up.sql
└── 20240315120000_melange.down.sql
```

Apply them with your framework. See [Running Migrations](../../guides/migrations/) for comparison modes and CI workflows.

## Next Steps

- [Checking Permissions](../../guides/checking-permissions/): Checker API, caching, bulk checks, decision overrides, error handling
- [Listing Objects](../../guides/listing-objects/): find all objects a user can access
- [Listing Subjects](../../guides/listing-subjects/): find all users with access to an object
- [How It Works](../../concepts/how-it-works/): the compiler model and generated SQL
- [SQL API Reference](../../reference/sql-api/): calling permission functions directly from any language
- [CLI Reference](../../reference/cli/): full command and flag documentation
