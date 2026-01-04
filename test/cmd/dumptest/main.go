// Command dumptest dumps OpenFGA test cases in a human-readable format.
//
// Usage:
//
//	dumptest                     # List all available test names
//	dumptest <name>              # Dump a specific test by exact name
//	dumptest -pattern <regex>    # Dump tests matching a regex pattern
//	dumptest -all                # Dump all tests (warning: very long output)
//
// Examples:
//
//	dumptest wildcard_direct
//	dumptest -pattern "^userset"
//	dumptest -pattern "computed_userset|ttu_"
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/openfga/openfga/assets"
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
	Model                 string                  `json:"model"`
	Tuples                []Tuple                 `json:"tuples"`
	CheckAssertions       []CheckAssertion        `json:"checkAssertions"`
	ListObjectsAssertions []ListObjectsAssertion  `json:"listObjectsAssertions"`
	ListUsersAssertions   []ListUsersAssertion    `json:"listUsersAssertions"`
}

// Tuple represents a relationship tuple.
type Tuple struct {
	User     string `json:"user"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

// CheckAssertion represents an expected result for a Check call.
type CheckAssertion struct {
	Tuple            Tuple   `json:"tuple"`
	ContextualTuples []Tuple `json:"contextualTuples"`
	Expectation      bool    `json:"expectation"`
	ErrorCode        int     `json:"errorCode"`
}

// ListObjectsAssertion represents an expected result for ListObjects.
type ListObjectsAssertion struct {
	Request struct {
		User     string `json:"user"`
		Type     string `json:"type"`
		Relation string `json:"relation"`
	} `json:"request"`
	Expectation []string `json:"expectation"`
}

// ListUsersAssertion represents an expected result for ListUsers.
type ListUsersAssertion struct {
	Request struct {
		Filters  []string `json:"filters"`
		Object   string   `json:"object"`
		Relation string   `json:"relation"`
	} `json:"request"`
	Expectation []string `json:"expectation"`
}

func main() {
	pattern := flag.String("pattern", "", "Regex pattern to match test names")
	all := flag.Bool("all", false, "Dump all tests (warning: very long output)")
	flag.Parse()

	tests, err := loadTests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading tests: %v\n", err)
		os.Exit(1)
	}

	// If no arguments and no flags, list all test names
	if flag.NArg() == 0 && *pattern == "" && !*all {
		fmt.Printf("Available OpenFGA tests (%d total):\n\n", len(tests))
		for _, tc := range tests {
			fmt.Printf("  %s\n", tc.Name)
		}
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  dumptest <name>            # Dump a specific test by name")
		fmt.Println("  dumptest -pattern <regex>  # Dump tests matching a pattern")
		fmt.Println("  dumptest -all              # Dump all tests")
		return
	}

	// Dump all tests
	if *all {
		for i, tc := range tests {
			if i > 0 {
				fmt.Println("\n" + strings.Repeat("=", 80) + "\n")
			}
			dumpTest(tc)
		}
		return
	}

	// Dump tests matching pattern
	if *pattern != "" {
		re, err := regexp.Compile(*pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid pattern: %v\n", err)
			os.Exit(1)
		}

		var matched int
		for _, tc := range tests {
			if re.MatchString(tc.Name) {
				if matched > 0 {
					fmt.Println("\n" + strings.Repeat("=", 80) + "\n")
				}
				dumpTest(tc)
				matched++
			}
		}

		if matched == 0 {
			fmt.Fprintf(os.Stderr, "No tests matched pattern %q\n", *pattern)
			os.Exit(1)
		}
		return
	}

	// Dump specific test by name
	name := flag.Arg(0)
	for _, tc := range tests {
		if tc.Name == name {
			dumpTest(tc)
			return
		}
	}

	// Try partial match if exact match fails
	var partialMatches []string
	for _, tc := range tests {
		if strings.Contains(tc.Name, name) {
			partialMatches = append(partialMatches, tc.Name)
		}
	}

	if len(partialMatches) > 0 {
		fmt.Fprintf(os.Stderr, "Test %q not found. Did you mean one of these?\n\n", name)
		for _, m := range partialMatches {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Test %q not found\n", name)
	os.Exit(1)
}

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

func dumpTest(tc TestCase) {
	fmt.Printf("Test: %s\n", tc.Name)
	fmt.Println(strings.Repeat("-", len(tc.Name)+6))

	for i, stage := range tc.Stages {
		fmt.Printf("\n=== Stage %d ===\n", i+1)

		// Model
		fmt.Println("\nModel:")
		fmt.Println("```fga")
		fmt.Print(stage.Model)
		if !strings.HasSuffix(stage.Model, "\n") {
			fmt.Println()
		}
		fmt.Println("```")

		// Tuples
		if len(stage.Tuples) > 0 {
			fmt.Println("\nTuples:")
			for _, t := range stage.Tuples {
				fmt.Printf("  %s | %s | %s\n", t.User, t.Relation, t.Object)
			}
		} else {
			fmt.Println("\nTuples: (none)")
		}

		// Check Assertions
		if len(stage.CheckAssertions) > 0 {
			fmt.Println("\nCheck Assertions:")
			for j, c := range stage.CheckAssertions {
				expectStr := "ALLOW"
				if !c.Expectation {
					expectStr = "DENY"
				}
				if c.ErrorCode != 0 {
					expectStr = fmt.Sprintf("ERROR(%d)", c.ErrorCode)
				}

				fmt.Printf("  [%d] %s: %s | %s | %s\n",
					j+1, expectStr, c.Tuple.User, c.Tuple.Relation, c.Tuple.Object)

				if len(c.ContextualTuples) > 0 {
					fmt.Println("      with contextual tuples:")
					for _, ct := range c.ContextualTuples {
						fmt.Printf("        %s | %s | %s\n", ct.User, ct.Relation, ct.Object)
					}
				}
			}
		}

		// ListObjects Assertions
		if len(stage.ListObjectsAssertions) > 0 {
			fmt.Println("\nListObjects Assertions:")
			for j, l := range stage.ListObjectsAssertions {
				fmt.Printf("  [%d] user=%s relation=%s type=%s\n",
					j+1, l.Request.User, l.Request.Relation, l.Request.Type)
				if len(l.Expectation) > 0 {
					fmt.Printf("      => %v\n", l.Expectation)
				} else {
					fmt.Println("      => (empty)")
				}
			}
		}

		// ListUsers Assertions
		if len(stage.ListUsersAssertions) > 0 {
			fmt.Println("\nListUsers Assertions:")
			for j, l := range stage.ListUsersAssertions {
				fmt.Printf("  [%d] object=%s relation=%s filters=%v\n",
					j+1, l.Request.Object, l.Request.Relation, l.Request.Filters)
				if len(l.Expectation) > 0 {
					fmt.Printf("      => %v\n", l.Expectation)
				} else {
					fmt.Println("      => (empty)")
				}
			}
		}
	}
}
