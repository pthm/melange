# Benchmark Analysis

## Executive Summary

The benchmark results reveal Melange's **real-world performance characteristics** and validate the core architectural claims while highlighting areas where documentation needs adjustment.

## Key Findings

### ✅ Core Claims Validated

1. **O(1) Check Performance**: Check operations maintain constant time (275-533 μs) regardless of dataset size (1K → 1M tuples)
2. **List Operations Scale with Results**: ListObjects/ListSubjects time correlates with result count, not total tuple count
3. **Cache Effectiveness**: 5000x speedup (422 μs → 83 ns) for repeated checks
4. **Scale Independence for Simple Relations**: Direct lookups remain fast at any scale

### ⚠️ Documentation Discrepancies

| Metric | Documented | Actual | Delta |
|--------|-----------|--------|-------|
| Check operations | 200-400 μs | **275-533 μs** | +38% on high end |
| Parallel direct check | 54 μs | **89 μs** | +65% |
| Parallel inherited check | 72 μs | **143 μs** | +99% |
| Cache cold | 315 μs | **422 μs** | +34% |
| Cache warm | 80 ns | **83 ns** | ✓ Accurate |

## Detailed Performance Analysis

### Check Operations (O(1) - Scale Independent)

| Operation | 1K | 10K | 100K | 1M | Pattern |
|-----------|-----|------|------|-----|---------|
| Direct Membership | 357 μs | 329 μs | 296 μs | 304 μs | ~**330 μs avg** |
| Inherited Permission | 412 μs | 410 μs | 420 μs | 418 μs | ~**415 μs avg** |
| Exclusion Pattern | 515 μs | 520 μs | 533 μs | 505 μs | ~**518 μs avg** |
| Denied Permission | 275 μs | 277 μs | 291 μs | 281 μs | ~**281 μs avg** |

**Insight**: Check latency varies by **operation complexity**, not data scale. Range: **275-533 μs** (0.27-0.53 ms).

### ListObjects Performance (Result-Dependent)

#### Small Result Sets (O(1) - Fast at Any Scale)

**List Accessible Orgs** (5-10 results):

| Scale | Latency | Behavior |
|-------|---------|----------|
| 1K | 299 μs | Constant time |
| 10K | 300 μs | No degradation |
| 100K | 316 μs | Minimal increase |
| 1M | 278 μs | Still fast! |

**Takeaway**: When result sets are small (<10), performance is excellent regardless of total data.

#### Large Result Sets (O(results) - Scales Linearly)

**List Accessible Repos** (10-10K results):

| Scale | Results | Latency | μs/result |
|-------|---------|---------|-----------|
| 1K | ~10 | 3.9 ms | 390 μs |
| 10K | ~500 | 34.1 ms | 68 μs |
| 100K | ~2K | 133.9 ms | 67 μs |
| 1M | ~10K | 672 ms | 67 μs |

**Takeaway**: After initial overhead, each additional result costs ~**67 μs**. The recursive CTE evaluates the full permission graph regardless of LIMIT.

### ListSubjects Performance (Generally Efficient)

**List Org Members** (20-200 members):

| Scale | Latency | Notes |
|-------|---------|-------|
| 1K | 288 μs | Fast |
| 10K | 335 μs | +16% |
| 100K | 399 μs | +39% |
| 1M | 480 μs | +67% (log n growth) |

**List Repo Readers** (via org membership):

| Scale | Latency | Notes |
|-------|---------|-------|
| 1K | 413 μs | Baseline |
| 10K-1M | 339-346 μs | Actually **faster** at scale! |

### Parallel Operations

**12-core benchmark results**:

| Operation | Sequential | Parallel | Speedup |
|-----------|-----------|----------|---------|
| Direct Check | ~330 μs | **89 μs** | 3.7x |
| Inherited Check | ~415 μs | **143 μs** | 2.9x |

**Insight**: Parallelism provides **3-4x throughput improvement**, not the 4-5x previously documented. Still excellent for high-load scenarios.

### Cache Performance

| Scenario | Latency | Speedup |
|----------|---------|---------|
| Cold cache (DB query) | **422 μs** | baseline |
| Warm cache (memory) | **83 ns** | **5,084x faster** |
| Cold start (first check) | **416 μs** | - |

**Insight**: Cache effectiveness is **even better than documented** (5,084x vs 4,000x).

### Page Size Impact (Minimal)

**ListObjects at 10K scale (500 repos accessible)**:

| Page Size | Latency | Delta |
|-----------|---------|-------|
| Page 10 | 34.1 ms | baseline |
| Page 50 | 33.4 ms | -2% |
| Page 100 | 33.9 ms | -0.6% |
| Page 500 | 34.4 ms | +1% |
| Paginate All | 34.0 ms | 0% |

**Insight**: Page size has **negligible impact** (<3% variance). Use small pages (10-100) for better UX without performance penalty.

### OpenFGA Pattern Performance

From the performance summary table at the end of the benchmark output:

#### Fast Patterns (<500 μs average)

- Direct relations, computed usersets, unions
- Simple TTU, wildcards (non-expanding)
- Most operations: **0.2-0.8 ms**

#### Medium Patterns (500 μs - 1.5 ms)

- TTU chains, exclusions, intersections
- Nested usersets
- Most operations: **0.5-1.5 ms**

#### Expensive Patterns (>1.5 ms)

- Recursive TTU unions: **1.3-1.7 ms** (check)
- Nested usersets recursively expanded: **2.3 ms** (list subjects)
- Deep relation chains: **1.5-2.2 ms**
- Contextual tuples: **~3 ms** (documented, not benchmarked here)

## Real-World Performance Expectations

### Typical API Latencies (95th Percentile Estimates)

| Operation | Small Dataset (<10K) | Medium Dataset (100K) | Large Dataset (1M+) |
|-----------|---------------------|----------------------|---------------------|
| Check (simple) | 0.3-0.5 ms | 0.3-0.6 ms | 0.3-0.6 ms |
| Check (complex) | 0.5-1 ms | 0.5-1 ms | 0.5-1 ms |
| List (small results <10) | 0.3-0.5 ms | 0.3-0.6 ms | 0.3-0.7 ms |
| List (medium results 10-100) | 1-5 ms | 3-10 ms | 5-15 ms |
| List (large results 100-1000) | 5-30 ms | 20-100 ms | 50-200 ms |
| List (huge results 1000+) | 30-100 ms | 100-500 ms | 200-1000 ms |

### Performance Budget Recommendations

For **latency-sensitive APIs** (p95 < 100ms):

- ✅ All check operations (always <1ms)
- ✅ List operations with <100 results
- ✅ Direct relations, computed usersets, unions
- ⚠️ Intersections, deep TTU chains (budget 1-2ms)
- ❌ Avoid: contextual tuples, wildcard expansion in lists

For **batch/background operations** (p95 < 1s):

- ✅ All check operations
- ✅ List operations with <1000 results
- ✅ All relation patterns
- ⚠️ Large result sets (paginate for UX)

## Recommendations for Documentation Updates

### 1. Homepage Claim ("Sub-Millisecond Checks")

**Current**: "Permission checks, object listing, and subject listing all complete in under 1ms"

**Issue**: Partially accurate
- ✅ Most checks: 275-533 μs (< 1ms)
- ✅ Most list operations with small results: < 1ms
- ❌ ListObjects with large results: can be 3-672 ms

**Suggested Update**: "**Sub-millisecond permission checks** with most operations completing in 0.3-0.5ms. List operations scale with result count, not total data."

### 2. Performance Documentation Ranges

**Current ranges are too optimistic.** Update to reflect actual measurements:

| Pattern | Current Doc | Actual | Suggested Doc Range |
|---------|-------------|--------|---------------------|
| Direct assignment | 155 μs | 296-357 μs | **300-400 μs** |
| Computed userset | 160 μs | similar | **300-400 μs** |
| Inherited (TTU) | 170 μs | 409-420 μs | **400-450 μs** |
| Exclusion | 170 μs | 504-533 μs | **500-550 μs** |
| Intersection | 170-250 μs | varies | **400-600 μs** |

### 3. Scale Benchmark Tables

**Check Operations** - Update to actual values:

| Operation | Current | Actual (all scales) | Suggested |
|-----------|---------|---------------------|-----------|
| Direct | 204-290 μs | 296-357 μs | **300-360 μs** |
| Inherited | 318-323 μs | 409-420 μs | **410-420 μs** |
| Exclusion | 399-412 μs | 504-533 μs | **505-535 μs** |
| Denied | 202-221 μs | 275-291 μs | **275-290 μs** |

### 4. Parallel Operations

**Update documented parallel performance**:

- Parallel Direct Check: ~~54 μs~~ → **89 μs** (~3.7x speedup)
- Parallel Inherited Check: ~~72 μs~~ → **143 μs** (~2.9x speedup)

### 5. Cache Performance

**Update cold cache baseline**:

- Cold cache: ~~315 μs~~ → **422 μs**
- Warm cache: 80 ns → **83 ns** ✓
- Speedup: ~~4,000x~~ → **~5,000x**

### 6. Add "Realistic Performance Expectations" Section

Create a new section explaining:
- **p50 (median)**: Use lower end of ranges
- **p95 (95th percentile)**: Use upper end + 30-50% buffer
- **p99 (99th percentile)**: Budget 2-3x median for outliers

Example for simple checks:
- p50: ~330 μs
- p95: ~500 μs (with network, serialization overhead)
- p99: ~800 μs (including DB connection pool waits)

## Positive Findings

1. **Consistency**: Check performance is remarkably stable across scales (CV < 10%)
2. **Predictability**: ListObjects latency is linear with results, not data size
3. **Cache effectiveness**: Better than documented (5,084x vs 4,000x)
4. **Small result optimization**: Queries returning <10 items are fast at any scale
5. **Page size irrelevance**: Use small pages for better UX with zero performance cost

## Areas for Future Investigation

1. **Why are parallel operations slower than expected?** (3-4x vs documented 4-5x)
   - Possible connection pool contention?
   - PostgreSQL lock contention?
   - GC pressure in benchmark harness?

2. **Why is cold cache slower?** (422 μs vs 315 μs documented)
   - Different test workload?
   - Hardware differences?
   - PostgreSQL configuration changes?

3. **ListSubjects shows strange behavior** (faster at higher scales for some queries)
   - Query planner choosing different strategy?
   - Cache effects from repeated benchmark runs?

## Conclusion

Melange's **core value proposition remains strong**:
- ✅ Constant-time checks regardless of scale
- ✅ Excellent performance for small result sets
- ✅ Predictable, linear scaling for large result sets
- ✅ Highly effective caching

**However, documentation should be updated to reflect real-world measurements** with appropriate buffers for p95/p99 performance, especially for API latency budgets.
