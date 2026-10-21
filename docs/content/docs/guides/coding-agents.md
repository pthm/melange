---
title: Using Coding Agents
description: Point coding agents like Claude Code, Codex, or Cursor at Melange's docs, and give them project instructions for working on your schema.
weight: 13
---

Coding agents work best with Melange when they can read the docs as markdown and know the project's workflow before they start editing. This page covers both.

## Docs for Agents

The documentation is published in machine-readable forms:

| URL | Contents |
| --- | -------- |
| [`/llms.txt`](https://melange.sh/llms.txt) | An index of every docs page with a one-line description, following the [llms.txt](https://llmstxt.org) convention. |
| [`/llms-full.txt`](https://melange.sh/llms-full.txt) | Every user-facing docs page as markdown in one file. It is large (around 90k tokens), so use it only when an agent needs the whole manual. |
| `<page>.md` | Any docs page as markdown, for example [`/docs/getting-started/quick-start.md`](https://melange.sh/docs/getting-started/quick-start.md). |

Point an agent at `https://melange.sh/llms.txt` and it can fetch only the pages it needs.

## Project Instructions

Most agents read a file of project instructions at the start of every session: `AGENTS.md` for Codex, Cursor, and most others, `CLAUDE.md` for Claude Code. A Melange section there makes the agent follow your workflow instead of guessing.

Generate the section with the CLI and append it to that file:

```bash
melange init agent >> AGENTS.md
# or, for Claude Code
melange init agent >> CLAUDE.md
```

`melange init agent` reads your [config file](../../reference/configuration/) and fills in the schema path, migration strategy (`melange migrate` or `melange generate migration`), and generated client location. Run it from the project root, and re-run it after changing those settings, replacing the old section.

For a new project, the `melange init` wizard asks whether to add it to `AGENTS.md` (or `CLAUDE.md` if that is the only one present), and skips the step if the section is already there. In non-interactive mode, pass `melange init -y --agents`.

For a project with built-in migrations and a Go client in `internal/authz`, the output is:

````markdown
## Authorization (Melange)

Permissions are an OpenFGA model that Melange compiles into PostgreSQL functions.

- Model: `melange/schema.fga`. Config: `melange/config.yaml`.
- Relationships come from the `melange_tuples` view over our tables, defined in our own migrations (Melange doesn't create it). Never insert tuples; change the underlying rows, or the view definition.
- `internal/authz` is generated; don't edit it.
- No OpenFGA conditions; express them as `WHERE` clauses in the tuples view or as contextual tuples (see [Replacing Conditions](../conditions/)). Use `check_permission` (or the client's check) for access decisions; listing objects or subjects can be slow and suits batch or admin use.

After editing the model:

1. `melange validate`
2. `melange generate client` (regenerates `internal/authz`)
3. `melange migrate` applies it to the configured database (`melange config show` prints the target). Only run it against a local dev database.

Unexpected allow or deny: run `melange explain <subject> <relation> <object>` (e.g. `user:alice viewer document:1`) before changing the model; it shows each path tried and its tuples. `melange doctor` diagnoses setup problems.

`validate` and `generate` work offline; `migrate`, `explain`, and `doctor` need the database. If the project's usual setup can't reach one, say so instead of improvising one.

Docs, when needed: https://melange.sh/llms.txt
````

The section is deliberately short (about 300 tokens), because agents load it into every session. It holds what an agent can't infer from the code, the edit loop, and guardrails against the mistakes below; the docs are linked for anything else. Avoid pasting docs pages or `llms-full.txt` into your instructions file.

## What Agents Get Wrong

- **Writing tuples.** Agents that know OpenFGA reach for a tuple-write API. Melange has none: the `melange_tuples` view derives tuples from your tables, so access changes are ordinary inserts and updates to those tables.
- **Guessing at a wrong answer.** An agent that sees an unexpected deny tends to loosen the model. [`melange explain`](../explaining-decisions/) shows which branch failed, which is usually a missing row or a view mapping, not the schema.
- **Forgetting to migrate.** Editing `schema.fga` changes nothing until `melange migrate` (or the generated migration) runs. `melange status` reports whether the database is behind the schema.
- **Migrating the wrong database.** `melange migrate` applies to whatever database the config names. Keep a local development database there, and point other environments at their own [environment profiles](../../reference/configuration/#environments).
- **Improvising a database.** Agents without database access can spend many turns trying to start Postgres. `validate`, `generate migration`, and `generate client` work offline; `migrate`, `explain`, and `doctor` need the database.
- **Unsupported features.** OpenFGA conditions fail validation. See [Replacing Conditions](../conditions/) for SQL equivalents.

## Next Steps

- [Explaining Decisions](../explaining-decisions/): reading `explain` output
- [Troubleshooting](../troubleshooting/): common errors and `melange doctor`
- [CLI Reference](../../reference/cli/): every command and flag
