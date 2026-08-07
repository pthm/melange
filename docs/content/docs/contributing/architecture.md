---
title: Architecture
weight: 4
---

Melange's compilation pipeline transforms an OpenFGA schema into specialized PostgreSQL functions. This page describes each stage.

## Pipeline Overview

```
Parse → Analyze → Closure → Generate → Install
```

1. **Parse**: OpenFGA DSL string → `[]schema.TypeDefinition`
2. **Analyze**: detect relation patterns (Direct, Implied, Union, TTU, Exclusion, Intersection, Wildcard, Userset)
3. **Closure**: compute transitive closure for role hierarchies
4. **Generate**: produce SQL functions using the SQL DSL
5. **Install**: apply functions to PostgreSQL in a single transaction

## Parsing (`pkg/parser/`)

The parser wraps the official OpenFGA language parser. It converts the OpenFGA AST into `schema.TypeDefinition` structs that the rest of the pipeline consumes.

`ParseSchema(path)` reads a `.fga` file. `ParseSchemaString(dsl)` parses a string. Both return `[]schema.TypeDefinition`.

This is the only package that imports the OpenFGA parser dependency.

## Schema Analysis (`pkg/schema/`)

Each relation is analyzed to detect its pattern:

| Pattern | Detection | Example |
|---------|-----------|---------|
| Direct | `subject_type` set | `define viewer: [user]` |
| Implied | `implied_by` set | `define viewer: owner` |
| Parent (TTU) | `parent_relation` set | `define viewer: viewer from org` |
| Exclusion | `excluded_relation` set | `define viewer: writer but not blocked` |
| Userset | `subject_type` + `subject_relation` set | `define viewer: [group#member]` |
| Intersection | `rule_group_id` + `rule_group_mode` set | `define viewer: writer and editor` |
| Wildcard | detected from type restrictions | `define public: [user:*]` |
| Union | multiple rules for same relation | `define viewer: [user] or owner` |

The analysis result determines which SQL generation template is used for each relation.

## Closure Computation (`pkg/schema/`)

Role hierarchies like `owner > admin > member` create transitive implications: if `alice` is an `owner`, she also has `admin` and `member`. Rather than resolving this at runtime through graph traversal, Melange precomputes the transitive closure at compile time.

For each relation, the closure includes all relations that imply it (directly or transitively). This is inlined into the generated SQL, so a check for `member` also checks for `admin` and `owner` in a single query.

The closure computation also detects cycles. Cyclic implied-by chains (e.g., `define a: b` and `define b: a`) return `ErrCyclicSchema`.

## SQL Generation (`internal/sqlgen/`)

### SQL DSL

The `sqlgen` package provides a Go DSL for building SQL:

- `SqlFunction` for `LANGUAGE sql` functions.
- `PlpgsqlFunction` for `LANGUAGE plpgsql` functions.
- Expression types (`Lit`, `Param`, `FuncCall`, etc.) for building queries.

Functions that reference `melange_tuples` must use `PlpgsqlFunction` because `LANGUAGE sql` validates table references at function creation time, but `melange_tuples` doesn't exist during migration. `LANGUAGE plpgsql` defers validation to call time.

### Check Functions

One check function is generated per type+relation pair (e.g., `check_document_viewer`). The function signature is:

```sql
check_<type>_<relation>(p_subject_type TEXT, p_subject_id TEXT, p_object_id TEXT, p_visited TEXT[])
```

Note: `p_object_type` is not a parameter. The object type is baked into the function name and body.

`p_visited` tracks already-visited relations for cycle detection at runtime. If `array_length(p_visited, 1) >= 25`, the function raises `SQLSTATE 'M2002'` (resolution too complex).

Each relation pattern produces different SQL. Direct assignment queries `melange_tuples` for an exact match. Implied relations call the implied-by function. Parent relations look up the parent object and call the parent's check function. Exclusions check the base relation and then negate the excluded relation.

### No-Wildcard Variants

Each check function also has a `_no_wildcard` variant that excludes `subject_id = '*'` matches. These are used internally by userset resolution to avoid granting wildcard access through indirect paths.

### Dispatchers

The dispatcher function `check_permission` routes calls to the correct specialized function using a `CASE` expression:

```sql
CASE
    WHEN (p_object_type = 'document' AND p_relation = 'viewer')
        THEN check_document_viewer(...)
    WHEN (p_object_type = 'document' AND p_relation = 'owner')
        THEN check_document_owner(...)
    ELSE 0
END
```

Dispatchers are always regenerated because they reference every relation.

### List Functions

`list_accessible_objects` and `list_accessible_subjects` use recursive CTEs to enumerate accessible objects/subjects. They follow the same pattern analysis as check functions but produce set-returning queries.

## Migration Orchestration (`pkg/migrator/`)

The migrator:

1. Parses the schema.
2. Generates all SQL functions.
3. Computes SHA-256 checksums for change detection.
4. Compares against the previous migration record (if using `--db` comparison).
5. Drops orphaned functions from removed relations.
6. Applies all changes in a single transaction.
7. Records the migration (schema checksum, codegen version, function inventory).

## Key Design Decisions

**Why `LANGUAGE plpgsql`**: `LANGUAGE sql` validates table references at creation time. Since `melange_tuples` is a user-created view that may not exist during migration, `plpgsql` defers validation to call time.

**Why specialized functions per relation**: each function has a predictable query plan. A generic function with dynamic parameters would require PostgreSQL to plan for all possible inputs, producing suboptimal plans.

**Why precomputed closure**: resolving role hierarchies at runtime requires recursive graph traversal with cycle detection. By precomputing the closure, the generated SQL inlines all transitive implications into a single `IN (...)` clause.

**Why `visited TEXT[]` parameter**: even with precomputed closures, certain patterns (parent relationships, usersets) can create cycles at runtime if the data forms a loop. The visited array prevents infinite recursion with a depth limit of 25.

## Next Steps

- [Adding Features](../adding-features/): step-by-step guide for new OpenFGA feature support
- [Project Structure](../project-structure/): package layout and key files
- [How It Works](../../concepts/how-it-works/): user-facing explanation of the compiler model
