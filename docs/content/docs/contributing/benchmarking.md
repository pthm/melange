---
title: Benchmarking
weight: 2
---

Melange includes comprehensive benchmarks for both the OpenFGA compatibility tests and scale testing with different tuple volumes.

## OpenFGA Benchmarks

### Running Benchmarks

```bash
# Run all OpenFGA benchmarks
just bench-openfga

# Quick sanity check (1 iteration, direct assignment only)
just bench-openfga-quick

# Benchmark a specific category
just bench-openfga-category DirectAssignment
just bench-openfga-category ComputedUserset
just bench-openfga-category TupleToUserset
just bench-openfga-category Wildcards

# Benchmark tests matching a pattern
just bench-openfga-pattern "^wildcard"
just bench-openfga-pattern "computed_userset|ttu_"

# Benchmark a specific test by name
just bench-openfga-name wildcard_direct

# Run checks-only benchmarks (isolates Check from List operations)
just bench-openfga-checks

# Run benchmarks organized by category
just bench-openfga-by-category
```

### Saving Results

Save benchmark results to a file for comparison:

```bash
# Save results
just bench-openfga-save baseline.txt

# Make changes...

# Save again and compare
just bench-openfga-save after.txt
diff baseline.txt after.txt
```

### Understanding Output

```
BenchmarkOpenFGA_DirectAssignment/this-12              1   14359250 ns/op   2.000 checks/op
BenchmarkOpenFGA_DirectAssignment/this_and_union-12    1   11305875 ns/op   4.000 checks/op
```

| Metric         | Description                                    |
| -------------- | ---------------------------------------------- |
| `ns/op`        | Nanoseconds per operation (lower is better)    |
| `checks/op`    | Number of Check assertions per operation       |
| `listobjs/op`  | Number of ListObjects assertions per operation |
| `listusers/op` | Number of ListUsers assertions per operation   |

## Scale Benchmarks

Test performance at different tuple volumes:

```bash
# Run benchmarks at all scales (1K, 10K, 100K, 1M)
just bench

# Run at specific scale
just bench SCALE=1K
just bench SCALE=10K
just bench SCALE=100K
just bench SCALE=1M

# Quick scale check
just bench-quick
```

### Test Schema

Scale benchmarks use a GitHub-like model with organizations, repositories, and pull requests. See [Performance Reference](../reference/performance.md#test-schema) for the full schema.

### Test Data Configuration

| Scale    | Users  | Orgs | Repos/Org | Members/Org | PRs/Repo | Total Repos | Total PRs | ~Tuples   |
| -------- | ------ | ---- | --------- | ----------- | -------- | ----------- | --------- | --------- |
| **1K**   | 100    | 5    | 10        | 20          | 10       | 50          | 500       | 1,150     |
| **10K**  | 500    | 10   | 50        | 50          | 20       | 500         | 10,000    | 21,000    |
| **100K** | 2,000  | 20   | 100       | 100         | 50       | 2,000       | 100,000   | 204,000   |
| **1M**   | 10,000 | 50   | 200       | 200         | 100      | 10,000      | 1,000,000 | 2,020,000 |

### Expected Performance

**Check Operations** (specialized SQL code generation):

| Operation            | Description                                  | 1K      | 10K     | 100K    | 1M      | Scaling |
| -------------------- | -------------------------------------------- | ------- | ------- | ------- | ------- | ------- |
| Direct Membership    | `user` → `can_read` → `organization`         | 357 µs  | 329 µs  | 296 µs  | 304 µs  | O(1)    |
| Inherited Permission | `user` → `can_read` → `repository` (via org) | 412 µs  | 410 µs  | 420 µs  | 418 µs  | O(1)    |
| Exclusion Pattern    | `user` → `can_review` → `pull_request`       | 515 µs  | 520 µs  | 533 µs  | 505 µs  | O(1)    |
| Denied Permission    | Non-member checking org access               | 275 µs  | 277 µs  | 291 µs  | 281 µs  | O(1)    |

**ListObjects Operations** (performance varies by result count):

| Operation             | Description                          | 1K      | 10K     | 100K    | 1M      | Scaling    |
| --------------------- | ------------------------------------ | ------- | ------- | ------- | ------- | ---------- |
| List Accessible Repos | All repos user can read (via org)    | 3.9 ms  | 34.1 ms | 133.9 ms| 672 ms  | O(results) |
| List Accessible Orgs  | All orgs user is member of           | 299 µs  | 300 µs  | 316 µs  | 278 µs  | O(1)       |
| List Accessible PRs   | All PRs user can read (via repo→org) | 4.1 ms  | 36.8 ms | 153.1 ms| 843 ms  | O(results) |

**ListSubjects Operations** (performance varies by relation complexity):

| Operation         | Description                             | 1K      | 10K     | 100K    | 1M      | Scaling  |
| ----------------- | --------------------------------------- | ------- | ------- | ------- | ------- | -------- |
| List Org Members  | All users who can read an org           | 288 µs  | 335 µs  | 399 µs  | 480 µs  | O(log n) |
| List Repo Readers | All users who can read a repo (via org) | 413 µs  | 346 µs  | 346 µs  | 339 µs  | O(1)     |
| List Repo Writers | All users who can write to a repo       | 269 µs  | 333 µs  | 266 µs  | 259 µs  | O(1)     |

**Parallel Check Operations**:

| Operation                | Time per Op | Speedup vs Sequential |
| ------------------------ | ----------- | --------------------- |
| Parallel Direct Check    | 89 µs       | ~3.7x                 |
| Parallel Inherited Check | 143 µs      | ~2.9x                 |

### Caching Benchmark

Test cache performance:

```go
func BenchmarkCacheHit(b *testing.B) {
    cache := melange.NewCache()
    checker := melange.NewChecker(db, melange.WithCache(cache))

    // Warm the cache
    checker.Check(ctx, user, "can_read", repo)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        checker.Check(ctx, user, "can_read", repo)
    }
}
```

Expected results:

- Cold cache: 422 µs
- Warm cache: 83 ns (~5,000x faster)

## Profiling

### CPU Profile

```bash
cd test && go test -bench="BenchmarkOpenFGA_DirectAssignment" \
    -run='^$' -cpuprofile=cpu.prof -timeout 10m ./openfgatests/...

go tool pprof cpu.prof
```

### Memory Profile

```bash
cd test && go test -bench="BenchmarkOpenFGA_DirectAssignment" \
    -run='^$' -memprofile=mem.prof -timeout 10m ./openfgatests/...

go tool pprof mem.prof
```

### Trace

```bash
cd test && go test -bench="BenchmarkOpenFGA_DirectAssignment" \
    -run='^$' -trace=trace.out -timeout 10m ./openfgatests/...

go tool trace trace.out
```

## Performance Tips

### Before Making Changes

1. Run benchmarks to establish a baseline:

   ```bash
   just bench-openfga-save baseline.txt
   ```

2. Make your changes

3. Run benchmarks again:

   ```bash
   just bench-openfga-save after.txt
   ```

4. Compare:
   ```bash
   benchstat baseline.txt after.txt
   ```

### Key Areas to Benchmark

- **SQL generation changes** (`internal/sqlgen/*.go`): Run full benchmark suite
- **Parser changes** (`pkg/parser/parser.go`): Run schema load benchmarks
- **Runtime changes** (`melange/checker.go`): Run check operation benchmarks
- **Cache changes** (`melange/cache.go`): Run cache-specific benchmarks

### Benchmark Commands Reference

```bash
# OpenFGA benchmarks
just bench-openfga              # All benchmarks
just bench-openfga-category X   # Single category
just bench-openfga-pattern X    # Pattern match
just bench-openfga-name X       # Single test
just bench-openfga-checks       # Check operations only
just bench-openfga-by-category  # Organized by category
just bench-openfga-save X       # Save to file

# Scale benchmarks
just bench                      # All scales
just bench SCALE=1K             # Specific scale
just bench-quick                # Quick check
just bench-save FILE            # Save results
```

## Direct Go Commands

For more control, use Go's test command directly:

```bash
# Run with specific iterations
cd test && go test -bench="BenchmarkOpenFGA_DirectAssignment" \
    -benchtime=10x -run='^$' -benchmem ./openfgatests/...

# Run with longer duration
cd test && go test -bench="BenchmarkOpenFGA_DirectAssignment" \
    -benchtime=30s -run='^$' -benchmem ./openfgatests/...

# Include memory allocations
cd test && go test -bench="BenchmarkOpenFGA_DirectAssignment" \
    -run='^$' -benchmem -memprofile=mem.out ./openfgatests/...
```
