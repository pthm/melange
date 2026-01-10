---
title: Getting Started
weight: 1
---

This guide walks you through installing Melange, defining an authorization model, and running your first permission check.

## Prerequisites

- PostgreSQL 14 or later

## Installation

### CLI Tool

Install the Melange CLI for migrations and code generation:

{{< tabs items="Homebrew,Go,Binary" >}}

{{< tab >}}
```bash
brew install pthm/tap/melange
```
{{< /tab >}}

{{< tab >}}
```bash
go install github.com/pthm/melange/cmd/melange@latest
```
{{< /tab >}}

{{< tab >}}
Download pre-built binaries from the [GitHub releases page](https://github.com/pthm/melange/releases).
{{< /tab >}}

{{< /tabs >}}

Verify the installation:

```bash
melange --help
```

## Quick Start

### Step 1: Define Your Authorization Model

Create a `schemas/schema.fga` file using OpenFGA DSL syntax:

```fga
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin

type repository
  relations
    define org: [organization]
    define owner: [user]
    define admin: [user] or owner
    define can_read: member from org or admin
    define can_write: admin
    define can_delete: owner
```

This model defines:
- **Users** as the primary subject type
- **Organizations** with a role hierarchy: `owner` > `admin` > `member`
- **Repositories** owned by organizations, with permissions inherited from the parent org

### Step 2: Create the melange_tuples View

Melange reads authorization data from a view called `melange_tuples`. This view maps your existing domain tables into the tuple format Melange expects.

Create a migration that defines this view:

```sql
CREATE OR REPLACE VIEW melange_tuples AS
-- Organization memberships
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,                -- 'owner', 'admin', or 'member'
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Repository -> Organization relationship
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories

UNION ALL

-- Direct repository owners
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_owners;
```

The view must provide these columns:

| Column | Type | Description |
|--------|------|-------------|
| `subject_type` | `text` | Type of the subject (e.g., `'user'`) |
| `subject_id` | `text` | ID of the subject |
| `relation` | `text` | The relation name (e.g., `'owner'`, `'member'`) |
| `object_type` | `text` | Type of the object (e.g., `'repository'`) |
| `object_id` | `text` | ID of the object |

### Step 3: Apply Migrations

Run the Melange CLI to apply the schema to your database:

```bash
melange migrate \
  --db postgres://localhost/mydb \
  --schemas-dir schemas
```

This generates and installs specialized SQL permission functions.

### Step 4: Generate Type-Safe Client Code (Optional)

Generate constants and helpers for your language of choice:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```bash
melange generate client \
  --runtime go \
  --schema schemas/schema.fga \
  --output internal/authz \
  --package authz
```

This creates constants like `authz.RelCanRead`, `authz.User("123")`, and `authz.Repository("456")`.
{{< /tab >}}

{{< tab >}}
```bash
melange generate client \
  --runtime typescript \
  --schema schemas/schema.fga \
  --output src/authz
```

This creates TypeScript types and factory functions for type-safe permission checks.
{{< /tab >}}

{{< /tabs >}}

### Step 5: Check Permissions

With migrations applied, you can check permissions using the generated SQL functions from any language, or use a client library for convenience.

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
package main

import (
    "context"
    "database/sql"
    "log"

    "github.com/pthm/melange/melange"
    _ "github.com/lib/pq"
)

func main() {
    ctx := context.Background()

    db, err := sql.Open("postgres", "postgres://localhost/mydb")
    if err != nil {
        log.Fatal(err)
    }

    // Create a checker
    checker := melange.NewChecker(db)

    // Define subject and object
    user := melange.Object{Type: "user", ID: "alice"}
    repo := melange.Object{Type: "repository", ID: "123"}

    // Check if user can read the repository
    allowed, err := checker.Check(ctx, user, "can_read", repo)
    if err != nil {
        log.Fatal(err)
    }

    if allowed {
        log.Println("Access granted")
    } else {
        log.Println("Access denied")
    }
}
```

With generated code:

```go
import "myapp/internal/authz"

// Using generated helpers
allowed, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("123"),
)
```

Install the Go runtime library:

```bash
go get github.com/pthm/melange/melange
```

The runtime module has zero external dependencies - only Go's standard library.
{{< /tab >}}

{{< tab >}}
```typescript
import { Pool } from 'pg';

const pool = new Pool({ connectionString: 'postgresql://localhost/mydb' });

async function checkPermission(
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string,
  objectId: string
): Promise<boolean> {
  const { rows } = await pool.query(
    'SELECT check_permission($1, $2, $3, $4, $5)',
    [subjectType, subjectId, relation, objectType, objectId]
  );
  return rows[0].check_permission === 1;
}

// Check if user can read the repository
const allowed = await checkPermission('user', 'alice', 'can_read', 'repository', '123');

if (allowed) {
  console.log('Access granted');
} else {
  console.log('Access denied');
}
```

With generated code:

```typescript
import { User, Repository, RelCanRead } from './authz';

// Using generated helpers
const allowed = await checkPermission(
  User('alice').type,
  User('alice').id,
  RelCanRead,
  Repository('123').type,
  Repository('123').id
);
```

Install the npm package:

```bash
npm install @pthm/melange
```
{{< /tab >}}

{{< tab >}}
```sql
-- Check permission directly in SQL
SELECT check_permission('user', 'alice', 'can_read', 'repository', '123');

-- Returns 1 for allowed, 0 for denied

-- Use in a WHERE clause
SELECT r.*
FROM repositories r
WHERE check_permission('user', 'alice', 'can_read', 'repository', r.id::text) = 1;
```
{{< /tab >}}

{{< /tabs >}}

## Working with Transactions

Permission checks see uncommitted changes within the same transaction:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback()

// Add user to organization within transaction
_, err = tx.ExecContext(ctx, `
    INSERT INTO organization_members (user_id, organization_id, role)
    VALUES ($1, $2, 'member')
`, userID, orgID)
if err != nil {
    return err
}

// Permission check sees the uncommitted row
checker := melange.NewChecker(tx)
allowed, err := checker.Check(ctx, user, "member", org)
// allowed == true, even though transaction isn't committed

if err := tx.Commit(); err != nil {
    return err
}
```
{{< /tab >}}

{{< tab >}}
```typescript
const client = await pool.connect();

try {
  await client.query('BEGIN');

  // Add user to organization within transaction
  await client.query(
    'INSERT INTO organization_members (user_id, organization_id, role) VALUES ($1, $2, $3)',
    [userId, orgId, 'member']
  );

  // Permission check sees the uncommitted row
  const { rows } = await client.query(
    'SELECT check_permission($1, $2, $3, $4, $5)',
    ['user', userId, 'member', 'organization', orgId]
  );
  const allowed = rows[0].check_permission === 1;
  // allowed == true, even though transaction isn't committed

  await client.query('COMMIT');
} catch (e) {
  await client.query('ROLLBACK');
  throw e;
} finally {
  client.release();
}
```
{{< /tab >}}

{{< tab >}}
```sql
BEGIN;

-- Insert new tuple (via domain table that feeds the view)
INSERT INTO organization_members (user_id, organization_id, role)
VALUES ('123', '456', 'member');

-- Permission check sees the uncommitted row
SELECT check_permission('user', '123', 'member', 'organization', '456');
-- Returns 1

ROLLBACK;

-- Now returns 0
SELECT check_permission('user', '123', 'member', 'organization', '456');
```
{{< /tab >}}

{{< /tabs >}}

## Caching

For high-throughput applications, enable caching to reduce database load:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))

// First check hits the database
allowed, _ := checker.Check(ctx, user, "can_read", repo)

// Subsequent checks for same tuple are served from cache
allowed, _ = checker.Check(ctx, user, "can_read", repo) // ~79ns vs ~980us
```
{{< /tab >}}

{{< tab >}}
```typescript
import { LRUCache } from 'lru-cache';

// Simple in-memory cache
const cache = new LRUCache<string, boolean>({
  max: 10000,
  ttl: 60 * 1000, // 1 minute
});

async function checkPermissionCached(
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string,
  objectId: string
): Promise<boolean> {
  const key = `${subjectType}:${subjectId}:${relation}:${objectType}:${objectId}`;

  const cached = cache.get(key);
  if (cached !== undefined) {
    return cached;
  }

  const { rows } = await pool.query(
    'SELECT check_permission($1, $2, $3, $4, $5)',
    [subjectType, subjectId, relation, objectType, objectId]
  );
  const allowed = rows[0].check_permission === 1;

  cache.set(key, allowed);
  return allowed;
}
```
{{< /tab >}}

{{< /tabs >}}

## Next Steps

- [How It Works](./concepts/how-it-works.md) - Understand specialized SQL generation and performance
- [Tuples View](./concepts/tuples-view.md) - Detailed guidance on mapping your domain tables
- [CLI Reference](./reference/cli.md) - Full CLI command documentation
- [Checking Permissions](./guides/checking-permissions.md) - Complete API reference
- [SQL API](./reference/sql-api.md) - Direct SQL function documentation for any language
- [OpenFGA Compatibility](./reference/openfga-compatibility.md) - Supported features and migration path
