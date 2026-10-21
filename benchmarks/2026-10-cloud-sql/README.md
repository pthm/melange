# Cloud SQL benchmark run, October 2026

Raw results behind the [Performance reference](../../docs/content/docs/reference/performance.md).

| | |
| --- | --- |
| Date | 2026-10-20 20:12 to 2026-10-21 00:24 UTC |
| Source | `49f41f35` (v0.10.0 plus unreleased changes), with the benchmark harness from the following commit |
| Database | Cloud SQL for PostgreSQL 17.11, Enterprise, `db-custom-4-16384` (4 vCPU, 16 GB), zonal, SSD |
| Client | Compute Engine `n2d-standard-4` (AMD EPYC 7B13), same zone (`europe-west2-a`), private IP, SSL |
| Runs | 6 per benchmark |
| Command | `PROJECT=melange-bench VM_TYPE=n2d-standard-4 just bench-cloud all` |

## Files

- `test.txt`: `go test -bench` output for the Go suites in `test/` (checks, lists, bulk, cache, concurrency, contextual tuples, kitchen sink, round trip).
- `openfga.txt`: `go test -bench` output for `BenchmarkOpenFGA_All`, one benchmark per assertion.
- `summary.md`: the "Where the time goes" tables, which split each kitchen-sink and OpenFGA result into database time and network and client time.
- `settings.env`, `server-version.txt`: the run's settings and the server version.

`docs/static/benchmarks/cloud-2026-10.csv` has the medians quoted in the docs.

## Reading them

```bash
# Medians and spread for every benchmark
go run golang.org/x/perf/cmd/benchstat@latest benchmarks/2026-10-cloud-sql/test.txt

# Database versus network split
cd test && go run ./cmd/benchreport ../benchmarks/2026-10-cloud-sql/test.txt ../benchmarks/2026-10-cloud-sql/openfga.txt
```

To compare a new run with this one, pass both files to `benchstat`.
