---
title: Listing Subjects
weight: 3
---

The `ListSubjects` method returns all subjects of a given type that have a specific relation on an object. This answers the question: "Who has access to this resource?"

## Basic Usage

```go
// Find all users who can read a repository
userIDs, err := checker.ListSubjects(ctx,
    authz.Repository("456"),
    authz.RelCanRead,
    "user",
)
if err != nil {
    return err
}

// userIDs = ["alice", "bob", "carol"]
```

## Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `ctx` | `context.Context` | Request context |
| `object` | `ObjectLike` | The object (what) |
| `relation` | `RelationLike` | The relation to check |
| `subjectType` | `ObjectType` | Type of subjects to return |

## Return Value

Returns `([]string, error)`:
- Slice of subject IDs that have the relation on the object
- Empty slice if no subjects found (not an error)
- Error if database query fails

## Examples

### Show Access List

Display who has access to a resource:

```go
func GetAccessList(ctx context.Context, repo Repository) ([]AccessEntry, error) {
    var entries []AccessEntry

    // Get users with each permission level
    permissions := []string{"owner", "admin", "can_write", "can_read"}

    for _, perm := range permissions {
        userIDs, err := checker.ListSubjects(ctx, repo, perm, "user")
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

### Check for Any Access

Verify at least one user has access:

```go
func HasAnyViewers(ctx context.Context, doc Document) (bool, error) {
    viewers, err := checker.ListSubjects(ctx, doc, "can_read", "user")
    if err != nil {
        return false, err
    }
    return len(viewers) > 0, nil
}
```

### Notify All Users with Access

```go
func NotifyCollaborators(ctx context.Context, repo Repository, message string) error {
    // Get all users who can read
    userIDs, err := checker.ListSubjects(ctx, repo, "can_read", "user")
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

### Audit Access

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
        userIDs, err := checker.ListSubjects(ctx, repo, perm, "user")
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

When using decision overrides:

| Decision | Behavior |
|----------|----------|
| `DecisionUnset` | Normal database query |
| `DecisionDeny` | Returns empty slice (no subjects have access) |
| `DecisionAllow` | Falls through to database query |

Note: `DecisionAllow` cannot enumerate "all" subjects, so it performs the normal query.

## Caching

Like `ListObjects`, `ListSubjects` does **not** use the permission cache. Implement application-level caching if needed:

```go
func (c *CachedChecker) ListSubjects(ctx context.Context, object ObjectLike, relation RelationLike, subjectType ObjectType) ([]string, error) {
    key := fmt.Sprintf("subjects:%s:%s:%s:%s",
        object.FGAObject().Type,
        object.FGAObject().ID,
        relation.FGARelation(),
        subjectType,
    )

    if cached, ok := c.cache.Get(key); ok {
        return cached.([]string), nil
    }

    ids, err := c.Checker.ListSubjects(ctx, object, relation, subjectType)
    if err != nil {
        return nil, err
    }

    c.cache.Add(key, ids)
    return ids, nil
}
```

## Common Patterns

### Permission Comparison

Compare who has what level of access:

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
        owners, ownerErr = checker.ListSubjects(ctx, repo, "owner", "user")
    }()
    go func() {
        defer wg.Done()
        admins, adminErr = checker.ListSubjects(ctx, repo, "admin", "user")
    }()
    go func() {
        defer wg.Done()
        writers, writerErr = checker.ListSubjects(ctx, repo, "can_write", "user")
    }()
    go func() {
        defer wg.Done()
        readers, readerErr = checker.ListSubjects(ctx, repo, "can_read", "user")
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

### Team Members via Object

When teams are modeled as objects:

```go
// Schema: type team
//           relations
//             define member: [user]

func GetTeamMembers(ctx context.Context, teamID string) ([]User, error) {
    team := melange.Object{Type: "team", ID: teamID}

    memberIDs, err := checker.ListSubjects(ctx, team, "member", "user")
    if err != nil {
        return nil, err
    }

    return db.GetUsersByIDs(ctx, memberIDs)
}
```

## See Also

- [Listing Objects](./listing-objects.md) - Find what objects a subject can access
- [Checking Permissions](./checking-permissions.md) - Single permission checks
