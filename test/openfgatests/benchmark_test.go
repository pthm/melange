package openfgatests_test

import (
	"os"
	"testing"

	"github.com/pthm/melange/test/openfgatests"
)

// =============================================================================
// OpenFGA Benchmarks
// These benchmarks run the official OpenFGA test cases as performance tests.
// =============================================================================

// BenchmarkOpenFGA_All runs benchmarks for all OpenFGA tests.
// This provides a comprehensive performance profile across all test patterns.
//
// Usage:
//
//	go test -bench=BenchmarkOpenFGA_All ./test/openfgatests/...
func BenchmarkOpenFGA_All(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchAllTests(b)
}

// BenchmarkOpenFGA_DirectAssignment benchmarks direct relation assignment [user].
func BenchmarkOpenFGA_DirectAssignment(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "this")
}

// BenchmarkOpenFGA_ComputedUserset benchmarks computed relations (role hierarchy).
func BenchmarkOpenFGA_ComputedUserset(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "computed_userset|computeduserset")
}

// BenchmarkOpenFGA_TupleToUserset benchmarks parent inheritance (FROM pattern).
func BenchmarkOpenFGA_TupleToUserset(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "tuple_to_userset|ttu_")
}

// BenchmarkOpenFGA_Wildcards benchmarks public access via wildcards [user:*].
func BenchmarkOpenFGA_Wildcards(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "wildcard|public")
}

// BenchmarkOpenFGA_Exclusion benchmarks the BUT NOT pattern.
func BenchmarkOpenFGA_Exclusion(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "exclusion|butnot|but_not")
}

// BenchmarkOpenFGA_Union benchmarks the OR pattern.
func BenchmarkOpenFGA_Union(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "union")
}

// BenchmarkOpenFGA_Intersection benchmarks the AND pattern.
func BenchmarkOpenFGA_Intersection(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "intersection")
}

// BenchmarkOpenFGA_UsersetReferences benchmarks userset references [type#relation].
func BenchmarkOpenFGA_UsersetReferences(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "userset")
}

// BenchmarkOpenFGA_Cycles benchmarks cycle handling in authorization models.
func BenchmarkOpenFGA_Cycles(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "cycle|recursive")
}

// BenchmarkOpenFGA_ContextualTuples benchmarks contextual tuples (temporary tuples in requests).
func BenchmarkOpenFGA_ContextualTuples(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	openfgatests.BenchTestsByPattern(b, "contextual")
}

// =============================================================================
// Custom Pattern Benchmarks
// =============================================================================

// BenchmarkOpenFGAByPattern runs benchmarks for tests matching a regex pattern.
// Use: OPENFGA_BENCH_PATTERN="^wildcard" go test -bench=BenchmarkOpenFGAByPattern ./test/openfgatests/...
func BenchmarkOpenFGAByPattern(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	pattern := os.Getenv("OPENFGA_BENCH_PATTERN")
	if pattern == "" {
		pattern = ".*" // Default to all tests
	}
	openfgatests.BenchTestsByPattern(b, pattern)
}

// BenchmarkOpenFGAByName runs a benchmark for a specific test by exact name.
// Use: OPENFGA_BENCH_NAME=wildcard_direct go test -bench=BenchmarkOpenFGAByName ./test/openfgatests/...
func BenchmarkOpenFGAByName(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}
	name := os.Getenv("OPENFGA_BENCH_NAME")
	if name == "" {
		b.Skip("OPENFGA_BENCH_NAME not set")
	}
	openfgatests.BenchTestByName(b, name)
}

// =============================================================================
// Aggregate Benchmarks
// These provide summary statistics across categories.
// =============================================================================

// BenchmarkOpenFGA_ByCategory runs benchmarks organized by authorization pattern.
// This is useful for comparing performance across different FGA patterns.
func BenchmarkOpenFGA_ByCategory(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	categories := []struct {
		name    string
		pattern string
	}{
		{"DirectAssignment", "this"},
		{"ComputedUserset", "computed_userset|computeduserset"},
		{"TupleToUserset", "tuple_to_userset|ttu_"},
		{"Wildcards", "wildcard|public"},
		{"Exclusion", "exclusion|butnot|but_not"},
		{"Union", "union"},
		{"Intersection", "intersection"},
		{"ContextualTuples", "contextual"},
	}

	for _, cat := range categories {
		b.Run(cat.name, func(b *testing.B) {
			openfgatests.BenchTestsByPattern(b, cat.pattern)
		})
	}
}
