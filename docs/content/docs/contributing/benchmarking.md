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

| Metric | Description |
|--------|-------------|
| `ns/op` | Nanoseconds per operation (lower is better) |
| `checks/op` | Number of Check assertions per operation |
| `listobjs/op` | Number of ListObjects assertions per operation |
| `listusers/op` | Number of ListUsers assertions per operation |

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

### Expected Performance

| Operation | 1K Tuples | 10K Tuples | 100K Tuples | 1M Tuples | Scaling |
|-----------|-----------|------------|-------------|-----------|---------|
| Direct Membership | ~426us | ~397us | ~384us | ~428us | O(1) |
| Inherited Permission | ~995us | ~1.1ms | ~1.4ms | ~3.4ms | O(log n) |
| Exclusion Pattern | ~1.8ms | ~3.4ms | ~18ms | ~173ms | O(n) |
| Denied Permission | ~612us | ~683us | ~739us | ~1.2ms | O(log n) |
| ListObjects | ~2.3ms | ~23ms | ~192ms | ~1.5s | O(n) |
| ListSubjects | ~708us | ~6.3ms | ~42ms | ~864ms | O(n) |

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
- Cold cache: ~980us
- Warm cache: ~79ns (12,400x faster)

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

- **SQL function changes** (`sql/functions.sql`): Run full benchmark suite
- **Parser changes** (`tooling/parser.go`): Run schema load benchmarks
- **Checker changes** (`checker.go`): Run check operation benchmarks
- **Cache changes** (`cache.go`): Run cache-specific benchmarks

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
