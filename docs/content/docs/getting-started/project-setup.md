---
title: Project Setup
description: "The melange init wizard in detail: starter templates, migration strategies, database settings, and client code generation."
weight: 3
prev: /docs/getting-started/quick-start
next: /docs/getting-started/tuples-view
---

This page covers the `melange init` wizard in detail: what each prompt does, how the starter templates differ, and how to customize the setup.

For the shortest path to a working setup, see [Quick Start](../quick-start).

## The Init Wizard

```bash
melange init
```

The wizard detects your project type and prompts for configuration values. If it finds a `go.mod`, it defaults to Go; if it finds a `package.json` (and no `go.mod`), it defaults to TypeScript. When both exist, Go takes precedence.

### Wizard Prompts

The wizard collects these settings in order:

| Prompt | Default | Description |
|--------|---------|-------------|
| Schema path | `melange/schema.fga` | Where to put your authorization model |
| Starter model | Organization RBAC | Pre-built schema template |
| Database URL | `postgres://localhost:5432/mydb` | PostgreSQL connection string |
| Migration strategy | Built-in | How schema changes are applied |
| Generate client code? | Yes (if project detected) | Whether to set up type-safe code generation |
| Add Melange instructions for coding agents? | Yes (skipped with `-y` unless `--agents`) | Appends a Melange section to `AGENTS.md` (or `CLAUDE.md`). See [Coding Agent Instructions](#coding-agent-instructions). |

If you choose **versioned** migration strategy, you'll also be asked:

| Prompt | Default | Description |
|--------|---------|-------------|
| Migration output directory | `migrations/` | Where to write versioned SQL files |
| File format | Split | Separate up/down files, or combined |
| Migration name suffix | `melange` | Suffix for generated filenames |

If you enable **client code generation**, you'll also be asked:

| Prompt | Default | Description |
|--------|---------|-------------|
| Runtime | Auto-detected | `go` or `typescript` |
| Output directory | `internal/authz` (Go) or `src/authz` (TypeScript) | Where to write generated code |
| Package name | `authz` | Go package name (Go only) |
| ID type | `string` | Type for object/subject IDs: `string`, `int64`, `uuid.UUID` |

## Starter Templates

Four starter templates are available:

{{< tabs >}}

{{< tab name="Organization RBAC" >}}
The default template. Models organizations with a role hierarchy and repositories with inherited permissions.

```fga
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin

type repository
  relations
    define org: [organization]
    define owner: [user]
    define admin: [user] or owner
    define can_read: member from org or admin
    define can_write: admin
    define can_delete: owner
```

Covers role hierarchies (`owner > admin > member`) and parent inheritance (`member from org`). A good starting point for SaaS applications with organizations, teams, and resources.
{{< /tab >}}

{{< tab name="Document Sharing" >}}
Permissions cascade: owners can edit, editors can view.

```fga
model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
```

A good starting point for content or document-based applications.
{{< /tab >}}

{{< tab name="Minimal" >}}
Just a `user` type. Add your own types and relations from here.

```fga
model
  schema 1.1

type user
```
{{< /tab >}}

{{< tab name="None" >}}
Skips schema file creation. Use this if you already have a `.fga` file.
{{< /tab >}}

{{< /tabs >}}

The [Authorization Modelling](../../concepts/modelling/) guide covers additional relation patterns including exclusions, intersections, and wildcards.

## Migration Strategies

The wizard asks you to choose how schema changes are applied to your database:

| Strategy | Command | Description |
|----------|---------|-------------|
| **Built-in** | `melange migrate` | Connects directly and applies SQL. Suited to solo projects, prototyping, and simple deployments. |
| **Versioned** | `melange generate migration` | Produces `.sql` files for your existing migration framework. Suited to teams, PR reviews, and CI/CD pipelines. |

**Built-in** connects directly to PostgreSQL and applies SQL functions. It tracks changes via a `melange_migrations` table and skips unchanged schemas automatically.

**Versioned** produces timestamped `.sql` files (e.g., `20240315_melange.up.sql`) that you apply with your existing migration tool (golang-migrate, Atlas, Flyway, etc.). This means schema changes go through code review in PRs like any other migration.

You can change strategies later by updating your config file. See [Running Migrations](../../guides/migrations/) for details on comparison modes and CI workflows.

## Generated Config File

After running `melange init`, the generated config file looks like this:

```yaml
database:
  url: postgres://localhost:5432/mydb
generate:
  client:
    id_type: string
    output: internal/authz
    package: authz
    runtime: go
schema: melange/schema.fga
```

With versioned migrations enabled:

```yaml
database:
  url: postgres://localhost:5432/mydb
generate:
  client:
    id_type: string
    output: internal/authz
    package: authz
    runtime: go
  migration:
    format: split
    name: melange
    output: migrations/
schema: melange/schema.fga
```

### Config File Placement

The config file location depends on your schema path:

- Schema under `melange/` (e.g., `melange/schema.fga`): config written to `melange/config.yaml`
- Otherwise: config written to `melange.yaml` at the project root

Melange discovers config files by searching up from the current directory. See [Configuration Reference](../../reference/configuration/) for the full discovery order and all options.

## Non-Interactive Mode

Use `-y` to accept all defaults without prompting:

```bash
melange init -y
```

Override individual values with flags:

```bash
melange init -y \
  --schema melange/auth.fga \
  --db postgres://prod:5432/app \
  --template doc-sharing \
  --migration-strategy versioned
```

Skip runtime dependency installation:

```bash
melange init -y --no-install
```

### All Init Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-y`, `--yes` | Accept all defaults without prompting | |
| `--no-install` | Skip installing runtime dependencies | |
| `--schema` | Schema file path | `melange/schema.fga` |
| `--db` | Database URL | `postgres://localhost:5432/mydb` |
| `--template` | Starter model: `org-rbac`, `doc-sharing`, `minimal`, `none` | `org-rbac` |
| `--runtime` | Client runtime: `go`, `typescript` | Auto-detected |
| `--output` | Client output directory | `internal/authz` (Go), `src/authz` (TS) |
| `--package` | Client package name (Go only) | `authz` |
| `--id-type` | Client ID type: `string`, `int64`, `uuid.UUID` | `string` |
| `--migration-strategy` | Migration strategy: `builtin`, `versioned` | `builtin` |
| `--migration-output` | Versioned migration output directory | `migrations/` |
| `--migration-format` | Versioned migration format: `split`, `single` | `split` |
| `--migration-name` | Versioned migration name suffix | `melange` |
| `--agents` | Add a Melange section to `AGENTS.md` / `CLAUDE.md` without asking (otherwise skipped with `-y`) | |

## Client Code Generation

When code generation is enabled, `melange init` installs the runtime dependency automatically:

- **Go**: `go get github.com/pthm/melange/melange`
- **TypeScript**: Detected package manager (`bun` > `pnpm` > `yarn` > `npm`) runs `add @pthm/melange`

To generate client code after setup:

```bash
melange generate client
```

This produces type-safe constants and constructors from your schema. For example, with Go:

```go
import "myapp/internal/authz"

// Generated constants and constructors
allowed, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("42"),
)
```

The `--filter` option (configurable in `generate.client.filter`) lets you limit which relations are generated. For example, `--filter can_` generates only relations starting with `can_`.

## Coding Agent Instructions

`melange init` can add a short Markdown section (about 300 tokens) that tells coding agents (Claude Code, Codex, Cursor, and others) how this project uses Melange: where the schema and config live, that relationship data comes from the `melange_tuples` view rather than tuple writes, the commands to run after changing the model, and how to debug an unexpected permission result.

The wizard asks before adding it. With `-y`, it is skipped unless you also pass `--agents`. The section goes into the instructions file your agent already reads:

1. `AGENTS.md`, if it exists
2. otherwise `CLAUDE.md`, if it exists
3. otherwise a new `AGENTS.md`

Only one file is written, because a `CLAUDE.md` often just imports `AGENTS.md`. Nothing is written if either file already has the section.

For an existing project, or after changing your schema path, client output, or migration strategy, generate the section from your config and append it yourself:

```bash
melange init agent >> AGENTS.md
```

See [Using Coding Agents](../../guides/coding-agents/) for sample output and the docs formats agents can read.

## Next Steps

- [Creating Your Tuples View](../tuples-view): mapping your domain tables to authorization tuples
- [Your First Migration](../first-migration): applying your schema and verifying the setup
