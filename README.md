# Melange

[![Go Reference](https://pkg.go.dev/badge/github.com/pthm/melange.svg)](https://pkg.go.dev/github.com/pthm/melange)
[![Go Report Card](https://goreportcard.com/badge/github.com/pthm/melange)](https://goreportcard.com/report/github.com/pthm/melange)

<img align="right" width="300" src="assets/mascot.png">

**An OpenFGA-to-PostgreSQL authorization compiler.**

Melange compiles [OpenFGA](https://openfga.dev) authorization schemas into specialized PL/pgSQL functions that run directly in your PostgreSQL database. Like [Protocol Buffers](https://protobuf.dev/) compiles `.proto` files into serialization code, Melange compiles `.fga` files into optimized SQL functions for [Zanzibar](https://research.google/pubs/pub48190/)-style relationship-based access control.

The generated functions query a `melange_tuples` view you define over your existing domain tables—no separate tuple storage or synchronization required.

## Why Melange?

Traditional authorization systems require syncing your application data to a separate service. Melange takes a different approach: **it's a compiler, not a runtime service**.

### How it works

**Compile time** — When you run `melange migrate`, the compiler:
- Parses your OpenFGA schema
- Analyzes relation patterns (direct, union, exclusion, etc.)
- Computes transitive closures for role hierarchies
- Generates specialized SQL functions for each relation
- Installs the functions into PostgreSQL

**Runtime** — Permission checks are simple SQL function calls:
- `check_permission()` executes the generated functions
- Functions query a `melange_tuples` view you define over your domain tables
- PostgreSQL's query planner optimizes the specialized functions

This compilation model gives you:

- **Always in sync** — Permissions query your tables directly, no replication lag
- **Transaction-aware** — Permission checks see uncommitted changes in the same transaction
- **Language-agnostic** — Use from any language that can call SQL (Go, TypeScript, Python, Ruby, etc.)
- **Optional runtime libraries** — Convenience clients for Go and TypeScript, or use raw SQL
- **Single query** — Role hierarchies resolved at compile time, no recursive lookups at runtime

Inspired by [OpenFGA](https://openfga.dev) and built on ideas from [pgFGA](https://github.com/rover-app/pgfga).

---

> **📚 Full Documentation**
>
> Visit **[melange.sh](https://melange.sh)** for comprehensive guides, API reference, and examples.

---

## Installation

### CLI

**Homebrew (macOS and Linux):**

```bash
brew install pthm/tap/melange
```

**Go install:**

```bash
go install github.com/pthm/melange/cmd/melange@latest
```

**Pre-built binaries:**
Download from [GitHub Releases](https://github.com/pthm/melange/releases) (macOS binaries are code-signed)

**Updating:**

```bash
# Homebrew
brew upgrade melange

# Go install
go install github.com/pthm/melange/cmd/melange@latest
```

Melange automatically checks for updates and notifies you when a new version is available. Use `--no-update-check` to disable.

### Optional: Go Runtime Library

If you want to use the Go convenience library instead of raw SQL:

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

### 2. Compile Schema into SQL Functions

Run the migration to generate specialized PL/pgSQL functions:

```bash
melange migrate --db postgres://localhost/mydb --schemas-dir ./schemas/
```

This generates optimized SQL functions like `check_permission()`, `list_objects()`, and specialized check functions for each relation.

### 3. Define Your Tuples View

Create a `melange_tuples` view that exposes your authorization data:

```sql
CREATE VIEW melange_tuples AS
SELECT
  'user' AS subject_type,
  user_id::text AS subject_id,
  'owner' AS relation,
  'repository' AS object_type,
  repo_id::text AS object_id
FROM repository_owners
UNION ALL
SELECT 'user', user_id::text, 'reader', 'repository', repo_id::text
FROM repository_readers;
```

### 4. Check Permissions

**With Go runtime (optional):**

```go
import "github.com/pthm/melange/melange"

checker := melange.NewChecker(db)
decision, err := checker.Check(ctx,
    melange.Object{Type: "user", ID: "alice"},
    "can_read",
    melange.Object{Type: "repository", ID: "my-repo"},
)
if !decision.Allowed {
    return ErrForbidden
}
```

**Or use raw SQL from any language:**

```sql
SELECT check_permission(
  'user', 'alice',
  'can_read',
  'repository', 'my-repo'
);
-- Returns: true/false
```

### 5. (Optional) Generate Type-Safe Client Code

For better type safety, generate constants and constructors:

```bash
melange generate client --runtime go --schema schema.fga --output ./authz/
```

```go
import "yourapp/authz"

checker := melange.NewChecker(db)
decision, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("my-repo"),
)
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

## Using from Any Language

Melange generates standard PostgreSQL functions, so you can use it from **any language** that can execute SQL:

```python
# Python
cursor.execute(
    "SELECT check_permission(%s, %s, %s, %s, %s)",
    ('user', 'alice', 'can_read', 'repository', 'my-repo')
)
```

```ruby
# Ruby
DB.fetch(
  "SELECT check_permission(?, ?, ?, ?, ?)",
  'user', 'alice', 'can_read', 'repository', 'my-repo'
).first
```

```typescript
// TypeScript (with pg or any SQL client)
const result = await db.query(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', 'alice', 'can_read', 'repository', 'my-repo']
);
```

### Optional Runtime Libraries

For convenience, Melange provides type-safe runtime clients:

| Language   | Runtime Package                   | CLI Flag               | Status      |
| ---------- | --------------------------------- | ---------------------- | ----------- |
| Go         | `github.com/pthm/melange/melange` | `--runtime go`         | Implemented |
| TypeScript | `@pthm/melange`                   | `--runtime typescript` | Planned     |

These libraries provide a nicer API but are completely optional. See [`clients/`](clients/) for language-specific implementations.

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

- **[Documentation](https://melange.sh)** — Guides, API reference, and examples
- **[OpenFGA](https://openfga.dev)** — The authorization model Melange implements
- **[Zanzibar Paper](https://research.google/pubs/pub48190/)** — Google's original authorization system
- **[pgFGA](https://github.com/rover-app/pgfga)** — PostgreSQL FGA implementation that inspired this project

---

## License

MIT License — see [LICENSE](LICENSE) for details.
