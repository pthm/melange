---
title: Performance
weight: 4
---

Melange is designed for high-performance permission checking at scale. This page explains the performance characteristics you can expect and how to optimize your setup.

## Performance Overview

Melange achieves sub-millisecond permission checks through:

1. **Specialized SQL functions**: Purpose-built functions generated for each relation
2. **Precomputed closures**: Role hierarchies resolved at migration time, not runtime
3. **Index utilization**: Generated SQL optimized for efficient index scans
4. **In-database execution**: No network round-trips for authorization

## Pattern Complexity Benchmarks

All benchmarks run on Apple M2 Pro with PostgreSQL 16. These results show performance across different authorization patterns from the OpenFGA compatibility test suite.

### Performance by Pattern Type

| Pattern                                 | Check   | ListObjects | ListUsers |
| --------------------------------------- | ------- | ----------- | --------- |
| Direct assignment (`[user]`)            | ~155 µs | ~165 µs     | ~155 µs   |
| Computed userset (`viewer: owner`)      | ~160 µs | ~165 µs     | ~160 µs   |
| Union (`[user] or owner`)               | ~155 µs | ~165 µs     | ~155 µs   |
| Tuple-to-userset (`viewer from parent`) | ~170 µs | ~180 µs     | ~160 µs   |
| Exclusion (`writer but not blocked`)    | ~170 µs | ~180 µs     | ~170 µs   |
| Intersection (`writer and editor`)      | ~170 µs | ~205 µs     | ~250 µs   |
| Wildcards (`[user:*]`)                  | ~155 µs | ~160 µs     | ~155 µs   |
| Userset references (`[group#member]`)   | ~170 µs | ~320 µs     | ~180 µs   |

### Complex Pattern Performance

More complex patterns combining multiple features:

| Pattern                      | Check   | ListObjects | ListUsers |
| ---------------------------- | ------- | ----------- | --------- |
| TTU + computed userset       | ~175 µs | ~190 µs     | ~170 µs   |
| TTU + TTU (nested)           | ~200 µs | ~190 µs     | ~190 µs   |
| Three-prong relation         | ~230 µs | ~475 µs     | ~330 µs   |
| Computed user indirect ref   | ~190 µs | ~270 µs     | ~230 µs   |
| Nested TTU with intersection | ~210 µs | ~240 µs     | ~265 µs   |
| Nested TTU with exclusion    | ~200 µs | ~210 µs     | ~275 µs   |

### Expensive Operations

Some patterns have significantly higher latency:

| Pattern                         | Check   | ListObjects | ListUsers | Notes                     |
| ------------------------------- | ------- | ----------- | --------- | ------------------------- |
| Contextual tuples               | -       | ~3.2 ms     | ~3.1 ms   | Temporary table overhead  |
| Wildcard expansion (worst case) | ~850 µs | ~9.2 ms     | ~21 ms    | Full enumeration required |
| Deep intersection chains        | ~175 µs | ~450 µs     | ~500 µs   | Multiple path evaluation  |

### Validation Performance

Invalid requests are rejected immediately without database queries:

| Validation               | Latency |
| ------------------------ | ------- |
| Invalid relation         | ~25 ns  |
| Invalid type             | ~30 ns  |
| Invalid user format      | ~30 ns  |
| Invalid contextual tuple | ~25 ns  |

## Scale Benchmarks

These benchmarks test performance at different tuple volumes using a GitHub-like authorization model.

### Test Schema

The scale benchmarks use a model with organizations, repositories, and pull requests:

```fga
type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define can_read: member

type repository
  relations
    define org: [organization]
    define reader: [user] or can_read from org
    define writer: [user] or reader
    define can_read: reader
    define can_write: writer
    define can_review: can_read but not author

type pull_request
  relations
    define repo: [repository]
    define author: [user]
    define can_read: can_read from repo
    define can_review: can_read from repo but not author
```

### Test Data Configuration

| Scale    | Users  | Orgs | Repos/Org | Members/Org | PRs/Repo | Total Repos | Total PRs | ~Tuples   |
| -------- | ------ | ---- | --------- | ----------- | -------- | ----------- | --------- | --------- |
| **1K**   | 100    | 5    | 10        | 20          | 10       | 50          | 500       | 1,150     |
| **10K**  | 500    | 10   | 50        | 50          | 20       | 500         | 10,000    | 21,000    |
| **100K** | 2,000  | 20   | 100       | 100         | 50       | 2,000       | 100,000   | 204,000   |
| **1M**   | 10,000 | 50   | 200       | 200         | 100      | 10,000      | 1,000,000 | 2,020,000 |

Tuples include: org memberships, repo→org relationships, PR→repo relationships, and PR→author relationships.

### Check Operations

Check operations execute specialized SQL functions and scale with **O(1) constant time** regardless of tuple count:

| Operation            | Description                                                    | 1K      | 10K     | 100K    | 1M      | Scaling |
| -------------------- | -------------------------------------------------------------- | ------- | ------- | ------- | ------- | ------- |
| Direct Membership    | `user` → `can_read` → `organization` (direct tuple lookup)     | ~246 µs | ~196 µs | ~210 µs | ~202 µs | O(1)    |
| Inherited Permission | `user` → `can_read` → `repository` (via org membership)        | ~319 µs | ~318 µs | ~309 µs | ~307 µs | O(1)    |
| Exclusion Pattern    | `user` → `can_review` → `pull_request` (reader but not author) | ~391 µs | ~388 µs | ~393 µs | ~392 µs | O(1)    |
| Denied Permission    | Non-member user checking org access (expected: denied)         | ~193 µs | ~208 µs | ~235 µs | ~210 µs | O(1)    |

**Key insight**: All check operations maintain constant time regardless of dataset size. This is achieved through specialized SQL code generation that eliminates runtime schema interpretation.

### List Operations

List operations find all objects a subject can access (or all subjects with access to an object). Performance varies by relation complexity:

**ListObjects** (find objects a user can access):

| Operation             | Description                                         | 1K      | 10K     | 100K    | 1M      | Scaling |
| --------------------- | --------------------------------------------------- | ------- | ------- | ------- | ------- | ------- |
| List Accessible Repos | All repositories user can read (via org membership) | ~2.9 ms | ~26 ms  | ~102 ms | ~531 ms | O(n)    |
| List Accessible Orgs  | All organizations user is member of (direct)        | ~184 µs | ~200 µs | ~195 µs | ~194 µs | O(1)    |
| List Accessible PRs   | All pull requests user can read (via repo→org)      | ~3.2 ms | ~30 ms  | ~113 ms | ~605 ms | O(n)    |

**ListSubjects** (find users with access):

| Operation         | Description                                      | 1K      | 10K     | 100K    | 1M      | Scaling  |
| ----------------- | ------------------------------------------------ | ------- | ------- | ------- | ------- | -------- |
| List Org Members  | All users who can read an organization           | ~178 µs | ~203 µs | ~255 µs | ~330 µs | O(log n) |
| List Repo Readers | All users who can read a repository (via org)    | ~6.7 ms | ~27 ms  | ~117 ms | ~682 ms | O(n)     |
| List Repo Writers | All users who can write to a repository (direct) | ~176 µs | ~184 µs | ~184 µs | ~184 µs | O(1)     |

**Why the variation?** Operations on direct relations (org membership, repo writers) achieve O(1) scaling. Operations that traverse parent relationships (repo readers via org, PRs via repo→org) scale linearly with the number of potential matches because they must join across relationship hierarchies.

### Parallel Operations

Permission checks are fully parallelizable since each check is independent:

| Operation                | Time per Op | Allocations  |
| ------------------------ | ----------- | ------------ |
| Parallel Direct Check    | ~55 µs      | 32 allocs/op |
| Parallel Inherited Check | ~70 µs      | 33 allocs/op |

At 12 parallel workers, throughput increases ~4x compared to sequential checks.

## Caching

The optional in-memory cache provides dramatic speedups for repeated checks:

| Scenario                    | Latency | Speedup        |
| --------------------------- | ------- | -------------- |
| Cold cache (database query) | ~336 µs | baseline       |
| Warm cache (memory lookup)  | ~79 ns  | ~4,250x faster |

Enable caching in your application:

```go
cache := melange.NewCache(
    melange.WithTTL(time.Minute),
    melange.WithMaxSize(10000),
)
checker := melange.NewChecker(db, melange.WithCache(cache))
```

**When to use caching:**

- High read-to-write ratio (permissions checked frequently, changed rarely)
- Acceptable staleness window (typically 30s-5min depending on application)
- Memory budget available for cache storage

**When to skip caching:**

- Permissions change frequently
- Strict real-time consistency required
- Memory-constrained environments

## Optimizing Performance

### 1. Design Efficient Schemas

Schema design has the biggest impact on query performance. The benchmarks show clear patterns:

**Performance tiers by pattern:**

| Tier      | Patterns                                     | Typical Check Latency |
| --------- | -------------------------------------------- | --------------------- |
| Fast      | Direct, computed userset, union, wildcards   | 150-165 µs            |
| Medium    | TTU, exclusion, userset references           | 165-180 µs            |
| Slow      | Intersection, nested TTU, complex multi-path | 180-250 µs            |
| Very slow | Deep intersection chains, contextual tuples  | 300+ µs               |

**Minimize intersections**: Intersection is the most expensive set operation, especially for ListObjects and ListUsers:

```fga
# Slower: Intersection requires evaluating multiple paths
type document
  relations
    define viewer: writer and editor

# Faster: Union is much cheaper
type document
  relations
    define viewer: writer or editor
```

**Prefer shallow hierarchies**: Deep parent traversals increase query complexity:

```fga
# Better: Direct assignment with union
type document
  relations
    define viewer: [user, team#member]

# Slower: Deep parent chain
type document
  relations
    define viewer: viewer from folder
type folder
  relations
    define viewer: viewer from workspace
```

**Use direct relations where possible**: `define viewer: [user]` (~155 µs) is faster than nested computed usersets (~190 µs).

**Be cautious with wildcards in ListObjects/ListUsers**: While Check with wildcards is fast (~155 µs), wildcard expansion for list operations can be expensive (up to 9-21 ms in worst cases).

### 2. Avoid Runtime Contextual Tuples

Contextual tuples add ~3ms overhead per operation due to temporary table setup. Use stored tuples when possible:

```go
// Slow: ~3.2ms with contextual tuples
checker.Check(ctx, user, "viewer", doc,
    melange.WithContextualTuples(temporaryPermissions))

// Fast: ~160µs with stored tuples
checker.Check(ctx, user, "viewer", doc)
```

If you need contextual tuples frequently, consider batching multiple checks together.

### 3. Index Your Tuples View

Proper indexing of your `melange_tuples` view source tables is critical:

```sql
-- Object-based lookups (most common pattern)
CREATE INDEX idx_org_members_lookup
    ON organization_members (organization_id, role, user_id);

-- Expression indexes for text ID conversion
CREATE INDEX idx_org_members_text
    ON organization_members ((organization_id::text), (user_id::text));
```

**Without expression indexes**, PostgreSQL must perform sequential scans when your view converts integer IDs to text.

See the [Tuples View](../concepts/tuples-view.md#performance-optimization) documentation for complete indexing guidance.

### 4. Use Appropriate Scaling Strategies

| Scale           | Recommended Approach                                              |
| --------------- | ----------------------------------------------------------------- |
| < 10K tuples    | Regular view + source table indexes                               |
| 10K-100K tuples | Regular view + expression indexes + caching                       |
| 100K-1M tuples  | Regular view + expression indexes + caching, or materialized view |
| > 1M tuples     | Dedicated table with trigger sync + caching                       |

See [Tuples View Scaling Strategies](../concepts/tuples-view.md#scaling-strategies) for implementation details.

### 5. Batch List Operations

For list operations at scale, consider pagination or streaming:

```go
// Stream results instead of loading all at once
objects, err := checker.ListObjects(ctx, user, "viewer", "document",
    melange.WithLimit(100),
    melange.WithCursor(lastCursor),
)
```

### 6. Monitor Query Performance

Use `EXPLAIN ANALYZE` to identify slow queries:

```sql
EXPLAIN ANALYZE
SELECT check_permission('user', '123', 'viewer', 'document', '456');
```

Look for:

- **Sequential scans**: Add indexes to eliminate these
- **High row estimates**: May indicate missing statistics (`ANALYZE` your tables)
- **Nested loops with high iterations**: Consider schema simplification

## Performance Comparison

How does Melange compare to alternatives?

| Approach                   | Check Latency | Consistency   | Scaling             |
| -------------------------- | ------------- | ------------- | ------------------- |
| Melange (in-database)      | 150-250 µs    | Transactional | O(1) for checks     |
| OpenFGA (external service) | ~1-5 ms       | Eventual      | O(1) for checks     |
| Application-level RBAC     | ~10-100 µs    | Varies        | O(roles)            |
| Direct SQL queries         | ~50-200 µs    | Transactional | O(query complexity) |

Melange provides the consistency guarantees of transactional SQL with performance comparable to simpler RBAC systems, while supporting the full expressiveness of Zanzibar-style authorization.

## Summary

**Typical performance expectations:**

- **Check operations**: 150-250 µs for most patterns
- **ListObjects**: 165-300 µs for simple patterns, 300-500 µs for complex patterns
- **ListUsers**: 155-330 µs for most patterns
- **Validation errors**: ~25-30 ns (instant rejection, no database hit)
- **Cache hits**: ~79 ns (~2000x faster than database)

**Patterns to avoid for latency-sensitive paths:**

- Deep intersection chains (use union where possible)
- Contextual tuples (use stored tuples)
- Wildcard expansion in ListObjects/ListUsers
- Deeply nested TTU chains

## Further Reading

- [Tuples View](../concepts/tuples-view.md) - Detailed indexing and scaling strategies
- [Contributing: Benchmarking](../contributing/benchmarking.md) - Run benchmarks yourself
- [How It Works](../concepts/how-it-works.md) - Architecture details explaining performance characteristics
