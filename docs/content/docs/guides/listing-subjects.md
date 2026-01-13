---
title: Listing Subjects
weight: 3
---

The `ListSubjects` operation returns all subjects of a given type that have a specific relation on an object. This answers the question: "Who has access to this resource?"

## Basic Usage

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Find all users who can read a repository (with pagination)
userIDs, cursor, err := checker.ListSubjects(ctx,
    authz.Repository("456"),
    authz.RelCanRead,
    "user",
    melange.PageOptions{Limit: 100},
)
if err != nil {
    return err
}

// userIDs = ["alice", "bob", "carol"]
// cursor = nil when no more pages, or a string to fetch the next page
```
{{< /tab >}}

{{< tab >}}
```typescript
// Find all users who can read a repository (with pagination)
const { rows } = await pool.query(
  'SELECT subject_id, next_cursor FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
  ['repository', '456', 'can_read', 'user', 100, null]
);
const userIds = rows.map(row => row.subject_id);
const nextCursor = rows.length > 0 ? rows[0].next_cursor : null;

// userIds = ["alice", "bob", "carol"]
```
{{< /tab >}}

{{< tab >}}
```sql
-- Get all users who can view document 456 (first 100)
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'user', 100, NULL);

-- Returns a table with subject_id and next_cursor columns
-- next_cursor is NULL when no more pages exist
```
{{< /tab >}}

{{< /tabs >}}

## Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `object_type` | `text` | Type of the object (e.g., `'repository'`) |
| `object_id` | `text` | ID of the object |
| `relation` | `text` | The relation to check |
| `subject_type` | `text` | Type of subjects to return |
| `p_limit` | `int` | Maximum number of results per page (NULL = no limit) |
| `p_after` | `text` | Cursor from previous page (NULL = start from beginning) |

## Return Value

Returns a table with `subject_id` and `next_cursor` columns. The `next_cursor` value is repeated on every row for convenience - use the last row's cursor to fetch the next page. Returns an empty result set if no subjects found (not an error).

{{< callout type="info" >}}
**Ordering**: Results are ordered with wildcard subjects (`'*'`) first, then alphabetically by `subject_id`. This ensures stable pagination while keeping wildcard entries grouped at the top.
{{< /callout >}}

## Pagination

### Cursor-Based Pagination

Use cursor-based pagination to iterate through large result sets efficiently:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Paginate through all users with access
var cursor *string
for {
    ids, next, err := checker.ListSubjects(ctx,
        authz.Repository("456"),
        authz.RelCanRead,
        "user",
        melange.PageOptions{Limit: 100, After: cursor},
    )
    if err != nil {
        return err
    }

    for _, id := range ids {
        // Process each user ID
        fmt.Println("Has access:", id)
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
// Paginate through all users with access
let cursor: string | null = null;

while (true) {
  const { rows } = await pool.query(
    'SELECT subject_id, next_cursor FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
    ['repository', repoId, 'can_read', 'user', 100, cursor]
  );

  for (const row of rows) {
    // Process each user ID
    console.log('Has access:', row.subject_id);
  }

  cursor = rows.length > 0 ? rows[rows.length - 1].next_cursor : null;
  if (!cursor) break; // No more pages
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- First page
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'user', 100, NULL);

-- Returns: subject_id | next_cursor
--          *          | user-100   (wildcard first)
--          alice      | user-100
--          bob        | user-100
--          ...

-- Next page (use the next_cursor value)
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'user', 100, 'user-100');
```
{{< /tab >}}

{{< /tabs >}}

### Fetching All Results

For convenience, use the `ListSubjectsAll` helper which automatically paginates through all results:

{{< tabs items="Go" >}}

{{< tab >}}
```go
// Get all users who can read (auto-paginates internally)
allUserIDs, err := checker.ListSubjectsAll(ctx,
    authz.Repository("456"),
    authz.RelCanRead,
    "user",
)
if err != nil {
    return err
}
// allUserIDs contains all user IDs with access
```
{{< /tab >}}

{{< /tabs >}}

{{< callout type="warning" >}}
**Use with caution**: `ListSubjectsAll` loads all IDs into memory. For large datasets, prefer paginated queries with `ListSubjects` to control memory usage.
{{< /callout >}}

## Examples

### Show Access List

Display who has access to a resource:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
func GetAccessList(ctx context.Context, repo Repository) ([]AccessEntry, error) {
    var entries []AccessEntry

    // Get users with each permission level
    permissions := []string{"owner", "admin", "can_write", "can_read"}

    for _, perm := range permissions {
        userIDs, err := checker.ListSubjectsAll(ctx, repo, perm, "user")
        if err != nil {
            return nil, err
        }

        for _, id := range userIDs {
            entries = append(entries, AccessEntry{
                UserID:     id,
                Permission: perm,
            })
        }
    }

    return entries, nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
interface AccessEntry {
  userId: string;
  permission: string;
}

async function getAccessList(repoId: string): Promise<AccessEntry[]> {
  const permissions = ['owner', 'admin', 'can_write', 'can_read'];
  const entries: AccessEntry[] = [];

  for (const perm of permissions) {
    const { rows } = await pool.query(
      'SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
      ['repository', repoId, perm, 'user', null, null]
    );

    for (const row of rows) {
      entries.push({
        userId: row.subject_id,
        permission: perm,
      });
    }
  }

  return entries;
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- Join with users table to get full user records (NULL limit returns all)
SELECT u.*
FROM users u
JOIN list_accessible_subjects('document', '456', 'viewer', 'user', NULL, NULL) a
    ON u.id::text = a.subject_id;
```
{{< /tab >}}

{{< /tabs >}}

### Check for Any Access

Verify at least one user has access:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
func HasAnyViewers(ctx context.Context, doc Document) (bool, error) {
    // Use Limit: 1 to efficiently check for any results
    viewers, _, err := checker.ListSubjects(ctx, doc, "can_read", "user",
        melange.PageOptions{Limit: 1})
    if err != nil {
        return false, err
    }
    return len(viewers) > 0, nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
async function hasAnyViewers(docId: string): Promise<boolean> {
  // Use limit 1 to efficiently check for any results
  const { rows } = await pool.query(
    'SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6) LIMIT 1',
    ['document', docId, 'can_read', 'user', 1, null]
  );
  return rows.length > 0;
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- Check if anyone has access (returns true/false)
SELECT EXISTS(
    SELECT 1 FROM list_accessible_subjects('document', '456', 'viewer', 'user', 1, NULL)
);
```
{{< /tab >}}

{{< /tabs >}}

### Notify All Users with Access

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
func NotifyCollaborators(ctx context.Context, repo Repository, message string) error {
    // Get all users who can read (using ListSubjectsAll)
    userIDs, err := checker.ListSubjectsAll(ctx, repo, "can_read", "user")
    if err != nil {
        return err
    }

    for _, userID := range userIDs {
        if err := notificationService.Send(ctx, userID, message); err != nil {
            log.Printf("Failed to notify user %s: %v", userID, err)
        }
    }

    return nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
async function notifyCollaborators(repoId: string, message: string): Promise<void> {
  // Get all users who can read (NULL limit returns all)
  const { rows } = await pool.query(
    'SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
    ['repository', repoId, 'can_read', 'user', null, null]
  );

  for (const row of rows) {
    try {
      await notificationService.send(row.subject_id, message);
    } catch (err) {
      console.error(`Failed to notify user ${row.subject_id}:`, err);
    }
  }
}
```
{{< /tab >}}

{{< /tabs >}}

### Audit Access

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
type AccessAuditEntry struct {
    ObjectType string
    ObjectID   string
    UserID     string
    Permission string
    Timestamp  time.Time
}

func AuditRepositoryAccess(ctx context.Context, repo Repository) ([]AccessAuditEntry, error) {
    var audit []AccessAuditEntry

    permissions := []string{"owner", "admin", "can_write", "can_read"}

    for _, perm := range permissions {
        userIDs, err := checker.ListSubjectsAll(ctx, repo, perm, "user")
        if err != nil {
            return nil, err
        }

        for _, userID := range userIDs {
            audit = append(audit, AccessAuditEntry{
                ObjectType: "repository",
                ObjectID:   fmt.Sprint(repo.ID),
                UserID:     userID,
                Permission: perm,
                Timestamp:  time.Now(),
            })
        }
    }

    return audit, nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
interface AccessAuditEntry {
  objectType: string;
  objectId: string;
  userId: string;
  permission: string;
  timestamp: Date;
}

async function auditRepositoryAccess(repoId: string): Promise<AccessAuditEntry[]> {
  const permissions = ['owner', 'admin', 'can_write', 'can_read'];
  const audit: AccessAuditEntry[] = [];

  for (const perm of permissions) {
    const { rows } = await pool.query(
      'SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
      ['repository', repoId, perm, 'user', null, null]
    );

    for (const row of rows) {
      audit.push({
        objectType: 'repository',
        objectId: repoId,
        userId: row.subject_id,
        permission: perm,
        timestamp: new Date(),
      });
    }
  }

  return audit;
}
```
{{< /tab >}}

{{< /tabs >}}

## Performance Characteristics

ListSubjects uses a recursive CTE similar to ListObjects:

| Tuple Count | Latency |
|-------------|---------|
| 1K tuples   | ~708us  |
| 10K tuples  | ~6.3ms  |
| 100K tuples | ~42ms   |
| 1M tuples   | ~864ms  |

ListSubjects is typically faster than ListObjects because it starts from a specific object rather than searching across all objects.

## Decision Override Behavior

| Decision | Behavior |
|----------|----------|
| `DecisionUnset` | Normal database query |
| `DecisionDeny` | Returns empty slice (no subjects have access) |
| `DecisionAllow` | Falls through to database query |

Note: `DecisionAllow` cannot enumerate "all" subjects, so it performs the normal query.

## Caching

Like `ListObjects`, `ListSubjects` does **not** use the permission cache. Implement application-level caching if needed:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
func (c *CachedChecker) ListSubjectsAll(ctx context.Context, object ObjectLike, relation RelationLike, subjectType ObjectType) ([]string, error) {
    key := fmt.Sprintf("subjects:%s:%s:%s:%s",
        object.FGAObject().Type,
        object.FGAObject().ID,
        relation.FGARelation(),
        subjectType,
    )

    if cached, ok := c.cache.Get(key); ok {
        return cached.([]string), nil
    }

    ids, err := c.Checker.ListSubjectsAll(ctx, object, relation, subjectType)
    if err != nil {
        return nil, err
    }

    c.cache.Add(key, ids)
    return ids, nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
import { LRUCache } from 'lru-cache';

const subjectCache = new LRUCache<string, string[]>({
  max: 1000,
  ttl: 60 * 1000, // 1 minute
});

async function listSubjectsCached(
  objectType: string,
  objectId: string,
  relation: string,
  subjectType: string
): Promise<string[]> {
  const key = `subjects:${objectType}:${objectId}:${relation}:${subjectType}`;

  const cached = subjectCache.get(key);
  if (cached) {
    return cached;
  }

  const { rows } = await pool.query(
    'SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
    [objectType, objectId, relation, subjectType, null, null]
  );
  const ids = rows.map(r => r.subject_id);

  subjectCache.set(key, ids);
  return ids;
}
```
{{< /tab >}}

{{< /tabs >}}

## Common Patterns

### Permission Comparison

Compare who has what level of access:

{{< tabs items="Go,TypeScript" >}}

{{< tab >}}
```go
type PermissionBreakdown struct {
    Owners  []string
    Admins  []string
    Writers []string
    Readers []string
}

func GetPermissionBreakdown(ctx context.Context, repo Repository) (*PermissionBreakdown, error) {
    var (
        owners, admins, writers, readers []string
        ownerErr, adminErr, writerErr, readerErr error
    )

    var wg sync.WaitGroup
    wg.Add(4)

    go func() {
        defer wg.Done()
        owners, ownerErr = checker.ListSubjectsAll(ctx, repo, "owner", "user")
    }()
    go func() {
        defer wg.Done()
        admins, adminErr = checker.ListSubjectsAll(ctx, repo, "admin", "user")
    }()
    go func() {
        defer wg.Done()
        writers, writerErr = checker.ListSubjectsAll(ctx, repo, "can_write", "user")
    }()
    go func() {
        defer wg.Done()
        readers, readerErr = checker.ListSubjectsAll(ctx, repo, "can_read", "user")
    }()

    wg.Wait()

    // Check errors...

    return &PermissionBreakdown{
        Owners:  owners,
        Admins:  admins,
        Writers: writers,
        Readers: readers,
    }, nil
}
```
{{< /tab >}}

{{< tab >}}
```typescript
interface PermissionBreakdown {
  owners: string[];
  admins: string[];
  writers: string[];
  readers: string[];
}

async function getPermissionBreakdown(repoId: string): Promise<PermissionBreakdown> {
  // Fetch all permission sets in parallel (NULL limit returns all)
  const [ownersRes, adminsRes, writersRes, readersRes] = await Promise.all([
    pool.query('SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)', ['repository', repoId, 'owner', 'user', null, null]),
    pool.query('SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)', ['repository', repoId, 'admin', 'user', null, null]),
    pool.query('SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)', ['repository', repoId, 'can_write', 'user', null, null]),
    pool.query('SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)', ['repository', repoId, 'can_read', 'user', null, null]),
  ]);

  return {
    owners: ownersRes.rows.map(r => r.subject_id),
    admins: adminsRes.rows.map(r => r.subject_id),
    writers: writersRes.rows.map(r => r.subject_id),
    readers: readersRes.rows.map(r => r.subject_id),
  };
}
```
{{< /tab >}}

{{< /tabs >}}

### Team Members via Object

When teams are modeled as objects:

{{< tabs items="Go,TypeScript,SQL" >}}

{{< tab >}}
```go
// Schema: type team
//           relations
//             define member: [user]

func GetTeamMembers(ctx context.Context, teamID string) ([]User, error) {
    team := melange.Object{Type: "team", ID: teamID}

    memberIDs, err := checker.ListSubjectsAll(ctx, team, "member", "user")
    if err != nil {
        return nil, err
    }

    return db.GetUsersByIDs(ctx, memberIDs)
}
```
{{< /tab >}}

{{< tab >}}
```typescript
// Schema: type team
//           relations
//             define member: [user]

async function getTeamMembers(teamId: string): Promise<User[]> {
  const { rows } = await pool.query(
    'SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4, $5, $6)',
    ['team', teamId, 'member', 'user', null, null]
  );
  const memberIds = rows.map(r => r.subject_id);

  return db.getUsersByIds(memberIds);
}
```
{{< /tab >}}

{{< tab >}}
```sql
-- Get all team members with access (userset filter)
-- Note: Use 'team#member' as subject_type to filter by userset
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'team#member', NULL, NULL);
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
    ids, next, err := txChecker.ListSubjects(ctx, repo, "can_read", "user",
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
SELECT subject_id, next_cursor
FROM list_accessible_subjects('repository', '456', 'can_read', 'user', 100, NULL);

-- Subsequent pages within the same transaction see consistent data
SELECT subject_id, next_cursor
FROM list_accessible_subjects('repository', '456', 'can_read', 'user', 100, 'user-100');

COMMIT;
```
{{< /tab >}}

{{< /tabs >}}

## See Also

- [Listing Objects](./listing-objects.md) - Find what objects a subject can access
- [Checking Permissions](./checking-permissions.md) - Single permission checks
