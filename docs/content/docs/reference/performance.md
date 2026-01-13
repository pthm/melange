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
| Direct Membership    | `user` → `can_read` → `organization` (direct tuple lookup)     | ~290 µs | ~209 µs | ~211 µs | ~204 µs | O(1)    |
| Inherited Permission | `user` → `can_read` → `repository` (via org membership)        | ~323 µs | ~324 µs | ~321 µs | ~318 µs | O(1)    |
| Exclusion Pattern    | `user` → `can_review` → `pull_request` (reader but not author) | ~399 µs | ~406 µs | ~412 µs | ~409 µs | O(1)    |
| Denied Permission    | Non-member user checking org access (expected: denied)         | ~221 µs | ~214 µs | ~203 µs | ~202 µs | O(1)    |

**Key insight**: All check operations maintain constant time regardless of dataset size. This is achieved through specialized SQL code generation that eliminates runtime schema interpretation.

### List Operations

List operations find all objects a subject can access (or all subjects with access to an object). Performance characteristics vary significantly by result set size, not total dataset size.

#### Performance Depends on Result Set Size, Not Total Data

The most important finding: **query time is determined by how many results match, not how many tuples exist in the database.**

**ListObjects** (find objects a user can access):

| Operation             | Description                                         | Results | 1K     | 10K    | 100K    | 1M      | Scaling      |
| --------------------- | --------------------------------------------------- | ------- | ------ | ------ | ------- | ------- | ------------ |
| List Accessible Orgs  | All organizations user is member of (direct)        | ~5      | 211 µs | 217 µs | 216 µs  | 204 µs  | O(1)         |
| List Accessible Repos | All repositories user can read (via org membership) | ~10-10K | 3.0 ms | 27 ms  | 105 ms  | 529 ms  | O(results)   |
| List Accessible PRs   | All pull requests user can read (via repo→org)      | ~100-1M | 3.3 ms | 28 ms  | 118 ms  | 629 ms  | O(results)   |

**ListSubjects** (find users with access):

| Operation         | Description                                      | Results | 1K     | 10K    | 100K    | 1M      | Scaling    |
| ----------------- | ------------------------------------------------ | ------- | ------ | ------ | ------- | ------- | ---------- |
| List Org Members  | All users who can read an organization           | ~20-200 | 234 µs | 241 µs | 279 µs  | 360 µs  | O(log n)   |
| List Repo Writers | All users who can write to a repository (direct) | ~1      | 201 µs | 199 µs | 196 µs  | 189 µs  | O(1)       |
| List Repo Readers | All users who can read a repository (via org)    | ~20-200 | 6.6 ms | 29 ms  | 125 ms  | 701 ms  | O(results) |

**Key insight**: Small result sets are fast regardless of total dataset size. At 1M tuples, listing organizations (5 results) takes 204µs while listing PRs (1M results) takes 629ms. The recursive CTE must evaluate all potential permission paths, making query time proportional to the number of accessible objects.

#### Page Size Has Minimal Impact

**Page size has almost no effect on query execution time** - the database walks the entire permission graph regardless of LIMIT:

**ListObjects performance at 10K scale (500 total repos)**:

| Page Size | Query Time | Notes                              |
| --------- | ---------- | ---------------------------------- |
| Page 10   | ~27 ms     | First 10 results                   |
| Page 50   | ~28 ms     | First 50 results                   |
| Page 100  | ~27 ms     | First 100 results                  |
| Page 500  | ~27 ms     | All 500 results                    |
| Paginate  | ~28 ms     | Walk all pages (100 items at time) |

**Recommendation**: Use page sizes of 10-100 for API responses. Since query time is constant, smaller pages reduce response size without performance penalty.

#### Pagination Overhead is Negligible

Walking through multiple pages adds almost no overhead:

| Operation               | Total Results | Single Query (all) | Paginated (100/page) | Overhead |
| ----------------------- | ------------- | ------------------ | -------------------- | -------- |
| List Accessible Repos   | 500 (10K)     | ~27 ms             | ~28 ms (5 pages)     | ~4%      |
| List Accessible Repos   | 10,000 (1M)   | ~529 ms            | ~1,100 ms (100 pages)| ~108%    |
| List Repo Readers       | 50 (10K)      | ~29 ms             | ~29 ms (1 page)      | ~0%      |
| List Repo Readers       | 200 (1M)      | ~701 ms            | ~1,403 ms (2 pages)  | ~100%    |

**Note**: At very large result sets (10K+), pagination overhead becomes significant due to cursor-based resumption. For full dataset downloads, use a single large page. For interactive APIs, use small pages for better UX.

### Parallel Operations

Permission checks are fully parallelizable since each check is independent:

| Operation                | Time per Op | vs Sequential | Throughput Gain |
| ------------------------ | ----------- | ------------- | --------------- |
| Parallel Direct Check    | ~54 µs      | ~290 µs       | ~5x             |
| Parallel Inherited Check | ~72 µs      | ~323 µs       | ~4.5x           |

At 12 parallel workers, throughput increases approximately 4-5x compared to sequential checks, demonstrating excellent concurrency characteristics for high-load scenarios.

## Caching

The optional in-memory cache provides dramatic speedups for repeated checks:

| Scenario                    | Latency | Speedup        |
| --------------------------- | ------- | -------------- |
| Cold cache (database query) | ~315 µs | baseline       |
| Warm cache (memory lookup)  | ~80 ns  | ~4,000x faster |

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

### 5. Use Pagination for Large Result Sets

For list operations with many results, use cursor-based pagination:

```go
// Fetch first page (recommended page size: 10-100)
objects, cursor, err := checker.ListObjects(ctx, user, "viewer", "document",
    melange.PageOptions{Limit: 50})

// Fetch subsequent pages
nextPage, nextCursor, err := checker.ListObjects(ctx, user, "viewer", "document",
    melange.PageOptions{Limit: 50, After: cursor})
```

**Page size recommendations:**

- **Interactive APIs**: 10-50 items (minimize response payload, low query overhead)
- **Batch processing**: 100-500 items (balance between memory and round-trips)
- **Full downloads**: Use `ListObjectsAll()` or single large page

Since query time is roughly constant regardless of page size, **smaller pages are better for UX** without performance penalty.

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

- **Check operations**: 200-400 µs (O(1) - constant time regardless of dataset size)
- **List operations with small results**: 200-500 µs (O(1) - e.g., listing orgs, writers)
- **List operations with large results**: Scales with result count, not total data
  - 10K results: ~27-30 ms
  - 100K results: ~105-125 ms
  - 1M results: ~530-700 ms
- **Page size impact**: Minimal (~0-4% overhead for different page sizes)
- **Pagination overhead**: Negligible for <1000 results, ~100% for 10K+ results
- **Validation errors**: ~25-30 ns (instant rejection, no database hit)
- **Cache hits**: ~80 ns (~4,000x faster than database)
- **Parallel throughput**: 4-5x improvement with 12 workers

**Key insights:**

- **Check performance is independent of dataset size** - 1K tuples vs 1M tuples: same latency
- **List performance depends on result count, not total tuples** - small result sets are fast at any scale
- **Page size has minimal effect on query time** - use small pages (10-100) for better UX
- **Caching provides 4,000x speedup** - critical for repeated checks

**Patterns to avoid for latency-sensitive paths:**

- Deep intersection chains (use union where possible)
- Contextual tuples (use stored tuples, ~3ms overhead)
- Wildcard expansion in ListObjects/ListUsers
- Deeply nested TTU chains

## Further Reading

- [Tuples View](../concepts/tuples-view.md) - Detailed indexing and scaling strategies
- [Contributing: Benchmarking](../contributing/benchmarking.md) - Run benchmarks yourself
- [How It Works](../concepts/how-it-works.md) - Architecture details explaining performance characteristics
