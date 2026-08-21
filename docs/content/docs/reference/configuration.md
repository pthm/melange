---
title: Configuration
weight: 2
---

Melange supports configuration via YAML files, environment variables, and command-line flags with the following precedence (highest to lowest):

1. **Command-line flags**
2. **Environment variables** (`MELANGE_*` prefix)
3. **Configuration file** (`melange.yaml` or `melange.yml`)
4. **Built-in defaults**

A selected [environment profile](#environments) overlays the base configuration
before this precedence is applied, so an explicit flag still wins over the
environment's value.

## Configuration File

Melange automatically discovers a configuration file by checking each directory for the following names (in order):

1. `melange.yaml`
2. `melange.yml`
3. `melange/config.yaml`
4. `melange/config.yml`
5. `melange/melange.yaml`
6. `melange/melange.yml`

The search starts in the current directory and walks up parent directories until a `.git` directory is found (repository boundary) or 25 levels are reached. The first match wins.

{{< callout type="info" >}}
The `melange/` directory convention is the default when you run `melange init`. Existing `melange.yaml` files at the project root continue to work. Both layouts are fully supported.
{{< /callout >}}

You can override auto-discovery with `--config`:

```bash
melange --config /path/to/custom-config.yaml migrate
```

## File Format

Create a `melange.yaml` in your project root, or use `melange init` to generate one:

```yaml
# Path to OpenFGA schema file (used by all commands)
schema: schemas/schema.fga

# Database connection (used by migrate, status, doctor, schema pull, explain, expand)
# See "Environments" below for per-environment overrides selected with --env.
database:
  # Option 1: Full connection URL
  url: postgres://user:password@localhost:5432/mydb?sslmode=prefer

  # Option 2: Discrete fields (used when url is empty)
  # host: localhost
  # port: 5432
  # name: mydb
  # user: melange
  # password: secret
  # sslmode: prefer

  # Optional: install melange objects in a specific PostgreSQL schema
  # schema: authz

# Code generation settings
generate:
  client:
    runtime: go
    schema: schemas/schema.fga  # Overrides top-level schema
    output: internal/authz
    package: authz
    filter: can_
    id_type: string

  # Migration file generation settings (for external frameworks)
  migration:
    output: db/migrations
    name: melange
    format: split           # "split" or "single"

# Migration settings
migrate:
  dry_run: false
  force: false

# Doctor command settings
doctor:
  verbose: false
  skip_performance: false
```

## Minimal Configuration

A minimal configuration for most projects:

```yaml
schema: schemas/schema.fga

database:
  url: postgres://localhost/mydb

generate:
  client:
    runtime: go
    output: internal/authz
```

## Configuration Reference

### Top-Level Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `schema` | string | `schemas/schema.fga` | Path to the OpenFGA schema file |

### Database Settings

Configure under `database:`:

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `url` | string | - | Full PostgreSQL connection URL |
| `host` | string | - | Database host (used when `url` is empty) |
| `port` | int | `5432` | Database port |
| `name` | string | - | Database name |
| `user` | string | - | Database user |
| `password` | string | - | Database password |
| `sslmode` | string | `prefer` | SSL mode for connection |
| `schema` | string | - | PostgreSQL schema for melange objects (see [Custom Database Schema](#custom-database-schema)) |

**Connection URL format:**
```
postgres://user:password@host:5432/dbname?sslmode=require
```

**Required fields when using discrete configuration:**
- `host`
- `name`
- `user`

**SSL Mode Values:**

| Value | Description |
|-------|-------------|
| `disable` | No SSL |
| `allow` | Try non-SSL first, then SSL |
| `prefer` | Try SSL first, then non-SSL (default) |
| `require` | Require SSL |
| `verify-ca` | Require SSL and verify CA |
| `verify-full` | Require SSL and verify CA and hostname |

### Generate Client Settings

Configure under `generate.client:`:

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `runtime` | string | - | Target runtime: `go`, `typescript` |
| `schema` | string | (top-level `schema`) | Path to schema file |
| `output` | string | - | Output directory for generated code |
| `package` | string | `authz` | Package/module name |
| `filter` | string | - | Relation prefix filter (e.g., `can_`) |
| `id_type` | string | `string` | ID type for constructors |

### Generate Migration Settings

Configure under `generate.migration:`:

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `output` | string | `""` (stdout) | Output directory for migration files |
| `name` | string | `melange` | Migration name suffix in filenames |
| `format` | string | `split` | Output format: `split` or `single` |

{{< callout type="warning" >}}
Do not configure both `generate.migration.output` and use `melange migrate` against the same database. The two strategies track state differently and mixing them causes warnings. See [Running Migrations](../../guides/migrations/) for guidance.
{{< /callout >}}

### Migrate Settings

Configure under `migrate:`:

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `dry_run` | bool | `false` | Output SQL without applying |
| `force` | bool | `false` | Force migration even if unchanged |

### Doctor Settings

Configure under `doctor:`:

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `verbose` | bool | `false` | Show detailed output |
| `skip_performance` | bool | `false` | Skip performance checks (view analysis) |

## Custom Database Schema

By default, melange installs all its objects into the connection's current schema (usually `public`). The `database.schema` option places them in a specific PostgreSQL schema instead.

Objects affected by this setting:

- Generated check functions (`check_permission`, `check_document_viewer`, etc.)
- Generated list functions (`list_accessible_objects`, `list_accessible_subjects`, etc.)
- The `melange_migrations` tracking table
- The `melange_tuples` view (you create this yourself, but it should be in the target schema)

Your application tables are not affected. They remain wherever you have them. The tuples view in the target schema can query tables in any other schema.

### Prerequisites

You must create the schema before running `melange migrate`:

```sql
CREATE SCHEMA IF NOT EXISTS authz;
```

The schema must be on your connection's `search_path` for permission checks to work at runtime. Most PostgreSQL configurations include `public` by default, but a custom schema needs to be added:

```sql
ALTER ROLE myuser SET search_path TO authz, public;
```

Or set it per-connection in your application's connection string or pool configuration.

### Configuration

```yaml
database:
  url: postgres://localhost/mydb
  schema: authz
```

Or via environment variable:

```bash
export MELANGE_DATABASE_SCHEMA=authz
```

Or via CLI flag:

```bash
melange migrate --db postgres://localhost/mydb --db-schema authz
```

When `--db-schema` is not passed, every database command resolves it from
`database.schema` in the config (or the selected environment), falling back to
`public`.

### Tuples view setup

Create your `melange_tuples` view in the target schema. The view itself lives in the custom schema but can reference tables in any schema:

```sql
CREATE OR REPLACE VIEW authz.melange_tuples AS
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'organization' AS object_type,
    organization_id::text AS object_id
FROM public.organization_members;
```

### Runtime client configuration

When using a custom schema, configure your runtime client to match:

**Go:**

```go
checker := melange.NewChecker(db, melange.WithDatabaseSchema("authz"))
```

**TypeScript:**

```typescript
const checker = new Checker({ db, databaseSchema: 'authz' });
```

## Environments

Named **environment profiles** let a single `--env` flag point a command at a
specific database — `local`, `staging`, `production` — instead of hand-swapping
`--db` connection strings. Each profile overlays the base configuration.

```yaml
schema: melange/schema.fga

# Base connection — used when no environment is selected, and the fallback
# for any field an environment leaves unset.
database:
  url: postgres://localhost:5432/mydb_dev

default_environment: local        # optional; used when --env / MELANGE_ENV absent

environments:
  local:
    database:
      url: postgres://localhost:5432/mydb_dev
  staging:
    database:
      host: staging.db.internal
      name: app
      user: melange
      password: ${STAGING_DB_PASSWORD}   # expanded from the OS environment
      schema: public
  production:
    database:
      url: ${PROD_DATABASE_URL}
```

Then target an environment on any database command:

```bash
melange status --env production
melange migrate --env staging
melange schema pull --env production -o recovered.fga
```

### Selecting an environment

The active environment is resolved with this precedence (highest first):

1. `--env <name>` flag
2. `MELANGE_ENV` environment variable
3. `default_environment` config key
4. none → the base `database` block (behaviour identical to configs without an `environments:` map)

An explicitly selected environment (`--env` or `MELANGE_ENV`) that isn't defined
is a hard error, so a typo can't silently run against the wrong database. A
`default_environment` that names a missing environment is treated leniently: a
warning is printed and the base config is used, so a stale default never blocks
diagnostic commands.

### Overlay semantics

An environment's `database` block overlays the base one — fields it sets win,
fields it omits fall through. To keep behaviour predictable, when an environment
supplies a discrete connection (`host`/`name`) on top of a base that uses a
`url`, the base URL is dropped rather than decomposed. In that case the
environment must provide a complete connection (including `user`); melange does
not split a base URL into its parts.

### Secrets with `${VAR}`

Environment values may reference OS environment variables with `${VAR}` syntax,
so production credentials stay out of the committed config file:

```yaml
environments:
  production:
    database:
      url: ${PROD_DATABASE_URL}
```

Only the braced `${VAR}` form is expanded; a literal `$` (e.g. in a password) is
left untouched. An unset variable referenced by a database field is reported
**when a command actually connects** — not at startup — so diagnostic commands
like `melange env list`, `melange config show`, and `melange validate` still run
even if a secret is missing from the current shell.

### Inspecting environments

`melange env list` prints the defined profiles and their targets (with the
active one marked `*`); `${VAR}` references are shown literally and passwords
redacted:

```bash
$ melange env list
Environments:
* local            postgres://localhost:5432/mydb_dev
  production       ${PROD_DATABASE_URL}
  staging          staging.db.internal:5432/app

Default: local
```

`melange config show --env production` prints the fully-resolved configuration
for a profile, with passwords masked by default (pass `--reveal-secrets` to see
them). See the [CLI reference](../cli/#config-show).

## Environment Variables

All configuration options can be set via environment variables with the `MELANGE_` prefix. Use underscores to separate nested keys:

| Environment Variable | Configuration Path |
|---------------------|-------------------|
| `MELANGE_SCHEMA` | `schema` |
| `MELANGE_DATABASE_URL` | `database.url` |
| `MELANGE_DATABASE_HOST` | `database.host` |
| `MELANGE_DATABASE_PORT` | `database.port` |
| `MELANGE_DATABASE_NAME` | `database.name` |
| `MELANGE_DATABASE_USER` | `database.user` |
| `MELANGE_DATABASE_PASSWORD` | `database.password` |
| `MELANGE_DATABASE_SSLMODE` | `database.sslmode` |
| `MELANGE_DATABASE_SCHEMA` | `database.schema` |
| `MELANGE_GENERATE_CLIENT_RUNTIME` | `generate.client.runtime` |
| `MELANGE_GENERATE_CLIENT_SCHEMA` | `generate.client.schema` |
| `MELANGE_GENERATE_CLIENT_OUTPUT` | `generate.client.output` |
| `MELANGE_GENERATE_CLIENT_PACKAGE` | `generate.client.package` |
| `MELANGE_GENERATE_CLIENT_FILTER` | `generate.client.filter` |
| `MELANGE_GENERATE_CLIENT_ID_TYPE` | `generate.client.id_type` |
| `MELANGE_GENERATE_MIGRATION_OUTPUT` | `generate.migration.output` |
| `MELANGE_GENERATE_MIGRATION_NAME` | `generate.migration.name` |
| `MELANGE_GENERATE_MIGRATION_FORMAT` | `generate.migration.format` |
| `MELANGE_MIGRATE_DRY_RUN` | `migrate.dry_run` |
| `MELANGE_MIGRATE_FORCE` | `migrate.force` |
| `MELANGE_DOCTOR_VERBOSE` | `doctor.verbose` |
| `MELANGE_DOCTOR_SKIP_PERFORMANCE` | `doctor.skip_performance` |
| `MELANGE_ENV` | selects an [environment profile](#environments) (like `--env`) |
| `CI` | _(special)_ |

Setting `CI` to any value disables the automatic update check. Most CI providers set this automatically.

**Example:**
```bash
export MELANGE_DATABASE_URL="postgres://prod-user:secret@prod-db:5432/myapp"
export MELANGE_GENERATE_CLIENT_RUNTIME="go"
melange migrate
```

**Boolean values:** Use `true`/`false` or `1`/`0` for boolean environment variables.

## Configuration Inheritance

Per-command settings can override top-level settings:

```yaml
# Top-level default
schema: schemas/schema.fga

generate:
  client:
    # Overrides top-level schema for generate command only
    schema: schemas/api-schema.fga
```

## Viewing Effective Configuration

Use `melange config show` to see the effective configuration after merging all
sources (and any selected environment). Passwords are masked by default; pass
`--reveal-secrets` to print them.

```bash
# Show effective configuration
melange config show

# Show with config file path and active environment
melange config show --source

# Show the resolved production profile
melange config show --env production
```

**Output:**
```
Config file: /path/to/project/melange.yaml
Environment: production

schema: schemas/schema.fga
database:
  url: postgres://prod-user:****@prod-db:5432/app
  host: ""
  port: 5432
  ...
```

## Security Considerations

- **Prefer environment variables for secrets** in production environments — reference them from an [environment profile](#environments) with `${VAR}` (e.g. `url: ${PROD_DATABASE_URL}`) so the config file stays credential-free
- Avoid committing `melange.yaml` files containing `database.password` to version control
- Consider using connection URLs with credentials stored in secure secret management systems
- For local development, discrete fields in a `.gitignore`d config file are acceptable
- `melange config show` masks passwords by default; `--reveal-secrets` is opt-in

## Example Configurations

### Local Development

```yaml
schema: schemas/schema.fga

database:
  url: postgres://localhost/myapp_dev

generate:
  client:
    runtime: go
    output: internal/authz
    package: authz
```

### CI/CD Pipeline

Use environment variables for credentials:

```bash
# .github/workflows/deploy.yml
env:
  MELANGE_DATABASE_URL: ${{ secrets.DATABASE_URL }}
```

With a minimal config file:

```yaml
schema: schemas/schema.fga

generate:
  client:
    runtime: go
    output: internal/authz
```

### Multi-Environment

Define [environment profiles](#environments) in one config and select them with
`--env`:

```yaml
# melange.yaml
schema: schemas/schema.fga

database:
  url: postgres://localhost:5432/myapp_dev   # base / default

default_environment: local

environments:
  local:
    database:
      url: postgres://localhost:5432/myapp_dev
  production:
    database:
      url: ${PROD_DATABASE_URL}   # from CI secrets, never committed
```

```bash
melange status --env production
melange migrate --env production
```

Alternatively, use separate config files (`melange --config melange.prod.yaml
migrate`) or override the connection with `MELANGE_DATABASE_URL` per invocation.
