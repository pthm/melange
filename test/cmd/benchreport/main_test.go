package main

import (
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	in := `goos: linux
BenchmarkRoundTrip-4  1000  100000 ns/op  1000 db-ns/op  99000 net-ns/op  1.000 stmts/op
BenchmarkRoundTrip-4  1000  102000 ns/op  1000 db-ns/op  101000 net-ns/op  1.000 stmts/op
BenchmarkKitchenSinkBulk/1K-4  1000  500000 ns/op  200000 db-ns/op  300000 net-ns/op  2.000 stmts/op
BenchmarkKitchenSinkBulk/1K-4  1000  520000 ns/op  200000 db-ns/op  320000 net-ns/op  2.000 stmts/op
BenchmarkCheckWithCache/WithCache-4  1000  80 ns/op
`
	got := report(t, in)

	// Medians: total (500+520)/2 = 510 µs, net (300+320)/2 = 310 µs.
	for _, want := range []string{
		"Medians of 2 runs",
		"a `SELECT 1` takes **101.0 µs** (1.0 µs of it in the database)",
		"| 1K | 510.0 µs | 200.0 µs | 310.0 µs (2.0 statements) | 39% | ±2.0% |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "WithCache") {
		t.Errorf("benchmark without breakdown metrics should be left out:\n%s", got)
	}
}

func TestWriteWithoutRoundTrip(t *testing.T) {
	in := "BenchmarkKitchenSinkExpand/1K-4  1000  500000 ns/op  200000 db-ns/op  300000 net-ns/op  1.000 stmts/op\n"
	got := report(t, in)
	if !strings.Contains(got, "| 1K | 500.0 µs | 200.0 µs | 300.0 µs | 40% |") {
		t.Errorf("breakdown missing without BenchmarkRoundTrip:\n%s", got)
	}
	if strings.Contains(got, "SELECT 1") {
		t.Errorf("round-trip reference shown without BenchmarkRoundTrip:\n%s", got)
	}
}

func TestWriteOpenFGASummary(t *testing.T) {
	in := `BenchmarkOpenFGA_All/a/Check/0-4  1000  100000 ns/op  20000 db-ns/op  80000 net-ns/op  1.000 stmts/op
BenchmarkOpenFGA_All/b/Check/0-4  1000  300000 ns/op  60000 db-ns/op  240000 net-ns/op  1.000 stmts/op
BenchmarkOpenFGA_All/a/ListUsers/0-4  1000  500000 ns/op  400000 db-ns/op  100000 net-ns/op  3.000 stmts/op
`
	got := report(t, in)
	for _, want := range []string{
		"| Check | 2 | 200.0 µs | 300.0 µs | 40.0 µs | 160.0 µs | 20% | 1.0 |",
		"| ListUsers | 1 | 500.0 µs | 500.0 µs | 400.0 µs | 100.0 µs | 80% | 3.0 |",
		"<details><summary>All 3 benchmarks</summary>",
		"| a/ListUsers/0 | 500.0 µs | 400.0 µs | 100.0 µs (3.0 statements) | 80% |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// report parses in and returns the rendered Markdown.
func report(t *testing.T, in string) string {
	t.Helper()
	var r results
	if err := r.parse(strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r.write(&out)
	return out.String()
}
