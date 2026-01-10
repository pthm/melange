---
title: Getting Started
weight: 1
---

This guide walks you through installing Melange, defining an authorization model, and running your first permission check.

## Prerequisites

- Go 1.21 or later
- PostgreSQL 14 or later

## Installation

Melange consists of two components:

### 1. Go Runtime Library

Add the core library to your project:

```bash
go get github.com/pthm/melange/melange
```

The runtime module has zero external dependencies - only Go's standard library.

### 2. CLI Tool

Install the CLI for migrations and code generation:

```bash
go install github.com/pthm/melange/cmd/melange@latest
```

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

This installs the generated SQL permission functions.

### Step 4: Generate Type-Safe Go Code (Optional)

Generate Go constants for type-safe permission checks:

```bash
melange generate client \
  --runtime go \
  --schema schemas/schema.fga \
  --output internal/authz \
  --package authz
```

This creates constants like `authz.RelCanRead`, `authz.User("123")`, and `authz.Repository("456")`.

### Step 5: Check Permissions in Go

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

## Working with Transactions

Melange permission checks see uncommitted changes within the same transaction:

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

## Enabling Caching

For high-throughput applications, enable in-memory caching:

```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))

// First check hits the database
allowed, _ := checker.Check(ctx, user, "can_read", repo)

// Subsequent checks for same tuple are served from cache
allowed, _ = checker.Check(ctx, user, "can_read", repo) // ~79ns vs ~980us
```

## Next Steps

- [How It Works](./concepts/how-it-works.md) - Understand specialized SQL generation and performance
- [Tuples View](./concepts/tuples-view.md) - Detailed guidance on mapping your domain tables
- [CLI Reference](./reference/cli.md) - Full CLI command documentation
- [Checking Permissions](./guides/checking-permissions.md) - Complete Checker API reference
- [OpenFGA Compatibility](./reference/openfga-compatibility.md) - Supported features and migration path
