// Command dumpsql dumps the generated SQL for a specific OpenFGA test case.
// This tool outputs the specialized check functions and dispatcher SQL
// that would be generated for a test's authorization model.
//
// Usage:
//
//	dumpsql <name>              # Dump SQL for a specific test by exact name
//
// Output Sections:
//
//	MODEL:      The original FGA model (for reference)
//	ANALYSIS:   Feature analysis for each relation (what SQL patterns needed)
//	FUNCTIONS:  Generated specialized check functions
//	DISPATCHER: The dispatcher function that routes to specialized functions
//
// Examples:
//
//	dumpsql wildcard_direct
//	dumpsql computed_userset_simple
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/openfga/openfga/assets"
	"sigs.k8s.io/yaml"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
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

func main() {
	analysisOnly := flag.Bool("analysis", false, "Only show relation analysis, not generated SQL")
	flag.Parse()

	tests, err := loadTests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading tests: %v\n", err)
		os.Exit(1)
	}

	// Require exactly one test name argument
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: dumpsql [options] <test_name>\n\n")
		fmt.Fprintf(os.Stderr, "Dump the generated SQL for an OpenFGA test case.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  -analysis    Only show relation analysis (features, patterns)\n\n")
		fmt.Fprintf(os.Stderr, "Use 'dumptest' to list available test names.\n")
		os.Exit(1)
	}

	opts := dumpOptions{
		analysisOnly: *analysisOnly,
	}

	// Dump specific test by name
	name := flag.Arg(0)
	for _, tc := range tests {
		if tc.Name == name {
			dumpSQL(tc, opts)
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

type dumpOptions struct {
	analysisOnly bool
}

func dumpSQL(tc TestCase, opts dumpOptions) {
	fmt.Printf("Test: %s\n", tc.Name)
	fmt.Println(strings.Repeat("-", len(tc.Name)+6))

	for i, stage := range tc.Stages {
		if i > 0 {
			fmt.Println("\n" + strings.Repeat("-", 40))
		}
		fmt.Printf("\n=== Stage %d ===\n", i+1)

		// Show original model
		fmt.Println("\n## MODEL (Original FGA)")
		fmt.Println("```fga")
		fmt.Print(stage.Model)
		if !strings.HasSuffix(stage.Model, "\n") {
			fmt.Println()
		}
		fmt.Println("```")

		// Parse the model
		types, err := parser.ParseSchemaString(stage.Model)
		if err != nil {
			fmt.Printf("\n⚠️  Parse error: %v\n", err)
			continue
		}

		// Compute derived data
		closureRows := schema.ComputeRelationClosure(types)
		// Analyze relations
		analyses := compiler.AnalyzeRelations(types, closureRows)
		analyses = compiler.ComputeCanGenerate(analyses)
		inline := compiler.BuildInlineSQLData(closureRows, analyses)

		// Show analysis
		fmt.Println("\n## RELATION ANALYSIS")
		printAnalysis(analyses)

		if opts.analysisOnly {
			continue
		}

		// Generate SQL
		generatedSQL, err := compiler.GenerateSQL(analyses, inline)
		if err != nil {
			fmt.Printf("\n⚠️  SQL generation error: %v\n", err)
			continue
		}

		// Show generated functions
		if len(generatedSQL.Functions) > 0 {
			fmt.Println("\n## GENERATED FUNCTIONS")
			for j, fn := range generatedSQL.Functions {
				if j > 0 {
					fmt.Println("\n-- " + strings.Repeat("-", 60))
				}
				fmt.Println()
				fmt.Println(fn)
			}
		} else {
			fmt.Println("\n## GENERATED FUNCTIONS")
			fmt.Println("(none - no generatable relations)")
		}

		// Show generated no-wildcard functions
		if len(generatedSQL.NoWildcardFunctions) > 0 {
			fmt.Println("\n## GENERATED FUNCTIONS NO-WILDCARD")
			for j, fn := range generatedSQL.NoWildcardFunctions {
				if j > 0 {
					fmt.Println("\n-- " + strings.Repeat("-", 60))
				}
				fmt.Println()
				fmt.Println(fn)
			}
		} else {
			fmt.Println("\n## GENERATED FUNCTIONS NO-WILDCARD")
			fmt.Println("(none - no generatable relations)")
		}

		// Show dispatcher
		if generatedSQL.Dispatcher != "" {
			fmt.Println("\n## DISPATCHER (check_permission)")
			fmt.Println()
			fmt.Println(generatedSQL.Dispatcher)
		}

		// Show no-wildcard dispatcher
		if generatedSQL.DispatcherNoWildcard != "" {
			fmt.Println("\n## DISPATCHER NO-WILDCARD (check_permission_no_wildcard)")
			fmt.Println()
			fmt.Println(generatedSQL.DispatcherNoWildcard)
		}

		// Generate list functions
		listSQL, err := compiler.GenerateListSQL(analyses, inline)
		if err != nil {
			fmt.Printf("\n⚠️  List SQL generation error: %v\n", err)
			continue
		}

		// Show list_objects dispatcher
		if listSQL.ListObjectsDispatcher != "" {
			fmt.Println("\n## LIST_OBJECTS DISPATCHER")
			fmt.Println()
			fmt.Println(listSQL.ListObjectsDispatcher)
		}

		// Show list_objects functions
		if len(listSQL.ListObjectsFunctions) > 0 {
			fmt.Println("\n## LIST_OBJECTS FUNCTIONS")
			for j, fn := range listSQL.ListObjectsFunctions {
				if j > 0 {
					fmt.Println("\n-- " + strings.Repeat("-", 60))
				}
				fmt.Println()
				fmt.Println(fn)
			}
		}

		// Show list_subjects dispatcher
		if listSQL.ListSubjectsDispatcher != "" {
			fmt.Println("\n## LIST_SUBJECTS DISPATCHER")
			fmt.Println()
			fmt.Println(listSQL.ListSubjectsDispatcher)
		}

		// Show list_subjects functions
		if len(listSQL.ListSubjectsFunctions) > 0 {
			fmt.Println("\n## LIST_SUBJECTS FUNCTIONS")
			for j, fn := range listSQL.ListSubjectsFunctions {
				if j > 0 {
					fmt.Println("\n-- " + strings.Repeat("-", 60))
				}
				fmt.Println()
				fmt.Println(fn)
			}
		}
	}
}

func printAnalysis(analyses []compiler.RelationAnalysis) {
	if len(analyses) == 0 {
		fmt.Println("(no relations)")
		return
	}

	// Sort by object type then relation for consistent output
	sort.Slice(analyses, func(i, j int) bool {
		if analyses[i].ObjectType != analyses[j].ObjectType {
			return analyses[i].ObjectType < analyses[j].ObjectType
		}
		return analyses[i].Relation < analyses[j].Relation
	})

	for _, a := range analyses {
		canGen := "✓"
		if !a.Capabilities.CheckAllowed {
			canGen = "✗"
		}
		canGenList := "✓"
		if !a.Capabilities.ListAllowed {
			canGenList = "✗"
		}
		fmt.Printf("\n%s.%s [%s] Check=%s List=%s Strategy=%s\n",
			a.ObjectType, a.Relation, a.Features.String(), canGen, canGenList, a.ListStrategy)
		if !a.Capabilities.CheckAllowed && a.Capabilities.CheckReason != "" {
			fmt.Printf("  ⚠️  Check reason: %s\n", a.Capabilities.CheckReason)
		}
		if !a.Capabilities.ListAllowed && a.Capabilities.ListReason != "" {
			fmt.Printf("  ⚠️  List reason: %s\n", a.Capabilities.ListReason)
		}

		if len(a.SatisfyingRelations) > 0 {
			fmt.Printf("  Satisfying: %v\n", a.SatisfyingRelations)
		}
		if len(a.DirectSubjectTypes) > 0 {
			fmt.Printf("  DirectSubjectTypes: %v\n", a.DirectSubjectTypes)
		}
		if len(a.AllowedSubjectTypes) > 0 {
			fmt.Printf("  AllowedSubjectTypes: %v\n", a.AllowedSubjectTypes)
		}
		if len(a.UsersetPatterns) > 0 {
			fmt.Printf("  UsersetPatterns: ")
			for i, p := range a.UsersetPatterns {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Printf("%s#%s", p.SubjectType, p.SubjectRelation)
			}
			fmt.Println()
		}
		if len(a.ClosureUsersetPatterns) > 0 {
			fmt.Printf("  ClosureUsersetPatterns: ")
			for i, p := range a.ClosureUsersetPatterns {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Printf("%s#%s", p.SubjectType, p.SubjectRelation)
			}
			fmt.Println()
		}
		if len(a.ParentRelations) > 0 {
			fmt.Printf("  ParentRelations: ")
			for i, p := range a.ParentRelations {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Printf("%s from %s", p.Relation, p.LinkingRelation)
			}
			fmt.Println()
		}
		if len(a.ExcludedRelations) > 0 {
			fmt.Printf("  ExcludedRelations: %v\n", a.ExcludedRelations)
		}
		if len(a.IntersectionGroups) > 0 {
			fmt.Printf("  IntersectionGroups: %d groups\n", len(a.IntersectionGroups))
			for gi, g := range a.IntersectionGroups {
				fmt.Printf("    [%d] ", gi)
				for pi, p := range g.Parts {
					if pi > 0 {
						fmt.Print(" AND ")
					}
					switch {
					case p.IsThis:
						fmt.Print("[this]")
					case p.ParentRelation != nil:
						fmt.Printf("(%s from %s)", p.ParentRelation.Relation, p.ParentRelation.LinkingRelation)
					default:
						fmt.Print(p.Relation)
					}
					if p.ExcludedRelation != "" {
						fmt.Printf(" but not %s", p.ExcludedRelation)
					}
				}
				fmt.Println()
			}
		}
	}
}
