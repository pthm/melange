---
title: Deployed Model
weight: 3
---

Every `melange migrate` records the authorization model it applied into the
`melange_migrations` table, so the database is **self-describing**: it knows which
schema is deployed without a separate state file to keep in sync. This is the
foundation for drift detection, safe deploys, and recovering a schema you have
lost.

## What is stored

Each migration record carries, alongside the function inventory used for orphan
cleanup:

| Column            | Contents                                                              |
| ----------------- | -------------------------------------------------------------------- |
| `schema_dsl`      | The exact OpenFGA DSL that was applied (a `.fga` file, or an `fga.mod` bundle) |
| `model_json`      | The parsed model — the same structures melange compiles to SQL       |
| `schema_format`   | `single` (a `.fga` file) or `modular` (an `fga.mod` manifest)        |
| `schema_checksum` | A SHA-256 of the schema, used for fast change detection              |
| `melange_version` | The melange version that applied the migration                       |

`schema_dsl` and `model_json` are two independent representations of the same
model. Tools prefer `model_json` (already parsed) and fall back to parsing
`schema_dsl` when it is absent, so a record stays usable even if one is missing.

## What reads it

The deployed model is the single source of truth behind a family of commands
(see the [CLI reference](cli) for each):

| Command                          | Uses the deployed model to…                                        |
| -------------------------------- | ------------------------------------------------------------------ |
| `status`                         | Report whether the local schema is in sync with the database       |
| `schema pull`                    | Reconstruct and print the deployed `.fga` for recovery or review   |
| `diff`                           | Classify local changes additive/breaking against what is deployed  |
| `history`                        | List the migration audit trail (versions, checksums, timestamps)   |
| `doctor`                         | Check the model is recorded and warn on a pending breaking change  |
| `migrate --if-deployed-checksum` | Apply only if the database is still at the expected checksum        |

## Why store the model

- **No drift.** The database describes itself, so there is no external state file
  that can disagree with reality.
- **Safe deploys.** `diff --exit-code` fails CI on a breaking change, and
  `migrate --if-deployed-checksum` refuses to apply against a database that has
  drifted since you planned the change.
- **Recovery.** `schema pull` reconstructs the deployed schema straight from the
  database when the source `.fga` has been lost.

## Compatibility

Model storage was introduced in melange v0.9. Databases migrated by an earlier
version have no recorded model; the commands above detect this and report it
plainly (for example, `schema pull` distinguishes "never migrated" from
"migrated before model storage existed"). Re-running `melange migrate` after
upgrading backfills the model without re-applying the generated functions, so a
plain rerun makes these commands work — no `--force` needed.

A **modular** (`fga.mod`) schema is stored as its manifest plus module bundle for
reference and recovery. That combined form does not re-parse as a single `.fga`,
so `schema pull` labels it as a non-parseable bundle rather than emitting a
schema you can feed straight back into `melange migrate`.
