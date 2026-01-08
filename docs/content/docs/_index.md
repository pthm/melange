---
title: Documentation
cascade:
  type: docs
---

Melange is a CLI tool that generates specialized PostgreSQL functions for authorization. You define your permission model using OpenFGA DSL, and Melange produces optimized SQL functions tailored to your exact schema - no generic runtime, just efficient, purpose-built queries.

## How It Works

1. **Define your model** - Write an OpenFGA schema describing your permission relationships
2. **Generate SQL** - Melange compiles your model into specialized PostgreSQL functions
3. **Query permissions** - Call the generated functions from any language or directly in SQL

## Key Features

- **Specialized Code Generation** - Each relation gets its own optimized check function
- **Works with Your Tables** - Permissions derived from a tuples view over your existing data
- **Transaction Aware** - Permission checks see uncommitted changes within transactions
- **Language Agnostic** - Use from Go, Python, Node.js, or any PostgreSQL client
- **OpenFGA Compatible** - Use familiar OpenFGA DSL syntax for authorization models

## Quick Start

{{< cards >}}
  {{< card link="getting-started" title="Getting Started" subtitle="Install Melange, set up your first schema, and run permission checks" icon="play" >}}
{{< /cards >}}

## Concepts

Understand the core architecture and design of Melange.

{{< cards >}}
  {{< card link="concepts/how-it-works" title="How It Works" subtitle="Specialized SQL generation, precomputed closures, and sub-millisecond checks" icon="cog" >}}
  {{< card link="concepts/modelling" title="Authorization Modelling" subtitle="Write OpenFGA schemas to define your permission model" icon="document-text" >}}
  {{< card link="concepts/tuples-view" title="Tuples View" subtitle="Map your domain tables to authorization tuples" icon="database" >}}
{{< /cards >}}

## Go Client Library

Melange includes a Go client library for convenient access to the generated SQL functions. For other languages, see the [SQL API reference](reference/sql-api).

{{< cards >}}
  {{< card link="guides/checking-permissions" title="Checking Permissions" subtitle="Use the Checker type to validate access" icon="shield-check" >}}
  {{< card link="guides/listing-objects" title="Listing Objects" subtitle="Find all objects a subject can access" icon="collection" >}}
  {{< card link="guides/listing-subjects" title="Listing Subjects" subtitle="Find all subjects with access to an object" icon="users" >}}
{{< /cards >}}

## Reference

Technical reference documentation.

{{< cards >}}
  {{< card link="reference/cli" title="CLI Reference" subtitle="Commands for migrations, code generation, and validation" icon="terminal" >}}
  {{< card link="reference/sql-api" title="SQL API" subtitle="Direct SQL functions for permission checks without the Go library" icon="database" >}}
  {{< card link="reference/performance" title="Performance" subtitle="Benchmark results, optimization strategies, and scaling guidance" icon="lightning-bolt" >}}
  {{< card link="reference/openfga-compatibility" title="OpenFGA Compatibility" subtitle="Feature support table and migration guidance" icon="badge-check" >}}
{{< /cards >}}

## Contributing

{{< cards >}}
  {{< card link="contributing" title="Contributing Guide" subtitle="Run tests, benchmarks, and understand the codebase" icon="code" >}}
{{< /cards >}}
