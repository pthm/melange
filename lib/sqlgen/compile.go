package sqlgen

import (
	"fmt"
	"slices"
	"strings"
)

// formatSQLStringList formats a list of strings as a SQL-safe list.
// For example, ["user", "org"] becomes "'user', 'org'".
// Returns empty string if the list is empty.
func formatSQLStringList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("'%s'", item)
	}
	return strings.Join(quoted, ", ")
}

func buildTupleLookupRelations(a RelationAnalysis) []string {
	// Build relation list from self + simple closure relations.
	relations := []string{a.Relation}
	relations = append(relations, a.SimpleClosureRelations...)

	// Fallback to satisfying relations only if no partition was computed at all
	// (for backwards compatibility when closure relations not yet partitioned).
	// If ComplexClosureRelations is non-empty, the partition was computed and
	// we should use only the simple relations (even if that's just self).
	if len(a.SimpleClosureRelations) == 0 && len(a.ComplexClosureRelations) == 0 && len(a.SatisfyingRelations) > 0 {
		relations = a.SatisfyingRelations
	}

	return relations
}

// GeneratedSQL contains all SQL generated for a schema.
// This is applied atomically during migration to ensure consistent state.
type GeneratedSQL struct {
	// Functions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized check function (check_{type}_{relation}).
	Functions []string

	// NoWildcardFunctions contains CREATE OR REPLACE FUNCTION statements
	// for no-wildcard variants (check_{type}_{relation}_nw).
	// These skip wildcard matching for performance-critical paths.
	NoWildcardFunctions []string

	// Dispatcher contains the check_permission dispatcher function
	// that routes requests to specialized functions based on object type and relation.
	Dispatcher string

	// DispatcherNoWildcard contains the check_permission_nw dispatcher.
	DispatcherNoWildcard string

	// BulkDispatcher contains the check_permission_bulk function that evaluates
	// multiple permission checks in a single SQL call using UNION ALL branches.
	BulkDispatcher string
}

// GenerateSQL generates specialized SQL functions for all relations in the schema.
//
// For each relation, it generates:
//   - A specialized check function that evaluates permission checks efficiently
//   - A no-wildcard variant for scenarios where wildcards are disallowed
//   - Dispatcher functions that route to the appropriate specialized function
//
// The inline parameter provides precomputed closure and userset data that is
// inlined into the generated functions as VALUES clauses, eliminating runtime
// table joins for this metadata.
//
// Returns an error if any function fails to generate, though this is rare
// as the analysis phase validates generation feasibility.
func GenerateSQL(analyses []RelationAnalysis, inline InlineSQLData) (GeneratedSQL, error) {
	var result GeneratedSQL

	// Generate specialized function for each relation
	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed {
			continue
		}
		fn, err := generateCheckFunction(a, inline, false)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating check function: %w", err)
		}
		result.Functions = append(result.Functions, fn)
		noWildcardFn, err := generateCheckFunction(a, inline, true)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating no-wildcard check function: %w", err)
		}
		result.NoWildcardFunctions = append(result.NoWildcardFunctions, noWildcardFn)
	}

	// Generate dispatchers
	var err error
	result.Dispatcher, err = generateDispatcher(analyses, false)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating dispatcher: %w", err)
	}
	result.DispatcherNoWildcard, err = generateDispatcher(analyses, true)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating no-wildcard dispatcher: %w", err)
	}

	// Generate bulk dispatcher
	result.BulkDispatcher = generateBulkDispatcher(analyses)

	return result, nil
}

// functionName returns the name for a specialized check function.
func functionName(objectType, relation string) string {
	return SafeIdentifier("check_", objectType, relation, "")
}

func functionNameNoWildcard(objectType, relation string) string {
	return SafeIdentifier("check_", objectType, relation, "_nw")
}

// computeHasStandaloneAccess determines if the relation has access paths outside of intersections.
func computeHasStandaloneAccess(a RelationAnalysis) bool {
	if !a.Features.HasIntersection {
		return a.Features.HasDirect || a.Features.HasImplied || a.Features.HasUserset || a.Features.HasRecursive
	}

	// Implied and recursive are always standalone, regardless of intersection.
	if a.Features.HasImplied || a.Features.HasRecursive {
		return true
	}

	// Check if any intersection group has a "This" part, meaning direct/userset access
	// is constrained by the intersection rather than being standalone.
	hasIntersectionWithThis := slices.ContainsFunc(a.IntersectionGroups, func(g IntersectionGroupInfo) bool {
		return slices.ContainsFunc(g.Parts, func(p IntersectionPart) bool {
			return p.IsThis
		})
	})

	// Direct and userset are standalone only if not inside an intersection.
	return (a.Features.HasDirect || a.Features.HasUserset) && !hasIntersectionWithThis
}

// DispatcherData contains data for rendering the dispatcher template.
type DispatcherData struct {
	FunctionName            string
	HasSpecializedFunctions bool
	Cases                   []DispatcherCase
}

// DispatcherCase represents a single CASE WHEN branch in the dispatcher.
// Each case routes a specific (object_type, relation) pair to its specialized function.
type DispatcherCase struct {
	ObjectType          string
	Relation            string
	CheckFunctionName   string
	Inlineable          bool     // true if simple direct-assignment only (bulk dispatcher can inline EXISTS)
	DirectSubjectTypes  []string // subject types allowed for direct tuples (used in inline)
	SatisfyingRelations []string // relations in closure that satisfy this one (used in inline userset check)
}

// NamedFunction pairs a specialized function name with its generated SQL body.
// Dispatcher functions are excluded from this set; see CollectNamedFunctions.
// The SQL field is used verbatim for checksum computation and for emitting
// changed-only migrations.
type NamedFunction struct {
	Name string
	SQL  string
}

// CollectNamedFunctions returns all specialized functions paired with their SQL.
// Dispatchers are excluded — they always change when any relation changes and
// should be unconditionally included in migrations.
//
// The analyses slice must be the same slice, in the same order, passed to
// GenerateSQL and GenerateListSQL that produced generatedSQL and listSQL.
// The function walks all three in lockstep; mismatched ordering will silently
// produce incorrect name-to-SQL pairings.
func CollectNamedFunctions(
	generatedSQL GeneratedSQL,
	listSQL ListGeneratedSQL,
	analyses []RelationAnalysis,
) []NamedFunction {
	var result []NamedFunction
	checkIdx, noWildcardIdx := 0, 0
	listObjIdx, listSubjIdx := 0, 0

	for _, a := range analyses {
		if a.Capabilities.CheckAllowed {
			result = append(result, NamedFunction{
				Name: functionName(a.ObjectType, a.Relation),
				SQL:  generatedSQL.Functions[checkIdx],
			})
			checkIdx++
			result = append(result, NamedFunction{
				Name: functionNameNoWildcard(a.ObjectType, a.Relation),
				SQL:  generatedSQL.NoWildcardFunctions[noWildcardIdx],
			})
			noWildcardIdx++
		}
		if a.Capabilities.ListAllowed {
			result = append(result, NamedFunction{
				Name: listObjectsFunctionName(a.ObjectType, a.Relation),
				SQL:  listSQL.ListObjectsFunctions[listObjIdx],
			})
			listObjIdx++
			result = append(result, NamedFunction{
				Name: listSubjectsFunctionName(a.ObjectType, a.Relation),
				SQL:  listSQL.ListSubjectsFunctions[listSubjIdx],
			})
			listSubjIdx++
		}
	}

	return result
}

// CollectFunctionNames returns all function names that will be generated for the given analyses.
// This is used for migration tracking and orphan detection to identify stale functions
// that need to be dropped when the schema changes.
//
// The returned list includes:
//   - Specialized check functions: check_{type}_{relation}
//   - No-wildcard check variants: check_{type}_{relation}_nw
//   - Specialized list functions: list_{type}_{relation}_obj, list_{type}_{relation}_sub
//   - Dispatcher functions (always included): check_permission, list_accessible_objects, etc.
func CollectFunctionNames(analyses []RelationAnalysis) []string {
	var names []string

	for _, a := range analyses {
		if a.Capabilities.CheckAllowed {
			names = append(names,
				functionName(a.ObjectType, a.Relation),
				functionNameNoWildcard(a.ObjectType, a.Relation),
			)
		}
		if a.Capabilities.ListAllowed {
			names = append(names,
				listObjectsFunctionName(a.ObjectType, a.Relation),
				listSubjectsFunctionName(a.ObjectType, a.Relation),
			)
		}
	}

	// Dispatchers are always generated
	names = append(names,
		"check_permission",
		"check_permission_internal",
		"check_permission_nw",
		"check_permission_nw_internal",
		"check_permission_bulk",
		"list_accessible_objects",
		"list_accessible_subjects",
	)

	return names
}
