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

The kitchen-sink benchmark measures performance across dataset sizes using a
single model that exercises every generator code path (all seven relation
features and all seven list strategies). It is the source for the numbers in the
[Performance Reference](../../reference/performance/).

```bash
# Run at all default scales (1K, 10K, 100K)
just bench-kitchensink

# Run at a specific scale
just bench-kitchensink 1K
just bench-kitchensink 10K
just bench-kitchensink 100K

# The 1M scale (~840K tuples) is opt-in because setup is slow:
MELANGE_BENCH_LARGE_SCALE=1 just bench-kitchensink 1M
```

For stable numbers, run several iterations and average with `benchstat`:

```bash
cd test && go test -run='^$' -bench='BenchmarkKitchenSink' -benchmem -count=6 . > bench.txt
benchstat bench.txt
```

### Test Data Configuration

Each scale grows users, groups, orgs, folder depth, and documents together
(`test/testutil/kitchensink_fixtures.go`). Scale names are round-number labels;
actual tuple counts are shown.

| Scale    | Users  | Orgs | Groups | Group chain | Folder depth | ~Tuples   |
| -------- | ------ | ---- | ------ | ----------- | ------------ | --------- |
| **1K**   | 200    | 5    | 40     | 4           | 4            | 3,824     |
| **10K**  | 1,000  | 15   | 120    | 5           | 5            | 25,386    |
| **100K** | 5,000  | 40   | 400    | 6           | 6            | 155,692   |
| **1M**   | 25,000 | 100  | 2,000  | 6           | 7            | 840,877   |

### Benchmark Surfaces

The benchmark measures each generated surface across a matrix of query subject
types and relation shapes:

- `BenchmarkKitchenSinkCheck`: `check_permission` for plain-user, userset-typed
  (`group#member`), service-account, and wildcard subjects.
- `BenchmarkKitchenSinkListObjects`: `list_accessible_objects` for direct,
  recursive-TTU, userset, and intersection relations, plus non-plain subjects.
- `BenchmarkKitchenSinkListSubjects`: `list_accessible_subjects` for recursive
  `viewer` and exclusion `can_view`.
- `BenchmarkKitchenSinkExpand`, `Explain`, `Bulk`: the `expand_*`, `explain_*`,
  and `check_permission_bulk` surfaces.

See the [Performance Reference](../../reference/performance/) for averaged results
and per-surface scaling. `check`, `expand`, `explain`, and bulk are O(1).
`ListObjects` scales with the result set. `ListSubjects` scales with the subject
population.

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

- **SQL generation changes** (`lib/sqlgen/*.go`): Run full benchmark suite
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
