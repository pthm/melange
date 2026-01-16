---
title: Listing Objects
weight: 2
---

The `ListObjects` operation returns all objects of a given type that a subject has a specific relation on. This answers the question: "What can this user access?"

## Basic Usage

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Find all repositories user can read (with pagination)
repoIDs, cursor, err := checker.ListObjects(ctx,
    authz.User("123"),
    authz.RelCanRead,
    "repository",
    melange.PageOptions{Limit: 100},
)
if err != nil {
    return err
}

// repoIDs = ["repo-1", "repo-456", "repo-789"]
// cursor = nil when no more pages, or a string to fetch the next page
```
{{< /tab >}}

{{< tab >}}
```typescript
// Find all repositories user can read (with pagination)
const { rows } = await pool.query(
  'SELECT object_id, next_cursor FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', '123', 'can_read', 'repository', 100, null]
);
const repoIds = rows.map(row => row.object_id);
const nextCursor = rows.length > 0 ? rows[0].next_cursor : null;

// repoIds = ["repo-1", "repo-456", "repo-789"]
```
{{< /tab >}}

{{< tab >}}
```sql
-- Get documents user 123 can view (first 100)
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);

-- Returns a table with object_id and next_cursor columns
-- next_cursor is NULL when no more pages exist
```
{{< /tab >}}

{{< /tabs >}}

## Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `subject_type` | `text` | Type of the subject (e.g., `'user'`) |
| `subject_id` | `text` | ID of the subject |
| `relation` | `text` | The relation to check |
| `object_type` | `text` | Type of objects to return |
| `p_limit` | `int` | Maximum number of results per page (NULL = no limit) |
| `p_after` | `text` | Cursor from previous page (NULL = start from beginning) |

## Return Value

Returns a table with `object_id` and `next_cursor` columns. The `next_cursor` value is repeated on every row for convenience - use the last row's cursor to fetch the next page. Returns an empty result set if no objects found (not an error).

{{< callout type="info" >}}
**Ordering**: Results are ordered deterministically by `object_id` to ensure stable pagination across requests.
{{< /callout >}}

## Pagination

### Cursor-Based Pagination

Use cursor-based pagination to iterate through large result sets efficiently:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Paginate through all accessible repositories
var cursor *string
for {
    ids, next, err := checker.ListObjects(ctx,
        authz.User("123"),
        authz.RelCanRead,
        "repository",
        melange.PageOptions{Limit: 100, After: cursor},
    )
    if err != nil {
        return err
    }

    for _, id := range ids {
        // Process each repository ID
        fmt.Println("Accessible:", id)
    }

    if next == nil {
        break // No more pages
    }
    cursor = next
}
```
{{< /tab >}}

{{< tab >}}
```typescript
// Paginate through all accessible repositories
let cursor: string | null = null;

while (true) {
  const { rows } = await pool.query(
    'SELECT object_id, next_cursor FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
    ['user', userId, 'can_read', 'repository', 100, cursor]
  );

  for (const row of rows) {
    // Process each repository ID
    console.log('Accessible:', row.object_id);
  }

  cursor = rows.length > 0 ? rows[rows.length - 1].next_cursor : null;
  if (!cursor) break; // No more pages
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- First page
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);

-- Returns: object_id | next_cursor
--          doc-001   | doc-100
--          doc-002   | doc-100
--          ...
--          doc-100   | doc-100

-- Next page (use the next_cursor value)
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, 'doc-100');
```
{{< /tab >}}

{{< /tabs >}}

### Fetching All Results

For convenience, use the `ListObjectsAll` helper which automatically paginates through all results:

{{< tabs items="Go" >}}

{{< tab >}}
```go
// Get all accessible repositories (auto-paginates internally)
allRepoIDs, err := checker.ListObjectsAll(ctx,
    authz.User("123"),
    authz.RelCanRead,
    "repository",
)
if err != nil {
    return err
}
// allRepoIDs contains all accessible repository IDs
```
{{< /tab >}}

{{< /tabs >}}

{{< callout type="warning" >}}
**Use with caution**: `ListObjectsAll` loads all IDs into memory. For large datasets, prefer paginated queries with `ListObjects` to control memory usage.
{{< /callout >}}

## Examples

### Filter a List of Resources

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Get all repositories from your data layer
repos, err := db.GetAllRepositories(ctx)
if err != nil {
    return err
}

// Get IDs the user can access (using ListObjectsAll for convenience)
accessibleIDs, err := checker.ListObjectsAll(ctx, user, "can_read", "repository")
if err != nil {
    return err
}

// Build a set for O(1) lookup
accessSet := make(map[string]bool, len(accessibleIDs))
for _, id := range accessibleIDs {
    accessSet[id] = true
}

// Filter to only accessible repos
var visibleRepos []Repository
for _, repo := range repos {
    if accessSet[fmt.Sprint(repo.ID)] {
        visibleRepos = append(visibleRepos, repo)
    }
}
```
{{< /tab >}}

{{< tab >}}
```typescript
// Get all repositories from your data layer
const repos = await db.getAllRepositories();

// Get IDs the user can access (NULL limit returns all)
const { rows } = await pool.query(
  'SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', userId, 'can_read', 'repository', null, null]
);
const accessibleIds = new Set(rows.map(r => r.object_id));

// Filter to only accessible repos
const visibleRepos = repos.filter(repo => accessibleIds.has(repo.id));
```
{{< /tab >}}

{{< tab >}}
```sql
-- Join with domain table to get full records (NULL limit returns all)
SELECT d.*
FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL) a
    ON d.id::text = a.object_id;
```
{{< /tab >}}

{{< /tabs >}}

### Fetch Only Accessible Resources

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Get accessible IDs first (using ListObjectsAll)
ids, err := checker.ListObjectsAll(ctx, user, "can_read", "document")
if err != nil {
    return nil, err
}

if len(ids) == 0 {
    return []Document{}, nil
}

// Query only those documents
docs, err := db.GetDocumentsByIDs(ctx, ids)
return docs, err
```
{{< /tab >}}

{{< tab >}}
```typescript
// Get accessible IDs first (NULL limit returns all)
const { rows } = await pool.query(
  'SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', userId, 'can_read', 'document', null, null]
);
const ids = rows.map(r => r.object_id);

if (ids.length === 0) {
  return [];
}

// Query only those documents
const docs = await db.getDocumentsByIds(ids);
return docs;
```
{{< /tab >}}

{{< tab >}}
```sql
-- Count accessible objects
SELECT COUNT(*) FROM list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL);
```
{{< /tab >}}

{{< /tabs >}}

### Check Multiple Permissions

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
type RepoWithPermissions struct {
    Repository
    CanRead   bool
    CanWrite  bool
    CanDelete bool
}

// Fetch all permission sets in parallel (using ListObjectsAll)
var (
    readIDs, writeIDs, deleteIDs []string
    readErr, writeErr, deleteErr error
)

var wg sync.WaitGroup
wg.Add(3)

go func() {
    defer wg.Done()
    readIDs, readErr = checker.ListObjectsAll(ctx, user, "can_read", "repository")
}()
go func() {
    defer wg.Done()
    writeIDs, writeErr = checker.ListObjectsAll(ctx, user, "can_write", "repository")
}()
go func() {
    defer wg.Done()
    deleteIDs, deleteErr = checker.ListObjectsAll(ctx, user, "can_delete", "repository")
}()
wg.Wait()

// Check errors...

// Build permission maps
canRead := toSet(readIDs)
canWrite := toSet(writeIDs)
canDelete := toSet(deleteIDs)

// Annotate repos with permissions
for _, repo := range repos {
    id := fmt.Sprint(repo.ID)
    result = append(result, RepoWithPermissions{
        Repository: repo,
        CanRead:    canRead[id],
        CanWrite:   canWrite[id],
        CanDelete:  canDelete[id],
    })
}
```
{{< /tab >}}

{{< tab >}}
```typescript
interface RepoWithPermissions {
  id: string;
  name: string;
  canRead: boolean;
  canWrite: boolean;
  canDelete: boolean;
}

// Fetch all permission sets in parallel (NULL limit returns all)
const [readRows, writeRows, deleteRows] = await Promise.all([
  pool.query('SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)', ['user', userId, 'can_read', 'repository', null, null]),
  pool.query('SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)', ['user', userId, 'can_write', 'repository', null, null]),
  pool.query('SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)', ['user', userId, 'can_delete', 'repository', null, null]),
]);

// Build permission sets
const canRead = new Set(readRows.rows.map(r => r.object_id));
const canWrite = new Set(writeRows.rows.map(r => r.object_id));
const canDelete = new Set(deleteRows.rows.map(r => r.object_id));

// Annotate repos with permissions
const result: RepoWithPermissions[] = repos.map(repo => ({
  id: repo.id,
  name: repo.name,
  canRead: canRead.has(repo.id),
  canWrite: canWrite.has(repo.id),
  canDelete: canDelete.has(repo.id),
}));
```
{{< /tab >}}

{{< /tabs >}}

## Performance Characteristics

ListObjects uses a recursive CTE that walks the permission graph in a single query. Performance depends primarily on the **number of accessible objects (results)**, not total tuple count:

| Result Count | Typical Latency |
|--------------|-----------------|
| <10 objects  | 300-500 μs      |
| 10-100       | 1-10 ms         |
| 100-1K       | 10-50 ms        |
| 1K-10K       | 30-200 ms       |
| 10K+         | 200-1000 ms     |

Performance scales with result set size. For large datasets:

1. **Use pagination** - Use `p_limit` to control result size and avoid loading unbounded data
2. **Pre-filter candidates** - If you know the user only cares about certain objects, filter at the application layer first
3. **Cache results** - Cache ListObjects results for repeated queries

## Decision Override Behavior

| Decision | Behavior |
|----------|----------|
| `DecisionUnset` | Normal database query |
| `DecisionDeny` | Returns empty slice (no access) |
| `DecisionAllow` | Falls through to database query |

Note: `DecisionAllow` cannot enumerate "all" objects, so it performs the normal query. If you need to return all objects when in admin mode, query your database directly.

## Caching

`ListObjects` does **not** use the single-tuple permission cache. For caching list results, implement application-level caching:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
type CachedChecker struct {
    *melange.Checker
    listCache *lru.Cache
}

func (c *CachedChecker) ListObjectsAll(ctx context.Context, subject SubjectLike, relation RelationLike, objectType ObjectType) ([]string, error) {
    key := fmt.Sprintf("list:%s:%s:%s:%s",
        subject.FGASubject().Type,
        subject.FGASubject().ID,
        relation.FGARelation(),
        objectType,
    )

    if cached, ok := c.listCache.Get(key); ok {
        return cached.([]string), nil
    }

    ids, err := c.Checker.ListObjectsAll(ctx, subject, relation, objectType)
    if err != nil {
        return nil, err
    }

    c.listCache.Add(key, ids)
    return ids, nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
import { LRUCache } from 'lru-cache';

const listCache = new LRUCache<string, string[]>({
  max: 1000,
  ttl: 60 * 1000, // 1 minute
});

async function listObjectsCached(
  subjectType: string,
  subjectId: string,
  relation: string,
  objectType: string
): Promise<string[]> {
  const key = `list:${subjectType}:${subjectId}:${relation}:${objectType}`;

  const cached = listCache.get(key);
  if (cached) {
    return cached;
  }

  const { rows } = await pool.query(
    'SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
    [subjectType, subjectId, relation, objectType, null, null]
  );
  const ids = rows.map(r => r.object_id);

  listCache.set(key, ids);
  return ids;
}
```
{{< /tab >}}

{{< /tabs >}}

## Common Patterns

### Paginated Access Control

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
func GetAccessibleRepos(ctx context.Context, user User, cursor *string, pageSize int) ([]Repository, *string, error) {
    // Get a page of accessible IDs with built-in pagination
    ids, nextCursor, err := checker.ListObjects(ctx, user, "can_read", "repository",
        melange.PageOptions{Limit: pageSize, After: cursor})
    if err != nil {
        return nil, nil, err
    }

    if len(ids) == 0 {
        return []Repository{}, nil, nil
    }

    // Fetch the repos for this page
    repos, err := db.GetRepositoriesByIDs(ctx, ids)
    return repos, nextCursor, err
}
```
{{< /tab >}}

{{< tab >}}
```typescript
async function getAccessibleRepos(
  userId: string,
  cursor: string | null,
  pageSize: number
): Promise<{ repos: Repository[]; nextCursor: string | null }> {
  // Get a page of accessible IDs with built-in pagination
  const { rows } = await pool.query(
    'SELECT object_id, next_cursor FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
    ['user', userId, 'can_read', 'repository', pageSize, cursor]
  );

  if (rows.length === 0) {
    return { repos: [], nextCursor: null };
  }

  const ids = rows.map(r => r.object_id);
  const nextCursor = rows[rows.length - 1].next_cursor;

  // Fetch the repos for this page
  const repos = await db.getRepositoriesByIds(ids);
  return { repos, nextCursor };
}
```
{{< /tab >}}

{{< /tabs >}}

### Admin Override

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
func GetVisibleRepos(ctx context.Context, user User, isAdmin bool) ([]Repository, error) {
    if isAdmin {
        // Admins see everything
        return db.GetAllRepositories(ctx)
    }

    // Regular users see only accessible repos
    ids, err := checker.ListObjectsAll(ctx, user, "can_read", "repository")
    if err != nil {
        return nil, err
    }

    return db.GetRepositoriesByIDs(ctx, ids)
}
```
{{< /tab >}}

{{< tab >}}
```typescript
async function getVisibleRepos(userId: string, isAdmin: boolean): Promise<Repository[]> {
  if (isAdmin) {
    // Admins see everything
    return db.getAllRepositories();
  }

  // Regular users see only accessible repos (NULL limit returns all)
  const { rows } = await pool.query(
    'SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
    ['user', userId, 'can_read', 'repository', null, null]
  );
  const ids = rows.map(r => r.object_id);

  return db.getRepositoriesByIds(ids);
}
```
{{< /tab >}}

{{< /tabs >}}

## Transaction Consistency

Paginated queries across multiple calls can observe changes between pages. For consistency-critical flows, run paging inside a transaction with repeatable-read or snapshot semantics:

{{< tabs items="Go,SQL" >}}

{{< tab >}}
```go
// For consistent pagination across pages, use a transaction
tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
if err != nil {
    return err
}
defer tx.Rollback()

txChecker := melange.NewChecker(tx)

var allIDs []string
var cursor *string
for {
    ids, next, err := txChecker.ListObjects(ctx, user, "can_read", "document",
        melange.PageOptions{Limit: 100, After: cursor})
    if err != nil {
        return err
    }
    allIDs = append(allIDs, ids...)
    if next == nil {
        break
    }
    cursor = next
}

tx.Commit()
```
{{< /tab >}}

{{< tab >}}
```sql
-- For consistent pagination, use a transaction
BEGIN ISOLATION LEVEL REPEATABLE READ;

-- First page
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);

-- Subsequent pages within the same transaction see consistent data
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, 'doc-100');

COMMIT;
```
{{< /tab >}}

{{< /tabs >}}

## See Also

- [Listing Subjects](./listing-subjects.md) - Find who has access to an object
- [Checking Permissions](./checking-permissions.md) - Single permission checks
