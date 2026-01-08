// Command dumpinventory produces an inventory report of relations that fall back
// to generic permission checking or list functions, grouped by the reason they
// cannot generate specialized SQL functions.
//
// This is a Phase 0 tool for tracking progress toward full codegen coverage.
//
// Usage:
//
//	dumpinventory              # Show summary for all OpenFGA tests (check + list)
//	dumpinventory <name>       # Show details for a specific test
//	dumpinventory -summary     # Show only the summary counts by reason
//	dumpinventory -check       # Show only check codegen inventory
//	dumpinventory -list        # Show only list codegen inventory
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
	Kind       string // "check" or "list"
}

func main() {
	summaryOnly := flag.Bool("summary", false, "Show only summary counts by reason")
	checkOnly := flag.Bool("check", false, "Show only check codegen inventory")
	listOnly := flag.Bool("list", false, "Show only list codegen inventory")
	flag.Parse()

	// Default to showing both if neither -check nor -list specified
	showCheck := !*listOnly
	showList := !*checkOnly
	if *checkOnly && *listOnly {
		showCheck = true
		showList = true
	}

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
				dumpTestInventory(tc, showCheck, showList)
				return
			}
		}
		fmt.Fprintf(os.Stderr, "Test %q not found\n", name)
		os.Exit(1)
	}

	// Collect all relations that can't generate across all tests
	checkByReason := make(map[string][]RelationInfo)
	listByReason := make(map[string][]RelationInfo)
	var totalRelations int
	var checkCanGenerate, checkCannotGenerate int
	var listCanGenerate, listCannotGenerate int

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

				// Check codegen stats
				if a.CanGenerate {
					checkCanGenerate++
				} else {
					checkCannotGenerate++
					reason := a.CannotGenerateReason
					if reason == "" {
						reason = "(no reason recorded)"
					}
					checkByReason[reason] = append(checkByReason[reason], RelationInfo{
						TestName:   tc.Name,
						ObjectType: a.ObjectType,
						Relation:   a.Relation,
						Features:   a.Features.String(),
						Reason:     reason,
						Kind:       "check",
					})
				}

				// List codegen stats
				if a.CanGenerateList() {
					listCanGenerate++
				} else {
					listCannotGenerate++
					reason := a.CannotGenerateListReason
					if reason == "" {
						reason = "(no reason recorded)"
					}
					listByReason[reason] = append(listByReason[reason], RelationInfo{
						TestName:   tc.Name,
						ObjectType: a.ObjectType,
						Relation:   a.Relation,
						Features:   a.Features.String(),
						Reason:     reason,
						Kind:       "list",
					})
				}
			}
		}
	}

	// Print summary
	fmt.Println("# Codegen Coverage Inventory Report")
	fmt.Println()
	fmt.Printf("Total relations analyzed: %d\n", totalRelations)
	fmt.Println()

	if showCheck {
		fmt.Println("## Check Function Coverage")
		fmt.Printf("Can generate:    %d (%.1f%%)\n", checkCanGenerate, float64(checkCanGenerate)/float64(totalRelations)*100)
		fmt.Printf("Cannot generate: %d (%.1f%%)\n", checkCannotGenerate, float64(checkCannotGenerate)/float64(totalRelations)*100)
		fmt.Println()
	}

	if showList {
		fmt.Println("## List Function Coverage")
		fmt.Printf("Can generate:    %d (%.1f%%)\n", listCanGenerate, float64(listCanGenerate)/float64(totalRelations)*100)
		fmt.Printf("Cannot generate: %d (%.1f%%)\n", listCannotGenerate, float64(listCannotGenerate)/float64(totalRelations)*100)
		fmt.Println()
	}

	// Print check reasons
	if showCheck && checkCannotGenerate > 0 {
		printReasonSection("Check Functions", checkByReason, *summaryOnly)
	}

	// Print list reasons
	if showList && listCannotGenerate > 0 {
		printReasonSection("List Functions", listByReason, *summaryOnly)
	}
}

func printReasonSection(title string, byReason map[string][]RelationInfo, summaryOnly bool) {
	// Sort reasons by count (descending)
	type reasonCount struct {
		reason string
		count  int
	}
	reasons := make([]reasonCount, 0, len(byReason))
	for reason, infos := range byReason {
		reasons = append(reasons, reasonCount{reason, len(infos)})
	}
	sort.Slice(reasons, func(i, j int) bool {
		return reasons[i].count > reasons[j].count
	})

	// Print by reason
	fmt.Printf("## %s - Cannot Generate by Reason\n", title)
	fmt.Println()
	for _, rc := range reasons {
		fmt.Printf("### %s (%d relations)\n", rc.reason, rc.count)
		if !summaryOnly {
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

func dumpTestInventory(tc TestCase, showCheck, showList bool) {
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

		if showCheck {
			var checkCanGenerate, checkCannotGenerate []schema.RelationAnalysis
			for _, a := range analyses {
				if a.CanGenerate {
					checkCanGenerate = append(checkCanGenerate, a)
				} else {
					checkCannotGenerate = append(checkCannotGenerate, a)
				}
			}

			fmt.Println()
			fmt.Println("## Check Functions")
			fmt.Println()
			fmt.Printf("**Can Generate (%d):**\n", len(checkCanGenerate))
			for _, a := range checkCanGenerate {
				fmt.Printf("  ✓ %s.%s [%s]\n", a.ObjectType, a.Relation, a.Features.String())
			}

			fmt.Println()
			fmt.Printf("**Cannot Generate (%d):**\n", len(checkCannotGenerate))
			for _, a := range checkCannotGenerate {
				fmt.Printf("  ✗ %s.%s [%s]\n", a.ObjectType, a.Relation, a.Features.String())
				if a.CannotGenerateReason != "" {
					fmt.Printf("    Reason: %s\n", a.CannotGenerateReason)
				}
			}
		}

		if showList {
			var listCanGenerate, listCannotGenerate []schema.RelationAnalysis
			for _, a := range analyses {
				if a.CanGenerateList() {
					listCanGenerate = append(listCanGenerate, a)
				} else {
					listCannotGenerate = append(listCannotGenerate, a)
				}
			}

			fmt.Println()
			fmt.Println("## List Functions")
			fmt.Println()
			fmt.Printf("**Can Generate (%d):**\n", len(listCanGenerate))
			for _, a := range listCanGenerate {
				fmt.Printf("  ✓ %s.%s [%s]\n", a.ObjectType, a.Relation, a.Features.String())
			}

			fmt.Println()
			fmt.Printf("**Cannot Generate (%d):**\n", len(listCannotGenerate))
			for _, a := range listCannotGenerate {
				fmt.Printf("  ✗ %s.%s [%s]\n", a.ObjectType, a.Relation, a.Features.String())
				if a.CannotGenerateListReason != "" {
					fmt.Printf("    Reason: %s\n", a.CannotGenerateListReason)
				}
			}
		}
	}
}
