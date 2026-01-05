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

### Library

```bash
# Core module (runtime, no external dependencies)
go get github.com/pthm/melange

# Tooling module (schema parsing, code generation)
go get github.com/pthm/melange/tooling
```

## Quick Example

```go
checker := melange.NewChecker(db)

// Check if user can read the repository
ok, err := checker.Check(ctx, user, "can_read", repo)
if !ok {
    return ErrForbidden
}
```

---

## Contributing

Contributions are welcome! Here's how to get started:

1. **Fork the repository** and clone locally
2. **Create a branch** for your changes
3. **Run tests** with `go test ./...`
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
