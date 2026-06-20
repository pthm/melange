---
title: Troubleshooting
weight: 9
---

Diagnose and fix common Melange issues.

## Using melange doctor

```bash
melange doctor --db "$DATABASE_URL"
```

Doctor runs six categories of checks against your database:

1. **Schema File**: exists, parses correctly, no cyclic dependencies.
2. **Migration State**: `melange_migrations` table exists, schema is in sync, codegen version matches.
3. **Generated Functions**: dispatcher and per-relation functions are present, no orphans from previous schemas.
4. **Tuples Source**: `melange_tuples` view exists with the correct columns.
5. **Data Health**: tuple count, types and relations validate against the schema.
6. **Performance**: UNION ALL usage, missing expression indexes on `::text` cast columns.

Use `--verbose` for detailed output (exact checksums, function lists, specific invalid tuples):

```bash
melange doctor --verbose
```

Skip performance checks if you only need structural validation:

```bash
melange doctor --skip-performance
```

## Common Errors

### "relation melange_tuples does not exist"

The `melange_tuples` view has not been created, or it was created in a different schema than the one your connection uses.

**Fix**: create the view. See [Creating Your Tuples View](../../getting-started/tuples-view/).

### "function check_permission does not exist"

The migration has not been run, or it was run against a different database or schema.

**Fix**: run `melange migrate --db "$DATABASE_URL"`.

### Permission checks always return false

Run `melange explain` to see which branches the engine tried:

{{< explaintree >}}
$ melange explain user:alice viewer document:1 --db postgres://localhost/mydb
✗ user:alice does NOT have viewer on document:1
└── union of 2 branches
    ├── ✗ no direct grant
    └── ✗ implied: implied via editor
        └── union of 1 branches
            └── ✗ no direct grant
{{< /explaintree >}}

Here neither a direct `viewer` grant nor an implied path via `editor` matched — add a tuple satisfying one of them. See [Explaining Decisions](../explaining-decisions/) for the full guide.

If Explain returns the "explain not yet supported" sentinel (the schema uses a pattern the renderer doesn't cover yet), inspect the tuples view directly:

```sql
-- Check what tuples exist for the subject
SELECT * FROM melange_tuples WHERE subject_type = 'user' AND subject_id = 'alice';

-- Check what tuples exist for the object
SELECT * FROM melange_tuples WHERE object_type = 'repository' AND object_id = '42';
```

Common causes:
- Relation names in the view don't match the schema (e.g., `'is_member'` vs `'member'`).
- ID types aren't cast to text (`user_id` vs `user_id::text`).
- The domain table has no data for the given subject/object.

### Permission checks always return true

A wildcard subject (`user:*`) is granting broader access than intended.

`melange explain` shows wildcard matches as a `NodeWildcard` sentinel:

{{< explaintree >}}
$ melange explain user:alice can_view document:1
✓ user:alice has can_view on document:1
└── wildcard: user:*
{{< /explaintree >}}

Find the wildcard tuples:

```sql
SELECT * FROM melange_tuples WHERE subject_id = '*';
```

Check they're scoped to the intended relation and object type.

### Slow permission checks

**Debug**:

```sql
EXPLAIN ANALYZE SELECT check_permission('user', '123', 'can_read', 'repository', '456');
```

Look for `Seq Scan` in the output. Common causes:

- Missing expression indexes on `::text` cast columns. Run `melange doctor` for specific `CREATE INDEX` recommendations.
- Large source tables without appropriate composite indexes.
- Complex schema patterns (deeply nested exclusions or parent chains).

See [Scaling](../scaling/) for optimization strategies.

### Schema validation errors

```bash
melange validate
```

Common causes:
- Syntax errors in the `.fga` file. The error message includes the line number.
- Cyclic dependencies in implied-by relationships (e.g., `define a: b` and `define b: a`).
- References to undefined types or relations.

### Migration "schema unchanged" when it shouldn't be

Melange tracks schema changes via SHA256 checksum. If you've updated Melange itself but not the schema, the new codegen may produce different SQL.

**Fix**: force re-migration:

```bash
melange migrate --force
```

### Schema out of sync after updating Melange

`melange doctor` detects when the codegen version has changed since the last migration.

**Fix**: run `melange migrate --force` to regenerate all SQL functions with the current Melange version.

## Debugging Techniques

### Explain a permission decision

```bash
melange explain <subject> <relation> <object> --db postgres://localhost/mydb
```

Output is a tree of every branch the engine tried with `✓`/`✗` markers per node. `--format=json` returns the raw `Trace` JSONB; `--max-nodes N` caps the trace size. See [Explaining Decisions](../explaining-decisions/).

### Preview Generated SQL

```bash
melange migrate --dry-run
```

Outputs the complete SQL that would be executed. Redirect to a file for inspection:

```bash
melange migrate --dry-run > migration.sql
```

### Inspect Tuple Data

```sql
-- All tuples for a specific object
SELECT * FROM melange_tuples
WHERE object_type = 'repository' AND object_id = '42'
ORDER BY relation, subject_type, subject_id;

-- Count tuples by type
SELECT object_type, relation, count(*)
FROM melange_tuples
GROUP BY object_type, relation
ORDER BY count DESC;
```

### Query Plan Analysis

```sql
EXPLAIN ANALYZE
SELECT * FROM melange_tuples
WHERE object_type = 'repository' AND object_id = '42'
  AND relation = 'can_read' AND subject_type = 'user';
```

Look for:
- `Seq Scan` with `Filter:` lines containing `::text` casts. This means expression indexes are needed.
- High `actual time` values on individual branches of the UNION ALL.

### Check Effective Configuration

```bash
melange config show --source
```

Shows which config file is in use and the effective values after merging defaults, config file, and environment variables.

## Getting Help

If you're stuck, open an issue at [github.com/pthm/melange/issues](https://github.com/pthm/melange/issues) with:

- Your `.fga` schema
- The `melange doctor --verbose` output
- The error message or unexpected behavior
- Your Melange version (`melange version`)

## Next Steps

- [CLI Reference](../../reference/cli/): full command documentation and exit codes
- [Errors](../../reference/errors/): error types and sentinel values
- [Scaling](../scaling/): performance optimization strategies
