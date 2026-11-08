---
title: Troubleshooting
description: "Diagnose and fix common Melange problems with melange doctor, error messages, and query plans."
weight: 14
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

The skip check compares the schema checksum, the codegen version and the SQL
API exposure, and then the checksums of the generated functions. An upgraded
Melange that generates different SQL is therefore applied by a plain `melange
migrate`. The check does not inspect the database, so a function changed or
dropped by hand since the last migration is not detected.

**Fix**: run `melange doctor` to see what differs. Its fix hint suggests
`--force` when a plain `melange migrate` would skip or only record, including
for a stored model that cannot be decoded, which the skip check does not read.
On a database a newer Melange migrated it names that version instead. If the
functions were changed outside Melange, re-apply them:

```bash
melange migrate --force
```

`--force` also drops functions the new SQL no longer generates with `CASCADE`,
together with any views or policies that call them. Run `melange migrate
--dry-run` first and check its drops; see
[Dependent objects](../migrations/#dependent-objects).

### "relation ... is not exposed" (SQLSTATE M2003)

The database was migrated with a restricted SQL API exposure (the `sql:`
section of `melange.yaml`), and the relation, or its function family, is not
part of it. The Go and TypeScript runtimes report it as a `ValidationError`
with code 2000; `melange explain`, `melange expand` and studio name the
relation and the setting to change.

**Fix**: add the relation to `sql.relations.include` (or remove it from
`exclude`, or turn its family back on) and run `melange migrate`. `melange
status` shows the recorded exposure. See [SQL API: Exposure](../../reference/sql-api/#exposure).

When the relation comes from a generated client constant, the client and the
database disagree on the exposure: `melange generate client --check --db`
reports every such relation (`relation_not_exposed`) and accessible function
name, with the fix. A migration run with `--include-relation` or
`--no-<family>` that `melange.yaml` does not have is the usual cause. See
[Checking a generated client](../../reference/cli/#checking-a-generated-client).

### "explain on demand failed" or "cannot compile them on demand"

The database was migrated with `sql.functions.explain` (or `expand`) off, so
`melange explain`, `melange expand` and studio create those functions in
`pg_temp` for each call, and could not:

- **on demand failed**: the connection cannot create them. It is a hot-standby
  replica or forces read-only transactions, or the role lacks the `TEMP`
  privilege. Connect to the primary, or `GRANT TEMPORARY ON DATABASE`.
- **no model recorded**: the last migration did not record the model to
  compile them from. Run `melange migrate`, which records it.
- **a name the schema language does not allow**: the recorded model has a
  type or relation name the DSL would reject, so `melange_migrations` was
  edited after the migration. Melange never compiles SQL from such a model.
  Run `melange migrate`, which records the model again.
- **generated by melange X and this is melange Y**: the installed functions
  come from another codegen version, which the compiled ones might not match.
  Run `melange migrate` with this melange, or use melange X.

Turning the family back on and running `melange migrate` installs the
functions instead. See
[Explain and Expand on Demand](../../reference/sql-api/#explain-and-expand-on-demand).

### Migration refused: functions that other database objects depend on

A view, policy or function calls a generated function the migration would
drop. The error lists each dependent.

**Fix**: change or drop the dependents and migrate again, or run `melange
migrate --force` to drop them with `CASCADE`. See
[Dependent objects](../migrations/#dependent-objects).

### "function accessible_... does not exist" for a long relation name

Function names longer than PostgreSQL's 63 bytes are truncated and end in a
hash of the type and relation, so a name built by hand from a long relation
(`accessible_pull_request_can_read_security_advisories_and_vulnerability_reports`)
does not exist. The installed one is
`accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c`.

**Fix**: look the name up instead of building it.
`melange functions show pull_request#can_read_security_advisories_and_vulnerability_reports`
prints every function of the relation under its exact name, with an example
call; `melange functions --kind accessible` lists every accessible function,
and studio's [SQL API page](../studio/#sql-api) has the same with a Copy
button. In application code, use the names the
[generated client](../../reference/generated-code/#accessible-function-names)
carries.

### "function accessible_... does not exist" with a name from the generated client

The client names an accessible function the database did not install: it was
generated under a wider `sql:` section than the database was migrated with,
or from an older schema.

**Fix**: run `melange generate client --check --db` (or `--env`). It compares
the files with what `melange.yaml` generates, then every accessible function
name with what the last migration installed, and says for each whether
`melange migrate` installs it or the client should be regenerated. `melange
doctor` reports the same under Generated Client.

### melange functions reports missing functions or drift

`melange functions --db` compares the database with the model and exposure
its last migration recorded:

- **missing** or **body_changed**: a function was dropped, or replaced by
  hand with another body. `melange migrate` skips while the schema, exposure
  and codegen version are unchanged, so run `melange migrate --force` to
  reinstall it (`--force` also drops orphans with `CASCADE`; check
  `--dry-run` first).
- **signature_differs**: a function of that name is installed with other
  arguments. `CREATE OR REPLACE` cannot rename an argument, and for other
  argument types it adds an overload, so drop the installed one first
  (`DROP FUNCTION name(<arguments>)`, the arguments as listed), then run
  `melange migrate --force`.
- Under codegen version skew the detail says otherwise: when an older
  melange migrated, run a plain `melange migrate` with this melange first,
  which applies its functions and refuses to drop dependents of the old
  ones; when a newer melange migrated, use that version, since `--force`
  would override migrate's refusal to downgrade.
- **codegen_differs**: this melange generates a different body than the
  version that migrated. The next `melange migrate` with this melange
  replaces it. A note says when the versions differ.
- **stale overloads**: an older signature of the function is still installed.
  Drop it (`DROP FUNCTION name(<arguments>)`, the arguments as listed).
- **orphans**: functions the last migration recorded that the model no longer
  generates. `melange migrate` drops them.

Unmanaged functions (named like generated ones, recorded by no migration,
such as your own `check_invoice`) are listed for information and never
dropped. `melange doctor` compares the local schema file instead, so it can
disagree with `functions` while a migration is pending.

### Schema out of sync after updating Melange

`melange doctor` detects when the codegen version has changed since the last migration.

**Fix**: run `melange migrate`. The codegen version is part of the skip check, so the migration regenerates the SQL functions with the current Melange version without `--force`. When the new release generates the same SQL, the migration only records the new version: functions dropped by hand stay missing, and doctor's fix hint says to use `--force`.

When the database was migrated by a newer Melange, doctor says so: this Melange's `migrate` refuses to downgrade it, so use that version or later.

## Debugging Techniques

### Explain a permission decision

```bash
melange explain <subject> <relation> <object> --db postgres://localhost/mydb
```

Output is a tree of every branch the engine tried with `✓`/`✗` markers per node. `--format=json` returns the raw `Trace` JSONB; `--max-nodes N` caps the trace size. See [Explaining Decisions](../explaining-decisions/).

### Inspect a database in studio

```bash
melange studio --db postgres://localhost/mydb
```

Opens a local, read-only web console with Explain, Expand, a check matrix of every relation on an object for one subject, `EXPLAIN ANALYZE` timings, the tuples behind each entity, and the doctor checks. See [Studio](../studio/).

### List the generated functions

```bash
melange functions                          # from the schema file and melange.yaml
melange functions --db postgres://localhost/mydb   # what the database has installed
melange functions show document#viewer     # signatures and example calls
```

See [`melange functions`](../../reference/cli/#functions).

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
