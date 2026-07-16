# sqlgen

SQL code generation for Melange authorization functions.

## Responsibility

Generates specialized PostgreSQL functions from OpenFGA schemas:

- **Check functions** - `check_<type>_<relation>()` for permission checks
- **List functions** - `list_<type>_<relation>_objects/subjects()` for enumeration
- **Dispatchers** - Route calls to specialized functions

This is the core compilation engine that transforms authorization models into executable SQL.

## Architecture Role

```
pkg/schema (types, closure)
       │
       └── internal/sqlgen (analysis, code generation)
               │
               └── pkg/migrator (applies generated SQL)
```

The package sits between schema analysis and database migration, producing all SQL artifacts.

## Code Generation Pipeline

```
Schema Types
     │
     ▼
┌─────────────┐
│  Analyze    │  Classify relations, detect patterns
└─────────────┘
     │
     ▼
┌─────────────┐
│   Plan      │  Build abstract query plan per relation
└─────────────┘
     │
     ▼
┌─────────────┐
│  Blocks     │  Convert plans to SQL query blocks
└─────────────┘
     │
     ▼
┌─────────────┐
│  Render     │  Emit final PL/pgSQL functions
└─────────────┘
```

## Key Components

### Subpackages

The package is organized into focused subpackages:

| Package | Purpose |
|---------|---------|
| `sqldsl` | Generic SQL builders (SelectStmt, Expr, TableExpr, operators) |
| `analysis` | Relation analysis and strategy selection |
| `tuples` | Tuple-table specific query builders |
| `plpgsql` | PL/pgSQL function builder types |
| `inline` | Inline SQL model data and typed VALUES rows |

The root `sqlgen` package re-exports types for backward compatibility.

### SQL DSL (`sqldsl/`)

Domain-specific types for building SQL:
- `SelectStmt` - SELECT query builder
- `Expr` - SQL expressions (columns, literals, operators)
- `TableExpr` - Table references (base tables, subqueries, VALUES)

### Analysis (`analysis/`)

Classifies relations by their patterns:
- Direct assignment, implied relations, wildcards
- Parent inheritance (tuple-to-userset)
- Exclusions, intersections, usersets

### Check Generation (`check_plan.go`, `check_blocks.go`, `check_render.go`)

Three-layer architecture for check functions:
1. **Plan** - Abstract representation of permission logic
2. **Blocks** - SQL query blocks with comments
3. **Render** - Final PL/pgSQL function output

### List Generation (`list_plan.go`, `list_blocks.go`, `list_render.go`)

Parallel architecture for list functions with additional complexity for:
- Recursive object enumeration
- Subject enumeration with wildcard handling
- Cursor-based pagination

### Inline Data (`inline/`)

Precomputes closure and userset data as SQL VALUES tables, eliminating runtime table lookups.

## Design Principles

1. **Pattern-specific code** - Each authorization pattern gets optimized SQL
2. **Compile-time closure** - Role hierarchies resolved before runtime
3. **Inline data** - No runtime schema lookups, all data embedded in functions
4. **Predictable plans** - PostgreSQL can optimize each specialized function independently
