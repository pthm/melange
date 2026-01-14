---
title: Checking Permissions
weight: 1
---

The permission check API evaluates whether a subject has a specific relation on an object. This is the core operation for authorization decisions.

## Basic Permission Check

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
import "github.com/pthm/melange/melange"

// Create a checker
checker := melange.NewChecker(db)

// Define subject and object
user := melange.Object{Type: "user", ID: "123"}
repo := melange.Object{Type: "repository", ID: "456"}

// Check permission
allowed, err := checker.Check(ctx, user, "can_read", repo)
if err != nil {
    return err
}
if !allowed {
    return ErrForbidden
}
```

The Checker accepts any type implementing the `Querier` interface:
- `*sql.DB` - Connection pool
- `*sql.Tx` - Transaction (sees uncommitted changes)
- `*sql.Conn` - Single connection
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

// Check permission
const allowed = await checkPermission('user', '123', 'can_read', 'repository', '456');
if (!allowed) {
  throw new ForbiddenError();
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- Check if user 123 can read repository 456
SELECT check_permission('user', '123', 'can_read', 'repository', '456');

-- Returns 1 for allowed, 0 for denied

-- Use in a WHERE clause to filter accessible records
SELECT d.*
FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;
```
{{< /tab >}}

{{< /tabs >}}

## Type-Safe Interfaces

Use generated code or implement type-safe interfaces for cleaner code:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
Implement `SubjectLike` and `ObjectLike` on your domain models:

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

With generated code from `melange generate client`:

```go
import "myapp/internal/authz"

allowed, err := checker.Check(ctx,
    authz.User("123"),
    authz.RelCanRead,
    authz.Repository("456"),
)
```
{{< /tab >}}

{{< tab >}}
Create factory functions or use generated code:

```typescript
// Define object factories
function User(id: string) {
  return { type: 'user' as const, id };
}

function Repository(id: string) {
  return { type: 'repository' as const, id };
}

const RelCanRead = 'can_read';

// Type-safe check function
async function check(
  subject: { type: string; id: string },
  relation: string,
  object: { type: string; id: string }
): Promise<boolean> {
  return checkPermission(
    subject.type,
    subject.id,
    relation,
    object.type,
    object.id
  );
}

// Use with factories
const allowed = await check(
  User('123'),
  RelCanRead,
  Repository('456')
);
```

With generated code from `melange generate client --runtime typescript`:

```typescript
import { User, Repository, RelCanRead, check } from './authz';

const allowed = await check(
  User('123'),
  RelCanRead,
  Repository('456')
);
```
{{< /tab >}}

{{< /tabs >}}

## Caching

Enable caching to reduce database load for repeated checks:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))

// First check hits the database (~422μs)
allowed, _ := checker.Check(ctx, user, "can_read", repo)

// Subsequent checks hit the cache (~83ns)
allowed, _ = checker.Check(ctx, user, "can_read", repo)
```

Cache characteristics:
- In-memory, process-local
- Thread-safe
- Caches both allowed and denied results
- Configurable TTL

### Cache API

```go
// Create cache
cache := melange.NewCache()                           // No expiry
cache := melange.NewCache(melange.WithTTL(time.Minute)) // 1 minute TTL

// Manual cache operations
allowed, err, found := cache.Get(subject, relation, object)
if found {
    // Use cached result
}

cache.Set(subject, relation, object, allowed, err)
cache.Clear()
size := cache.Size()
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
{{< /tab >}}

{{< tab >}}
```typescript
import { LRUCache } from 'lru-cache';

// Create cache
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

  const allowed = await checkPermission(
    subjectType,
    subjectId,
    relation,
    objectType,
    objectId
  );

  cache.set(key, allowed);
  return allowed;
}
```

For distributed caching, use Redis:

```typescript
import { Redis } from 'ioredis';

const redis = new Redis();

async function checkPermissionRedis(
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string,
  objectId: string
): Promise<boolean> {
  const key = `perm:${subjectType}:${subjectId}:${relation}:${objectType}:${objectId}`;

  const cached = await redis.get(key);
  if (cached !== null) {
    return cached === '1';
  }

  const allowed = await checkPermission(
    subjectType,
    subjectId,
    relation,
    objectType,
    objectId
  );

  await redis.setex(key, 60, allowed ? '1' : '0');
  return allowed;
}
```
{{< /tab >}}

{{< /tabs >}}

## Decision Overrides

Bypass database checks for testing or admin tools:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
// Always allow - for admin tools or testing authorized paths
checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))

// Always deny - for testing unauthorized paths
checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))
```

When a decision override is set, no database query is performed.

### Context-Based Overrides

Enable context-based decision overrides for request-scoped behavior:

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
{{< /tab >}}

{{< tab >}}
```typescript
// For admin users or testing, wrap the check function
function createChecker(options?: { alwaysAllow?: boolean; alwaysDeny?: boolean }) {
  return async function check(
    subjectType: string,
    subjectId: string,
    relation: string,
    objectType: string,
    objectId: string
  ): Promise<boolean> {
    if (options?.alwaysAllow) return true;
    if (options?.alwaysDeny) return false;

    return checkPermission(subjectType, subjectId, relation, objectType, objectId);
  };
}

// For testing
const testChecker = createChecker({ alwaysAllow: true });

// For admin middleware
function authMiddleware(req, res, next) {
  if (req.user.isAdmin) {
    req.check = createChecker({ alwaysAllow: true });
  } else {
    req.check = createChecker();
  }
  next();
}
```
{{< /tab >}}

{{< /tabs >}}

## Transaction Support

Permission checks work within transactions and see uncommitted changes:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
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
{{< /tab >}}

{{< tab >}}
```typescript
const client = await pool.connect();

try {
  await client.query('BEGIN');

  // Insert new data within transaction
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
  // allowed == true, even before commit

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

## Error Handling

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
### Sentinel Errors

```go
import "github.com/pthm/melange/melange"

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

### Must - Panic on Failure

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
{{< /tab >}}

{{< tab >}}
```typescript
// Handle database errors
try {
  const allowed = await checkPermission('user', '123', 'can_read', 'repository', '456');
} catch (error) {
  if (error.code === '42P01') {
    // Relation does not exist - melange_tuples view missing
    console.error('Authorization not configured: missing melange_tuples view');
  } else if (error.code === '42883') {
    // Function does not exist - need to run migrations
    console.error("Authorization not configured: run 'melange migrate'");
  }
  throw error;
}
```

For throwing on unauthorized access:

```typescript
async function mustCheck(
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string,
  objectId: string
): Promise<void> {
  const allowed = await checkPermission(
    subjectType,
    subjectId,
    relation,
    objectType,
    objectId
  );
  if (!allowed) {
    throw new ForbiddenError(`${subjectType}:${subjectId} does not have ${relation} on ${objectType}:${objectId}`);
  }
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- Handle resolution too complex error (code M2002)
DO $$
BEGIN
    PERFORM check_permission('user', '123', 'viewer', 'document', '456');
EXCEPTION
    WHEN SQLSTATE 'M2002' THEN
        RAISE NOTICE 'Permission resolution too complex';
END;
$$;
```

The M2002 error occurs when permission resolution exceeds the depth limit (25 levels), which can happen with:
- Deeply nested parent relationships
- Complex userset chains
- Cyclic permission structures
{{< /tab >}}

{{< /tabs >}}

## Performance Tips

### 1. Use Request-Scoped Caching

Create a cache per request to avoid stale data across requests:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
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
{{< /tab >}}

{{< tab >}}
```typescript
// Express middleware
function authMiddleware(req, res, next) {
  // Fresh cache per request
  req.permissionCache = new Map<string, boolean>();
  next();
}

async function checkWithRequestCache(
  req: Request,
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string,
  objectId: string
): Promise<boolean> {
  const key = `${subjectType}:${subjectId}:${relation}:${objectType}:${objectId}`;

  if (req.permissionCache.has(key)) {
    return req.permissionCache.get(key)!;
  }

  const allowed = await checkPermission(
    subjectType,
    subjectId,
    relation,
    objectType,
    objectId
  );

  req.permissionCache.set(key, allowed);
  return allowed;
}
```
{{< /tab >}}

{{< /tabs >}}

### 2. Batch Checks Efficiently

For multiple checks, use a shared cache:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
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
{{< /tab >}}

{{< tab >}}
```typescript
const cache = new Map<string, boolean>();

async function checkBatch(
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string,
  objectIds: string[]
): Promise<string[]> {
  const accessible: string[] = [];

  for (const objectId of objectIds) {
    const key = `${subjectType}:${subjectId}:${relation}:${objectType}:${objectId}`;

    let allowed: boolean;
    if (cache.has(key)) {
      allowed = cache.get(key)!;
    } else {
      allowed = await checkPermission(
        subjectType,
        subjectId,
        relation,
        objectType,
        objectId
      );
      cache.set(key, allowed);
    }

    if (allowed) {
      accessible.push(objectId);
    }
  }

  return accessible;
}
```
{{< /tab >}}

{{< /tabs >}}

### 3. Use ListObjects for Filtering

Instead of checking each object individually, use `list_accessible_objects`:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
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
{{< /tab >}}

{{< tab >}}
```typescript
// Inefficient: N database queries
const visible = [];
for (const repo of repos) {
  if (await checkPermission('user', userId, 'can_read', 'repository', repo.id)) {
    visible.push(repo);
  }
}

// Efficient: 1 database query
const { rows } = await pool.query(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', userId, 'can_read', 'repository']
);
const accessibleIds = new Set(rows.map(r => r.object_id));

const visible = repos.filter(repo => accessibleIds.has(repo.id));
```
{{< /tab >}}

{{< tab >}}
```sql
-- Inefficient: N function calls
SELECT d.* FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;

-- Efficient: 1 function call + JOIN
SELECT d.* FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document') a
    ON d.id::text = a.object_id;
```
{{< /tab >}}

{{< /tabs >}}

See [Listing Objects](./listing-objects.md) for details.

## Schema Validation

On first Checker creation, Melange validates the database schema (once per process). Issues are logged as warnings:

```
[melange] WARNING: check_permission function not found. Run 'melange migrate' to create it.
```

These warnings don't prevent Checker creation, allowing applications to start before authorization is fully configured.
