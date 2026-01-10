---
title: Listing Objects
weight: 2
---

The `ListObjects` operation returns all objects of a given type that a subject has a specific relation on. This answers the question: "What can this user access?"

## Basic Usage

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Find all repositories user can read
repoIDs, err := checker.ListObjects(ctx,
    authz.User("123"),
    authz.RelCanRead,
    "repository",
)
if err != nil {
    return err
}

// repoIDs = ["repo-1", "repo-456", "repo-789"]
```
{{< /tab >}}

{{< tab >}}
```typescript
// Find all repositories user can read
const { rows } = await pool.query(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', '123', 'can_read', 'repository']
);
const repoIds = rows.map(row => row.object_id);

// repoIds = ["repo-1", "repo-456", "repo-789"]
```
{{< /tab >}}

{{< tab >}}
```sql
-- Get all documents user 123 can view
SELECT * FROM list_accessible_objects('user', '123', 'viewer', 'document');

-- Returns a table with object_id column
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

## Return Value

Returns a list of object IDs that the subject has the relation on. Empty list if no objects found (not an error).

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

// Get IDs the user can access
accessibleIDs, err := checker.ListObjects(ctx, user, "can_read", "repository")
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

// Get IDs the user can access
const { rows } = await pool.query(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', userId, 'can_read', 'repository']
);
const accessibleIds = new Set(rows.map(r => r.object_id));

// Filter to only accessible repos
const visibleRepos = repos.filter(repo => accessibleIds.has(repo.id));
```
{{< /tab >}}

{{< tab >}}
```sql
-- Join with domain table to get full records
SELECT d.*
FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document') a
    ON d.id::text = a.object_id;
```
{{< /tab >}}

{{< /tabs >}}

### Fetch Only Accessible Resources

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Get accessible IDs first
ids, err := checker.ListObjects(ctx, user, "can_read", "document")
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
// Get accessible IDs first
const { rows } = await pool.query(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', userId, 'can_read', 'document']
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
SELECT COUNT(*) FROM list_accessible_objects('user', '123', 'viewer', 'document');
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

// Fetch all permission sets in parallel
var (
    readIDs, writeIDs, deleteIDs []string
    readErr, writeErr, deleteErr error
)

var wg sync.WaitGroup
wg.Add(3)

go func() {
    defer wg.Done()
    readIDs, readErr = checker.ListObjects(ctx, user, "can_read", "repository")
}()
go func() {
    defer wg.Done()
    writeIDs, writeErr = checker.ListObjects(ctx, user, "can_write", "repository")
}()
go func() {
    defer wg.Done()
    deleteIDs, deleteErr = checker.ListObjects(ctx, user, "can_delete", "repository")
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

// Fetch all permission sets in parallel
const [readRows, writeRows, deleteRows] = await Promise.all([
  pool.query('SELECT * FROM list_accessible_objects($1, $2, $3, $4)', ['user', userId, 'can_read', 'repository']),
  pool.query('SELECT * FROM list_accessible_objects($1, $2, $3, $4)', ['user', userId, 'can_write', 'repository']),
  pool.query('SELECT * FROM list_accessible_objects($1, $2, $3, $4)', ['user', userId, 'can_delete', 'repository']),
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

ListObjects uses a recursive CTE that walks the permission graph in a single query:

| Tuple Count | Latency |
|-------------|---------|
| 1K tuples   | ~2.3ms  |
| 10K tuples  | ~23ms   |
| 100K tuples | ~192ms  |
| 1M tuples   | ~1.5s   |

Performance scales linearly with the number of tuples. For large datasets:

1. **Pre-filter candidates** - If you know the user only cares about certain objects, filter at the application layer first
2. **Use pagination** - Limit results and paginate through large sets
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

func (c *CachedChecker) ListObjects(ctx context.Context, subject SubjectLike, relation RelationLike, objectType ObjectType) ([]string, error) {
    key := fmt.Sprintf("list:%s:%s:%s:%s",
        subject.FGASubject().Type,
        subject.FGASubject().ID,
        relation.FGARelation(),
        objectType,
    )

    if cached, ok := c.listCache.Get(key); ok {
        return cached.([]string), nil
    }

    ids, err := c.Checker.ListObjects(ctx, subject, relation, objectType)
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
    'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
    [subjectType, subjectId, relation, objectType]
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
func GetAccessibleRepos(ctx context.Context, user User, page, pageSize int) ([]Repository, error) {
    // Get all accessible IDs
    allIDs, err := checker.ListObjects(ctx, user, "can_read", "repository")
    if err != nil {
        return nil, err
    }

    // Paginate the IDs
    start := page * pageSize
    if start >= len(allIDs) {
        return []Repository{}, nil
    }
    end := start + pageSize
    if end > len(allIDs) {
        end = len(allIDs)
    }
    pageIDs := allIDs[start:end]

    // Fetch only the page of repos
    return db.GetRepositoriesByIDs(ctx, pageIDs)
}
```
{{< /tab >}}

{{< tab >}}
```typescript
async function getAccessibleRepos(
  userId: string,
  page: number,
  pageSize: number
): Promise<Repository[]> {
  // Get all accessible IDs
  const { rows } = await pool.query(
    'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
    ['user', userId, 'can_read', 'repository']
  );
  const allIds = rows.map(r => r.object_id);

  // Paginate the IDs
  const start = page * pageSize;
  if (start >= allIds.length) {
    return [];
  }
  const pageIds = allIds.slice(start, start + pageSize);

  // Fetch only the page of repos
  return db.getRepositoriesByIds(pageIds);
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
    ids, err := checker.ListObjects(ctx, user, "can_read", "repository")
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

  // Regular users see only accessible repos
  const { rows } = await pool.query(
    'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
    ['user', userId, 'can_read', 'repository']
  );
  const ids = rows.map(r => r.object_id);

  return db.getRepositoriesByIds(ids);
}
```
{{< /tab >}}

{{< /tabs >}}

## See Also

- [Listing Subjects](./listing-subjects.md) - Find who has access to an object
- [Checking Permissions](./checking-permissions.md) - Single permission checks
