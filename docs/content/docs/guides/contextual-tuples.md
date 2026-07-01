---
title: Contextual Tuples
weight: 6
---

Contextual tuples are temporary tuples injected at check time. They are not persisted to the database and only affect the single check or list call they are passed to.

## When to Use

Contextual tuples are for authorization data that exists at request time but is not stored in your domain tables:

- **IP-based access**: inject an `ip_allowed` tuple based on the client's IP address.
- **Time-based access**: inject a `within_window` tuple based on the current time.
- **Feature flags**: inject a `feature_enabled` tuple based on a feature flag service.
- **Request context**: inject tuples derived from headers, tokens, or session data.

## Go API

```go
tuples := []melange.ContextualTuple{
    {
        Subject:  melange.Object{Type: "user", ID: "alice"},
        Relation: melange.Relation("ip_allowed"),
        Object:   melange.Object{Type: "network", ID: "office"},
    },
}

allowed, err := checker.CheckWithContextualTuples(ctx, user, "can_access", resource, tuples)
```

List operations also support contextual tuples:

```go
ids, cursor, err := checker.ListObjectsWithContextualTuples(ctx, user, "can_read", "document", tuples, page)
ids, cursor, err := checker.ListSubjectsWithContextualTuples(ctx, user, "can_read", "user", tuples, page)
```

If `tuples` is empty, these methods delegate to their non-contextual equivalents.

## SQL-Level Implementation

Contextual tuples work by temporarily shadowing the `melange_tuples` view:

1. A session-scoped temporary table `melange_contextual_tuples` is created.
2. The contextual tuples are inserted into this table.
3. A temporary view `melange_tuples` is created that `UNION ALL`s the base view with the temporary table.
4. The permission check runs against this combined view.
5. Both temporary objects are dropped after the check.

Because PostgreSQL resolves temporary objects before schema-qualified ones, the generated SQL functions see the combined tuples without modification.

## Connection Requirements

Temporary objects in PostgreSQL are session-scoped (tied to a specific connection). The setup, check, and cleanup must all happen on the same connection.

| Querier Type | Contextual Tuples |
|--------------|-------------------|
| `*sql.DB` | Supported. A dedicated connection is acquired from the pool, pinned for the operation, and returned afterward. |
| `*sql.Tx` | Supported. Already on a single connection. Also sees uncommitted changes. |
| `*sql.Conn` | Supported. Already pinned to a single connection. |
| Custom `Querier` | Returns `ErrContextualTuplesUnsupported`. |

## Bulk Check with Contextual Tuples

```go
results, err := checker.NewBulkCheck(ctx).
    Add(user, "can_read", doc1).
    Add(user, "can_write", doc1).
    WithContextualTuples(
        melange.ContextualTuple{
            Subject:  melange.Object{Type: "user", ID: "alice"},
            Relation: melange.Relation("ip_allowed"),
            Object:   melange.Object{Type: "network", ID: "office"},
        },
    ).
    Execute()
```

The contextual tuples are set up once before all checks in the batch and cleaned up afterward.

## Validation

Contextual tuples are validated before setup. With a `Validator` attached to the checker, each tuple is checked against the schema. Without a validator, basic shape validation runs:

- If a subject ID contains `#` (userset format like `group:123#member`), the part after `#` must not be empty.

Invalid tuples return `ErrInvalidContextualTuple` with the tuple index.

## Caching

Checks with contextual tuples bypass the cache entirely. Each call goes directly to the database. This is by design: contextual tuples are unique to each request, so cached results would be incorrect.

## Performance

Contextual tuple checks have additional overhead compared to regular checks:

- ~3-5ms setup per operation (creating temporary table, inserting tuples, creating temporary view).
- Regular check latency on top of setup.
- Cleanup after each operation.

For high-throughput paths, consider whether the authorization data can be modelled in your domain tables instead.

## Example Patterns

### IP Allowlist

Schema:

```fga
type network
  relations
    define ip_allowed: [user]

type resource
  relations
    define can_access: [user] and ip_allowed from network
```

Application code:

```go
tuples := []melange.ContextualTuple{
    {
        Subject:  melange.Object{Type: "user", ID: userID},
        Relation: melange.Relation("ip_allowed"),
        Object:   melange.Object{Type: "network", ID: "corporate"},
    },
}

// Only inject the tuple if the IP is in the allowlist
if isAllowedIP(clientIP) {
    allowed, err = checker.CheckWithContextualTuples(ctx, user, "can_access", resource, tuples)
} else {
    allowed, err = checker.Check(ctx, user, "can_access", resource)
}
```

### Temporal Access

```go
// Grant access only during a maintenance window
var tuples []melange.ContextualTuple
if time.Now().Before(maintenanceEnd) {
    tuples = append(tuples, melange.ContextualTuple{
        Subject:  melange.Object{Type: "user", ID: userID},
        Relation: melange.Relation("maintenance_access"),
        Object:   melange.Object{Type: "system", ID: "prod"},
    })
}

allowed, err := checker.CheckWithContextualTuples(ctx, user, "can_deploy", system, tuples)
```

## Next Steps

- [Checking Permissions](../checking-permissions/): core Checker API
- [Go API](../../reference/go-api/): full method signatures and types
- [Errors](../../reference/errors/): `ErrContextualTuplesUnsupported` and `ErrInvalidContextualTuple`
