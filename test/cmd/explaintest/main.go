// Command explaintest runs EXPLAIN ANALYZE on OpenFGA test cases to show query
// execution plans, buffer statistics, and performance metrics.
//
// By default, runs EXPLAIN with ANALYZE, BUFFERS, TIMING, VERBOSE, SETTINGS, and WAL
// options enabled to provide comprehensive query analysis including:
// - Execution plans with actual row counts and timing
// - Buffer cache hit/miss statistics
// - Configuration parameters affecting query planning
// - WAL (Write-Ahead Log) generation information
// - Verbose details like output columns and schema-qualified names
//
// Usage:
//
//	explaintest <name>                      # Run EXPLAIN on specific test
//	explaintest --format json <name>        # JSON output
//	explaintest --assertion 3 <name>        # Single assertion only
//	explaintest --summary "^userset"        # Summary mode for pattern
//	explaintest --verbose=false <name>      # Disable verbose output
//	explaintest --settings=false <name>     # Disable settings output
//
// Examples:
//
//	explaintest wildcard_direct
//	explaintest --format json computed_userset_simple
//	explaintest --assertion 1 wildcard_direct
//	explaintest --summary ".*"
//	explaintest --verbose=false --settings=false wildcard_direct
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/openfga/openfga/assets"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"sigs.k8s.io/yaml"
)

// TestFile represents the structure of the YAML test files.
type TestFile struct {
	Tests []TestCase `json:"tests"`
}

// TestCase represents a single test from the OpenFGA test suite.
type TestCase struct {
	Name   string  `json:"name"`
	Stages []Stage `json:"stages"`
}

// Stage represents a stage within a test case.
type Stage struct {
	Model                 string                        `json:"model"`
	Tuples                []*openfgav1.TupleKey         `json:"tuples"`
	CheckAssertions       []CheckAssertion              `json:"checkAssertions"`
	ListObjectsAssertions []ListObjectsAssertion        `json:"listObjectsAssertions"`
	ListUsersAssertions   []ListUsersAssertion          `json:"listUsersAssertions"`
}

// CheckAssertion represents an expected result for a Check call.
type CheckAssertion struct {
	Tuple            *openfgav1.TupleKey   `json:"tuple"`
	ContextualTuples []*openfgav1.TupleKey `json:"contextualTuples"`
	Expectation      bool                  `json:"expectation"`
	ErrorCode        int                   `json:"errorCode"`
}

// ListObjectsAssertion represents an expected result for ListObjects.
type ListObjectsAssertion struct {
	Request struct {
		User     string `json:"user"`
		Type     string `json:"type"`
		Relation string `json:"relation"`
	} `json:"request"`
	Expectation []string `json:"expectation"`
	ErrorCode   int      `json:"errorCode"`
}

// ListUsersAssertion represents an expected result for ListUsers.
type ListUsersAssertion struct {
	Request struct {
		Filters  []string `json:"filters"`
		Object   string   `json:"object"`
		Relation string   `json:"relation"`
	} `json:"request"`
	Expectation []string `json:"expectation"`
	ErrorCode   int      `json:"errorCode"`
}

// Options for running EXPLAIN ANALYZE.
type Options struct {
	Format    string // "text", "json"
	Assertion int    // -1 for all, 1-based index for specific
	Summary   bool
	Pattern   string
	Buffers   bool
	Timing    bool
	Verbose   bool
	Settings  bool
	WAL       bool
}

func main() {
	// Flags
	format := flag.String("format", "text", "Output format: text, json")
	assertion := flag.Int("assertion", -1, "Run only specific assertion (1-based)")
	summary := flag.Bool("summary", false, "Summary mode across tests")
	pattern := flag.String("pattern", "", "Pattern for summary mode (default: all tests)")
	buffers := flag.Bool("buffers", true, "Include buffer statistics")
	timing := flag.Bool("timing", true, "Include timing information")
	verbose := flag.Bool("verbose", true, "Include verbose output (column lists, schema-qualified names)")
	settings := flag.Bool("settings", true, "Include configuration parameters that affect query planning")
	wal := flag.Bool("wal", true, "Include information about WAL record generation")
	flag.Parse()

	opts := Options{
		Format:    *format,
		Assertion: *assertion,
		Summary:   *summary,
		Pattern:   *pattern,
		Buffers:   *buffers,
		Timing:    *timing,
		Verbose:   *verbose,
		Settings:  *settings,
		WAL:       *wal,
	}

	// Summary mode
	if *summary {
		summaryPattern := *pattern
		if summaryPattern == "" {
			summaryPattern = ".*" // All tests
		}
		if err := runSummaryMode(summaryPattern); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Single test mode
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: explaintest [options] <test_name>\n\n")
		fmt.Fprintf(os.Stderr, "Run EXPLAIN ANALYZE on an OpenFGA test case.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  explaintest wildcard_direct\n")
		fmt.Fprintf(os.Stderr, "  explaintest --format json computed_userset_simple\n")
		fmt.Fprintf(os.Stderr, "  explaintest --assertion 1 wildcard_direct\n")
		fmt.Fprintf(os.Stderr, "  explaintest --summary \"^userset\"\n")
		fmt.Fprintf(os.Stderr, "\nUse 'dumptest' to list available test names.\n")
		os.Exit(1)
	}

	testName := flag.Arg(0)

	// Load and run test
	tests, err := loadTests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading tests: %v\n", err)
		os.Exit(1)
	}

	// Find test by exact name
	for _, tc := range tests {
		if tc.Name == testName {
			if err := runTest(tc, opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	// Try partial match if exact match fails
	var partialMatches []string
	for _, tc := range tests {
		if strings.Contains(tc.Name, testName) {
			partialMatches = append(partialMatches, tc.Name)
		}
	}

	if len(partialMatches) > 0 {
		fmt.Fprintf(os.Stderr, "Test %q not found. Did you mean one of these?\n\n", testName)
		for _, m := range partialMatches {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Test %q not found\n", testName)
	fmt.Fprintf(os.Stderr, "Use 'dumptest' to list available tests.\n")
	os.Exit(1)
}

// loadTests loads all OpenFGA test cases from embedded YAML.
func loadTests() ([]TestCase, error) {
	files := []string{
		"tests/consolidated_1_1_tests.yaml",
	}

	var allTests []TestCase

	for _, file := range files {
		b, err := assets.EmbedTests.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file, err)
		}

		var tf TestFile
		if err := yaml.Unmarshal(b, &tf); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}

		allTests = append(allTests, tf.Tests...)
	}

	return allTests, nil
}

// loadTestsByPattern loads tests matching a regex pattern.
func loadTestsByPattern(pattern string) ([]TestCase, error) {
	allTests, err := loadTests()
	if err != nil {
		return nil, err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	var matched []TestCase
	for _, tc := range allTests {
		if re.MatchString(tc.Name) {
			matched = append(matched, tc)
		}
	}

	if len(matched) == 0 {
		return nil, fmt.Errorf("no tests matched pattern %q", pattern)
	}

	return matched, nil
}
