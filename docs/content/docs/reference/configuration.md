---
title: Configuration
weight: 2
---

Melange supports configuration via YAML files, environment variables, and command-line flags with the following precedence (highest to lowest):

1. **Command-line flags**
2. **Environment variables** (`MELANGE_*` prefix)
3. **Configuration file** (`melange.yaml` or `melange.yml`)
4. **Built-in defaults**

## Configuration File

Melange automatically discovers a configuration file by:

1. Looking for `melange.yaml` or `melange.yml` in the current directory
2. Walking up parent directories until a `.git` directory is found (repository boundary)
3. Stopping after 25 parent levels if no repository boundary is found

You can override auto-discovery with `--config`:

```bash
melange --config /path/to/custom-config.yaml migrate
```

## File Format

Create a `melange.yaml` in your project root:

```yaml
# Path to OpenFGA schema file (used by all commands)
schema: schemas/schema.fga

# Database connection (used by migrate, status, doctor)
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

# Code generation settings
generate:
  client:
    runtime: go
    schema: schemas/schema.fga  # Overrides top-level schema
    output: internal/authz
    package: authz
    filter: can_
    id_type: string

# Migration settings
migrate:
  dry_run: false
  force: false

# Doctor command settings
doctor:
  verbose: false
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
| `runtime` | string | - | Target runtime: `go`, `typescript`, `python` |
| `schema` | string | (top-level `schema`) | Path to schema file |
| `output` | string | - | Output directory for generated code |
| `package` | string | `authz` | Package/module name |
| `filter` | string | - | Relation prefix filter (e.g., `can_`) |
| `id_type` | string | `string` | ID type for constructors |

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
| `MELANGE_GENERATE_CLIENT_RUNTIME` | `generate.client.runtime` |
| `MELANGE_GENERATE_CLIENT_SCHEMA` | `generate.client.schema` |
| `MELANGE_GENERATE_CLIENT_OUTPUT` | `generate.client.output` |
| `MELANGE_GENERATE_CLIENT_PACKAGE` | `generate.client.package` |
| `MELANGE_GENERATE_CLIENT_FILTER` | `generate.client.filter` |
| `MELANGE_GENERATE_CLIENT_ID_TYPE` | `generate.client.id_type` |
| `MELANGE_MIGRATE_DRY_RUN` | `migrate.dry_run` |
| `MELANGE_MIGRATE_FORCE` | `migrate.force` |
| `MELANGE_DOCTOR_VERBOSE` | `doctor.verbose` |

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

Use `melange config show` to see the effective configuration after merging all sources:

```bash
# Show effective configuration
melange config show

# Show with config file path
melange config show --source
```

**Output:**
```
Config file: /path/to/project/melange.yaml

schema: schemas/schema.fga
database:
  url: postgres://localhost/mydb
  host: ""
  port: 5432
  ...
```

## Security Considerations

- **Prefer environment variables for secrets** in production environments
- Avoid committing `melange.yaml` files containing `database.password` to version control
- Consider using connection URLs with credentials stored in secure secret management systems
- For local development, discrete fields in a `.gitignore`d config file are acceptable

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

Use different config files per environment:

```bash
# Development
melange --config melange.dev.yaml migrate

# Production
melange --config melange.prod.yaml migrate
```

Or use environment variable overrides with a shared base config:

```yaml
# melange.yaml (shared)
schema: schemas/schema.fga

generate:
  client:
    runtime: go
    output: internal/authz
```

```bash
# Override database per environment
MELANGE_DATABASE_URL="postgres://prod-host/myapp" melange migrate
```
