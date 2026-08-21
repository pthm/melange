---
title: CLI Reference
weight: 1
---

The Melange CLI provides commands for validating schemas, generating client code, and applying migrations to your database. Built on Cobra/Viper, it supports [configuration files](../configuration/), environment variables, and command-line flags with consistent precedence.

## Installation

```bash
go install github.com/pthm/melange@latest
```

## Global Flags

These flags are available on all commands:

| Flag                | Description                                                                                         |
| ------------------- | --------------------------------------------------------------------------------------------------- |
| `--config`          | Path to config file (default: auto-discover `melange.yaml`). See [Configuration](../configuration/). |
| `--env`             | Select a named environment profile. See [Configuration → Environments](../configuration/#environments). |
| `-v`, `--verbose`   | Increase verbosity (can be repeated: `-vv`, `-vvv`)                                                 |
| `-q`, `--quiet`     | Suppress non-error output                                                                           |
| `--no-update-check` | Disable automatic update checking                                                                   |

Any command that connects to a database accepts `--env <name>` to target one of
the [environment profiles](../configuration/#environments) defined in your
config (e.g. `melange status --env production`). An explicit `--db` still wins
over the environment's connection.

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
  brew upgrade melange  or  go install github.com/pthm/melange@latest
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

**Schema Commands:** `validate`, `migrate`, `status`, `schema pull`, `diff`, `history`, `doctor`, `explain`, `expand`
**Client Commands:** `generate client`, `generate migration`
**Utility Commands:** `init`, `config show`, `env list`, `version`, `license`

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

| Flag                     | Default              | Description                                                    |
| ------------------------ | -------------------- | ------------------------------------------------------------- |
| `--db`                   | (from config)        | PostgreSQL connection string                                  |
| `--db-schema`            | (config, else `public`) | PostgreSQL schema for melange objects                      |
| `--schema`               | `schemas/schema.fga` | Path to schema.fga file                                       |
| `--dry-run`              | `false`              | Output SQL to stdout without applying changes                 |
| `--force`                | `false`              | Force migration even if schema is unchanged                   |
| `--if-deployed-checksum` | (unset)              | Apply only if the deployed schema checksum matches (see below) |

This command:

1. Checks if the schema has changed since the last migration
2. Installs generated SQL functions (`check_permission`, `list_accessible_objects`, etc.)
3. Cleans up orphaned functions from removed relations
4. Records the migration in `melange_migrations` table — including the schema
   DSL and parsed model, so the database is self-describing. This is what
   `melange status` and `melange schema pull` read back.

**Skip-if-unchanged behavior:**

Melange tracks schema changes using a SHA256 checksum. If you run `migrate` and the schema hasn't changed since the last migration, it will be skipped automatically:

```
Schema unchanged, migration skipped.
Use --force to re-apply.
```

Use `--force` to re-apply the migration anyway (useful after updating Melange itself).

**Drift-safe apply (`--if-deployed-checksum`):**

`--if-deployed-checksum` turns migrate into a compare-and-swap: it applies only if
the database is currently at the schema checksum you pass, otherwise it aborts
having changed nothing and exits non-zero. This closes the window where someone
else migrates the database between the time you plan a change and the time your
deploy applies — which last-writer-wins would silently clobber.

The intended CI pattern reads the current checksum from `status`, then gates the
apply on it:

```bash
CURRENT=$(melange status --env production --format json | jq -r .deployed.schema_checksum)
# review / plan the change …
melange migrate --env production --if-deployed-checksum "$CURRENT"
```

On a mismatch:

```
Error: migration aborted: deployed model changed: expected checksum abc… but database has def…; nothing was applied
```

An empty value (`--if-deployed-checksum ""`) matches a database with no migration
recorded (a fresh database). The guard is enforced in `--dry-run` too, so a
drift-gated preview aborts rather than printing SQL against a drifted database.

{{< callout type="info" >}}
The guard is verified up front and again inside the apply transaction, so a
change committed while your migration runs aborts it. It is not a hard lock
against two migrations that verify at the exact same instant, but concurrent
migrations against one database are unsupported regardless.
{{< /callout >}}

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

See [Tuples View](../../concepts/tuples-view/) for setup instructions.

### status

Show whether the schema file and `melange_tuples` view are present, what model
the database has deployed, and whether the local schema file is in sync with it.

```bash
melange status \
  --db postgres://localhost/mydb \
  --schema schemas/schema.fga
```

**Flags:**

| Flag          | Default              | Description                                                     |
| ------------- | -------------------- | -------------------------------------------------------------- |
| `--db`        | (from config)        | PostgreSQL connection string                                   |
| `--db-schema` | (config, else `public`) | PostgreSQL schema for melange objects                       |
| `--schema`    | `schemas/schema.fga` | Path to schema.fga file                                        |
| `--format`    | `text`               | Output format: `text` or `json`                                |

**Output:**

```
Schema file:  present
Tuples view:  present
Deployed:     checksum d0c1746f7e26… · melange v0.9.0 · 2026-07-01T20:46:10Z
Sync:         in sync — local schema matches deployed
```

When the local schema has drifted, the change summary follows the Sync line:

```
Schema file:  present
Tuples view:  present
Deployed:     checksum d0c1746f7e26… · melange v0.9.0 · 2026-07-01T20:46:10Z
Sync:         drift — local schema differs from deployed (`melange diff` to see changes, `melange migrate` to apply)
              1 breaking, 2 additive
              + type audit_log added
              + relation document.can_export added
              - relation document.legacy_viewer removed
```

At most five changes are listed; run `melange diff` for the full list. The
summary is omitted when the deployed model was never recorded, when either side
fails to parse (a `Note:` explains why), or when the two models are semantically
equivalent — reformatting or a comment edit moves the checksum without changing
behavior, and that is reported as drift with no changes to show.

The **Deployed** line reports the model recorded by the most recent migration
(checksum, melange version, and time). The **Sync** state compares the local
schema file's checksum against the deployed one:

| Sync state       | Meaning                                                                   |
| ---------------- | ------------------------------------------------------------------------- |
| `in_sync`        | The local schema matches what's deployed                                     |
| `drift`          | The local schema differs — run `melange migrate` to apply                    |
| `database_ahead` | Drift where the deployed model is in no recent commit of your schema file    |
| `unknown`        | No local schema file to compare against                                      |
| `not_recorded`   | The database has no migration record                                         |

`database_ahead` is the warning case: the deployed model is not the local file
and is not any version of it in your schema's git history, so someone migrated
that database from a different checkout — applying your local schema would
overwrite their model rather than move the database forward. Check with
`melange schema pull` before migrating, and use
`melange migrate --if-deployed-checksum` to make the apply conditional.

The probe is advisory and errs toward plain `drift`. It follows the schema across
renames and reads each revision as a checkout would (so end-of-line conversion
doesn't cause spurious mismatches), and it stays silent whenever it cannot search
every version:

- outside a git repository, or without git installed
- in a shallow (`--depth`) or partial clone — CI checkouts are shallow by
  default, so this is the common case in automation
- when the schema is uncommitted or has local modifications: migrating from a
  dirty working tree is routine, so the deployed model may be a version git
  never saw
- for modular (`fga.mod`) schemas, whose checksum covers the manifest plus every
  module
- for a schema with more than 50 commits of history, past which the search is no
  longer exhaustive
- when the two models are semantically equivalent — a checksum that moved on
  formatting or comments alone is drift, never an allegation

Treat it as a prompt to look, not a verdict: it reports what git could see, and
git cannot see every place a model may have come from.

Databases migrated before v0.9 (or by `MigrateWithTypes`) have no recorded model
DSL; status notes this and `melange schema pull` cannot recover them. Reading the
migration record is non-fatal — if it fails, status still reports schema/tuples
presence and adds a `Note:` line.

**JSON output** (`--format json`) emits the machine-readable report, including a
`notes` array for any non-fatal warnings:

```json
{
  "schema_file": "present",
  "tuples_view": "present",
  "sync": "in_sync",
  "deployed": {
    "melange_version": "v0.9.0",
    "migrated_at": "2026-07-01T20:46:10Z",
    "schema_checksum": "d0c1746f7e26ea40027a24b1c0e0c5f34e279e7c27f6fa17e4611ce2f1ec0962",
    "schema_format": "single",
    "model_recorded": true
  }
}
```

When the sync state is `drift` or `database_ahead` and both models are
available, a `drift` object
carries the same detail as the text output — the counts plus every change, not
just the first five. Changes are sorted by type, then relation, so the order is
stable across runs:

```json
{
  "sync": "drift",
  "deployed": { "schema_checksum": "d0c1746f7e26…", "model_recorded": true },
  "drift": {
    "additive": 2,
    "breaking": 1,
    "changes": [
      { "class": "additive", "type": "audit_log", "summary": "type audit_log added" },
      { "class": "additive", "type": "document", "relation": "can_export", "summary": "relation document.can_export added" },
      { "class": "breaking", "type": "document", "relation": "legacy_viewer", "summary": "relation document.legacy_viewer removed" }
    ]
  }
}
```

### schema pull

Reconstruct the OpenFGA schema recorded by the most recent migration. Use it to
recover a schema whose source file was lost, or to see exactly what model a
database is running.

```bash
# Print the deployed schema
melange schema pull --db postgres://localhost/mydb

# Recover it to a file, targeting a named environment
melange schema pull --env production -o recovered.fga
```

**Flags:**

| Flag             | Default              | Description                                            |
| ---------------- | -------------------- | ------------------------------------------------------ |
| `--db`           | (from config)        | PostgreSQL connection string                           |
| `--db-schema`    | (config, else `public`) | PostgreSQL schema for melange objects               |
| `-o`, `--output` | (stdout)             | Write to this file instead of stdout                   |
| `--no-header`    | `false`              | Omit the provenance header comment                     |

**Output:**

```
# Pulled from a melange-migrated database by `melange schema pull`
# Deployed: 2026-07-01T20:46:10Z by melange v0.9.0
# Schema checksum: d0c1746f7e26ea40027a24b1c0e0c5f34e279e7c27f6fa17e4611ce2f1ec0962

model
  schema 1.1

type user
...
```

The provenance header is written as `#` comments (valid DSL), so the output still
parses; the database URL is never included. Pass `--no-header` for the bare
schema, which for a single-file schema is byte-identical to what you migrated.

{{< callout type="info" >}}
A **modular** (`fga.mod`) schema is emitted as the stored manifest + module
bundle for reference/recovery. That combined form does **not** re-parse as a
single `.fga`, and splitting it back into module files is not supported.
{{< /callout >}}

Requires a database migrated by melange v0.9 or later. Older databases did not
record the model; pull reports whether the database was never migrated or was
migrated before model storage existed.

### diff

Show what changed between a deployed (or previous) model and your local schema,
with each change classified **additive** (safe — widens access or adds structure)
or **breaking** (narrows access or removes structure).

```bash
# What would migrating apply to production?
melange diff --env production

# Compare against the schema on main, or a previous file
melange diff --git-ref main
melange diff --previous-schema old.fga
```

**Flags:**

| Flag                | Default              | Description                                                          |
| ------------------- | -------------------- | ------------------------------------------------------------------- |
| `--db`              | (from config)        | Database to compare against (the default source)                    |
| `--db-schema`       | (config, else `public`) | PostgreSQL schema for melange objects                            |
| `--schema`          | `schemas/schema.fga` | Local `.fga` — the new side of the diff                             |
| `--git-ref`         | (unset)              | Compare against your schema at a git commit/branch/tag              |
| `--previous-schema` | (unset)              | Compare against a previous `.fga` file (modular not supported)      |
| `--format`          | `tree`               | Output format: `tree` (default) or `json`                           |
| `--exit-code`       | `false`              | Exit 1 if any change is breaking (CI gate)                          |

The comparison source is the deployed model by default (`--db`/`--env`, or the
configured database); `--git-ref` and `--previous-schema` select a file/git
source instead and are mutually exclusive with a database source.

**Output:**

```
Comparing deployed → melange/schema.fga

  BREAKING  document.legacy removed
  additive  type audit_log added
  additive  document.viewer grants [org]

1 breaking, 2 additive
```

Classification reflects melange's compiled model and is deliberately conservative:
it accounts for implied-by closure, intersection subsumption, and exclusion
polarity, and it never under-reports a breaking change — so `--exit-code` is safe
as a CI gate. `--format=json` emits the structured `SchemaDiff` for tooling.

### history

List recent entries from the `melange_migrations` table — when each migration
ran, the melange version, the schema checksum, and how many functions it
installed. An audit trail of how a database's model has evolved.

```bash
melange history --db postgres://localhost/mydb
```

**Flags:**

| Flag          | Default              | Description                              |
| ------------- | -------------------- | ---------------------------------------- |
| `--db`        | (from config)        | PostgreSQL connection string             |
| `--db-schema` | (config, else `public`) | PostgreSQL schema for melange objects |
| `--format`    | `text`               | Output format: `text` (default) or `json` |
| `--limit`     | `20`                 | Maximum number of entries to show        |

**Output:**

```
Migration history (most recent first):
  2026-07-02T20:46:10Z · melange v0.9.1 · checksum 550b0008a779… · single · 23 functions
  2026-07-01T18:12:03Z · melange v0.9.0 · checksum 0ba53b1429e9… · single · 17 functions
```

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
| `--db-schema`        | `""`                 | Database schema                              |
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

**Expand Fan-Out Advisory:**

Runs whenever `melange_tuples` exists (both view and table backends). Flags relations whose Expand response can grow large depending on tuple counts:

- **Wildcard grants** (`[user:*]`): every Expand response surfaces the wildcard entry (`user:*`). Downstream consumers that enumerate the wildcard client-side hit unbounded fan-out.
- **Recursive TTU** (`viewer from parent` on a self-referential type): `ExpandRecursive` walks the parent pointer chain, so a deep hierarchy multiplies round-trip cost.

The advisory is `StatusPass` — a hint, not a diagnosed problem — and suggests setting `melange.max_expand_leaf` as a session-level guardrail. See [Expanding Permissions](../../guides/expanding-permissions/#per-leaf-cap).

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

### explain

Return the resolution trace for a check — every attempted branch, contributing tuples, per-branch success/failure. See the [Explaining Decisions guide](../../guides/explaining-decisions/) for the trace structure.

```bash
melange explain user:alice viewer document:1 --db postgres://localhost/mydb
```

**Flags:**

| Flag           | Default       | Description                                                                                          |
| -------------- | ------------- | ---------------------------------------------------------------------------------------------------- |
| `--db`         | (from config) | PostgreSQL connection string                                                                          |
| `--db-schema`  | `"public"`    | Database schema                                                                                       |
| `--format`     | `tree`        | `tree` (unicode pretty-print) or `json` (raw `Trace` JSONB)                                          |
| `--max-nodes`  | `0`           | Cap on trace nodes. `0` defers to the session GUC `melange.max_explain_nodes`, then to 100           |
| `--color`      | `auto`        | `auto` (TTY + `NO_COLOR` unset), `always`, or `never`. See [Colour Output](#colour-output) below.    |

**Tree output:**

{{< explaintree >}}
✓ user:alice has viewer on document:1
└── via userset: via [group#member] → group:engineering
    └── direct: user:alice → member → group:engineering
{{< /explaintree >}}

**Failure with attempted branches:**

{{< explaintree >}}
✗ user:bob does NOT have viewer on document:1
└── union of 3 branches
    ├── ✗ no direct grant
    ├── ✗ implied: implied via editor
    └── ✗ via userset: via [group#member] → group:engineering
        └── union of 1 branches
            └── ✗ no direct grant
{{< /explaintree >}}

`--format=json` returns the raw `Trace` JSONB for tooling.

### expand

Return the OpenFGA-shaped `UsersetTree` for a `(object, relation)` pair — the structured "who has access?" answer. See the [Expanding Permissions guide](../../guides/expanding-permissions/) for the tree structure and the shallow-by-default resolution model.

```bash
melange expand document:1 viewer --db postgres://localhost/mydb
```

**Flags:**

| Flag             | Default       | Description                                                                                                      |
| ---------------- | ------------- | ---------------------------------------------------------------------------------------------------------------- |
| `--db`           | (from config) | PostgreSQL connection string                                                                                     |
| `--db-schema`   | `"public"`    | Database schema                                                                                                  |
| `--format`       | `tree`        | `tree` (unicode pretty-print) or `json` (raw `UsersetTree` JSONB)                                                |
| `--flatten`      | `false`       | Call `ExpandRecursive` and print the flat, deduplicated user list (chases `Leaf.Computed` and TTU pointers)      |
| `--recursive`    | `false`       | Alias for `--flatten`                                                                                            |
| `--subject-type` | (unset)       | Melange extension. Narrow `Leaf.Users` to a single subject type                                                  |
| `--max-leaf`     | `0`           | Melange extension. Cap each `Leaf.Users` list. `0` defers to session GUC `melange.max_expand_leaf`, then unbounded |
| `--color`        | `auto`        | `auto` (TTY + `NO_COLOR` unset), `always`, or `never`. See [Colour Output](#colour-output) below.                |

**Tree output:**

```
document:1#viewer • union of 2
├── document:1#viewer • users
│   ├── user:alice
│   ├── user:bob
│   └── group:eng#member
└── document:1#viewer • computed pointer
    └── computed → document:1#editor  (melange expand document:1#editor to chase)
```

**Flattened user list:**

```
$ melange expand document:1 viewer --flatten
user:alice
user:bob
user:carol
group:eng#member
user:*
```

`--flatten` chases every `Leaf.Computed` and `Leaf.TupleToUserset` pointer with follow-up Expand calls. Cost is N round-trips per N distinct pointers; suitable for admin flows, not the request path. `--format=json` returns the raw `UsersetTree` JSONB for tooling.

### Colour output

`melange explain` and `melange expand` colourise identifiers in `tree` output to match the [OpenFGA VS Code extension](https://github.com/openfga/vscode-ext)'s `openfga-dark` theme. Types render green, relations cyan, type restrictions (`[user]`, `[group#member]`) mint, keywords / delimiters grey, dim prose and tree connectors dim grey. The `✓` / `✗` result markers in the header render as bold white glyphs on a coloured background chip.

Colours are emitted as 24-bit ANSI true colour so the mapping matches the VS Code palette on modern terminals. `--color=never` disables all escapes for piped writers or legacy terminals. `--color=auto` (default) enables colour when stdout is a TTY and `NO_COLOR` is unset.

`--format=json` output is never colourised — the raw JSONB is unaffected by `--color`.

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

### generate migration

Generate versioned SQL migration files for use with external migration frameworks (golang-migrate, Atlas, Flyway, etc.). Instead of applying SQL directly like `melange migrate`, this command produces `.sql` files you commit, review, and apply through your existing workflow.

For a conceptual overview of when to use this versus `melange migrate`, see [Running Migrations](../../guides/migrations/).

```bash
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations
```

**Flags:**

| Flag                  | Default              | Description                                                    |
| --------------------- | -------------------- | -------------------------------------------------------------- |
| `--schema`            | `schemas/schema.fga` | Path to current `.fga` schema file (required)                  |
| `--db-schema`         | `""`                 | PostgreSQL schema for melange objects                          |
| `--output`            | (stdout)             | Output directory for migration files                           |
| `--name`              | `melange`            | Migration name suffix used in filenames                        |
| `--format`            | `split`              | Output format: `split` (`.up.sql`/`.down.sql`) or `single`    |
| `--up`                | `false`              | Output only the UP migration (stdout mode only)                |
| `--down`              | `false`              | Output only the DOWN migration (stdout mode only)              |
| `--db`                | -                    | Database URL. Compare against most recent migration record     |
| `--git-ref`           | -                    | Git ref. Compare against schema at that commit/branch/tag      |
| `--previous-schema`   | -                    | File path. Compare against a previous `.fga` file              |

{{< callout type="info" >}}
The three comparison flags (`--db`, `--git-ref`, `--previous-schema`) are mutually exclusive. When none is specified, a full migration is generated containing all functions.
{{< /callout >}}

**Output modes:**

When `--output` is specified, timestamped files are written to that directory:

```
db/migrations/20260322143000_melange.up.sql
db/migrations/20260322143000_melange.down.sql
```

When `--output` is omitted (stdout mode), you must specify `--up` or `--down` to select which migration to print. This is useful for piping into other tools:

```bash
melange generate migration --schema schemas/schema.fga --git-ref main --up | psql "$DATABASE_URL"
```

**Output formats:**

| Format   | Files                                          | Use case                        |
| -------- | ---------------------------------------------- | ------------------------------- |
| `split`  | `TIMESTAMP_NAME.up.sql`, `TIMESTAMP_NAME.down.sql` | golang-migrate, Atlas, most frameworks |
| `single` | `TIMESTAMP_NAME.sql` with UP and DOWN sections | Flyway, frameworks expecting a single file |

**Comparison modes:**

By default, the UP migration includes every generated function (full mode). To emit only what changed, use one of:

- **`--db`** - Reads the previous function inventory from the `melange_migrations` table. Most precise, but requires database access.
- **`--git-ref`** - Compiles the schema from a git ref and compares. No database needed, suitable for CI.
- **`--previous-schema`** - Compiles a previous schema from a local file and compares.

When a comparison mode is active:

- Only functions with changed SQL bodies (or newly added) are included in UP
- Orphaned functions (removed from schema) get `DROP FUNCTION IF EXISTS` statements
- Dispatcher functions are always included regardless of changes

**Examples:**

```bash
# First migration (full - all functions)
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations

# Incremental migration comparing against database
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --db postgres://localhost/mydb

# Incremental migration comparing against git
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --git-ref main

# Preview changes to stdout
melange generate migration \
  --schema schemas/schema.fga \
  --git-ref HEAD~1 \
  --up

# Single-file format for Flyway
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --format single \
  --git-ref main
```

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

Display the effective configuration after merging defaults, config file,
environment variables, and any selected environment profile.

```bash
melange config show
```

**Flags:**

| Flag              | Default | Description                                                 |
| ----------------- | ------- | ----------------------------------------------------------- |
| `--source`        | `false` | Show the config file path and active environment            |
| `--reveal-secrets`| `false` | Print passwords in cleartext instead of masking them        |

Passwords — in both `database.url` and the discrete `password` field, across the
base config and every environment profile — are **masked by default**, including
values resolved from `${VAR}` references. Pass `--reveal-secrets` to print them.

**Example — inspect the resolved production profile:**

```bash
melange config show --env production --source
```

**Output:**

```
Config file: /path/to/project/melange.yaml
Environment: production

schema: schemas/schema.fga
database:
  url: postgres://prod-user:****@prod-db:5432/app
  ...
```

This is useful for debugging configuration issues and understanding which values
are in effect for a given environment.

### env list

List the [environment profiles](../configuration/#environments) defined in your
config and their connection targets. The active environment is marked with `*`.

```bash
melange env list
```

**Output:**

```
Environments:
* local            postgres://localhost:5432/mydb_dev
  production       ${PROD_DATABASE_URL}
  staging          staging.db.internal:5432/app

Default: local
Active:  local (marked with *)
```

Targets are shown as configured — `${VAR}` references are left literal (not
expanded) and any URL password is redacted, so the listing is safe to share.

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

The CI gates exit non-zero on an intentional gate failure, not just tool errors:
`melange diff --exit-code` exits 1 when a change is breaking, and `melange migrate
--if-deployed-checksum` exits 1 when the deployed model has drifted. In both cases
the accompanying message explains the condition.

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

**Gated deploy against a live environment.** Fail the build on a breaking change,
then apply only if the environment hasn't drifted since you inspected it:

```bash
# Fail review if the change narrows access
melange diff --env production --exit-code

# Capture the state you reviewed
CURRENT=$(melange status --env production --format json | jq -r .deployed.schema_checksum)

# … approval gate …

# Apply only if production is still at that checksum
melange migrate --env production --if-deployed-checksum "$CURRENT"
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

### External Migration Frameworks

If you use golang-migrate, Atlas, Flyway, or a similar tool, replace `melange migrate` with `melange generate migration`:

```bash
# First time - generate a full migration
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations

# After schema changes - generate an incremental migration
melange generate migration \
  --schema schemas/schema.fga \
  --output db/migrations \
  --git-ref main

# Commit the schema change and migration together
git add schemas/schema.fga db/migrations/
git commit -m "Add editor relation to document type"

# Apply with your framework
migrate -path db/migrations -database "$DATABASE_URL" up
```

See [Running Migrations](../../guides/migrations/) for guidance on choosing between built-in and external migration strategies.

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

Run health checks with the `melange doctor` command (see above). The doctor
package is internal to the CLI and is not a supported programmatic API; for
automation, run the command and check its exit code (non-zero on failure).

See [Checking Permissions](../../guides/checking-permissions/) for the full Go API reference.
