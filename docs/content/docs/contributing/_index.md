---
title: Contributing
weight: 20
---

This section covers how to contribute to Melange development, including running tests, benchmarks, and understanding the project structure.

## Getting Started

Clone the repository and install dependencies:

```bash
git clone https://github.com/pthm/melange.git
cd melange

# Install development tools
go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
```

## Building

```bash
# Build the CLI
go build -o bin/melange ./cmd/melange

# Build test utilities
go build -o bin/dumptest ./test/cmd/dumptest
```

## Running Tests

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run specific package
go test -v ./test/...
```

## Quick Links

{{< cards >}}
  {{< card link="testing" title="Testing" subtitle="Run the OpenFGA compatibility test suite" >}}
  {{< card link="benchmarking" title="Benchmarking" subtitle="Performance testing and profiling" >}}
  {{< card link="project-structure" title="Project Structure" subtitle="Understand the codebase layout" >}}
{{< /cards >}}

## Development Workflow

1. **Make changes** to the relevant files
2. **Run tests** to ensure nothing breaks: `go test ./...`
3. **Run the OpenFGA test suite** for compatibility: `just test-openfga`
4. **Run benchmarks** if performance-sensitive: `just bench-openfga`
5. **Submit a pull request**

## Key Commands

```bash
# Validate your schema changes
melange validate --schema schemas/schema.fga

# Run OpenFGA compatibility tests
just test-openfga

# Run benchmarks
just bench-openfga

# Inspect a specific test case
just dump-openfga <test_name>
```
