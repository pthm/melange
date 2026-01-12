package sqlgen

import (
	"fmt"
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
// This is applied atomically during migration.
type GeneratedSQL struct {
	// Functions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized check function.
	Functions []string
	// NoWildcardFunctions contains CREATE OR REPLACE FUNCTION statements
	// for no-wildcard variants of each specialized check function.
	NoWildcardFunctions []string

	// Dispatcher contains the check_permission dispatcher function
	// that routes to specialized functions.
	Dispatcher string

	// DispatcherNoWildcard contains the check_permission_no_wildcard dispatcher.
	DispatcherNoWildcard string
}

// GenerateSQL generates specialized SQL functions for all relations.
// The generated SQL includes:
//   - Per-relation check functions (check_{type}_{relation})
//   - A dispatcher that routes check_permission to specialized functions
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
func sanitizeIdentifier(s string) string {
	var result strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			result.WriteRune(c)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}

// computeHasStandaloneAccess determines if the relation has access paths outside of intersections.
func computeHasStandaloneAccess(a RelationAnalysis) bool {
	// If no intersection, all access paths are standalone
	if !a.Features.HasIntersection {
		return a.Features.HasDirect || a.Features.HasImplied || a.Features.HasUserset || a.Features.HasRecursive
	}

	// Check if any intersection group has a "This" part, meaning direct access is
	// constrained by the intersection rather than being standalone.
	hasIntersectionWithThis := false
	for _, group := range a.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				hasIntersectionWithThis = true
				break
			}
		}
		if hasIntersectionWithThis {
			break
		}
	}

	// If direct types are inside an intersection (This pattern), don't count them as standalone.
	// Userset patterns from subject type restrictions (e.g., [group#member]) are also part of
	// the "This" pattern, so they shouldn't be standalone either.
	// Check for other standalone access paths (implied, recursive).
	hasStandaloneDirect := a.Features.HasDirect && !hasIntersectionWithThis
	hasStandaloneImplied := a.Features.HasImplied
	hasStandaloneUserset := a.Features.HasUserset && !hasIntersectionWithThis
	hasStandaloneRecursive := a.Features.HasRecursive

	return hasStandaloneDirect || hasStandaloneImplied || hasStandaloneUserset || hasStandaloneRecursive
}

// DispatcherData contains data for rendering dispatcher template.
type DispatcherData struct {
	FunctionName            string
	HasSpecializedFunctions bool
	Cases                   []DispatcherCase
}

// DispatcherCase represents a single CASE WHEN branch in the dispatcher.
type DispatcherCase struct {
	ObjectType        string
	Relation          string
	CheckFunctionName string
}

// CollectFunctionNames returns all function names that will be generated for the given analyses.
// This is used for migration tracking and orphan detection.
//
// The returned list includes:
//   - Specialized check functions: check_{type}_{relation}
//   - No-wildcard check variants: check_{type}_{relation}_no_wildcard
//   - Specialized list functions: list_{type}_{relation}_objects, list_{type}_{relation}_subjects
//   - Dispatcher functions (always included)
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
