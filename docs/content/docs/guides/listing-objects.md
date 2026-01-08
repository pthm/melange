---
title: Listing Objects
weight: 2
---

The `ListObjects` method returns all objects of a given type that a subject has a specific relation on. This answers the question: "What can this user access?"

## Basic Usage

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

## Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `ctx` | `context.Context` | Request context |
| `subject` | `SubjectLike` | The subject (who) |
| `relation` | `RelationLike` | The relation to check |
| `objectType` | `ObjectType` | Type of objects to return |

## Return Value

Returns `([]string, error)`:
- Slice of object IDs that the subject has the relation on
- Empty slice if no objects found (not an error)
- Error if database query fails

## Examples

### Filter a List of Resources

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

### Fetch Only Accessible Resources

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

### Check Multiple Permissions

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

When using decision overrides:

| Decision | Behavior |
|----------|----------|
| `DecisionUnset` | Normal database query |
| `DecisionDeny` | Returns empty slice (no access) |
| `DecisionAllow` | Falls through to database query |

Note: `DecisionAllow` cannot enumerate "all" objects, so it performs the normal query. If you need to return all objects when in admin mode, query your database directly.

## Caching

`ListObjects` does **not** use the permission cache (configured via `WithCache`). The cache is designed for single-tuple checks, not list operations.

For caching list results, implement application-level caching:

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

## Common Patterns

### Paginated Access Control

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

### Admin Override

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

## See Also

- [Listing Subjects](./listing-subjects.md) - Find who has access to an object
- [Checking Permissions](./checking-permissions.md) - Single permission checks
