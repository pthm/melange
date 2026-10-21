---
title: Documentation
description: Melange compiles OpenFGA authorization models into PostgreSQL functions, so permission checks run inside your existing database.
cascade:
  type: docs
---

Melange is an **OpenFGA-to-PostgreSQL compiler**. It takes your authorization model (written in OpenFGA DSL) and compiles it into specialized PostgreSQL functions that run permission checks directly inside your database.

## What is an Authorization Compiler?

The pattern is similar to [Protocol Buffers](https://protobuf.dev/) or [GraphQL Code Generator](https://the-guild.dev/graphql/codegen): define a schema, run a compiler, get code tailored to your definitions.

Melange applies this pattern to authorization:

```mermaid
flowchart LR
    schema["schema.fga<br/>(OpenFGA DSL)"] --> melange["melange<br/>migrate"]
    melange --> funcs["PostgreSQL Functions<br/>(check_permission, etc)"]
```

Instead of a generic runtime that interprets your model at query time, Melange generates a SQL function for each relation in your schema. The authorization logic runs as PostgreSQL code: transaction-aware, query-planned, and with zero network hops.

## How It Works

1. **Define your model** - Write an OpenFGA schema (`.fga` file) describing types, relations, and permission rules
2. **Compile to SQL** - Run `melange migrate` to generate and install specialized PostgreSQL functions
3. **Query permissions** - Call the generated functions from any language or directly in SQL

Read more about Melange's architecture and how it works [here](concepts/how-it-works).

## Why Compile to PostgreSQL?

| Traditional FGA | Melange |
|----------------|---------|
| Separate authorization service | Authorization lives in your database |
| Network round-trip for every check | Single SQL query |
| Eventual consistency with your data | Transaction-aware, always consistent |
| Generic graph traversal at runtime | Specialized functions per relation |
| Sync tuples to external store | Query your existing tables directly |

## Key Features

- **Per-Relation Code Generation** - Each relation gets its own check function, not a generic interpreter
- **Works with Your Tables** - Permissions derived from a view over your existing data (no tuple sync)
- **Transaction Aware** - Permission checks see uncommitted changes within the same transaction
- **Language Agnostic** - Generated SQL functions callable from Go, Python, Node.js, or any PostgreSQL client
- **OpenFGA Compatible** - Use familiar OpenFGA DSL syntax for authorization models

## Getting Started

{{< cards >}}
  {{< card link="getting-started/installation" title="Installation" subtitle="Install the Melange CLI on macOS, Linux, or Windows" icon="download" >}}
  {{< card link="getting-started/quick-start" title="Quick Start" subtitle="From zero to a working permission check" icon="play" >}}
  {{< card link="getting-started/project-setup" title="Project Setup" subtitle="Configure the init wizard, starter templates, and code generation" icon="cog" >}}
{{< /cards >}}

## Concepts

Architecture and design.

{{< cards >}}
  {{< card link="concepts/components" title="Key Components" subtitle="CLI, schema, tuples view, and runtime libraries" icon="template" >}}
  {{< card link="concepts/how-it-works" title="How It Works" subtitle="Specialized SQL generation, precomputed closures, and permission checks that stay flat as data grows" icon="cog" >}}
  {{< card link="concepts/modelling" title="Authorization Modelling" subtitle="Write OpenFGA schemas to define your permission model" icon="document-text" >}}
  {{< card link="concepts/tuples-view" title="Tuples View" subtitle="Map your domain tables to authorization tuples" icon="database" >}}
{{< /cards >}}

## Guides

Using Melange's client libraries, middleware patterns, and operational workflows. For direct SQL access, see the [SQL API reference](reference/sql-api).

{{< cards >}}
  {{< card link="guides/checking-permissions" title="Checking Permissions" subtitle="Validate access using the Checker API" icon="shield-check" >}}
  {{< card link="guides/listing-objects" title="Listing Objects" subtitle="Find all objects a subject can access" icon="collection" >}}
  {{< card link="guides/listing-subjects" title="Listing Subjects" subtitle="Find all subjects with access to an object" icon="users" >}}
  {{< card link="guides/caching" title="Caching" subtitle="In-memory, request-scoped, and custom cache strategies" icon="lightning-bolt" >}}
  {{< card link="guides/coding-agents" title="Using Coding Agents" subtitle="Docs for agents and project instructions for your schema" icon="sparkles" >}}
  {{< card link="guides/troubleshooting" title="Troubleshooting" subtitle="Diagnose common issues with melange doctor" icon="search" >}}
{{< /cards >}}

## Reference

Technical reference documentation.

{{< cards >}}
  {{< card link="reference/configuration" title="Configuration" subtitle="Config files, environment variables, and precedence" icon="cog" >}}
  {{< card link="reference/cli" title="CLI Reference" subtitle="Commands for migrations, code generation, and validation" icon="terminal" >}}
  {{< card link="reference/sql-api" title="SQL API" subtitle="Generated SQL functions for permission checks" icon="database" >}}
  {{< card link="reference/go-api" title="Go API" subtitle="Runtime types, Checker, bulk checks, caching, and decision overrides" icon="code" >}}
  {{< card link="reference/generated-code" title="Generated Code" subtitle="What melange generate client produces" icon="document-text" >}}
  {{< card link="reference/errors" title="Errors" subtitle="Sentinel errors, validation codes, and error helpers" icon="exclamation-circle" >}}
  {{< card link="reference/openfga-compatibility" title="OpenFGA Compatibility" subtitle="Feature support table and migration guidance" icon="badge-check" >}}
  {{< card link="reference/performance" title="Performance" subtitle="Benchmark results, optimization strategies, and scaling guidance" icon="lightning-bolt" >}}
{{< /cards >}}

## Contributing

{{< cards >}}
  {{< card link="contributing" title="Contributing Guide" subtitle="Run tests, benchmarks, and understand the codebase" icon="code" >}}
{{< /cards >}}
