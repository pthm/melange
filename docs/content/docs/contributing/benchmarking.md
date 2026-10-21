---
title: Benchmarking
description: How to run Melange's OpenFGA and scale benchmarks locally and compare results.
weight: 5
---

{{< callout type="info" >}}
Looking for results? See the [Performance Reference](../../reference/performance/) for check and list latencies measured on managed PostgreSQL. This page covers running the benchmarks yourself.
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

Each assertion in a test is its own benchmark, named `<test>/<operation>/<index>`:

```
BenchmarkOpenFGA_All/this/Check/0-4   477   442453 ns/op   46579 db-ns/op   395872 net-ns/op   1.000 stmts/op
```

| Metric      | Description                                                                 |
| ----------- | --------------------------------------------------------------------------- |
| `ns/op`     | Time per call, as the client measured it (lower is better)                 |
| `db-ns/op`  | Server time for the statements the call sent, from `pg_stat_statements`    |
| `net-ns/op` | The rest: network round trips, transferring results, driver and client work |
| `stmts/op`  | Statements the call sent                                                    |

The last three appear only when the server preloads `pg_stat_statements`; see [Where the time goes](#where-the-time-goes).

## Scale Benchmarks

The kitchen-sink benchmark measures performance across dataset sizes using a
single model that exercises every generator code path (all seven relation
features and all seven list strategies). The `BenchmarkCheck`, `BenchmarkListObjects`,
`BenchmarkListSubjects`, and `BenchmarkBulkCheck` suites in `test/benchmark_test.go`
do the same for a simpler organization → repository → pull request model, through
the Go `Checker`.

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
  `viewer`, exclusion `can_view`, intersections, and folder relations.
- `BenchmarkKitchenSinkExpand`, `Explain`, `Bulk`: the `expand_*`, `explain_*`,
  and `check_permission_bulk` surfaces.

See the [Performance Reference](../../reference/performance/) for results.
The selected `check`, `expand`, `explain`, and fixed-size bulk queries stayed roughly flat across the measured scales; this is not a constant-time guarantee for arbitrary graphs or batch sizes. List operations
grow with the part of the permission graph they walk, which for intersections,
exclusions, recursive relations, and userset subjects can be much larger than the
result.

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

Expected results: a cold check costs a database round trip (about 0.6 ms through the `Checker` on a same-zone managed database), a warm hit about 120 ns. See [Caching](../../reference/performance/#caching) for the measured numbers.

## Cloud Benchmarks

The numbers in the [Performance Reference](../../reference/performance/) come from a cloud run, not a laptop. Local runs talk to a container on the same machine: they hide the network round trip an application pays on every call, and container networking skews the split between database and client time. Use local runs to compare two versions of Melange on the same machine, and cloud runs for numbers users can plan with.

`scripts/cloud-bench.sh` creates a Cloud SQL for PostgreSQL instance and a Compute Engine VM in the same zone, connects them over private IP, runs the Go and OpenFGA suites `COUNT` times, writes a report, and deletes the machines.

```bash
# Setup, run, report, and teardown in one go (tears down even on failure)
PROJECT=my-gcp-project just bench-cloud all

# Or step by step
PROJECT=my-gcp-project just bench-cloud setup
PROJECT=my-gcp-project just bench-cloud run       # safe to re-run if the connection drops
just bench-cloud report                           # writes .cloud-bench/latest/report.md
PROJECT=my-gcp-project just bench-cloud teardown
```

The Performance Reference was produced with:

```bash
PROJECT=melange-bench VM_TYPE=n2d-standard-4 just bench-cloud all
```

`n2d-standard-4` replaced the default `n2-standard-4` only because the zone had no `n2` capacity that day. Defaults are a 4 vCPU, 16 GB Cloud SQL Enterprise instance (`DB_TIER`), an `n2-standard-4` VM (`VM_TYPE`), `europe-west2-a` (`ZONE`), PostgreSQL 17 (`PG_VERSION`), and 6 runs (`COUNT`). The script header lists every setting.

A full run takes about 4½ hours: 2 for the Go suites, most of it loading and querying the largest datasets, and 2¼ for the 2,000 OpenFGA benchmarks. The machines cost about $1 an hour. Results stay in `.cloud-bench/results-<timestamp>/`, with `latest` pointing at the newest. The 10M to 100M scale benchmarks are left out by default; they need hundreds of gigabytes and days to run. Narrow or widen the selection with `BENCH_TEST` and `BENCH_OPENFGA`, which are passed to `-bench`.

To follow a run in progress, look at the result files on the VM:

```bash
gcloud compute ssh melange-bench-vm --zone europe-west2-a --project my-gcp-project \
  --command 'grep -o "^Benchmark[^/ ]*" ~/results/*.txt | uniq -c; tail -c 400 ~/results/*.txt'
```

### Where the time goes

The kitchen-sink and OpenFGA benchmarks split each result in two:

- **In database** (`db-ns/op`): server execution time plus planning time when `pg_stat_statements.track_planning` is enabled, for the statements the operation sent, from `pg_stat_statements` snapshots taken before and after the timed loop.
- **Network and client** (`net-ns/op`): the rest of the measured time: round trips, transferring results, and driver and client work.

`BenchmarkRoundTrip` times a `SELECT 1` through the same driver, an observed reference for one statement's network and client time. The report shows it above the tables.

The split needs a server that preloads `pg_stat_statements`, as Cloud SQL does; without it the benchmarks report only `ns/op`. To get it locally, start Postgres with `-c shared_preload_libraries=pg_stat_statements` and point `MELANGE_TEST_SERVER_URL` at it. `go run ./cmd/benchreport results.txt` (from `test/`) prints the tables for any saved run.

The test helpers use `MELANGE_TEST_SERVER_URL`, when set, as the server on which to create and drop per-test databases, so any benchmark can run against any PostgreSQL server. It is separate from `DATABASE_URL`, which only redirects the shared `testutil.DB` database:

```bash
MELANGE_TEST_SERVER_URL="postgres://user:pass@host:5432/postgres" just bench-kitchensink 1K
```

### Updating the Performance Reference

1. Run `just bench-cloud all` with the settings above.
2. Read `.cloud-bench/latest/report.md`. The "Where the time goes" section has the kitchen-sink and OpenFGA tables; the "All results" section has `benchstat` output for every benchmark.
3. Copy `test.txt`, `openfga.txt`, `settings.env`, `server-version.txt`, and the "Where the time goes" part of the report (as `summary.md`) into a new `benchmarks/<year>-<month>-<setup>/` directory, with a README recording the source commit and command.
4. Update the tables in the Performance Reference, the downloadable measurement snapshot, and the methodology with the source marker, any uncommitted patch, settings, server version, and date. Use six-run medians per benchmark; feature examples must name fixtures and separate Check, ListObjects, and ListUsers. Suite p90 is across benchmark medians, not individual requests.
5. If the models or generators changed, update [Benchmark schemas](../../reference/benchmark-models/), tuple counts, indexes, seed descriptions, and query selection. Distinguish accessible-universe counts from returned page sizes.
6. Search the docs for the summary figures (check latency, cache hit, bulk, contextual tuples) and keep them in step: the homepage, How It Works, Scaling, Caching, Components, Contextual Tuples, and the listing guides quote them.

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
just bench 1K                   # Specific scale
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
