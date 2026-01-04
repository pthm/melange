---
title: Documentation
cascade:
  type: docs
---

Welcome to the Melange documentation. Melange provides zero-copy authorization for PostgreSQL using OpenFGA-compatible schemas.

## Getting Started

Melange works by compiling OpenFGA authorization models into PostgreSQL functions that query your existing domain tables directly. This means:

- **No tuple synchronization** - permissions always reflect current data
- **Transaction awareness** - uncommitted changes are visible to permission checks
- **Zero external dependencies** - everything runs inside PostgreSQL

## Quick Example

```go
import "github.com/pthm/melange"

// Parse your OpenFGA model
schema, err := melange.ParseSchema(modelDSL)
if err != nil {
    return err
}

// Apply migrations to your database
migrator := melange.NewMigrator(db, schema)
if err := migrator.Up(ctx); err != nil {
    return err
}

// Check permissions
allowed, err := melange.CheckPermission(ctx, db, schema,
    "user", "alice",
    "can_edit",
    "document", "123",
)
```

## Key Concepts

{{< cards >}}
  {{< card link="melange-tuples" title="The melange_tuples View" subtitle="How to map your domain tables to authorization tuples" >}}
{{< /cards >}}
