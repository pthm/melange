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
import { Checker } from '@pthm/melange';

const pool = new Pool({ connectionString: 'postgresql://localhost/mydb' });
const checker = new Checker(pool);

// Define subject and object
const user = { type: 'user', id: '123' };
const repo = { type: 'repository', id: '456' };

// Check permission
const decision = await checker.check(user, 'can_read', repo);
if (!decision.allowed) {
  throw new ForbiddenError();
}
```

The Checker accepts any object implementing the `Queryable` interface. The `pg` Pool and Client both satisfy this. Use adapters for other drivers.
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
Create factory functions for cleaner code:

```typescript
import { Checker } from '@pthm/melange';

// Define object factories
function User(id: string) {
  return { type: 'user' as const, id };
}

function Repository(id: string) {
  return { type: 'repository' as const, id };
}

const RelCanRead = 'can_read';

// Use with Checker
const checker = new Checker(pool);
const decision = await checker.check(
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
import { Checker, MemoryCache } from '@pthm/melange';

const cache = new MemoryCache(60000); // 60 second TTL
const checker = new Checker(pool, { cache });

// First check hits the database
const decision1 = await checker.check(user, 'can_read', repo);

// Subsequent checks hit the cache
const decision2 = await checker.check(user, 'can_read', repo);
```

Cache characteristics:
- In-memory, process-local
- Caches both allowed and denied results
- Configurable TTL

### Custom Cache Implementation

Implement the `Cache` interface for distributed caches:

```typescript
import type { Cache, Decision } from '@pthm/melange';

class RedisCache implements Cache {
  constructor(private redis: Redis, private ttlSeconds = 60) {}

  async get(key: string): Promise<Decision | undefined> {
    const val = await this.redis.get(key);
    if (val === null) return undefined;
    return { allowed: val === '1' };
  }

  async set(key: string, value: Decision): Promise<void> {
    await this.redis.setex(key, this.ttlSeconds, value.allowed ? '1' : '0');
  }

  async clear(): Promise<void> {
    // Clear melange keys or flush
  }
}

const checker = new Checker(pool, { cache: new RedisCache(redis) });
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
import { Checker, DecisionAllow, DecisionDeny } from '@pthm/melange';

// Always allow — for admin tools or testing authorized paths
const allowChecker = new Checker(pool, { decision: DecisionAllow });

// Always deny — for testing unauthorized paths
const denyChecker = new Checker(pool, { decision: DecisionDeny });
```

When a decision override is set, no database query is performed. This works with both `check()` and `newBulkCheck()`.

```typescript
// In middleware — create request-scoped checker
function authMiddleware(req, res, next) {
  if (req.user.isAdmin) {
    req.checker = new Checker(pool, { decision: DecisionAllow });
  } else {
    req.checker = new Checker(pool);
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
import { Checker } from '@pthm/melange';

const client = await pool.connect();

try {
  await client.query('BEGIN');

  // Insert new data within transaction
  await client.query(
    'INSERT INTO organization_members (user_id, organization_id, role) VALUES ($1, $2, $3)',
    [userId, orgId, 'member']
  );

  // Create Checker on the transaction client to see uncommitted rows
  const txChecker = new Checker(client);
  const decision = await txChecker.check(
    { type: 'user', id: userId },
    'member',
    { type: 'organization', id: orgId }
  );
  // decision.allowed == true, even before commit

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
import { Checker, MelangeError, ValidationError } from '@pthm/melange';

const checker = new Checker(pool);

try {
  const decision = await checker.check(user, 'can_read', repo);
} catch (error) {
  if (error instanceof ValidationError) {
    // Invalid input (empty type, missing ID, etc.)
    console.error('Invalid check request:', error.message);
  } else if (error instanceof MelangeError) {
    // Other melange errors
    console.error('Authorization error:', error.message);
  }
  throw error;
}
```

For guard-style checks with bulk operations, use `allOrError`:

```typescript
import { BulkCheckDeniedError, isBulkCheckDeniedError } from '@pthm/melange';

const results = await checker.newBulkCheck()
  .add(user, 'can_read', repo)
  .add(user, 'can_write', repo)
  .execute();

const err = results.allOrError();
if (err) {
  // err is a BulkCheckDeniedError with subject, relation, object, index, total
  console.error(`Denied: ${err.message}`);
  throw err;
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
import { Checker, MemoryCache } from '@pthm/melange';

// Express middleware — fresh cache per request
function authMiddleware(req, res, next) {
  const cache = new MemoryCache(); // Fresh cache per request
  req.checker = new Checker(pool, { cache });
  next();
}

// In a handler — repeated checks are cached automatically
async function handleRequest(req) {
  const user = { type: 'user', id: req.userId };

  // First check hits the database
  await req.checker.check(user, 'can_read', repo);

  // Same check later in the request uses cache
  await req.checker.check(user, 'can_read', repo);

  // Or batch multiple checks for efficiency
  const results = await req.checker.newBulkCheck()
    .add(user, 'can_read', repo1)
    .add(user, 'can_write', repo1)
    .execute();
}
```
{{< /tab >}}

{{< /tabs >}}

### 2. Batch Checks with Bulk API

Use `NewBulkCheck` to check many permissions in a single SQL call instead of looping over individual checks:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
bulk := checker.NewBulkCheck(ctx)

// Queue checks — all execute in one SQL call
for _, repo := range repos {
    bulk.Add(user, "can_read", repo)
}

// Or use AddMany for one subject+relation across multiple objects
bulk.AddMany(user, "can_read", repos...)

results, err := bulk.Execute()
if err != nil {
    return err
}

// Check aggregate results
if results.All() {
    // Every check was allowed
}
if results.Any() {
    // At least one check was allowed
}

// Iterate individual results
for _, r := range results.Allowed() {
    fmt.Printf("%s:%s is accessible\n", r.Object().Type, r.Object().ID)
}

// Use AllOrError for guard-style checks
if err := results.AllOrError(); err != nil {
    return err // Returns BulkCheckDeniedError with details
}
```

Use `AddWithID` to tag checks with meaningful identifiers:

```go
bulk := checker.NewBulkCheck(ctx)
bulk.AddWithID("read-repo", user, "can_read", repo)
bulk.AddWithID("write-repo", user, "can_write", repo)
bulk.AddWithID("admin-repo", user, "admin", repo)

results, err := bulk.Execute()
if err != nil {
    return err
}

// Look up results by ID
if r := results.GetByID("write-repo"); r != nil && r.IsAllowed() {
    // User can write
}
```
{{< /tab >}}

{{< tab >}}
```typescript
import { Checker } from '@pthm/melange';

const checker = new Checker(pool);

const results = await checker.newBulkCheck()
  .add(user, 'can_read', repo1)
  .add(user, 'can_read', repo2)
  .addMany(user, 'can_write', repo1, repo2)
  .execute();

// Check aggregate results
if (results.all()) {
  // Every check was allowed
}
if (results.any()) {
  // At least one check was allowed
}

// Iterate individual results
for (const r of results.allowed()) {
  console.log(`${r.object.type}:${r.object.id} is accessible`);
}

// Use allOrError for guard-style checks
const err = results.allOrError();
if (err) {
  throw err; // BulkCheckDeniedError with details
}
```

Use `addWithId` to tag checks with meaningful identifiers:

```typescript
const results = await checker.newBulkCheck()
  .addWithId('read-repo', user, 'can_read', repo)
  .addWithId('write-repo', user, 'can_write', repo)
  .addWithId('admin-repo', user, 'admin', repo)
  .execute();

// Look up results by ID
const writeResult = results.getById('write-repo');
if (writeResult?.allowed) {
  // User can write
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- Check multiple permissions in a single call
SELECT idx, allowed
FROM check_permission_bulk(
    ARRAY['user', 'user', 'user'],
    ARRAY['123', '123', '123'],
    ARRAY['viewer', 'editor', 'admin'],
    ARRAY['document', 'document', 'document'],
    ARRAY['456', '456', '456']
);
```
{{< /tab >}}

{{< /tabs >}}

{{< callout type="info" >}}
**Deduplication**: Duplicate checks within a batch are automatically deduplicated — only unique permission tuples are sent to the database. Results are fanned out to all original positions.
{{< /callout >}}

{{< callout type="warning" >}}
**Size limit**: A single bulk check supports up to 10,000 checks (`MaxBulkCheckSize` in Go, `MAX_BULK_CHECK_SIZE` in TypeScript). Exceeding this limit returns an error.
{{< /callout >}}

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
import { Checker } from '@pthm/melange';

const checker = new Checker(pool);
const user = { type: 'user', id: userId };

// Inefficient: N database queries
const visible = [];
for (const repo of repos) {
  const d = await checker.check(user, 'can_read', { type: 'repository', id: repo.id });
  if (d.allowed) visible.push(repo);
}

// Efficient: 1 database query
const result = await checker.listObjects(user, 'can_read', 'repository');
const accessibleIds = new Set(result.items);

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
