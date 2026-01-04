---
title: Documentation
cascade:
  type: docs
---

Melange is a pure PostgreSQL authorization library implementing OpenFGA/Zanzibar concepts. It provides fine-grained permission checking that queries your existing domain tables directly - no separate tuple storage or synchronization required.

## Key Features

- **Zero Tuple Sync** - Permissions derived from views over your existing tables
- **Transaction Aware** - Permission checks see uncommitted changes within transactions
- **Pure PostgreSQL** - Everything runs as SQL functions with zero external dependencies
- **OpenFGA Compatible** - Use familiar OpenFGA DSL syntax for authorization models

## Documentation

{{< cards >}}
  {{< card link="getting-started" title="Getting Started" subtitle="Install Melange, set up your first schema, and run permission checks" icon="play" >}}
  {{< card link="tuples-view" title="Tuples View" subtitle="Map your domain tables to authorization tuples" icon="database" >}}
  {{< card link="cli" title="CLI Reference" subtitle="Commands for migrations, code generation, and validation" icon="terminal" >}}
  {{< card link="modelling" title="Authorization Modelling" subtitle="Write OpenFGA schemas for your permission model" icon="document-text" >}}
{{< /cards >}}

## Checker API

{{< cards >}}
  {{< card link="checking-permissions" title="Checking Permissions" subtitle="Use the Checker API to validate access" icon="shield-check" >}}
  {{< card link="listing-objects" title="Listing Objects" subtitle="Find all objects a subject can access" icon="collection" >}}
  {{< card link="listing-subjects" title="Listing Subjects" subtitle="Find all subjects with access to an object" icon="users" >}}
{{< /cards >}}

## Reference

{{< cards >}}
  {{< card link="openfga-compatibility" title="OpenFGA Compatibility" subtitle="Feature support table and migration guidance" icon="badge-check" >}}
{{< /cards >}}

## Contributing

{{< cards >}}
  {{< card link="contributing" title="Contributing Guide" subtitle="Run tests, benchmarks, and understand the codebase" icon="code" >}}
{{< /cards >}}
