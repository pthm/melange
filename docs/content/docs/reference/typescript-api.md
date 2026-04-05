---
title: TypeScript API
weight: 4
---

{{< callout type="info" >}}
The TypeScript runtime client is in development. This page will be expanded when it ships. For now, use the SQL functions directly from any PostgreSQL client.
{{< /callout >}}

## Current Status

**Code generation** is available. `melange generate client --runtime typescript` produces type-safe constants and factory functions. See [Generated Code](../generated-code/) for details.

**Runtime library** (`@pthm/melange`) is planned. It will mirror the [Go API](../go-api/) with TypeScript idioms:

- `Checker` class with `check()`, `listObjects()`, `listSubjects()`
- Bulk check builder
- Cache interface
- Decision overrides
- Contextual tuples
- Error types (`ValidationError`, `BulkCheckDeniedError`)
- Custom database schema (`databaseSchema` option)

## Using SQL Directly

Until the runtime ships, call the generated SQL functions from any PostgreSQL client:

```typescript
import { Pool } from 'pg';

const pool = new Pool({ connectionString: process.env.DATABASE_URL });

// Permission check
const { rows } = await pool.query(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', 'alice', 'can_read', 'repository', '42']
);
const allowed = rows[0].check_permission === 1;

// List accessible objects
const { rows: objects } = await pool.query(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', 'alice', 'can_read', 'repository']
);
const objectIds = objects.map(r => r.object_id);
```

See the [SQL API Reference](../sql-api/) for all available functions and their signatures.

## Next Steps

- [Generated Code](../generated-code/): TypeScript code generation output
- [SQL API](../sql-api/): calling permission functions directly
- [Go API](../go-api/): the Go runtime API (reference for the planned TypeScript API)
