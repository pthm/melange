---
title: Using Coding Agents
description: Point coding agents like Claude Code, Codex, or Cursor at Melange's docs, and give them project instructions for working on your schema.
weight: 12
---

Coding agents work best with Melange when they can read the docs as markdown and know the project's workflow before they start editing. This page covers both.

## Docs for Agents

The documentation is published in machine-readable forms:

| URL | Contents |
| --- | -------- |
| [`/llms.txt`](https://melange.sh/llms.txt) | An index of every docs page with a one-line description, following the [llms.txt](https://llmstxt.org) convention. |
| [`/llms-full.txt`](https://melange.sh/llms-full.txt) | Every user-facing docs page as markdown in one file, for agents that can load the whole manual into context. |
| `<page>.md` | Any docs page as markdown, for example [`/docs/getting-started/quick-start.md`](https://melange.sh/docs/getting-started/quick-start.md). |

Give an agent `https://melange.sh/llms.txt` and it can fetch only the pages it needs.

## Project Instructions

Most agents read a file of project instructions at the start of every session: `AGENTS.md` for Codex, Cursor, and most others, `CLAUDE.md` for Claude Code. Add a Melange section to it so the agent follows your workflow instead of guessing. Adjust the paths to match your `melange/config.yaml`.

````markdown
## Authorization (Melange)

Authorization is compiled by [Melange](https://melange.sh) from an OpenFGA model into
PostgreSQL functions. Docs for agents: https://melange.sh/llms.txt

- Model: `melange/schema.fga` (OpenFGA DSL, schema 1.1). Config: `melange/config.yaml`.
- Relationship data comes from the `melange_tuples` SQL view over our domain tables.
  It is a view: never insert tuples. To grant access, write rows to the underlying
  tables, or change the view definition in a regular migration.
- Generated client code lives in `internal/authz`. Do not edit it by hand.

After changing the model:

1. `melange validate --format json` to check the schema. It reports syntax errors,
   undefined types and relations, and cycles, and exits non-zero on failure.
2. `melange migrate` to compile and apply it to the dev database.
   (If the project uses versioned migrations, run `melange generate migration` instead
   and apply the generated SQL with our migration tool.)
3. `melange generate client` to regenerate the typed client.
4. `melange doctor --format json` to check the view, functions, and indexes. Each
   failing check has a `fix` field.

When a permission check returns an unexpected answer, run
`melange explain <type>:<id> <relation> <type>:<id>` before changing the model. It
prints every path that was tried and the tuples behind it. `melange expand
<type>:<id> <relation>` shows everyone who has a relation on an object.

Constraints: OpenFGA conditions are not supported. Prefer `check` over listing
subjects on request paths; listing is for batch and admin work.
````

The snippet assumes the defaults that `melange init` writes. If you use built-in migrations only, drop the versioned-migrations sentence; if you don't generate client code, drop step 3 and the generated-code line.

## What Agents Get Wrong

- **Writing tuples.** Agents that know OpenFGA reach for a tuple-write API. Melange has none: the `melange_tuples` view derives tuples from your tables, so access changes are ordinary inserts and updates to those tables.
- **Guessing at a wrong answer.** An agent that sees an unexpected deny tends to loosen the model. [`melange explain`](../explaining-decisions/) shows which branch failed, which is usually a missing row or a view mapping, not the schema.
- **Forgetting to migrate.** Editing `schema.fga` changes nothing until `melange migrate` (or the generated migration) runs. `melange status` reports whether the database is behind the schema.
- **Unsupported features.** OpenFGA conditions fail validation. See [OpenFGA Compatibility](../../reference/openfga-compatibility/).

## Next Steps

- [Explaining Decisions](../explaining-decisions/): reading `explain` output
- [Troubleshooting](../troubleshooting/): common errors and `melange doctor`
- [CLI Reference](../../reference/cli/): every command and flag
