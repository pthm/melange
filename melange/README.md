# Melange Go Client

This directory contains the Go runtime module (`github.com/pthm/melange/melange`) for performing permission checks against PostgreSQL. It has **zero external dependencies** (stdlib only), making it lightweight for production use.

## Installation

```bash
go get github.com/pthm/melange/melange
```

## Overview

The melange client provides a `Checker` API for evaluating permissions against the authorization model and tuples stored in PostgreSQL. It works with `*sql.DB`, `*sql.Tx`, or `*sql.Conn`, allowing permission checks to participate in transactions and see uncommitted changes.

## Basic Usage

```go
package main

import (
    "context"
    "database/sql"

    "github.com/pthm/melange/melange"
    _ "github.com/lib/pq"
)

func main() {
    db, _ := sql.Open("postgres", "postgres://localhost/mydb")
    checker := melange.NewChecker(db)

    // Define subject and object
    user := melange.Object{Type: "user", ID: "123"}
    repo := melange.Object{Type: "repository", ID: "456"}

    // Check permission
    allowed, err := checker.Check(context.Background(), user, "can_read", repo)
    if err != nil {
        panic(err)
    }
    if !allowed {
        // Handle unauthorized access
    }
}
```

## Type-Safe Domain Models

Implement `SubjectLike` and `ObjectLike` interfaces on your domain types:

```go
type User struct {
    ID   int64
    Name string
}

func (u User) FGASubject() melange.Object {
    return melange.Object{Type: "user", ID: fmt.Sprint(u.ID)}
}

type Repository struct {
    ID   int64
    Name string
}

func (r Repository) FGAObject() melange.Object {
    return melange.Object{Type: "repository", ID: fmt.Sprint(r.ID)}
}

// Use directly in checks
allowed, err := checker.Check(ctx, user, "can_read", repo)
```

## Caching

Enable caching to reduce database load for repeated checks:

```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))

// First check hits the database
allowed, _ := checker.Check(ctx, user, "can_read", repo)

// Subsequent checks hit the cache
allowed, _ = checker.Check(ctx, user, "can_read", repo)
```

## Listing Objects

Find all objects a subject can access:

```go
// With pagination
ids, cursor, err := checker.ListObjects(ctx, user, "can_read", "repository",
    melange.PageOptions{Limit: 100})

// Get next page
ids, cursor, err = checker.ListObjects(ctx, user, "can_read", "repository",
    melange.PageOptions{Limit: 100, After: cursor})

// Or fetch all at once (auto-paginates)
allIDs, err := checker.ListObjectsAll(ctx, user, "can_read", "repository")
```

## Listing Subjects

Find all subjects that have access to an object:

```go
// Who can read repository 456?
userIDs, cursor, err := checker.ListSubjects(ctx, repo, "can_read", "user",
    melange.PageOptions{Limit: 100})

// Or fetch all
allUserIDs, err := checker.ListSubjectsAll(ctx, repo, "can_read", "user")
```

## Transaction Support

Permission checks work within transactions and see uncommitted changes:

```go
tx, _ := db.BeginTx(ctx, nil)
defer tx.Rollback()

// Insert new data
tx.ExecContext(ctx, `INSERT INTO org_members ...`)

// Checker on transaction sees the uncommitted row
checker := melange.NewChecker(tx)
allowed, _ := checker.Check(ctx, user, "member", org) // allowed == true

tx.Commit()
```

## Decision Overrides

Bypass database checks for testing or admin tools:

```go
// Always allow (for admin tools or testing)
checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))

// Always deny (for testing unauthorized paths)
checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))

// Context-based overrides
checker := melange.NewChecker(db, melange.WithContextDecision())
ctx := melange.WithDecisionContext(ctx, melange.DecisionAllow)
allowed, _ := checker.Check(ctx, user, "can_read", repo) // always true
```

## Contextual Tuples

Pass temporary tuples that only affect a single check:

```go
tuples := []melange.ContextualTuple{
    {
        Subject:  melange.Object{Type: "user", ID: "123"},
        Relation: "editor",
        Object:   melange.Object{Type: "document", ID: "456"},
    },
}

allowed, err := checker.CheckWithContextualTuples(ctx, user, "can_edit", doc, tuples)
```

## Error Handling

```go
allowed, err := checker.Check(ctx, user, "can_read", repo)
if err != nil {
    if melange.IsNoTuplesTableErr(err) {
        // melange_tuples view needs to be created
    } else if melange.IsMissingFunctionErr(err) {
        // Run 'melange migrate' to install SQL functions
    }
    return err
}
```

Use `Must` for internal invariants where unauthorized access is a programmer error:

```go
// Panics if check fails or errors
checker.Must(ctx, user, "can_write", repo)
```

## Package Contents

| File | Description |
|------|-------------|
| `melange.go` | Core types: `Object`, `Relation`, `Querier`, interfaces |
| `checker.go` | `Checker` API for permission checks and listing |
| `cache.go` | In-memory cache with optional TTL |
| `decision.go` | Decision overrides and context support |
| `errors.go` | Sentinel errors and error helpers |
| `validator.go` | Request validation interface |

## See Also

- [Checking Permissions Guide](../docs/content/docs/guides/checking-permissions.md)
- [Listing Objects Guide](../docs/content/docs/guides/listing-objects.md)
- [Listing Subjects Guide](../docs/content/docs/guides/listing-subjects.md)
- [SQL API Reference](../docs/content/docs/reference/sql-api.md)
