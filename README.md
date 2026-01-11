# Melange

[![Go Reference](https://pkg.go.dev/badge/github.com/pthm/melange.svg)](https://pkg.go.dev/github.com/pthm/melange)
[![Go Report Card](https://goreportcard.com/badge/github.com/pthm/melange)](https://goreportcard.com/report/github.com/pthm/melange)

<img align="right" width="300" src="assets/mascot.png">

**Fine-grained authorization for PostgreSQL applications.**

Melange brings [Zanzibar](https://research.google/pubs/pub48190/)-style relationship-based access control directly into your PostgreSQL database. Define authorization schemas using the [OpenFGA DSL](https://openfga.dev/docs/configuration-language), and Melange runs permission checks as efficient SQL queries against your existing data.

## Why Melange?

Traditional authorization systems require syncing your application data to a separate service. Melange takes a different approach: **permissions are derived from views over your existing tables**. This means:

- **Always in sync** — No replication lag or eventual consistency
- **Transaction-aware** — Permission checks see uncommitted changes
- **Zero runtime deps** — Core library is pure Go stdlib
- **Single query** — No recursive lookups at runtime

Inspired by [OpenFGA](https://openfga.dev) and built on ideas from [pgFGA](https://github.com/rover-app/pgfga).

---

> **📚 Full Documentation**
>
> Visit **[melange.pthm.dev](https://melange.pthm.dev)** for comprehensive guides, API reference, and examples.

---

## Installation

### CLI

```bash
go install github.com/pthm/melange/cmd/melange@latest
```

### Go Runtime

```bash
go get github.com/pthm/melange/melange
```

The runtime module has zero external dependencies (Go stdlib only).

## Quick Start

### 1. Define Your Schema

Create a schema file (`schema.fga`) using the OpenFGA DSL:

```
model
  schema 1.1

type user

type repository
  relations
    define owner: [user]
    define reader: [user] or owner
    define can_read: reader
```

### 2. Generate Type-Safe Code

```bash
melange generate client --runtime go --schema schema.fga --output ./authz/
```

This generates constants and constructors for your schema:

```go
package authz

// Generated type constants
const TypeUser melange.ObjectType = "user"
const TypeRepository melange.ObjectType = "repository"

// Generated relation constants
const RelOwner melange.Relation = "owner"
const RelCanRead melange.Relation = "can_read"

// Generated constructors
func User(id string) melange.Object { ... }
func Repository(id string) melange.Object { ... }
```

### 3. Check Permissions

```go
import (
    "github.com/pthm/melange/melange"
    "yourapp/authz"
)

checker := melange.NewChecker(db)

// Check if user can read the repository
decision, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("my-repo"),
)
if !decision.Allowed {
    return ErrForbidden
}
```

## CLI Reference

```
melange - PostgreSQL Fine-Grained Authorization

Commands:
  generate client  Generate type-safe client code from schema
  migrate          Apply schema to database
  validate         Validate schema syntax
  status           Show current schema status
  doctor           Run health checks on authorization infrastructure
```

### Generate Client Code

```bash
# Generate Go code
melange generate client --runtime go --schema schema.fga --output ./authz/

# With custom package name
melange generate client --runtime go --schema schema.fga --output ./authz/ --package myauthz

# With int64 IDs instead of strings
melange generate client --runtime go --schema schema.fga --output ./authz/ --id-type int64

# Only generate permission relations (can_*)
melange generate client --runtime go --schema schema.fga --output ./authz/ --filter can_
```

Supported runtimes: `go` (TypeScript coming soon)

### Apply Schema to Database

```bash
# Apply schema
melange migrate --db postgres://localhost/mydb --schemas-dir ./schemas/

# Dry run (show SQL without applying)
melange migrate --db postgres://localhost/mydb --schemas-dir ./schemas/ --dry-run

# Force re-apply even if unchanged
melange migrate --db postgres://localhost/mydb --schemas-dir ./schemas/ --force
```

### Validate Schema

```bash
melange validate --schema schema.fga
```

### Check Status

```bash
melange status --db postgres://localhost/mydb
```

### Health Check

```bash
melange doctor --db postgres://localhost/mydb --verbose
```

---

## Multi-Language Support

Melange supports generating client code for multiple languages:

| Language   | Runtime Package                        | CLI Flag              | Status      |
|------------|----------------------------------------|-----------------------|-------------|
| Go         | `github.com/pthm/melange/melange`      | `--runtime go`        | Implemented |
| TypeScript | `@pthm/melange`                        | `--runtime typescript`| Planned     |

See [`clients/`](clients/) for language-specific runtime implementations.

---

## Contributing

Contributions are welcome! Here's how to get started:

1. **Fork the repository** and clone locally
2. **Create a branch** for your changes
3. **Run tests** with `just test`
4. **Submit a pull request** with a clear description

Please ensure your code:

- Passes all existing tests
- Includes tests for new functionality
- Follows the existing code style

For bug reports and feature requests, please [open an issue](https://github.com/pthm/melange/issues).

---

## Resources

- **[Documentation](https://melange.pthm.dev)** — Guides, API reference, and examples
- **[OpenFGA](https://openfga.dev)** — The authorization model Melange implements
- **[Zanzibar Paper](https://research.google/pubs/pub48190/)** — Google's original authorization system
- **[pgFGA](https://github.com/rover-app/pgfga)** — PostgreSQL FGA implementation that inspired this project

---

## License

MIT License — see [LICENSE](LICENSE) for details.
