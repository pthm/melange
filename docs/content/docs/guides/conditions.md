---
title: Replacing Conditions
description: "Express OpenFGA conditions with SQL predicates in the tuples view, contextual tuples, and session settings."
weight: 8
---

Melange rejects schemas that use [OpenFGA conditions](../../reference/openfga-compatibility/#conditions). Most conditions can still be expressed, because Melange has two places to put the logic that a CEL expression would hold:

- **The tuples view.** `melange_tuples` is a SQL view over your tables, so a tuple can exist only while a SQL predicate holds. This covers conditions on stored data: expiry times, status flags, stored attributes.
- **Contextual tuples.** Your application decides at request time whether to add a tuple. This covers conditions on request data: client IP, current time, feature flags, token claims.

A third pattern combines the two: the application passes a request value to PostgreSQL as a session setting, and the view compares it against stored data.

| Condition depends on | Put the logic in | Pattern |
|----------------------|------------------|---------|
| Stored data only | The tuples view | [Predicates in the view](#predicates-in-the-view) |
| Request data only | The application | [Contextual tuples](#contextual-tuples) |
| Request data compared with stored data | The tuples view, reading a session setting | [Session settings](#session-settings) |

## Predicates in the view

In OpenFGA, a time-limited grant is a conditional tuple. The schema references the condition, and every tuple write carries the grant time and duration:

```fga
# OpenFGA
type document
  relations
    define viewer: [user with non_expired_grant]

condition non_expired_grant(current_time: timestamp, grant_time: timestamp, grant_duration: duration) {
  current_time < grant_time + grant_duration
}
```

In Melange, the schema drops the condition and the view only produces the tuple while the grant is live:

```fga
type document
  relations
    define viewer: [user]
```

```sql
CREATE VIEW melange_tuples AS
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'viewer' AS relation,
    'document' AS object_type,
    document_id::text AS object_id
FROM document_grants
WHERE expires_at > now();
```

The grant parameters already live in your table, so the caller sends nothing. `now()` is evaluated when the check runs, which means an expired grant stops matching without any cleanup job.

The same approach covers any condition over stored attributes:

```sql
-- Only active members of an organization
FROM organization_members WHERE status = 'active'

-- Only documents that are not archived
FROM documents WHERE archived_at IS NULL

-- Only during a stored access window
FROM shift_assignments WHERE now() BETWEEN starts_at AND ends_at
```

Every check, list, and expand call reads through the view, so the predicate applies to all of them.

## Contextual tuples

Conditions that only depend on the request, such as "the client IP is on the corporate network", can be evaluated in the application. If the condition holds, pass a [contextual tuple](../contextual-tuples/) that satisfies the relation:

```fga
type network
  relations
    define member: [user]

type document
  relations
    define viewer: [user]
    define can_view: viewer and member from network
    define network: [network]
```

```go
var tuples []melange.ContextualTuple
if isCorporateIP(clientIP) {
    tuples = append(tuples, melange.ContextualTuple{
        Subject:  melange.Object{Type: "user", ID: userID},
        Relation: melange.Relation("member"),
        Object:   melange.Object{Type: "network", ID: "corporate"},
    })
}

allowed, err := checker.CheckWithContextualTuples(ctx, user, "can_view", doc, tuples)
```

The condition can use anything the application can compute: time of day, request headers, JWT claims, feature flags, results from another service.

Each call with contextual tuples sets up a temporary view, which adds a few milliseconds per call (see [Performance](../contextual-tuples/#performance)). For hot paths, prefer a session setting.

## Session settings

Some conditions compare a request value with stored data. For example, each user has an allowed CIDR range, and access depends on whether the client IP falls inside it. OpenFGA writes the CIDR as a tuple parameter and the caller passes the IP as context.

In Melange, store the CIDR in a table and pass the IP as a transaction-local setting. The view reads it with `current_setting`:

```sql
CREATE VIEW melange_tuples AS
-- ... other relations ...
UNION ALL
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'member' AS relation,
    'network' AS object_type,
    'corporate' AS object_id
FROM user_allowed_networks
WHERE nullif(current_setting('app.client_ip', true), '')::inet <<= cidr;
```

Set the value in the same transaction as the check:

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback()

if _, err := tx.ExecContext(ctx, "SELECT set_config('app.client_ip', $1, true)", clientIP); err != nil {
    return err
}

allowed, err := melange.NewChecker(tx).Check(ctx, user, "can_view", doc)
```

The `true` argument to `set_config` makes the setting local to the transaction (equivalent to `SET LOCAL`), so it cannot leak to the next user of a pooled connection.

When the setting is missing, the predicate is `NULL` and the tuple is not produced, so the check denies. Two details make that hold:

- `current_setting(name, true)` returns `NULL` instead of raising an error when the setting was never defined.
- After a `SET LOCAL` ends, the same connection returns `''` rather than `NULL`. Without `nullif(..., '')`, the `::inet` cast fails and the check returns an error.

Session settings cost nothing beyond the predicate itself, and the setting is visible to every check in the transaction.

## Common conditions

| OpenFGA condition | Melange approach |
|-------------------|------------------|
| Grant expires after a duration | `WHERE expires_at > now()` in the view |
| Access only within a time window | `WHERE now() BETWEEN starts_at AND ends_at` in the view |
| Client IP in a fixed allowlist | Contextual tuple, computed in the application |
| Client IP in a per-user or per-tenant range | Session setting compared with a stored `cidr` column |
| Resource attribute matches (status, region, tier) | `WHERE` on the attribute column in the view |
| Request attribute matches a stored attribute | Session setting compared with the column |
| Feature flag or entitlement from another service | Contextual tuple |

## Things to know

- **Caching.** The [built-in cache](../caching/) keys on subject, relation, and object only. It does not know about `now()`, session settings, or contextual tuples (calls with contextual tuples bypass it). Do not cache relations that depend on a session setting, and keep the TTL short for relations that depend on time.
- **Missing context denies.** OpenFGA returns an error when a condition's parameters are missing. With the patterns above, a missing value produces no tuple, so the check denies. Make sure the application always sets the values it means to set.
- **The schema no longer shows the rule.** The `.fga` file says `[user]` while the view decides which users qualify. Comment the relation in the schema to point at the view, and test the view with [authorization tests](../testing-authorization/).
- **Indexes.** Predicates like `expires_at > now()` can use a normal B-tree index on `expires_at`. Partial indexes cannot reference `now()` because it is not immutable.
- **Not covered.** A condition evaluated over parameters that only exist on the tuple write, with no table to store them in, needs a table first. Melange has no tuple store of its own.

## Next Steps

- [Tuples View](../../concepts/tuples-view/): view schema, query patterns, and indexes
- [Contextual Tuples](../contextual-tuples/): API and connection requirements
- [OpenFGA Compatibility](../../reference/openfga-compatibility/): supported and unsupported features
