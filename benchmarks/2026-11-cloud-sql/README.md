# Cloud SQL benchmark run, November 2026

Raw results behind the [Performance reference](../../docs/content/docs/reference/performance.md).

| | |
| --- | --- |
| Date | 2026-11-09 05:32 to 11:55 UTC, including setup and teardown |
| Source | Melange 0.11.0 (`117fdfdb`, the same tree as `main` at `9c487b52`) |
| Database | Cloud SQL for PostgreSQL 18.6, Enterprise, 4 vCPU, 16 GiB (`db-custom-4-16384`), zonal, SSD |
| Client | Compute Engine `n2d-standard-4` (AMD EPYC 7B13), same zone (`europe-west2-a`), private IP, SSL |
| Runs | 6 per benchmark |
| Command | `PROJECT=melange-bench VM_TYPE=n2d-standard-4 DB_INSTANCE=melange-bench-pg18 just bench-cloud all` |

## Files

- `test.txt`: `go test -bench` output for the Go suites in `test/` (checks, lists, bulk, cache, concurrency, contextual tuples, kitchen sink, on-demand, SaaS, round trip).
- `openfga.txt`: `go test -bench` output for `BenchmarkOpenFGA_All`, one benchmark per assertion.
- `summary.md`: the "Where the time goes" tables, which split each kitchen-sink, SaaS, and OpenFGA result into database time and network and client time.
- `settings.env`, `server-version.txt`: the run's settings and the server version.

`docs/static/benchmarks/cloud-2026-11.csv` has the medians quoted in the docs, for the same benchmarks as `cloud-2026-10.csv`.

## Reading them

```bash
# Medians and spread for every benchmark
go run golang.org/x/perf/cmd/benchstat@latest benchmarks/2026-11-cloud-sql/test.txt

# Database versus network split
cd test && go run ./cmd/benchreport ../benchmarks/2026-11-cloud-sql/test.txt ../benchmarks/2026-11-cloud-sql/openfga.txt
```
