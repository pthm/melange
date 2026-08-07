---
title: Contributing
weight: 20
---

Melange compiles OpenFGA schemas into PostgreSQL functions through a multi-stage pipeline: parse the DSL, analyze relation patterns, compute transitive closures, generate specialized SQL, and install it. Understanding this pipeline is key to contributing effectively.

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
go build -o bin/melange .

# Build test utilities
go build -o bin/dumptest ./test/cmd/dumptest
```

## Running Tests

```bash
# Run all tests
go test ./...

# Run OpenFGA compatibility suite
just test-openfga

# Run benchmarks
just bench-openfga
```

## Quick Links

{{< cards >}}
  {{< card link="architecture" title="Architecture" subtitle="Compilation pipeline, SQL generation, and key design decisions" >}}
  {{< card link="adding-features" title="Adding Features" subtitle="Step-by-step guide for new OpenFGA feature support" >}}
  {{< card link="testing" title="Testing" subtitle="Run the OpenFGA compatibility test suite" >}}
  {{< card link="benchmarking" title="Benchmarking" subtitle="Performance testing and profiling" >}}
  {{< card link="project-structure" title="Project Structure" subtitle="Codebase layout, modules, and key files" >}}
{{< /cards >}}

## Development Workflow

1. **Make changes** to the relevant files.
2. **Run tests** to ensure nothing breaks: `just test-openfga`
3. **Run benchmarks** if performance-sensitive: `just bench-openfga`
4. **Submit a pull request**.

## Good First Issues

Look for issues labelled `good first issue` on [GitHub](https://github.com/pthm/melange/issues). These typically involve:

- Adding test cases for edge cases in existing patterns.
- Improving error messages or doctor check output.
- Documentation improvements.
- Small refactors with clear scope.
