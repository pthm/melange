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

func main() {
	modelsOnly := flag.Bool("models", false, "Only show model data (melange_model rows), not generated functions")
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
		fmt.Fprintf(os.Stderr, "  -models      Only show model data (melange_model rows)\n")
		fmt.Fprintf(os.Stderr, "  -analysis    Only show relation analysis (features, patterns)\n\n")
		fmt.Fprintf(os.Stderr, "Use 'dumptest' to list available test names.\n")
		os.Exit(1)
	}

	opts := dumpOptions{
		modelsOnly:   *modelsOnly,
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
	modelsOnly   bool
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
		types, err := tooling.ParseSchemaString(stage.Model)
		if err != nil {
			fmt.Printf("\n⚠️  Parse error: %v\n", err)
			continue
		}

		// Compute derived data
		models := schema.ToAuthzModels(types)
		closureRows := schema.ComputeRelationClosure(types)
		usersetRules := schema.ToUsersetRules(types, closureRows)

		// Show model data
		if opts.modelsOnly || !opts.analysisOnly {
			fmt.Println("\n## MODEL DATA (melange_model rows)")
			printModelData(models)

			fmt.Println("\n## CLOSURE DATA (melange_relation_closure rows)")
			printClosureData(closureRows)

			fmt.Println("\n## USERSET RULES (melange_userset_rules rows)")
			printUsersetRules(usersetRules)
		}

		if opts.modelsOnly {
			continue
		}

		// Analyze relations
		analyses := schema.AnalyzeRelations(types, closureRows)
		analyses = schema.ComputeCanGenerate(analyses)

		// Show analysis
		fmt.Println("\n## RELATION ANALYSIS")
		printAnalysis(analyses)

		if opts.analysisOnly {
			continue
		}

		// Generate SQL
		generatedSQL, err := schema.GenerateSQL(analyses)
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
			fmt.Println("(none - all relations use generic check_permission)")
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
		listSQL, err := schema.GenerateListSQL(analyses)
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

func printModelData(models []schema.AuthzModel) {
	if len(models) == 0 {
		fmt.Println("(empty)")
		return
	}

	// Group by object_type and relation for readability
	fmt.Printf("%-15s %-15s %-12s %-12s %-15s %-12s\n",
		"object_type", "relation", "subject_type", "implied_by", "parent_rel", "excluded")
	fmt.Println(strings.Repeat("-", 85))

	for _, m := range models {
		subjectType := "-"
		if m.SubjectType != nil {
			subjectType = *m.SubjectType
		}
		impliedBy := "-"
		if m.ImpliedBy != nil {
			impliedBy = *m.ImpliedBy
		}
		parentRel := "-"
		if m.ParentRelation != nil {
			parentRel = *m.ParentRelation
		}
		excluded := "-"
		if m.ExcludedRelation != nil {
			excluded = *m.ExcludedRelation
		}
		wildcard := ""
		if m.SubjectWildcard != nil && *m.SubjectWildcard {
			wildcard = " [*]"
		}
		fmt.Printf("%-15s %-15s %-12s %-12s %-15s %-12s%s\n",
			m.ObjectType, m.Relation, subjectType, impliedBy, parentRel, excluded, wildcard)
	}
}

func printClosureData(closure []schema.ClosureRow) {
	if len(closure) == 0 {
		fmt.Println("(empty)")
		return
	}

	fmt.Printf("%-15s %-15s %-20s %s\n",
		"object_type", "relation", "satisfying_relation", "via_path")
	fmt.Println(strings.Repeat("-", 70))

	for _, c := range closure {
		viaPath := "-"
		if len(c.ViaPath) > 0 {
			viaPath = strings.Join(c.ViaPath, " -> ")
		}
		fmt.Printf("%-15s %-15s %-20s %s\n",
			c.ObjectType, c.Relation, c.SatisfyingRelation, viaPath)
	}
}

func printUsersetRules(rules []schema.UsersetRule) {
	if len(rules) == 0 {
		fmt.Println("(empty)")
		return
	}

	fmt.Printf("%-15s %-12s %-12s %-12s %-15s %s\n",
		"object_type", "relation", "tuple_rel", "subj_type", "subj_relation", "satisfying")
	fmt.Println(strings.Repeat("-", 85))

	for _, r := range rules {
		tupleRel := r.TupleRelation
		if tupleRel == "" {
			tupleRel = "-"
		}
		subjType := r.SubjectType
		if subjType == "" {
			subjType = "-"
		}
		subjRel := r.SubjectRelation
		if subjRel == "" {
			subjRel = "-"
		}
		satisfying := r.SubjectRelationSatisfying
		if satisfying == "" {
			satisfying = "-"
		}
		fmt.Printf("%-15s %-12s %-12s %-12s %-15s %s\n",
			r.ObjectType, r.Relation, tupleRel, subjType, subjRel, satisfying)
	}
}

func printAnalysis(analyses []schema.RelationAnalysis) {
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
		if !a.CanGenerate {
			canGen = "✗"
		}
		canGenList := "✓"
		if !a.CanGenerateList() {
			canGenList = "✗"
		}
		fmt.Printf("\n%s.%s [%s] CanGenerate=%s CanGenerateList=%s\n",
			a.ObjectType, a.Relation, a.Features.String(), canGen, canGenList)
		if !a.CanGenerate && a.CannotGenerateReason != "" {
			fmt.Printf("  ⚠️  Reason: %s\n", a.CannotGenerateReason)
		}
		if !a.CanGenerateList() && a.CannotGenerateListReason != "" {
			fmt.Printf("  ⚠️  List reason: %s\n", a.CannotGenerateListReason)
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
					if p.IsThis {
						fmt.Print("[this]")
					} else if p.ParentRelation != nil {
						fmt.Printf("(%s from %s)", p.ParentRelation.Relation, p.ParentRelation.LinkingRelation)
					} else {
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

func strPtr(s string) *string {
	return &s
}
