// Command benchreport turns benchmark output into a Markdown table of where
// each operation's time goes: in the database, or on the network and in the
// client.
//
// It reads the db-ns/op, net-ns/op, and stmts/op metrics that the kitchen-sink
// benchmarks report when pg_stat_statements is available, plus the SELECT 1
// reference from BenchmarkRoundTrip. Benchmarks without those metrics are left
// out; use benchstat for them.
//
// Usage:
//
//	benchreport results.txt [more.txt...]
package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const roundTrip = "BenchmarkRoundTrip"

// cpuSuffix is the -GOMAXPROCS suffix go test appends to benchmark names.
var cpuSuffix = regexp.MustCompile(`-\d+$`)

// results maps benchmark name to unit to one value per run, in input order.
type results struct {
	names  []string
	values map[string]map[string][]float64
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: benchreport results.txt [more.txt...]")
		os.Exit(2)
	}
	var r results
	for _, path := range os.Args[1:] {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		err = r.parse(f)
		_ = f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
	}
	// Write errors are sticky in bufio.Writer, so Flush reports any of them.
	out := bufio.NewWriter(os.Stdout)
	r.write(out)
	if err := out.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parse reads benchmark lines: name, iterations, then value-unit pairs.
func (r *results) parse(in io.Reader) error {
	if r.values == nil {
		r.values = map[string]map[string][]float64{}
	}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		name := cpuSuffix.ReplaceAllString(fields[0], "")
		if r.values[name] == nil {
			r.values[name] = map[string][]float64{}
			r.names = append(r.names, name)
		}
		for i := 2; i+1 < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				continue
			}
			r.values[name][fields[i+1]] = append(r.values[name][fields[i+1]], v)
		}
	}
	return sc.Err()
}

func (r *results) write(w io.Writer) {
	_, _ = fmt.Fprintln(w, "## Where the time goes")
	_, _ = fmt.Fprintln(w)
	var split []string
	for _, name := range r.names {
		if name != roundTrip && r.values[name]["db-ns/op"] != nil {
			split = append(split, name)
		}
	}
	if len(split) == 0 {
		_, _ = fmt.Fprintln(w, "No breakdown: no benchmark reported db-ns/op, so `pg_stat_statements` was probably unavailable on the server.")
		return
	}
	if runs := len(r.values[split[0]]["ns/op"]); runs == 1 {
		_, _ = fmt.Fprint(w, "One run. Each row splits the time the client measured into:\n\n")
	} else {
		_, _ = fmt.Fprintf(w, "Medians of %d runs. Each row splits the time the client measured into:\n\n", runs)
	}
	_, _ = fmt.Fprintln(w, "- **In database**: server time for the statements the operation sent, from `pg_stat_statements`.")
	_, _ = fmt.Fprintln(w, "- **Network and client**: the rest: round trips, transferring results, and driver and client work in Go.")
	_, _ = fmt.Fprintln(w)
	rt := r.values[roundTrip]
	if len(rt["ns/op"]) > 0 && rt["db-ns/op"] != nil {
		_, _ = fmt.Fprintf(w, "For reference, a `SELECT 1` takes **%s** (%s of it in the database), the floor for one statement's network and client time.\n", formatNs(median(rt["ns/op"])), formatNs(median(rt["db-ns/op"])))
	}
	_, _ = fmt.Fprintln(w, "Every operation below sends one statement unless the network column says otherwise.")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "**Spread** is the largest difference between one run's total and the median.")

	// Group rows by top-level benchmark, keeping input order.
	var groups []string
	rows := map[string][]row{}
	for _, name := range split {
		v := r.values[name]
		top, sub, _ := strings.Cut(strings.TrimPrefix(name, "Benchmark"), "/")
		if rows[top] == nil {
			groups = append(groups, top)
		}
		if sub == "" {
			sub = top
		}
		rw := row{
			name:  sub,
			total: median(v["ns/op"]),
			db:    median(v["db-ns/op"]),
			net:   median(v["net-ns/op"]),
			stmts: median(v["stmts/op"]),
		}
		rw.spread = spread(v["ns/op"], rw.total)
		rows[top] = append(rows[top], rw)
	}

	for _, top := range groups {
		_, _ = fmt.Fprintf(w, "\n### %s\n\n", top)
		if !strings.HasPrefix(top, "OpenFGA") {
			writeRows(w, rows[top])
			continue
		}
		// The OpenFGA suite has thousands of small benchmarks: summarize by
		// operation, with every row behind a fold.
		writeSummary(w, rows[top])
		_, _ = fmt.Fprintf(w, "\n<details><summary>All %d benchmarks</summary>\n\n", len(rows[top]))
		writeRows(w, rows[top])
		_, _ = fmt.Fprintln(w, "\n</details>")
	}
}

// row is one benchmark's medians across runs, plus its total's spread.
type row struct {
	name                          string
	total, db, net, stmts, spread float64
}

func writeRows(w io.Writer, rows []row) {
	_, _ = fmt.Fprintln(w, "| Benchmark | Total | In database | Network and client | In database % | Spread |")
	_, _ = fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: |")
	for _, rw := range rows {
		netCell := formatNs(rw.net)
		if math.Abs(rw.stmts-1) >= 0.05 {
			netCell += fmt.Sprintf(" (%.1f statements)", rw.stmts)
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %.0f%% | ±%.1f%% |\n",
			rw.name, formatNs(rw.total), formatNs(rw.db), netCell, 100*rw.db/rw.total, 100*rw.spread/rw.total)
	}
}

// operations are the OpenFGA runner's operation names, which appear as a path
// segment after the test name (test/Check/assertion, test/BulkCheck, ...).
var operations = []string{"Check", "BulkCheck", "ListObjects", "ListUsers"}

// writeSummary shows, per operation, the median and 90th percentile across
// benchmarks of each benchmark's median.
func writeSummary(w io.Writer, rows []row) {
	byOp := map[string][]row{}
	for _, rw := range rows {
		for seg := range strings.SplitSeq(rw.name, "/") {
			if slices.Contains(operations, seg) {
				byOp[seg] = append(byOp[seg], rw)
				break
			}
		}
	}
	_, _ = fmt.Fprintln(w, "Each column is the median across benchmarks, except p90, the 90th percentile of totals.")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "| Operation | Benchmarks | Total | p90 total | In database | Network and client | In database % | Statements |")
	_, _ = fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, op := range operations {
		rs := byOp[op]
		if len(rs) == 0 {
			continue
		}
		col := func(f func(row) float64) []float64 {
			xs := make([]float64, len(rs))
			for i, rw := range rs {
				xs[i] = f(rw)
			}
			return xs
		}
		totals := col(func(rw row) float64 { return rw.total })
		total := median(totals)
		db := median(col(func(rw row) float64 { return rw.db }))
		net := median(col(func(rw row) float64 { return rw.net }))
		share := median(col(func(rw row) float64 { return rw.db / rw.total }))
		stmts := median(col(func(rw row) float64 { return rw.stmts }))
		_, _ = fmt.Fprintf(w, "| %s | %d | %s | %s | %s | %s | %.0f%% | %.1f |\n",
			op, len(rs), formatNs(total), formatNs(percentile(totals, 0.9)),
			formatNs(db), formatNs(net), 100*share, stmts)
	}
}

// percentile returns the nearest-rank p-th percentile of xs.
func percentile(xs []float64, p float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[int(math.Ceil(p*float64(len(s))))-1]
}

// median returns the median of xs, or 0 when xs is empty.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 0 {
		return (s[mid-1] + s[mid]) / 2
	}
	return s[mid]
}

// spread is the largest distance of any value from center.
func spread(xs []float64, center float64) float64 {
	var m float64
	for _, x := range xs {
		m = math.Max(m, math.Abs(x-center))
	}
	return m
}

func formatNs(ns float64) string {
	switch a := math.Abs(ns); {
	case a >= 1e9:
		return fmt.Sprintf("%.2f s", ns/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.1f ms", ns/1e6)
	default:
		return fmt.Sprintf("%.1f µs", ns/1e3)
	}
}
