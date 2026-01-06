// Command dumpinventory produces an inventory report of relations that fall back
// to generic permission checking, grouped by the reason they cannot generate
// specialized SQL functions.
//
// This is a Phase 0 tool for tracking progress toward full codegen coverage.
//
// Usage:
//
//	dumpinventory              # Show summary for all OpenFGA tests
//	dumpinventory <name>       # Show details for a specific test
//	dumpinventory -summary     # Show only the summary counts by reason
//
// Output:
//
//	Groups relations by CannotGenerateReason and lists affected relations.
//	This serves as a progress checklist for improving codegen coverage.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/openfga/openfga/assets"
	"sigs.k8s.io/yaml"

	"github.com/pthm/melange/schema"
	"github.com/pthm/melange/tooling"
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
	Model string `json:"model"`
}

// RelationInfo holds information about a relation that can't generate.
type RelationInfo struct {
	TestName   string
	ObjectType string
	Relation   string
	Features   string
	Reason     string
}

func main() {
	summaryOnly := flag.Bool("summary", false, "Show only summary counts by reason")
	flag.Parse()

	tests, err := loadTests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading tests: %v\n", err)
		os.Exit(1)
	}

	// If a specific test is requested, show detailed output for that test
	if flag.NArg() == 1 {
		name := flag.Arg(0)
		for _, tc := range tests {
			if tc.Name == name {
				dumpTestInventory(tc)
				return
			}
		}
		fmt.Fprintf(os.Stderr, "Test %q not found\n", name)
		os.Exit(1)
	}

	// Collect all relations that can't generate across all tests
	byReason := make(map[string][]RelationInfo)
	var totalRelations, canGenerateCount, cannotGenerateCount int

	for _, tc := range tests {
		for _, stage := range tc.Stages {
			types, err := tooling.ParseSchemaString(stage.Model)
			if err != nil {
				continue
			}

			closureRows := schema.ComputeRelationClosure(types)
			analyses := schema.AnalyzeRelations(types, closureRows)
			analyses = schema.ComputeCanGenerate(analyses)

			for _, a := range analyses {
				totalRelations++
				if a.CanGenerate {
					canGenerateCount++
				} else {
					cannotGenerateCount++
					reason := a.CannotGenerateReason
					if reason == "" {
						reason = "(no reason recorded)"
					}
					byReason[reason] = append(byReason[reason], RelationInfo{
						TestName:   tc.Name,
						ObjectType: a.ObjectType,
						Relation:   a.Relation,
						Features:   a.Features.String(),
						Reason:     reason,
					})
				}
			}
		}
	}

	// Print summary
	fmt.Println("# Codegen Coverage Inventory Report")
	fmt.Println()
	fmt.Printf("Total relations analyzed: %d\n", totalRelations)
	fmt.Printf("Can generate:            %d (%.1f%%)\n", canGenerateCount, float64(canGenerateCount)/float64(totalRelations)*100)
	fmt.Printf("Cannot generate:         %d (%.1f%%)\n", cannotGenerateCount, float64(cannotGenerateCount)/float64(totalRelations)*100)
	fmt.Println()

	// Sort reasons by count (descending)
	type reasonCount struct {
		reason string
		count  int
	}
	var reasons []reasonCount
	for reason, infos := range byReason {
		reasons = append(reasons, reasonCount{reason, len(infos)})
	}
	sort.Slice(reasons, func(i, j int) bool {
		return reasons[i].count > reasons[j].count
	})

	// Print by reason
	fmt.Println("## Relations by Reason")
	fmt.Println()
	for _, rc := range reasons {
		fmt.Printf("### %s (%d relations)\n", rc.reason, rc.count)
		if !*summaryOnly {
			fmt.Println()
			infos := byReason[rc.reason]
			// Sort by test name, then object type, then relation
			sort.Slice(infos, func(i, j int) bool {
				if infos[i].TestName != infos[j].TestName {
					return infos[i].TestName < infos[j].TestName
				}
				if infos[i].ObjectType != infos[j].ObjectType {
					return infos[i].ObjectType < infos[j].ObjectType
				}
				return infos[i].Relation < infos[j].Relation
			})
			// Group by test for readability
			currentTest := ""
			for _, info := range infos {
				if info.TestName != currentTest {
					if currentTest != "" {
						fmt.Println()
					}
					fmt.Printf("  **%s**\n", info.TestName)
					currentTest = info.TestName
				}
				fmt.Printf("    - %s.%s [%s]\n", info.ObjectType, info.Relation, info.Features)
			}
			fmt.Println()
		}
	}
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

func dumpTestInventory(tc TestCase) {
	fmt.Printf("# Inventory for: %s\n", tc.Name)
	fmt.Println(strings.Repeat("-", len(tc.Name)+16))

	for i, stage := range tc.Stages {
		if i > 0 {
			fmt.Println()
		}
		if len(tc.Stages) > 1 {
			fmt.Printf("\n## Stage %d\n", i+1)
		}

		types, err := tooling.ParseSchemaString(stage.Model)
		if err != nil {
			fmt.Printf("\n⚠️  Parse error: %v\n", err)
			continue
		}

		closureRows := schema.ComputeRelationClosure(types)
		analyses := schema.AnalyzeRelations(types, closureRows)
		analyses = schema.ComputeCanGenerate(analyses)

		// Sort for consistent output
		sort.Slice(analyses, func(i, j int) bool {
			if analyses[i].ObjectType != analyses[j].ObjectType {
				return analyses[i].ObjectType < analyses[j].ObjectType
			}
			return analyses[i].Relation < analyses[j].Relation
		})

		var canGenerate, cannotGenerate []schema.RelationAnalysis
		for _, a := range analyses {
			if a.CanGenerate {
				canGenerate = append(canGenerate, a)
			} else {
				cannotGenerate = append(cannotGenerate, a)
			}
		}

		fmt.Println()
		fmt.Printf("**Can Generate (%d):**\n", len(canGenerate))
		for _, a := range canGenerate {
			fmt.Printf("  ✓ %s.%s [%s]\n", a.ObjectType, a.Relation, a.Features.String())
		}

		fmt.Println()
		fmt.Printf("**Cannot Generate (%d):**\n", len(cannotGenerate))
		for _, a := range cannotGenerate {
			fmt.Printf("  ✗ %s.%s [%s]\n", a.ObjectType, a.Relation, a.Features.String())
			if a.CannotGenerateReason != "" {
				fmt.Printf("    Reason: %s\n", a.CannotGenerateReason)
			}
		}
	}
}
