---
title: Benchmarking
description: How to run Melange's OpenFGA and scale benchmarks locally and compare results.
weight: 5
---

{{< callout type="info" >}}
Looking for results? See the [Performance Reference](../../reference/performance/) for measured check and list latencies at 1K to 1M tuples. This page covers running the benchmarks yourself.
{{< /callout >}}

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

Expected results: a cold check costs a database round-trip (~130 µs in the database), a warm hit ~80 ns. See [Caching](../../reference/performance/#caching) for the measured numbers.

## Cloud Benchmarks

Local runs talk to a container on the same machine, so they measure database cost with almost no network. `scripts/cloud-bench.sh` measures what an application sees: it creates a Cloud SQL for PostgreSQL instance and a Compute Engine VM in the same zone, connects them over private IP, runs every benchmark suite (including the 1M scale) `COUNT` times, and summarises the results with `benchstat`.

```bash
# Setup, run, report, and teardown in one go (tears down even on failure)
PROJECT=my-gcp-project just bench-cloud all

# Or step by step
PROJECT=my-gcp-project just bench-cloud setup
PROJECT=my-gcp-project just bench-cloud run       # safe to re-run if the connection drops
just bench-cloud report                           # writes .cloud-bench/latest/report.md
PROJECT=my-gcp-project just bench-cloud teardown
```

The report records the instance tier, VM type, zone, and server version, then splits each kitchen-sink result into where the time went:

- **In database**: server time for the statements the operation sent, from `pg_stat_statements`.
- **Network and client**: the rest of the same measured time: round trips, transferring results, and driver and client work.

A `SELECT 1` through the same driver (`BenchmarkRoundTrip`) is reported alongside, as the floor for one statement's network and client time.

The kitchen-sink benchmarks report the split as `db-ns/op`, `net-ns/op`, and `stmts/op` whenever the server preloads `pg_stat_statements`, as Cloud SQL does. To get it locally, start Postgres with `-c shared_preload_libraries=pg_stat_statements` and point `MELANGE_TEST_SERVER_URL` at it. `go run ./cmd/benchreport results.txt` (from `test/`) prints the table for any saved run.

Defaults are a 4 vCPU, 16 GB Cloud SQL Enterprise instance (`DB_TIER`), an `n2-standard-4` VM (`VM_TYPE`), `europe-west2-a` (`ZONE`), PostgreSQL 17 (`PG_VERSION`), and 6 runs (`COUNT`). The script header lists every setting.

A full run takes several hours, mostly loading and querying the 1M dataset. The 10M to 100M scale benchmarks are left out by default; they need hundreds of gigabytes and days to run. Narrow or widen the selection with `BENCH_TEST` and `BENCH_OPENFGA`, which are passed to `-bench`.

The script works because the test helpers use `MELANGE_TEST_SERVER_URL`, when set, as the server on which to create and drop per-test databases. You can point a local run at any PostgreSQL server the same way:

```bash
MELANGE_TEST_SERVER_URL="postgres://user:pass@host:5432/postgres" just bench-kitchensink 1K
```

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
