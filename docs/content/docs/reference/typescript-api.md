---
title: TypeScript API
weight: 4
---

Reference for the `@pthm/melange` TypeScript runtime client. Mirrors the [Go API](../go-api/) with TypeScript idioms. Ships with the type-safe generated client from `melange generate client --runtime typescript`.

## Checker

```typescript
import { Checker } from '@pthm/melange';
import { Pool } from 'pg';

const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const checker = new Checker(pool);
```

### Permission Checks

```typescript
class Checker {
  check(subject: MelangeObject, relation: Relation, object: MelangeObject): Promise<boolean>
  checkWithContextualTuples(subject: MelangeObject, relation: Relation, object: MelangeObject, tuples: ContextualTuple[]): Promise<boolean>
}
```

`check` returns `true` if the subject has the relation on the object. `checkWithContextualTuples` mirrors the Go equivalent — temporary tuples for the call only; requires a client that supports transactions.

### List Operations

```typescript
class Checker {
  listObjects(subject: MelangeObject, relation: Relation, objectType: ObjectType, page?: PageOptions): Promise<{ ids: string[]; nextCursor: string | null }>
  listSubjects(object: MelangeObject, relation: Relation, subjectType: ObjectType, page?: PageOptions): Promise<{ ids: string[]; nextCursor: string | null }>
}
```

Cursor-paginated. Pass `page.after` (the previous page's `nextCursor`) to continue.

### Explain

```typescript
class Checker {
  explain(subject: MelangeObject, relation: Relation, object: MelangeObject, options?: ExplainOptions): Promise<Trace>
}

interface ExplainOptions {
  maxNodes?: number;  // 0 or absent defers to session GUC / built-in default
}
```

Returns the resolution tree for a check. See the [Explaining Decisions guide](../../guides/explaining-decisions/) for the trace structure.

```typescript
const trace = await checker.explain(
  { type: 'user', id: 'alice' },
  'viewer',
  { type: 'document', id: '1' },
);

if (trace.result === true) {
  console.log('allowed via', trace.root.type);
}
```

`Trace`, `Node`, `NodeType`, `TupleRef`, and `SubjectRef` type mirrors live in `@pthm/melange` (source: `clients/typescript/src/trace.ts`). JSON tags are snake_case to match the SQL columns.

### Expand

```typescript
class Checker {
  expand(object: MelangeObject, relation: Relation, options?: ExpandOptions): Promise<UsersetTree>
  expandRecursive(object: MelangeObject, relation: Relation, options?: ExpandOptions): Promise<string[]>
}

interface ExpandOptions {
  subjectType?: ObjectType;  // Melange extension: narrow Leaf.Users to one type
  maxLeaf?: number;          // Melange extension: cap Leaf.Users; 0 = unbounded (matches OpenFGA)
}

function flattenUsers(tree: UsersetTree): string[]
```

`expand` returns the OpenFGA-shaped `UsersetTree`, shallow by default (computed rewrites surface as `Leaf.Computed` pointers, TTUs as `Leaf.TupleToUserset` pointers). `expandRecursive` walks the pointer chains and returns the flat, deduplicated user list. `flattenUsers` collects every `Leaf.Users` entry from an already-returned tree without issuing additional queries.

See the [Expanding Permissions guide](../../guides/expanding-permissions/) for the tree structure and worked examples.

```typescript
const tree = await checker.expand(
  { type: 'document', id: '1' },
  'viewer',
);
const users = flattenUsers(tree);
// users = ['user:alice', 'user:bob', 'group:eng#member', 'user:*']
```

`UsersetTree`, `UsersetTreeNode`, `Leaf`, `Users`, `Computed`, `TupleToUserset`, `Difference`, and `Nodes` type mirrors live in `@pthm/melange` (source: `clients/typescript/src/expand.ts`). Field names match OpenFGA's `openfgav1.UsersetTree` proto so existing OpenFGA tooling deserialises the JSON without adapters.

## Caching

```typescript
import { Checker, MemoryCache } from '@pthm/melange';

const cache = new MemoryCache({ ttlMs: 300_000 });
const checker = new Checker(pool, { cache });
```

`MemoryCache` is the in-memory implementation. See [Caching](../../guides/caching/) for the interface and custom backend guidance.

## Generated Code

`melange generate client --runtime typescript` produces type-safe constants and factory functions. See [Generated Code](../generated-code/) for details.

## Calling SQL Directly

For flows outside the client (background jobs, scripts, ad-hoc queries), call the generated SQL functions from any PostgreSQL client:

```typescript
import { Pool } from 'pg';
import type { Trace } from '@pthm/melange';

const { rows } = await pool.query<{ explain_permission: Trace }>(
  'SELECT explain_permission($1, $2, $3, $4, $5)',
  ['user', 'alice', 'viewer', 'document', '1']
);
const trace = rows[0].explain_permission;
```

See the [SQL API Reference](../sql-api/) for all functions.

## Next Steps

- [Explaining Decisions](../../guides/explaining-decisions/): the `explain` API and trace structure
- [Expanding Permissions](../../guides/expanding-permissions/): the `expand` and `expandRecursive` APIs
- [Caching](../../guides/caching/): opt-in caching for Check, Explain, and Expand
- [Go API](../go-api/): the Go runtime API — the TypeScript client mirrors its shape
- [Generated Code](../generated-code/): TypeScript code generation output
