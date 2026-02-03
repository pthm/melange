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
	// for no-wildcard variants (check_{type}_{relation}_no_wildcard).
	// These skip wildcard matching for performance-critical paths.
	NoWildcardFunctions []string

	// Dispatcher contains the check_permission dispatcher function
	// that routes requests to specialized functions based on object type and relation.
	Dispatcher string

	// DispatcherNoWildcard contains the check_permission_no_wildcard dispatcher.
	DispatcherNoWildcard string
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

	return result, nil
}

// functionName returns the name for a specialized check function.
func functionName(objectType, relation string) string {
	return fmt.Sprintf("check_%s_%s", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

func functionNameNoWildcard(objectType, relation string) string {
	return fmt.Sprintf("check_%s_%s_no_wildcard", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// sanitizeIdentifier converts a type/relation name to a valid SQL identifier.
// Delegates to the canonical implementation in sqldsl.
func sanitizeIdentifier(s string) string {
	return Ident(s)
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
	ObjectType        string
	Relation          string
	CheckFunctionName string
}

// CollectFunctionNames returns all function names that will be generated for the given analyses.
// This is used for migration tracking and orphan detection to identify stale functions
// that need to be dropped when the schema changes.
//
// The returned list includes:
//   - Specialized check functions: check_{type}_{relation}
//   - No-wildcard check variants: check_{type}_{relation}_no_wildcard
//   - Specialized list functions: list_{type}_{relation}_objects, list_{type}_{relation}_subjects
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
		"check_permission_no_wildcard",
		"check_permission_no_wildcard_internal",
		"list_accessible_objects",
		"list_accessible_subjects",
	)

	return names
}
