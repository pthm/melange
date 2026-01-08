---
title: Checking Permissions
weight: 1
---

The `Checker` is the core API for evaluating permissions. It calls PostgreSQL functions to validate access based on your authorization model and tuple data.

## Creating a Checker

```go
import "github.com/pthm/melange"

// Basic checker
checker := melange.NewChecker(db)

// With options
checker := melange.NewChecker(db,
    melange.WithCache(cache),
    melange.WithDecision(melange.DecisionAllow),
)
```

The Checker accepts any type implementing the `Querier` interface:
- `*sql.DB` - Connection pool
- `*sql.Tx` - Transaction (sees uncommitted changes)
- `*sql.Conn` - Single connection

## Basic Permission Check

```go
user := melange.Object{Type: "user", ID: "123"}
repo := melange.Object{Type: "repository", ID: "456"}

allowed, err := checker.Check(ctx, user, "can_read", repo)
if err != nil {
    return err
}
if !allowed {
    return ErrForbidden
}
```

## Type-Safe Interfaces

Implement `SubjectLike` and `ObjectLike` on your domain models for cleaner code:

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

// Now use directly in checks
allowed, err := checker.Check(ctx, user, "can_read", repo)
```

With generated code from `melange generate`:

```go
import "myapp/internal/authz"

allowed, err := checker.Check(ctx,
    authz.User("123"),
    authz.RelCanRead,
    authz.Repository("456"),
)
```

## Checker Options

### WithCache

Enable caching to reduce database load:

```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))

// First check hits the database (~1ms)
allowed, _ := checker.Check(ctx, user, "can_read", repo)

// Subsequent checks hit the cache (~79ns)
allowed, _ = checker.Check(ctx, user, "can_read", repo)
```

Cache characteristics:
- In-memory, process-local
- Thread-safe
- Caches both allowed and denied results
- Configurable TTL

### WithDecision

Bypass database checks for testing or admin tools:

```go
// Always allow - for admin tools or testing authorized paths
checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))

// Always deny - for testing unauthorized paths
checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))
```

When a decision override is set, no database query is performed.

### WithContextDecision

Enable context-based decision overrides:

```go
checker := melange.NewChecker(db, melange.WithContextDecision())

// In middleware or handler:
ctx := melange.WithDecisionContext(ctx, melange.DecisionAllow)

// Check uses context decision, no database query
allowed, _ := checker.Check(ctx, user, "can_read", repo)
```

Decision precedence (when `WithContextDecision` is enabled):
1. Context decision (via `WithDecisionContext`)
2. Checker decision (via `WithDecision`)
3. Database check

## Transaction Support

Permission checks work within transactions and see uncommitted changes:

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback()

// Insert new data within transaction
_, err = tx.ExecContext(ctx, `
    INSERT INTO organization_members (user_id, organization_id, role)
    VALUES ($1, $2, 'member')
`, userID, orgID)
if err != nil {
    return err
}

// Checker on transaction sees the uncommitted row
checker := melange.NewChecker(tx)
allowed, err := checker.Check(ctx, user, "member", org)
// allowed == true, even before commit

if err := tx.Commit(); err != nil {
    return err
}
```

## Must - Panic on Failure

Use `Must` for internal invariants where unauthorized access is a programmer error:

```go
// Panic if check fails or errors
checker.Must(ctx, user, "can_write", repo)

// Only reachable if permission granted
```

Prefer `Check` for user-facing authorization. Use `Must` when:
- Access denial indicates a bug (not a user error)
- You've already validated access at a higher level
- In tests where failure should panic

## Error Handling

### Sentinel Errors

```go
import "github.com/pthm/melange"

var (
    melange.ErrNoTuplesTable   // melange_tuples view doesn't exist
    melange.ErrMissingFunction // SQL functions not installed
)
```

### Error Checkers

```go
allowed, err := checker.Check(ctx, user, "can_read", repo)
if err != nil {
    if melange.IsNoTuplesTableErr(err) {
        // melange_tuples view needs to be created
        log.Error("Authorization not configured: missing melange_tuples view")
    } else if melange.IsMissingFunctionErr(err) {
        // Run melange migrate
        log.Error("Authorization not configured: run 'melange migrate'")
    }
    return err
}
```

## Caching API

### NewCache

```go
cache := melange.NewCache()                           // No expiry
cache := melange.NewCache(melange.WithTTL(time.Minute)) // 1 minute TTL
```

### Cache Methods

```go
// Get cached result
allowed, err, found := cache.Get(subject, relation, object)
if found {
    // Use cached result
}

// Store result
cache.Set(subject, relation, object, allowed, err)

// Check size
size := cache.Size()

// Clear all entries
cache.Clear()
```

### Custom Cache Implementation

Implement the `Cache` interface for distributed caches:

```go
type Cache interface {
    Get(subject Object, relation Relation, object Object) (allowed bool, err error, ok bool)
    Set(subject Object, relation Relation, object Object, allowed bool, err error)
}

// Example: Redis-backed cache
type RedisCache struct {
    client *redis.Client
    ttl    time.Duration
}

func (c *RedisCache) Get(subject Object, relation Relation, object Object) (bool, error, bool) {
    key := fmt.Sprintf("perm:%s:%s:%s:%s:%s",
        subject.Type, subject.ID, relation, object.Type, object.ID)
    val, err := c.client.Get(ctx, key).Result()
    if err == redis.Nil {
        return false, nil, false
    }
    // Parse and return cached result...
}
```

## Performance Tips

### 1. Use Request-Scoped Caching

Create a cache per request to avoid stale data across requests:

```go
func authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        cache := melange.NewCache() // Fresh cache per request
        checker := melange.NewChecker(db, melange.WithCache(cache))
        ctx := context.WithValue(r.Context(), "checker", checker)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### 2. Batch Checks Efficiently

For multiple checks, use a shared cache:

```go
cache := melange.NewCache()
checker := melange.NewChecker(db, melange.WithCache(cache))

// Multiple checks reuse cache
for _, repo := range repos {
    allowed, _ := checker.Check(ctx, user, "can_read", repo)
    if allowed {
        visibleRepos = append(visibleRepos, repo)
    }
}
```

### 3. Use ListObjects for Filtering

Instead of checking each object individually, use `ListObjects`:

```go
// Inefficient: N database queries
for _, repo := range repos {
    if allowed, _ := checker.Check(ctx, user, "can_read", repo); allowed {
        // ...
    }
}

// Efficient: 1 database query
accessibleIDs, _ := checker.ListObjects(ctx, user, "can_read", "repository")
idSet := make(map[string]bool)
for _, id := range accessibleIDs {
    idSet[id] = true
}
for _, repo := range repos {
    if idSet[repo.ID] {
        // ...
    }
}
```

See [Listing Objects](./listing-objects.md) for details.

## Schema Validation

On first Checker creation, Melange validates the database schema (once per process). Issues are logged as warnings:

```
[melange] WARNING: check_permission function not found. Run 'melange migrate' to create it.
```

These warnings don't prevent Checker creation, allowing applications to start before authorization is fully configured.
