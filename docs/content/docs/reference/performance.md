---
title: Performance
description: "Measured Melange latency on managed PostgreSQL: what checks and list queries cost, what drives the cost, and how to plan for it."
weight: 10
---

Use these measurements to choose an API and identify what to profile. They come from a same-zone application VM and Cloud SQL for PostgreSQL 18, measured in November 2026 on Melange 0.11.0. They describe these fixtures and hardware; benchmark your own schema before setting a latency or capacity target.

Detailed [measurements](#checks), [schemas](#benchmark-models), and [methodology](#methodology) follow the optimization advice.

## Summary

| Operation | What to expect | What drives the cost |
| --- | --- | --- |
| Check | About 0.35 to 0.55 ms end to end, about 0.1 ms of it in Postgres | Mostly network and client overhead in the measured fixtures. |
| Bulk check | 100 checks in about 8 ms, one round trip | Number of checks in the batch |
| Cached check | About 120 ns | No database call |
| ListObjects, ListSubjects | Under 1 ms to hundreds of ms | How much of the permission graph Postgres has to walk (see [List operations](#list-operations)) |
| Contextual tuples | 7.7 to 11.2 ms total for 1 to 100 tuples | Creating and dropping a temporary view on each call |

A check is one SQL statement. On a same-zone connection, the round trip costs more than the query: a bare `SELECT 1` took 0.33 ms, and the kitchen-sink `PlainUser` check at the 1M label took 0.487 ms. For similar check paths, your round-trip time plus roughly 0.1 to 0.2 ms is a useful starting estimate, not a latency guarantee.

## Choose an API and optimize

| Your request needs… | Start with | Why |
| --- | --- | --- |
| One permission decision | `Check` | One statement; measured checks are mostly network and client time. |
| Decisions for objects already loaded | `BulkCheck` over those IDs | At the largest hierarchy scale, 100 checks took 7.99 ms in bulk versus 46.1 ms sequentially. |
| Repeated decisions with an acceptable staleness window | Permission cache | A warm hit measured 116 ns; it does not measure authorization after a permission change. |
| IDs accessible to a user | `ListObjects` | Profile broad or recursive relations; a small page may still be expensive. |
| Everyone who can access an object | `ListSubjects` | Measure both candidate expansion and result size; schedule broad audits outside hot request paths. |
| Request-specific grants | Contextual tuples | Include temporary-view setup and cleanup in the budget: 7.7–11.2 ms for the measured checks. |

1. **Measure from your application.** Compare a bare `SELECT 1` with the authorization call on the same connection path. The 0.334 ms cloud reference is an observed baseline, not a hard lower bound.
2. **Batch checks before increasing concurrency.** Batching amortizes round trips. Tune pool size against database CPU and request latency; the measured four-worker throughput is not a capacity guarantee.
3. **Index the tuple sources.** Run `melange doctor`; inspect the tables behind your view, including casts. See [Indexing](#indexing).
4. **Profile expensive lists in each direction.** Record returned rows, candidates, buffers, joins, and recursive work. An empty answer can still require a large traversal. See [Schema shape](#schema-shape).
5. **Use pagination to bound payloads.** For object lists a smaller page also reduces database work; check the plan for subject lists.
6. **Cache deliberately.** Choose an invalidation or TTL policy that fits your permission-change requirements. The single-check cache does not cache list results; see [Caching](../../guides/caching/).

## Indexing

Index the tables behind the `melange_tuples` view, including expression indexes for any `::text` id casts. Missing indexes can cause scans; verify index usage in your plan before applying these measurements to your workload:

```sql
-- Composite index covering the columns the view selects
CREATE INDEX idx_org_members_lookup
    ON organization_members (organization_id, role, user_id);

-- Expression index for text id conversion
CREATE INDEX idx_org_members_text
    ON organization_members ((organization_id::text), (user_id::text));
```

`melange doctor` detects missing expression indexes. See [Tuples View](../../concepts/tuples-view/#recommended-indexes) for indexing guidance and [Scaling](../../guides/scaling/) for alternatives such as materialized views.

To investigate a slow query on your own data, use `EXPLAIN ANALYZE` or the generated `explain_*` functions:

```sql
EXPLAIN ANALYZE SELECT check_permission('user', '123', 'viewer', 'document', '456');
```

## Benchmark models

Scale names such as `1K` and `1M` are labels, not exact tuple counts. The fixtures grow users, groups, hierarchy depth, and fan-out together. The results cannot isolate tuple-count scaling or a fixed cost per feature. See [Benchmark schemas](../benchmark-models/) for both complete FGA schemas, physical storage and indexes, seed rules, and the exact mapping from benchmark names to relations.

| Label | Kitchen-sink tuples | Users | Groups / chain length | Folder depth | Hierarchy tuples |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1K | 3,824 | 200 | 40 / 4 | 4 | 1,150 |
| 10K | 25,386 | 1,000 | 120 / 5 | 5 | 21,000 |
| 100K | 155,692 | 5,000 | 400 / 6 | 6 | 204,000 |
| 1M | 840,877 | 25,000 | 2,000 / 6 | 7 | 2,020,000 |

## Checks

In the measured kitchen-sink queries, `check_permission` latency stayed roughly flat across a 220× range of dataset sizes (3,824 to 840,877 tuples). These are selected queries, not a bound for every relation or graph:

| Check | End to end | In database | Network and client |
| --- | ---: | ---: | ---: |
| Union of direct, group, folder-inherited, and public grants, then an exclusion (`viewer but not blocked`) | 412 to 487 µs | 107 to 119 µs | 303 to 371 µs |
| Userset subject (`group:eng#member`) | 410 to 529 µs | 116 to 167 µs | 293 to 364 µs |
| Service account, direct grant | 364 to 465 µs | 70 to 85 µs | 294 to 377 µs |
| Intersection (`editor and active`) | 418 to 515 µs | 114 to 135 µs | 304 to 379 µs |
| Public subject (`user:*`) on the same relation | 418 to 509 µs | 104 to 123 µs | 308 to 387 µs |

Through the Go `Checker`, which adds input validation and connection handling, a check took 0.33 to 0.55 ms at every size from 1,150 to 2,020,000 tuples. Across 947 check assertion benchmarks in the OpenFGA compatibility suite, the median of benchmark medians was 0.43 ms and their 90th percentile was 0.51 ms. These are not per-request latency percentiles.

Other fixed-input operations also stayed in a narrow band:

| Operation | End to end | In database |
| --- | ---: | ---: |
| `expand_permission` | 0.35 to 0.39 ms | 61 to 68 µs |
| `explain_permission` (full trace) | 0.61 to 0.63 ms | 287 to 320 µs |
| `check_permission_bulk`, 3 checks across 3 object types | 0.85 to 0.89 ms | 499 to 511 µs |

The Go `BenchmarkCheck` cases allocated about 1.3 KiB in 26 allocations per call across the measured sizes. Other APIs and cache paths have different allocation costs.

### Bulk checks

Bulk checks send every check in one statement, so you pay the round trip once:

| Checks | One at a time | One bulk call |
| ---: | ---: | ---: |
| 10 | 4.4 to 5.4 ms | 1.4 to 1.5 ms |
| 100 | 44 to 54 ms | 5.6 to 8.3 ms |
| 500 | Not measured | 35 to 36 ms (10K through 1M labels) |

At the 1K label there are only 50 repositories, so the 500-entry batch repeats IDs and takes 6.0 ms; it is not a measurement of 500 distinct checks. Sequential cases also vary users while homogeneous bulk cases fix one user, so the comparison reflects those application patterns rather than isolating only transport overhead.

Duplicate checks in a batch are evaluated once: 100 checks with 50 distinct took 4.4 to 5.5 ms. Use `BulkCheck` whenever a request needs more than a couple of answers, such as filtering a page of results or rendering per-item buttons.

### Concurrency

With four benchmark workers on the 21,000-tuple hierarchy fixture, direct checks achieved about 10,200 operations/s (98 µs of elapsed benchmark time per completed operation); inherited checks achieved about 6,600 operations/s (151 µs/op). These are aggregate throughput measurements, not individual request latencies. Only this concurrency level was measured; they do not establish saturation capacity or linear scaling with more connections.

### Caching

The optional in-memory cache answers repeated checks without a database call:

| Scenario | Latency |
| --- | ---: |
| Uncached check | 591 µs |
| First check for a key (cache miss) | 722 µs |
| Cached check | 116 ns |
| Bulk check, all cached | 65 to 80 µs |

```go
cache := melange.NewCache(
    melange.WithTTL(time.Minute),
)
checker := melange.NewChecker(db, melange.WithCache(cache))
```

Use caching when permissions are read much more often than they change and a staleness window (typically 30 seconds to 5 minutes) is acceptable. Skip it when permissions change frequently or you need every check to see the latest writes. See [Caching](../../guides/caching/).

### Invalid requests

The Go checker rejects invalid relations, types, and subject formats before sending anything to the database, so they cost no round trip.

## List operations

`list_accessible_objects` and `list_accessible_subjects` return every match, or one page of matches when you pass `p_limit`, so their cost depends on how much of the permission graph Postgres has to walk to find them. That is not always the size of the answer:

- **Candidates, not results.** An intersection enumerates candidates from a part and filters them; an exclusion starts from its base relation. `viewer and active` over a document set costs about the same as listing every `viewer`, even when only a few documents pass the `active` filter.
- **Graph size, not results.** Recursive relations (`viewer from parent`) and userset subjects (`group:eng#member` through nested groups) walk the reachable graph. A query that returns 10 results can still take hundreds of milliseconds if those results are found through thousands of group memberships.
- **Pagination bounds list_objects work.** `list_accessible_objects` keeps only the IDs after the cursor in each part of the relation, up to the page limit, and reads a tuple-to-userset a parent at a time, so a first page usually costs less than the full list. At the largest hierarchy scale the first PR page took 6.9 ms for a limit of 10 and 22 ms for a limit of 500. Inspect your plan before relying on a page limit for `list_accessible_subjects`.

### ListObjects

Measured end to end. Each cell shows fixture-derived result cardinality, then measured latency. The benchmark output records timings, not row counts. Scale labels refer to two different datasets; see [Benchmark models](#benchmark-models). The PR row uses a paginated first-page call with a limit of 10: its count is the accessible universe, not the page payload.

| Query shape | 1K label | 10K label | 100K label | 1M label |
| --- | ---: | ---: | ---: | ---: |
| Few results, short paths (organizations a user belongs to, groups a service account is in) | 1 to 2: 0.49 to 0.51 ms | 1 to 2: 0.46 to 0.52 ms | 1: 0.40 to 0.57 ms | 1: 0.47 to 1.0 ms |
| Objects inherited through one parent (pull requests readable via repository → organization) | 100: 0.82 ms | 1,000: 1.8 ms | 5,000: 3.7 ms | 20,000: 6.9 ms |
| Union of direct, group, folder-inherited, and public grants | 36: 4.2 ms | 380: 38 ms | 2,580: 33 ms | 13,020: 156 ms |
| Intersection over that union (`viewer and active`) | 12: 3.4 ms | 20: 6.7 ms | 30: 24 ms | 42: 95 ms |
| Recursive folders (`viewer from parent`, depth 4 to 7) | 12: 1.9 ms | 20: 2.4 ms | 30: 3.9 ms | 42: 8.0 ms |
| Userset subject through nested groups (40 to 2,000 groups) | 3: 2.3 ms | 5: 8.5 ms | 8: 43 ms | 10: 273 ms |

The union at the 10K label measured slower than at the 100K label in this run, with a six-run spread of 15%.

Do not extrapolate a universal cost per returned object from these rows. At the largest kitchen-sink scale, `DocCanView` spent 154.5 ms of 156.4 ms in PostgreSQL; `DocGated` spent 94.4 ms of 95.0 ms there despite its much smaller result. Reducing round-trip latency alone would barely change either total. Profile candidate enumeration, joins, and recursive expansion.

### ListSubjects

| Query shape | 1K label | 10K label | 100K label | 1M label |
| --- | ---: | ---: | ---: | ---: |
| One subject (intersections such as `viewer and active`, folder editors) | 1: 0.55 to 0.81 ms | 1: 0.57 to 0.83 ms | 1: 0.63 to 0.92 ms | 1: 0.57 to 0.81 ms |
| Direct members of an organization | 20: 0.53 to 0.54 ms | 50: 0.57 to 0.63 ms | 100: 0.63 to 0.77 ms | 200: 0.74 to 1.04 ms |
| Readers inherited from the parent organization | 20: 0.87 to 0.93 ms | 50: 0.92 to 1.1 ms | 100: 1.0 to 1.5 ms | 200: 1.4 to 2.1 ms |
| Viewers of a folder, through parent folders | 15: 3.0 ms | 32: 5.3 ms | 68: 10 ms | 168: 13 ms |
| Viewers of a document, through nested groups and folders | 16: 15 ms | 33: 30 ms | 69: 62 ms | 169: 157 ms |
| Same, with an exclusion that removes every subject (`viewer but not blocked`) | 0: 27 ms | 0: 58 ms | 0: 117 ms | 0: 308 ms |

The last row returns nothing and still costs the most: the exclusion has to find every viewer before it can remove them.

### Where large list queries spend their time

At the 1M kitchen-sink label (840,877 tuples), the expensive lists are dominated by PostgreSQL. These are selected full-result SQL calls; the paginated PR benchmarks do not record this split.

| Direction / relation | Total | In database | Network and client |
| --- | ---: | ---: | ---: |
| Objects: organization `member`, userset subject | 272.9 ms | 272.2 ms | 0.652 ms |
| Objects: document `can_view` (union + exclusion) | 156.4 ms | 154.5 ms | 2.3 ms |
| Objects: document `gated` (intersection) | 95.0 ms | 94.4 ms | 0.637 ms |
| Objects: folder `viewer` (recursive TTU) | 8.0 ms | 7.5 ms | 0.534 ms |
| Objects: document `can_edit` (intersection) | 5.4 ms | 4.9 ms | 0.499 ms |
| Subjects: document `can_view` (exclusion) | 308.1 ms | 307.5 ms | 0.667 ms |
| Subjects: document `viewer` (union + inherited grants) | 157.0 ms | 156.4 ms | 0.631 ms |
| Subjects: document `gated` (intersection) | 0.567 ms | 0.269 ms | 0.298 ms |

## Schema shape

The same relation can behave very differently in the two listing directions. Use this map when interpreting the measurements:

| Feature | What to inspect | Evidence in this run |
| --- | --- | --- |
| Direct / computed userset / union | Index lookups, number of grants, duplicate paths | Small fixtures generally below 0.5 ms; a broad document union plus exclusion reaches 156 ms for object listing. |
| TTU (`viewer from parent`) | Parent count, join selectivity, inherited candidates | PR first pages took 6.9 ms (limit 10) to 22 ms (limit 500) at the largest hierarchy scale (see [List operations](#list-operations)). |
| Recursive TTU | Depth, branching, overlap in reachable ancestors | Folder viewer object lists: 1.9–8.0 ms; subject lists: 3.0–12.5 ms. |
| Userset references (`group#member`) | Nested membership expansion; querying a userset versus querying a user | Userset-subject organization lists: 2.3–273 ms, versus 0.410–0.529 ms for the corresponding point checks. |
| Intersection | Candidate part size and selectivity of the other parts | Document `gated` object lists: 3.4–95.0 ms; subject lists: 0.567–0.627 ms. |
| Exclusion | Size of base relation and cost of subtracting grants | Document `can_view` subject lists: 26.8–308.1 ms even though this selected document's wildcard block removes all subjects. |
| Wildcard | Boolean membership versus expansion into objects or subjects | Named wildcard fixture object lists range from 0.339 to 20.7 ms. |

Do not replace `and` with `or` as a performance optimization: that changes who has access. Keep the required semantics and optimize indexes, candidate selection, data layout, or the API used by the application.

## OpenFGA test suite

The OpenFGA compatibility suite covers every supported feature on small datasets. Each assertion runs as its own benchmark, 2,354 in all:

| Operation | Benchmarks | Median of medians | p90 of medians | Median database share |
| --- | ---: | ---: | ---: | ---: |
| Check | 947 | 0.43 ms | 0.51 ms | 22% |
| BulkCheck | 221 | 0.67 ms | 1.1 ms | 42% |
| ListObjects | 685 | 0.50 ms | 0.69 ms | 31% |
| ListUsers | 501 | 0.59 ms | 1.0 ms | 41% |

These are Melange timings while executing OpenFGA compatibility fixtures, not measurements of an OpenFGA server. Percentiles summarize equally weighted assertion benchmarks after taking each one's six-run median. They do not describe production traffic or request p90. A fixture can combine several features, so a single pooled “intersection latency” would hide both operation direction and workload differences.

### Feature examples on small fixtures

Each cell is the range of six-run medians across assertions of that operation in the **named fixture**, in milliseconds. Check includes allowed and denied assertions. The examples are useful for comparing query shapes, but are not controlled experiments isolating the cost of one feature. `ListUsers` is the compatibility-suite name for listing subjects.

| Feature | Fixture | Check (ms) | ListObjects (ms) | ListUsers (ms) |
| --- | --- | ---: | ---: | ---: |
| Direct `[user]` | `this` | 0.352–0.378 | 0.354–0.390 | 0.381–0.410 |
| Computed userset / relation alias | `computed_userset` | 0.406–0.418 | 0.435–0.442 | 0.393–0.435 |
| Union (`or`) | `this_and_union` | 0.331–0.358 | 0.379–0.392 | 0.399–0.416 |
| Tuple-to-userset (`from`) | `tuple_to_userset` | 0.385 | 0.425 | 0.299–0.454 |
| Intersection (`and`) | `this_and_intersection` | 0.355–0.361 | 0.391–0.412 | 0.442 |
| Exclusion (`but not`) | `this_and_exclusion_base` | 0.357–0.373 | 0.368–0.414 | 0.372–0.414 |
| Wildcard expansion | `list_objects_expands_wildcard_tuple` | 0.91–2.1 | 0.339–20.7 | 0.342–15.2 |

The wildcard fixture has both cheap and expensive assertions: the expensive object lists (18 to 21 ms) spend about 97% in PostgreSQL. A wildcard grant can be cheap to check while enumeration is expensive. It is not a uniformly cheap feature.

For nested usersets and recursive TTU at scale, use the kitchen-sink tables above: folder viewer lists rise from 1.9 to 8.0 ms, and listing organization membership for a userset subject rises from 2.3 to 273 ms. Those queries also change graph size and fan-out; they do not measure a fixed per-hop penalty.

## Contextual tuples

Contextual tuples are made visible by creating a temporary view for the call and dropping it afterwards. On Cloud SQL a check with contextual tuples took:

| Contextual tuples | Latency |
| ---: | ---: |
| None (same connection handling) | 0.45 ms |
| 1 | 7.7 ms |
| 25 | 8.8 ms |
| 100 | 11.2 ms |

List queries with contextual tuples in the OpenFGA suite took 6 to 11 ms. The strategy timings include connection handling, temporary-view DDL, the permission query, and cleanup; they do not provide a server/client split. Some contextual compatibility cases also require multiple statements, so the one-statement check model does not apply. Keep contextual tuples off hot paths, and model data in your tables where you can.

## Methodology

| | |
| --- | --- |
| Database | Cloud SQL for PostgreSQL 18.6, Enterprise edition, 4 vCPU, 16 GB (`db-custom-4-16384`), zonal, SSD |
| Client | Compute Engine `n2d-standard-4` (4 vCPU, AMD EPYC 7B13), Go, `pgx` through `database/sql` |
| Network | Same zone (`europe-west2-a`), private IP, SSL |
| Melange | 0.11.0 (source `117fdfdb`) |
| Runs | Each benchmark run 6 times; the page shows medians, or the range of medians across dataset sizes |
| Date | November 9, 2026 (05:32 to 11:55 UTC, including setup) |

**Datasets.** See the complete schemas and fixture configuration in [Benchmark models](#benchmark-models). Two generated datasets, each at four sizes:

- A schema that uses every relation feature: nested groups, recursive folders, organizations, intersections, exclusions, and wildcards. 3,824, 25,386, 155,692, and 840,877 tuples. Measured by calling the generated SQL functions directly.
- An organization → repository → pull request hierarchy. 1,150, 21,000, 204,000, and 2,020,000 tuples. Measured through the Go `Checker`. The bulk, concurrency, and cache numbers, and the list rows that mention organizations, repositories, or pull requests, use this dataset; concurrency and caching use the 21,000-tuple size.

The OpenFGA suite uses each test's own small fixture, through the Go `Checker` (bulk checks call `check_permission_bulk` directly).

The [downloadable measurement snapshot](/benchmarks/cloud-2026-11.csv) preserves six-run medians for the scale, cache, concurrency, contextual-strategy, and named feature examples on this page, with units and sample counts. It was extracted from the raw benchmark output in [`benchmarks/2026-11-cloud-sql/`](https://github.com/pthm/melange/tree/main/benchmarks/2026-11-cloud-sql), which also has the database and network split for every kitchen-sink, SaaS, and OpenFGA benchmark. Blank database metrics mean that benchmark did not record a split.

Six-run spreads reach about 33% for some kitchen-sink cases and 48% for one expensive wildcard object list. These are repeated benchmark averages, not individual request samples. There is no production p95/p99, cold-cache study, saturation test, or before/after baseline here. Independently calculated column medians need not add up exactly.

**End to end** is the time the client measured for one call, including the network round trip and driver work. **In database** is the execution time plus any tracked planning time for the statements the call sent, from `pg_stat_statements`. Planning time is included only if `pg_stat_statements.track_planning` is enabled; the saved run does not record that setting. **Network and client** is the difference.

**What will differ for you.** Your round-trip time sets the floor for every call: a cross-zone or cross-region connection adds its latency to each one. Database time depends on your schema, your data's shape, the indexes on your tuple source tables, and the instance size. Compare runs on the same setup; local Docker timings are not directly comparable to these cloud measurements.

Run the same benchmarks on your own Google Cloud project with `just bench-cloud`, or locally; see [Benchmarking](../../contributing/benchmarking/).

## Further reading

- [Benchmarking](../../contributing/benchmarking/): run these benchmarks yourself.
- [Scaling](../../guides/scaling/): indexing and view strategies for large datasets.
- [How It Works](../../concepts/how-it-works/): the architecture behind these characteristics.
