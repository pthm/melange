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

Typical latencies across all scales (1K-1M tuples). Real-world p95 latencies may be 30-50% higher with network and connection pool overhead.

| Pattern                                 | Check       | ListObjects | ListUsers   |
| --------------------------------------- | ----------- | ----------- | ----------- |
| Direct assignment (`[user]`)            | 300-400 µs  | 300-500 µs  | 300-400 µs  |
| Computed userset (`viewer: owner`)      | 300-400 µs  | 300-500 µs  | 300-400 µs  |
| Union (`[user] or owner`)               | 300-400 µs  | 300-500 µs  | 300-400 µs  |
| Tuple-to-userset (`viewer from parent`) | 400-450 µs  | 400-600 µs  | 350-450 µs  |
| Exclusion (`writer but not blocked`)    | 500-550 µs  | 400-600 µs  | 400-500 µs  |
| Intersection (`writer and editor`)      | 400-600 µs  | 500-800 µs  | 600-900 µs  |
| Wildcards (`[user:*]`)                  | 300-400 µs  | 300-500 µs  | 300-400 µs  |
| Userset references (`[group#member]`)   | 400-600 µs  | 600-1000 µs | 400-600 µs  |

### Complex Pattern Performance

More complex patterns combining multiple features (typical ranges):

| Pattern                      | Check       | ListObjects | ListUsers   |
| ---------------------------- | ----------- | ----------- | ----------- |
| TTU + computed userset       | 400-600 µs  | 500-800 µs  | 400-600 µs  |
| TTU + TTU (nested)           | 500-800 µs  | 600-1000 µs | 500-800 µs  |
| Three-prong relation         | 600-1000 µs | 1-2 ms      | 800-1200 µs |
| Computed user indirect ref   | 400-700 µs  | 700-1200 µs | 600-1000 µs |
| Nested TTU with intersection | 600-1000 µs | 800-1500 µs | 1-1.5 ms    |
| Nested TTU with exclusion    | 500-900 µs  | 700-1200 µs | 1-1.5 ms    |

### Expensive Operations

Some patterns have significantly higher latency:

| Pattern                         | Check       | ListObjects | ListUsers | Notes                     |
| ------------------------------- | ----------- | ----------- | --------- | ------------------------- |
| Recursive TTU unions            | 1.3-1.7 ms  | 1-2 ms      | 1-2 ms    | Deep relation traversal   |
| Nested usersets (recursive)     | 1-1.5 ms    | 1.5-2.5 ms  | 2-3 ms    | Full graph expansion      |
| Contextual tuples               | +2-4 ms     | +3-5 ms     | +3-5 ms   | Temporary table overhead  |
| Wildcard expansion (worst case) | 800-1200 µs | 5-15 ms     | 10-30 ms  | Full enumeration required |

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
| Direct Membership    | `user` → `can_read` → `organization` (direct tuple lookup)     | 357 µs  | 329 µs  | 296 µs  | 304 µs  | O(1)    |
| Inherited Permission | `user` → `can_read` → `repository` (via org membership)        | 412 µs  | 410 µs  | 420 µs  | 418 µs  | O(1)    |
| Exclusion Pattern    | `user` → `can_review` → `pull_request` (reader but not author) | 515 µs  | 520 µs  | 533 µs  | 505 µs  | O(1)    |
| Denied Permission    | Non-member user checking org access (expected: denied)         | 275 µs  | 277 µs  | 291 µs  | 281 µs  | O(1)    |

**Key insight**: All check operations maintain constant time regardless of dataset size. This is achieved through specialized SQL code generation that eliminates runtime schema interpretation.

### List Operations

List operations find all objects a subject can access (or all subjects with access to an object). Performance characteristics vary significantly by result set size, not total dataset size.

#### Performance Depends on Result Set Size, Not Total Data

The most important finding: **query time is determined by how many results match, not how many tuples exist in the database.**

**ListObjects** (find objects a user can access):

| Operation             | Description                                         | Results | 1K      | 10K     | 100K    | 1M      | Scaling    |
| --------------------- | --------------------------------------------------- | ------- | ------- | ------- | ------- | ------- | ---------- |
| List Accessible Orgs  | All organizations user is member of (direct)        | ~5      | 299 µs  | 300 µs  | 316 µs  | 278 µs  | O(1)       |
| List Accessible Repos | All repositories user can read (via org membership) | ~10-10K | 3.9 ms  | 34.1 ms | 133.9 ms| 672 ms  | O(results) |
| List Accessible PRs   | All pull requests user can read (via repo→org)      | ~100-1M | 4.1 ms  | 36.8 ms | 153.1 ms| 843 ms  | O(results) |

**ListSubjects** (find users with access):

| Operation         | Description                                      | Results | 1K      | 10K     | 100K    | 1M      | Scaling    |
| ----------------- | ------------------------------------------------ | ------- | ------- | ------- | ------- | ------- | ---------- |
| List Org Members  | All users who can read an organization           | ~20-200 | 288 µs  | 335 µs  | 399 µs  | 480 µs  | O(log n)   |
| List Repo Writers | All users who can write to a repository (direct) | ~1      | 269 µs  | 333 µs  | 266 µs  | 259 µs  | O(1)       |
| List Repo Readers | All users who can read a repository (via org)    | ~20-200 | 413 µs  | 346 µs  | 346 µs  | 339 µs  | O(1)       |

**Key insight**: Small result sets are fast regardless of total dataset size. At 1M tuples, listing organizations (5 results) takes 204µs while listing PRs (1M results) takes 629ms. The recursive CTE must evaluate all potential permission paths, making query time proportional to the number of accessible objects.

#### Page Size Has Minimal Impact

**Page size has almost no effect on query execution time** - the database walks the entire permission graph regardless of LIMIT:

**ListObjects performance at 10K scale (500 total repos)**:

| Page Size | Query Time | Notes                              |
| --------- | ---------- | ---------------------------------- |
| Page 10   | 34.1 ms    | First 10 results                   |
| Page 50   | 33.4 ms    | First 50 results                   |
| Page 100  | 33.9 ms    | First 100 results                  |
| Page 500  | 34.4 ms    | All 500 results                    |
| Paginate  | 34.0 ms    | Walk all pages (100 items at time) |

**Recommendation**: Use page sizes of 10-100 for API responses. Since query time is constant, smaller pages reduce response size without performance penalty.

#### Pagination Overhead is Negligible

Walking through multiple pages adds almost no overhead:

| Operation               | Total Results | Single Query (all) | Paginated (100/page)  | Overhead |
| ----------------------- | ------------- | ------------------ | --------------------- | -------- |
| List Accessible Repos   | 500 (10K)     | 34.1 ms            | 34.0 ms (5 pages)     | ~0%      |
| List Accessible Repos   | 10,000 (1M)   | 672 ms             | ~1,200 ms (100 pages) | ~79%     |
| List Repo Readers       | 50 (10K)      | 346 µs             | 346 µs (1 page)       | ~0%      |
| List Repo Readers       | 200 (1M)      | 339 µs             | 339 µs (1 page)       | ~0%      |

**Note**: At very large result sets (10K+), pagination overhead becomes significant due to cursor-based resumption. For full dataset downloads, use a single large page. For interactive APIs, use small pages for better UX.

### Parallel Operations

Permission checks are fully parallelizable since each check is independent:

| Operation                | Time per Op | vs Sequential | Throughput Gain |
| ------------------------ | ----------- | ------------- | --------------- |
| Parallel Direct Check    | 89 µs       | ~330 µs       | ~3.7x           |
| Parallel Inherited Check | 143 µs      | ~415 µs       | ~2.9x           |

At 12 parallel workers, throughput increases approximately 3-4x compared to sequential checks, demonstrating excellent concurrency characteristics for high-load scenarios.

## Caching

The optional in-memory cache provides dramatic speedups for repeated checks:

| Scenario                    | Latency | Speedup        |
| --------------------------- | ------- | -------------- |
| Cold cache (database query) | 422 µs  | baseline       |
| Warm cache (memory lookup)  | 83 ns   | ~5,000x faster |

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
| Fast      | Direct, computed userset, union, wildcards   | 300-400 µs            |
| Medium    | TTU, exclusion, userset references           | 400-550 µs            |
| Slow      | Intersection, nested TTU, complex multi-path | 500-800 µs            |
| Very slow | Deep chains, recursive TTU, contextual tuples| 1-4 ms                |

**Minimize intersections**: Intersection is one of the more expensive set operations (~400-800 µs for checks, 500-1500 µs for lists), especially for ListObjects and ListUsers:

```fga
# Slower: Intersection requires evaluating multiple paths (~600 µs check)
type document
  relations
    define viewer: writer and editor

# Faster: Union is much cheaper (~350 µs check)
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

**Use direct relations where possible**: `define viewer: [user]` (~330 µs) is faster than nested computed usersets (~450 µs).

**Be cautious with wildcards in ListObjects/ListUsers**: While Check with wildcards is fast (~330 µs), wildcard expansion for list operations can be expensive (5-30 ms in worst cases).

### 2. Avoid Runtime Contextual Tuples

Contextual tuples add ~3-5ms overhead per operation due to temporary table setup. Use stored tuples when possible:

```go
// Slow: Adds 3-5ms overhead with contextual tuples
checker.CheckWithContextualTuples(ctx, user, "viewer", doc, temporaryPermissions)

// Fast: 300-500µs with stored tuples
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
| Melange (in-database)      | 300-600 µs    | Transactional | O(1) for checks     |
| OpenFGA (external service) | 1-5 ms        | Eventual      | O(1) for checks     |
| Application-level RBAC     | 10-100 µs     | Varies        | O(roles)            |
| Direct SQL queries         | 50-300 µs     | Transactional | O(query complexity) |

Melange provides the consistency guarantees of transactional SQL with performance comparable to simpler RBAC systems, while supporting the full expressiveness of Zanzibar-style authorization.

## Summary

**Typical performance expectations:**

- **Check operations**: 300-600 µs (O(1) - constant time regardless of dataset size)
  - Simple patterns (direct, union): 300-400 µs
  - Medium complexity (TTU, exclusion): 400-550 µs
  - Complex patterns (intersection, nested TTU): 500-800 µs
  - Very complex (deep chains, recursive): 1-2 ms
- **List operations with small results** (<10 items): 300-500 µs (O(1) - e.g., listing orgs, writers)
- **List operations with large results**: Scales with result count, not total data
  - 100 results: ~4-5 ms
  - 1K results: ~10-15 ms
  - 10K results: ~34-37 ms
  - 100K results: ~134-153 ms
  - 1M results: ~672-843 ms
- **Page size impact**: Minimal (~0-3% variance for different page sizes)
- **Pagination overhead**: Negligible for <1K results, ~79% for 10K+ results
- **Validation errors**: ~25-30 ns (instant rejection, no database hit)
- **Cache hits**: ~83 ns (~5,000x faster than database)
- **Parallel throughput**: 3-4x improvement with 12 workers

**Key insights:**

- **Check performance is independent of dataset size** - 1K tuples vs 1M tuples: same latency
- **List performance depends on result count, not total tuples** - small result sets are fast at any scale
- **Page size has minimal effect on query time** - use small pages (10-100) for better UX
- **Caching provides 5,000x speedup** - critical for repeated checks
- **Real-world latencies**: Add 30-50% to these numbers for p95 API latencies with network/connection overhead

**Patterns to avoid for latency-sensitive paths:**

- Deep intersection chains (use union where possible)
- Contextual tuples (use stored tuples, ~3ms overhead)
- Wildcard expansion in ListObjects/ListUsers
- Deeply nested TTU chains

## Further Reading

- [Tuples View](../concepts/tuples-view.md) - Detailed indexing and scaling strategies
- [Contributing: Benchmarking](../contributing/benchmarking.md) - Run benchmarks yourself
- [How It Works](../concepts/how-it-works.md) - Architecture details explaining performance characteristics
