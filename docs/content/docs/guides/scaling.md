---
title: Scaling
description: "Run Melange at scale: indexing the tables behind the tuples view, materialized views, and keeping list queries fast."
weight: 8
---

Melange's check operations are O(1) regardless of dataset size. Benchmarks show a flat ~130 µs in-database check latency (~350-450 µs end to end) from 1K to 1M tuples; see [Performance](../../reference/performance/). The regular view approach works at scale when source tables are properly indexed.

The areas that benefit from optimization are:

- **Missing indexes** cause sequential scans on source tables, degrading all operations.
- **List operations with large result sets** scale with result count (O(results)), not total tuple count.
- **Repeated checks** benefit from caching.

## Measure First

Use `EXPLAIN ANALYZE` to identify actual bottlenecks:

```sql
EXPLAIN ANALYZE SELECT check_permission('user', '123', 'can_read', 'repository', '456');
```

Look for `Seq Scan` entries. If you see sequential scans with filters like `Filter: ((id)::text = '123'::text)`, you need expression indexes.

Run `melange doctor` for automated recommendations:

```bash
melange doctor --db "$DATABASE_URL"
```

Doctor detects missing expression indexes and provides exact `CREATE INDEX` statements.

## Expression Indexes

When your view casts integer IDs to text (`id::text`), PostgreSQL cannot use your existing integer indexes for those lookups. Expression indexes restore efficient index scans:

```sql
CREATE INDEX idx_org_members_text
    ON organization_members ((organization_id::text), (user_id::text));
```

Without expression indexes, PostgreSQL falls back to sequential scans on each source table in the `UNION ALL` view. This affects all operations (check, list, and doctor performance analysis).

Add expression indexes for every `::text` cast in your view. Run `ANALYZE` afterward to update query planner statistics. See [Tuples View](../../concepts/tuples-view/) for detailed index patterns.

## Source Table Indexes

Create composite indexes on the columns used in the view for each access pattern:

```sql
-- Object-based lookups (check operations)
CREATE INDEX idx_org_members_lookup
    ON organization_members (organization_id, role, user_id);

-- Subject-based lookups (list operations)
CREATE INDEX idx_org_members_user
    ON organization_members (user_id, role, organization_id);

-- Parent relationship lookups
CREATE INDEX idx_repos_parent
    ON repositories (id, organization_id);
```

See [Tuples View](../../concepts/tuples-view/) for the full index pattern guide.

## Application-Level Caching

Caching is the highest-impact optimization for repeated checks. Cached lookups return in ~80 ns vs ~350-450 µs uncached.

```go
cache := melange.NewCache(melange.WithTTL(time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))
```

Request-scoped caching (fresh cache per request) avoids stale results while still deduplicating multiple checks within a single request. See [Caching](../caching/) for details.

## List Operations at Scale

List operations (ListObjects, ListSubjects) scale with result set size, not total tuple count. Small result sets are fast at any scale:

| Results | Typical Latency |
|---------|----------------|
| ~5 | 300-500 μs |
| ~100 | 4-5 ms |
| ~1K | 10-15 ms |
| ~10K | 34-37 ms |
| ~100K | 134-153 ms |

Page size has minimal effect on query time because the database walks the full permission graph regardless of `LIMIT`. Use small pages (10-100) for API responses.

For paths where you only need a boolean answer, use `Check` or `BulkCheck` instead of `ListObjects`.

## Alternative View Strategies

The regular view is the recommended default. These alternatives trade simplicity for specific operational properties.

### Materialized View

Replaces the live view with pre-computed, directly indexable data:

```sql
CREATE MATERIALIZED VIEW melange_tuples AS
-- ... same query as your regular view ...
WITH DATA;

CREATE INDEX idx_mt_object
    ON melange_tuples (object_type, object_id, relation, subject_type, subject_id);
CREATE INDEX idx_mt_subject
    ON melange_tuples (subject_type, subject_id, relation, object_type, object_id);
```

Refresh with `REFRESH MATERIALIZED VIEW CONCURRENTLY melange_tuples`. The `CONCURRENTLY` option allows reads during refresh but requires a unique index.

**Trade-off**: permissions become stale between refreshes, and checks no longer see uncommitted changes within a transaction. Consider this only if you have specific reasons (e.g., very complex source queries that are expensive to plan, or you need direct index support without expression indexes).

### Dedicated Tuples Table

Replaces the view with a table synced via triggers:

```sql
CREATE TABLE melange_tuples (
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    relation text NOT NULL,
    object_type text NOT NULL,
    object_id text NOT NULL,
    PRIMARY KEY (object_type, object_id, relation, subject_type, subject_id)
);
```

Sync with triggers on your domain tables. This gives direct index access with real-time consistency, but adds significant write-path complexity and maintenance burden. Consider this only if you have measured a specific performance problem that expression indexes and source table indexes don't solve.

## Next Steps

- [Tuples View](../../concepts/tuples-view/): index patterns and expression index details
- [Caching](../caching/): cache configuration and strategies
- [Performance](../../reference/performance/): full benchmark data
