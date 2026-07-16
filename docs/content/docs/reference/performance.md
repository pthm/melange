---
title: Performance
weight: 4
---

Melange compiles permission checks into specialized SQL functions. This page documents measured latency and scaling behavior.

## Summary

- `check`, `expand`, `explain`, and bulk check run in constant time regardless of dataset size.
- `list_accessible_objects` cost scales with the number of matching objects and path depth, not total tuple count.
- `list_accessible_subjects` cost scales with the number of subjects that have access. For broad relations over a large user base this reaches seconds.

## Methodology

Numbers come from the kitchen-sink benchmark (`just bench-kitchensink`), a single model that exercises every generator code path (all seven relation features and all seven list strategies) across four dataset sizes.

- Hardware: Apple M2 Pro, PostgreSQL 16, local connection.
- Measured: latency of a single generated function call (`SELECT check_permission(...)`, `SELECT * FROM list_accessible_objects(...)`) over one pooled connection. This is the database cost only.
- Averaging: mean of 6 runs (4 runs at the 1M scale). Run-to-run variance is ±1-8%.

These are database-side latencies. The Go checker adds input validation, parameter binding, and connection-pool acquisition: add roughly 150-300 µs to the check numbers, plus the network round-trip to Postgres. The [in-memory cache](#caching) removes the database round-trip for repeated checks (~80 ns).

## Model

The kitchen-sink schema exercises the following relation shapes:

| Type                      | Relations                                                                                    |
| ------------------------- | -------------------------------------------------------------------------------------------- |
| `user`, `service_account` | Two concrete subject principals.                                                             |
| `group`                   | Self-referential userset (`member: [group#member]`), wildcard, and exclusion (`active_member`). |
| `organization`            | Role hierarchy (`owner`, `admin`, `member`) and userset.                                      |
| `team`, `project`         | Cross-type TTU (`member from org`), multi-type wildcards, computed unions.                    |
| `folder`                  | Self-referential recursive TTU (`viewer from parent`), cross-type anchor, intersection.       |
| `document`                | Direct, userset, TTU, implied, and wildcard grants, plus every intersection and exclusion shape. |
| `report`, `comment`       | Pure-TTU and pure-userset (Composed strategy), and closure-inherited exclusion.               |

## Scales

Each scale grows users, groups, orgs, folder depth, and documents together. Scale names are round-number labels; actual tuple counts are shown.

| Scale    | Users  | Orgs | Groups | Group chain | Folder depth | Tuples  |
| -------- | ------ | ---- | ------ | ----------- | ------------ | ------- |
| **1K**   | 200    | 5    | 40     | 4           | 4            | 3,824   |
| **10K**  | 1,000  | 15   | 120    | 5           | 5            | 25,386  |
| **100K** | 5,000  | 40   | 400    | 6           | 6            | 155,692 |
| **1M**   | 25,000 | 100  | 2,000  | 6           | 7            | 840,877 |

## Check, expand, explain, bulk

These operations resolve a single question in constant time. Latency is flat across scales. Check is measured for four query-subject types: a plain user with deep inherited access, a userset-typed subject (`group:g#member`), a service account, and a wildcard grant.

`check_permission` (µs):

| Query subject                    | 1K  | 10K | 100K | 1M  | Scaling |
| -------------------------------- | --- | --- | ---- | --- | ------- |
| Plain user (deep inherited)      | 128 | 149 | 150  | 127 | O(1)    |
| Userset subject (`group#member`) | 131 | 155 | 159  | 146 | O(1)    |
| Service account                  | 115 | 135 | 137  | 116 | O(1)    |
| Wildcard grant                   | 126 | 148 | 148  | 128 | O(1)    |

Other single-answer surfaces (µs):

| Operation                          | 1K  | 10K | 100K | 1M  | Scaling |
| ---------------------------------- | --- | --- | ---- | --- | ------- |
| `expand_permission` (userset tree) | 135 | 138 | 136  | 127 | O(1)    |
| `explain_permission` (trace)       | 286 | 296 | 287  | 290 | O(1)    |
| `check_permission_bulk`            | 292 | 307 | 291  | 294 | O(1)    |

Each check allocates ~1 KiB and 21 allocations at every scale. There is no runtime graph traversal whose cost grows with the data.

## ListObjects

`list_accessible_objects` returns every object a subject can reach. Cost depends on the number of matching objects and path depth, not on total tuple count. A query with a small result set stays fast at every scale. A query that walks a large recursive graph grows with it.

`list_accessible_objects`, averaged, sorted by cost:

| Query                                        | 1K     | 10K    | 100K   | 1M     | Scaling  |
| -------------------------------------------- | ------ | ------ | ------ | ------ | -------- |
| Service account to groups (few results)      | 166 µs | 178 µs | 170 µs | 348 µs | ~O(1)    |
| Userset subject to orgs (`group#member`)     | 878 µs | 4.0 ms | 21 ms  | 121 ms | O(paths) |
| Folder viewer (recursive TTU)                | 930 µs | 2.5 ms | 4.5 ms | 15 ms  | O(paths) |
| Document editor (userset and TTU)            | 1.0 ms | 1.7 ms | 5.5 ms | 25 ms  | O(paths) |
| Document `can_edit` (intersection)           | 1.1 ms | 2.0 ms | 5.6 ms | 25 ms  | O(paths) |
| Document `gated` (intersection)              | 3.3 ms | 7.6 ms | 22 ms  | 104 ms | O(paths) |
| Document `can_view` (large union, wildcard)  | 6.2 ms | 15 ms  | 45 ms  | 212 ms | O(paths) |

The service-account query returns few objects and stays ~170 µs from 4K to 840K tuples. `can_view` unions direct grants, userset membership, recursive folder inheritance, and a public wildcard, so it grows with the number of accessible paths. Intersections cost more than unions because each candidate object is validated against multiple sub-relations.

## ListSubjects

`list_accessible_subjects` returns every subject with access to an object. For a broadly-granted relation this enumerates a large fraction of the user base. Cost scales with the number of subjects, not with the requested page size.

`list_accessible_subjects`, all users who can view a document:

| Query                                 | 1K    | 10K    | 100K  | 1M     | Scaling     |
| ------------------------------------- | ----- | ------ | ----- | ------ | ----------- |
| `viewer` (recursive, all paths)       | 41 ms | 206 ms | 1.4 s | 16.8 s | O(subjects) |
| `can_view` (`viewer but not blocked`) | 60 ms | 268 ms | 1.6 s | 17.8 s | O(subjects) |

At 1M tuples and 25,000 users, listing every subject of a deeply-inherited relation takes seconds. Resolving "who can see this" over a recursive, wildcard-bearing relation must expand the whole reachable subject graph. Use broad `ListSubjects` for batch and offline work, not interactive request paths. For "does this user have access", use `check`.

Pagination does not reduce this cost. The database walks the full permission graph to produce an ordered page regardless of `LIMIT`. Page size affects payload size, not query time.

## Validation

Invalid requests are rejected in the Go checker before any database query:

| Validation               | Latency |
| ------------------------ | ------- |
| Invalid relation         | ~25 ns  |
| Invalid type             | ~30 ns  |
| Invalid user format      | ~30 ns  |
| Invalid contextual tuple | ~25 ns  |

## Parallel checks

Checks are independent. On a 12-core machine, throughput scales about 3-4x over sequential checks.

## Caching

The optional in-memory cache removes the database round-trip for repeated checks:

| Scenario              | Latency | Ratio  |
| --------------------- | ------- | ------ |
| Cold (database query) | ~130 µs | 1x     |
| Warm (memory lookup)  | ~80 ns  | ~1,600x |

```go
cache := melange.NewCache(
    melange.WithTTL(time.Minute),
)
checker := melange.NewChecker(db, melange.WithCache(cache))
```

Use caching when:

- Read-to-write ratio is high (permissions checked often, changed rarely).
- A staleness window is acceptable (typically 30s-5min).
- Memory is available for cache storage.

Skip caching when:

- Permissions change frequently.
- Real-time consistency is required.
- Memory is constrained.

## Optimization

### Prefer check over list on interactive paths

Check is constant time (~130 µs database, ~350-450 µs end-to-end). If a request can be phrased as "does this subject have access", use `check`. Reserve `ListSubjects` and large `ListObjects` for batch, export, and admin surfaces.

### Schema shape

Schema shape is the main driver of list cost:

| Tier      | Patterns                                       | List behavior                       |
| --------- | ---------------------------------------------- | ----------------------------------- |
| Cheap     | Direct, computed userset, union, wildcards     | Scales only with result count.      |
| Moderate  | TTU, exclusion, userset references             | Adds a traversal per path.          |
| Expensive | Intersection, recursive TTU, deep union chains | Multiplies work per candidate.      |

Prefer union over intersection where the semantics allow. An intersection validates every candidate against multiple sub-relations:

```fga
# Intersection: gated ranges 3-100 ms for ListObjects across scales
define viewer: writer and editor

# Union is cheaper
define viewer: writer or editor
```

Prefer shallow hierarchies. Deep, self-referential `... from parent` chains drive the recursive CTE and dominate list latency at scale.

### Avoid runtime contextual tuples on hot paths

Contextual tuples add temporary-table setup per call. Use stored tuples where possible, and batch checks that share a contextual set.

### Index tuples view source tables

Index the tables behind the `melange_tuples` view, including expression indexes for any `::text` id casts. Without them, PostgreSQL falls back to sequential scans:

```sql
-- Composite index covering the columns the view selects
CREATE INDEX idx_org_members_lookup
    ON organization_members (organization_id, role, user_id);

-- Expression index for text id conversion
CREATE INDEX idx_org_members_text
    ON organization_members ((organization_id::text), (user_id::text));
```

See [Tuples View](../../concepts/tuples-view/#performance-optimization) for indexing guidance.

### Paginate large ListObjects results

For `list_accessible_objects` with many results, use cursor-based pagination to bound payload size:

```go
objects, cursor, err := checker.ListObjects(ctx, user, "viewer", "document",
    melange.PageOptions{Limit: 50})
nextPage, nextCursor, err := checker.ListObjects(ctx, user, "viewer", "document",
    melange.PageOptions{Limit: 50, After: cursor})
```

Page size affects response size, not query time. Use small pages (10-100) for interactive APIs.

### Monitor query performance

Use `EXPLAIN ANALYZE`, or the generated `explain_*` functions, to find sequential scans, stale statistics, or deep nested loops:

```sql
EXPLAIN ANALYZE SELECT check_permission('user', '123', 'viewer', 'document', '456');
```

## Comparison

| Approach                   | Check latency | Consistency   | Scaling             |
| -------------------------- | ------------- | ------------- | ------------------- |
| Melange (in-database)      | ~130 µs       | Transactional | O(1) for checks     |
| OpenFGA (external service) | 1-5 ms        | Eventual      | O(1) for checks     |
| Application-level RBAC     | 10-100 µs     | Varies        | O(roles)            |
| Direct SQL queries         | 50-300 µs     | Transactional | O(query complexity) |

Melange runs checks inside the database transaction with latency comparable to application-level RBAC, while supporting Zanzibar-style relation modeling.

## Further reading

- [Tuples View](../../concepts/tuples-view/): indexing and scaling strategies.
- [Contributing: Benchmarking](../../contributing/benchmarking/): run these benchmarks.
- [How It Works](../../concepts/how-it-works/): architecture behind these characteristics.
