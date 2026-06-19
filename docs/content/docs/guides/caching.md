---
title: Caching
weight: 5
---

Melange's built-in cache reduces database load by storing permission check results in memory. Cached checks return in ~83ns compared to ~422μs for uncached checks.

## Built-in Cache

```go
cache := melange.NewCache(melange.WithTTL(5 * time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))

// First check hits the database
allowed, _ := checker.Check(ctx, user, "can_read", repo)

// Subsequent identical checks hit the cache
allowed, _ = checker.Check(ctx, user, "can_read", repo)
```

Characteristics:

- **In-memory, process-local**. Not shared across processes.
- **Thread-safe**. Safe for concurrent goroutines.
- **Caches both allowed and denied results**. Denied checks are cached too.
- **Unbounded within TTL**. Entries expire individually based on insertion time but the map grows without limit while entries are live.

### Options

| Option | Description |
|--------|-------------|
| `WithTTL(d time.Duration)` | Time-to-live for entries. Default: no expiry. |

```go
cache := melange.NewCache()                              // No expiry
cache := melange.NewCache(melange.WithTTL(time.Minute))  // 1 minute TTL
```

### Manual Operations

```go
cache.Size()   // Number of entries
cache.Clear()  // Remove all entries
```

### What Is Cached

Only `Check` and `CheckWithContextualTuples` results are cached. List operations (`ListObjects`, `ListSubjects`) always query the database.

Checks with contextual tuples bypass the cache entirely. Each contextual tuple check goes directly to the database because the temporary tuples are unique to that call.

## Request-Scoped Caching

Create a fresh cache per request to avoid stale results across requests:

{{< tabs >}}

{{< tab name="Go" >}}
```go
func authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        cache := melange.NewCache()
        checker := melange.NewChecker(db, melange.WithCache(cache))
        ctx := context.WithValue(r.Context(), "checker", checker)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Within a single request, repeated checks for the same permission hit the cache. The cache is discarded when the request ends.
{{< /tab >}}

{{< tab name="TypeScript" >}}
```typescript
import { Checker, MemoryCache } from '@pthm/melange';

function authMiddleware(req, res, next) {
  const cache = new MemoryCache();
  req.checker = new Checker(pool, { cache });
  next();
}
```
{{< /tab >}}

{{< /tabs >}}

## Long-Lived Cache

For applications where permissions change infrequently, use a shared cache with a TTL:

```go
// Create once at startup
cache := melange.NewCache(melange.WithTTL(5 * time.Minute))

// Share across all requests
func newChecker() *melange.Checker {
    return melange.NewChecker(db, melange.WithCache(cache))
}
```

TTL guidance:

- **Short (seconds)**: frequently changing permissions, high security requirements.
- **Medium (minutes)**: typical web applications.
- **Long or none**: near-static permissions, performance-critical paths.

The cache grows unbounded within the TTL window. For long-running processes with many unique permission tuples, use a short TTL or clear periodically.

## Custom Cache Implementations

Implement the `Cache` interface for distributed caches (Redis, Memcached, etc.):

```go
type Cache interface {
    Get(subject Object, relation Relation, object Object) (allowed bool, err error, ok bool)
    Set(subject Object, relation Relation, object Object, allowed bool, err error)
}
```

Example Redis implementation:

```go
type RedisCache struct {
    client *redis.Client
    ttl    time.Duration
}

func (c *RedisCache) Get(subject melange.Object, relation melange.Relation, object melange.Object) (bool, error, bool) {
    key := fmt.Sprintf("perm:%s:%s:%s:%s:%s",
        subject.Type, subject.ID, relation, object.Type, object.ID)
    val, err := c.client.Get(ctx, key).Result()
    if err == redis.Nil {
        return false, nil, false // Not in cache
    }
    if err != nil {
        return false, nil, false // Treat Redis errors as cache miss
    }
    return val == "1", nil, true
}

func (c *RedisCache) Set(subject melange.Object, relation melange.Relation, object melange.Object, allowed bool, err error) {
    if err != nil {
        return // Don't cache errors
    }
    key := fmt.Sprintf("perm:%s:%s:%s:%s:%s",
        subject.Type, subject.ID, relation, object.Type, object.ID)
    val := "0"
    if allowed {
        val = "1"
    }
    c.client.Set(ctx, key, val, c.ttl)
}
```

## Cache and Transactions

A checker created with `*sql.Tx` sees uncommitted changes. The cache does not differentiate between committed and uncommitted results. If you mix transactional and non-transactional checkers with the same cache, stale results are possible.

For transactional workflows, use a dedicated cache (or no cache) for the transaction-scoped checker:

```go
// Non-transactional checker with shared cache
checker := melange.NewChecker(db, melange.WithCache(sharedCache))

// Transactional checker with its own cache
txChecker := melange.NewChecker(tx, melange.WithCache(melange.NewCache()))
```

## Cache Invalidation

The built-in cache supports only TTL-based expiry. For event-driven invalidation:

- **Clear on write**: call `cache.Clear()` after modifying domain tables that feed `melange_tuples`.
- **Key-targeted invalidation**: implement `Cache` with a backend that supports key deletion.
- **Hybrid**: use a short TTL combined with event-driven clearing for critical paths.

## When Not to Cache

- **Transactional checks that must see uncommitted changes**. Use `*sql.Tx` without a cache.
- **Contextual tuple checks**. These bypass the cache automatically.
- **Very low check volume**. At ~422μs per uncached check, caching adds complexity without meaningful benefit if you're doing fewer than ~100 checks/second.

## Next Steps

- [Checking Permissions](../checking-permissions/): full Checker API usage
- [Go API](../../reference/go-api/): cache interface and options reference
- [Performance](../../reference/performance/): benchmark data
