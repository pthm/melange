---
title: Architecture
description: "Each stage of the Melange compiler pipeline, from parsing the OpenFGA DSL to installing generated SQL functions."
weight: 2
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

## Schema Analysis (`internal/sqlgen/analysis/`)

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

Cycle detection is a separate pass, `schema.DetectCycles`, which validate and migrate run before compiling. Cycles among relations that resolve on the same object (implied-by chains such as `define a: b` and `define b: a`, and intersection or exclusion operands) return `ErrCyclicSchema`. Parent relation cycles, within a type or across types, are valid: the generated functions detect revisits or walk breadth first.

## SQL Generation (`internal/sqlgen/`)

### SQL DSL

The `sqlgen` package provides a Go DSL for building SQL:

- `SqlFunction` for `LANGUAGE sql` functions.
- `PlpgsqlFunction` for `LANGUAGE plpgsql` functions.
- Expression types (`Lit`, `Param`, `FuncCall`, etc.) for building queries.

Functions that reference `melange_tuples` are normally `PlpgsqlFunction`: `LANGUAGE sql` validates table references at creation time, but `melange_tuples` may not exist during migration, while `LANGUAGE plpgsql` defers validation to call time. The `accessible_<type>_<relation>` functions are the exception: they are `LANGUAGE sql` without SET options so PostgreSQL inlines them into the calling query, and the migrator (and generated migration files) create them with `check_function_bodies = off`.

### Check Functions

One check function is generated per type+relation pair (e.g., `check_document_viewer`). The function signature is:

```sql
check_<type>_<relation>(p_subject_type TEXT, p_subject_id TEXT, p_object_id TEXT, p_visited TEXT[])
```

Note: `p_object_type` is not a parameter. The object type is baked into the function name and body.

`p_visited` tracks already-visited relations for cycle detection at runtime, and its length counts resolution steps the way OpenFGA does: a parent lookup or userset hop appends the caller's key, while computed relations and intersection or exclusion operands pass it on unchanged. If `array_length(p_visited, 1) >= 26`, the function raises `SQLSTATE 'M2002'` (resolution too complex), so a check resolves 25 steps as in OpenFGA. Every check function and `_nw` variant does this as its first statement, before the cycle check in recursive functions, so the limit holds whether the function is reached through the dispatcher or called directly from another generated function.

A generated function calls another relation's check function directly when its type and relation are known when the SQL is generated: a literal type, or a tupleset the query already restricts to one type. Otherwise (a tupleset with several allowed types) it calls `check_permission_internal`, which routes by the runtime type. Every check function is declared `COST 1000`, the dispatcher's cost, so a direct call in a `WHERE` clause keeps the place the planner gave the dispatcher call.

Each relation pattern produces different SQL. Direct assignment queries `melange_tuples` for an exact match. Implied relations call the implied-by function. Parent relations look up the parent object and call the parent's check function. Exclusions check the base relation and then negate the excluded relation.

### No-Wildcard Variants

A check function that has or reaches a wildcard also gets a `_nw` variant (`check_<type>_<relation>_nw`) that excludes `subject_id = '*'` matches. Userset resolution uses it to avoid granting wildcard access through indirect paths. For other relations the body would be identical, so no variant is emitted and `_nw` calls go to the base function.

### Dispatchers

`check_permission` is a `LANGUAGE sql` wrapper that calls `check_permission_internal(..., ARRAY[]::TEXT[])`. The internal dispatcher raises M2002 at the depth limit, then routes with an `IF` chain grouped by object type:

```sql
IF p_object_type = 'document' THEN
    IF p_relation = 'viewer' THEN RETURN check_document_viewer(p_subject_type, p_subject_id, p_object_id, p_visited); END IF;
    IF p_relation = 'owner' THEN RETURN check_document_owner(p_subject_type, p_subject_id, p_object_id, p_visited); END IF;
    RETURN 0;
END IF;
RETURN 0;
```

The internal dispatchers route every check function the exposure installs. The public ones answer for the exposed relations and raise SQLSTATE M2003 (through `check_permission_unexposed`) for any other relation the model defines. Dispatchers are always regenerated because they reference every installed relation.

### List Functions

`list_accessible_objects` and `list_accessible_subjects` dispatch to per-relation `list_<type>_<relation>_obj` and `list_<type>_<relation>_sub` functions, which follow the same pattern analysis as check functions but return sets. Pages are top-k queries: IDs after the cursor in order, `LIMIT p_limit + 1`, with `next_cursor` set when more remain. Recursive relations are listed by a breadth-first PL/pgSQL walk that expands each object once and raises M2002 (resolution too complex) at 25 levels instead of truncating. `accessible_<type>_<relation>` is the same query as an inlinable `LANGUAGE sql` function for filtering application queries; `accessible_objects` takes the relation and type as arguments; and `accessible_<type>_<relation>_by_<link>` returns parent IDs for relations whose every grant goes through one link to one parent type.

## Migration Orchestration (`pkg/migrator/`)

The migrator:

1. Validates the schema (`DetectCycles`).
2. Reads the last migration record and applies the guards: `--if-deployed-checksum` drift, a database migrated by a newer melange, and an exposure change on a run that records nothing.
3. Skips when the schema checksum, codegen version and exposure all match the record (phase 1).
4. Generates the SQL for the exposure and computes function checksums.
5. Records only, without re-applying, when the generated functions are identical to what is deployed (phase 2). `Migrator.Plan` predicts this decision without changing the database.
6. In one transaction under an advisory lock: re-checks the guards, refuses to drop a function that other database objects (for example a view or RLS policy) depend on (unless `--force`, which drops them with CASCADE), applies the functions, and drops the orphans.
7. Records the migration (schema checksum, codegen version, function inventory, model JSON and exposure).

## Key Design Decisions

**Why `LANGUAGE plpgsql`**: `LANGUAGE sql` validates table references at creation time. Since `melange_tuples` is a user-created view that may not exist during migration, `plpgsql` defers validation to call time. The inlinable `accessible_<type>_<relation>` functions are `LANGUAGE sql` and are created with `check_function_bodies = off` instead.

**Why specialized functions per relation**: each function has a predictable query plan. A generic function with dynamic parameters would require PostgreSQL to plan for all possible inputs, producing suboptimal plans.

**Why precomputed closure**: resolving role hierarchies at runtime requires recursive graph traversal with cycle detection. By precomputing the closure, the generated SQL inlines all transitive implications into a single `IN (...)` clause.

**Why `visited TEXT[]` parameter**: even with precomputed closures, certain patterns (parent relationships, usersets) can create cycles at runtime if the data forms a loop. The visited array detects cycles, and its length counts the steps taken so check can raise M2002 at the same depth as OpenFGA (see `checkDepthLimit`).

## Next Steps

- [Adding Features](../adding-features/): step-by-step guide for new OpenFGA feature support
- [Project Structure](../project-structure/): package layout and key files
- [How It Works](../../concepts/how-it-works/): user-facing explanation of the compiler model
