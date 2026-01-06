package schema

import (
	"bytes"
	"fmt"
	"strings"
)

// ListGeneratedSQL contains all SQL generated for list functions.
// This is separate from check function generation to keep concerns isolated.
// Applied atomically during migration alongside check functions.
type ListGeneratedSQL struct {
	// ListObjectsFunctions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized list_objects function (list_{type}_{relation}_objects).
	ListObjectsFunctions []string

	// ListSubjectsFunctions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized list_subjects function (list_{type}_{relation}_subjects).
	ListSubjectsFunctions []string

	// ListObjectsDispatcher contains the list_accessible_objects dispatcher function
	// that routes to specialized functions or falls back to generic.
	ListObjectsDispatcher string

	// ListSubjectsDispatcher contains the list_accessible_subjects dispatcher function
	// that routes to specialized functions or falls back to generic.
	ListSubjectsDispatcher string
}

// GenerateListSQL generates specialized SQL functions for list operations.
// The generated SQL includes:
//   - Per-relation list_objects functions (list_{type}_{relation}_objects)
//   - Per-relation list_subjects functions (list_{type}_{relation}_subjects)
//   - Dispatchers that route to specialized functions or fall back to generic
//
// During the migration phase, relations that cannot be generated will use
// the generic list functions as fallback. As more patterns are supported,
// the CanGenerateList criteria will be relaxed.
func GenerateListSQL(analyses []RelationAnalysis) (ListGeneratedSQL, error) {
	var result ListGeneratedSQL

	// Generate specialized functions for each relation that can be generated
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}

		// Generate list_objects function
		objFn, err := generateListObjectsFunction(a)
		if err != nil {
			return ListGeneratedSQL{}, fmt.Errorf("generating list_objects function for %s.%s: %w",
				a.ObjectType, a.Relation, err)
		}
		result.ListObjectsFunctions = append(result.ListObjectsFunctions, objFn)

		// Generate list_subjects function
		subjFn, err := generateListSubjectsFunction(a)
		if err != nil {
			return ListGeneratedSQL{}, fmt.Errorf("generating list_subjects function for %s.%s: %w",
				a.ObjectType, a.Relation, err)
		}
		result.ListSubjectsFunctions = append(result.ListSubjectsFunctions, subjFn)
	}

	// Generate dispatchers (always generated, even if no specialized functions)
	var err error
	result.ListObjectsDispatcher, err = generateListObjectsDispatcher(analyses)
	if err != nil {
		return ListGeneratedSQL{}, fmt.Errorf("generating list_objects dispatcher: %w", err)
	}

	result.ListSubjectsDispatcher, err = generateListSubjectsDispatcher(analyses)
	if err != nil {
		return ListGeneratedSQL{}, fmt.Errorf("generating list_subjects dispatcher: %w", err)
	}

	return result, nil
}

// listObjectsFunctionName returns the name for a specialized list_objects function.
func listObjectsFunctionName(objectType, relation string) string {
	return fmt.Sprintf("list_%s_%s_objects", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// listSubjectsFunctionName returns the name for a specialized list_subjects function.
func listSubjectsFunctionName(objectType, relation string) string {
	return fmt.Sprintf("list_%s_%s_subjects", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// generateListObjectsFunction generates a specialized list_objects function for a relation.
func generateListObjectsFunction(a RelationAnalysis) (string, error) {
	data := ListObjectsFunctionData{
		ObjectType:     a.ObjectType,
		Relation:       a.Relation,
		FunctionName:   listObjectsFunctionName(a.ObjectType, a.Relation),
		FeaturesString: a.Features.String(),
	}

	// Build relation list from simple closure relations (tuple lookup)
	data.RelationList = buildRelationList(a)

	// Populate complex closure relations (need check_permission_internal)
	data.ComplexClosureRelations = a.ComplexClosureRelations

	// Build subject_id check (with or without wildcard)
	data.SubjectIDCheck = buildSubjectIDCheck(a.Features.HasWildcard)

	// Build allowed subject types list for type restriction enforcement
	data.AllowedSubjectTypes = buildAllowedSubjectTypes(a)

	// Populate exclusion fields (Phase 3)
	data.HasExclusion = a.Features.HasExclusion
	data.SimpleExcludedRelations = a.SimpleExcludedRelations
	data.ComplexExcludedRelations = a.ComplexExcludedRelations
	data.ExcludedParentRelations = a.ExcludedParentRelations
	data.ExcludedIntersectionGroups = a.ExcludedIntersectionGroups

	// Select appropriate template based on features
	templateName := selectListObjectsTemplate(a)

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("executing list_objects template %s: %w", templateName, err)
	}
	return buf.String(), nil
}

// selectListObjectsTemplate selects the appropriate list_objects template based on features.
func selectListObjectsTemplate(a RelationAnalysis) string {
	// Phase 3: Use exclusion template if relation has any exclusion patterns
	if a.Features.HasExclusion {
		return "list_objects_exclusion.tpl.sql"
	}
	// Phase 2: Direct/implied patterns use the direct template
	return "list_objects_direct.tpl.sql"
}

// generateListSubjectsFunction generates a specialized list_subjects function for a relation.
func generateListSubjectsFunction(a RelationAnalysis) (string, error) {
	data := ListSubjectsFunctionData{
		ObjectType:     a.ObjectType,
		Relation:       a.Relation,
		FunctionName:   listSubjectsFunctionName(a.ObjectType, a.Relation),
		FeaturesString: a.Features.String(),
		HasWildcard:    a.Features.HasWildcard,
	}

	// Build relation list from simple closure relations (tuple lookup)
	data.RelationList = buildRelationList(a)

	// Populate complex closure relations (need check_permission_internal)
	data.ComplexClosureRelations = a.ComplexClosureRelations

	// Build allowed subject types list for type restriction enforcement
	data.AllowedSubjectTypes = buildAllowedSubjectTypes(a)

	// Populate exclusion fields (Phase 3)
	data.HasExclusion = a.Features.HasExclusion
	data.SimpleExcludedRelations = a.SimpleExcludedRelations
	data.ComplexExcludedRelations = a.ComplexExcludedRelations
	data.ExcludedParentRelations = a.ExcludedParentRelations
	data.ExcludedIntersectionGroups = a.ExcludedIntersectionGroups

	// Select appropriate template based on features
	templateName := selectListSubjectsTemplate(a)

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("executing list_subjects template %s: %w", templateName, err)
	}
	return buf.String(), nil
}

// selectListSubjectsTemplate selects the appropriate list_subjects template based on features.
func selectListSubjectsTemplate(a RelationAnalysis) string {
	// Phase 3: Use exclusion template if relation has any exclusion patterns
	if a.Features.HasExclusion {
		return "list_subjects_exclusion.tpl.sql"
	}
	// Phase 2: Direct/implied patterns use the direct template
	return "list_subjects_direct.tpl.sql"
}

// ListObjectsFunctionData contains data for rendering list_objects function templates.
type ListObjectsFunctionData struct {
	ObjectType     string
	Relation       string
	FunctionName   string
	FeaturesString string

	// RelationList is a SQL-formatted list of simple closure relations to check.
	// e.g., "'viewer', 'editor', 'owner'" - only relations that can use tuple lookup
	RelationList string

	// ComplexClosureRelations are closure relations that need check_permission_internal.
	// These have exclusions or other complex features that can't be resolved via tuple lookup.
	ComplexClosureRelations []string

	// SubjectIDCheck is the SQL fragment for checking subject_id with wildcard support.
	// e.g., "(t.subject_id = p_subject_id OR t.subject_id = '*')"
	SubjectIDCheck string

	// AllowedSubjectTypes is a SQL-formatted list of allowed subject types.
	// e.g., "'user', 'employee'" - used to enforce model type restrictions.
	AllowedSubjectTypes string

	// Exclusion-related fields (Phase 3)
	HasExclusion bool // true if this relation has exclusion patterns

	// SimpleExcludedRelations are excluded relations that can use direct tuple lookup.
	// These are relations without userset, TTU, exclusion, intersection, or implied closure.
	SimpleExcludedRelations []string

	// ComplexExcludedRelations are excluded relations that need check_permission_internal.
	// These have userset, TTU, intersection, exclusion, or implied closure.
	ComplexExcludedRelations []string

	// ExcludedParentRelations are TTU exclusions like "but not viewer from parent".
	ExcludedParentRelations []ParentRelationInfo

	// ExcludedIntersectionGroups are intersection exclusions like "but not (editor and owner)".
	ExcludedIntersectionGroups []IntersectionGroupInfo
}

// ListSubjectsFunctionData contains data for rendering list_subjects function templates.
type ListSubjectsFunctionData struct {
	ObjectType     string
	Relation       string
	FunctionName   string
	FeaturesString string

	// RelationList is a SQL-formatted list of simple closure relations to check.
	RelationList string

	// ComplexClosureRelations are closure relations that need check_permission_internal.
	ComplexClosureRelations []string

	// AllowedSubjectTypes is a SQL-formatted list of allowed subject types.
	// e.g., "'user', 'employee'" - used to enforce model type restrictions.
	AllowedSubjectTypes string

	// HasWildcard is true if the model allows wildcard subjects.
	// When false, wildcard tuples (subject_id = '*') should be excluded from results.
	HasWildcard bool

	// Exclusion-related fields (Phase 3)
	HasExclusion bool // true if this relation has exclusion patterns

	// SimpleExcludedRelations are excluded relations that can use direct tuple lookup.
	SimpleExcludedRelations []string

	// ComplexExcludedRelations are excluded relations that need check_permission_internal.
	ComplexExcludedRelations []string

	// ExcludedParentRelations are TTU exclusions like "but not viewer from parent".
	ExcludedParentRelations []ParentRelationInfo

	// ExcludedIntersectionGroups are intersection exclusions like "but not (editor and owner)".
	ExcludedIntersectionGroups []IntersectionGroupInfo
}

// ListDispatcherData contains data for rendering list dispatcher templates.
type ListDispatcherData struct {
	// HasSpecializedFunctions is true if any specialized list functions were generated.
	HasSpecializedFunctions bool

	// Cases contains the routing cases for specialized functions.
	Cases []ListDispatcherCase
}

// ListDispatcherCase represents a single routing case in the list dispatcher.
type ListDispatcherCase struct {
	ObjectType   string
	Relation     string
	FunctionName string
}

// generateListObjectsDispatcher generates the list_accessible_objects dispatcher.
// For Phase 1, this always falls through to the generic implementation.
func generateListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	data := ListDispatcherData{
		HasSpecializedFunctions: false,
		Cases:                   nil,
	}

	// Build cases for relations that can be generated
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}
		data.Cases = append(data.Cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		})
	}
	data.HasSpecializedFunctions = len(data.Cases) > 0

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "list_objects_dispatcher.tpl.sql", data); err != nil {
		return "", fmt.Errorf("executing list_objects_dispatcher template: %w", err)
	}
	return buf.String(), nil
}

// generateListSubjectsDispatcher generates the list_accessible_subjects dispatcher.
// For Phase 1, this always falls through to the generic implementation.
func generateListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	data := ListDispatcherData{
		HasSpecializedFunctions: false,
		Cases:                   nil,
	}

	// Build cases for relations that can be generated
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}
		data.Cases = append(data.Cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		})
	}
	data.HasSpecializedFunctions = len(data.Cases) > 0

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "list_subjects_dispatcher.tpl.sql", data); err != nil {
		return "", fmt.Errorf("executing list_subjects_dispatcher template: %w", err)
	}
	return buf.String(), nil
}

// buildRelationList builds a SQL-formatted list of simple relations from the closure.
// For example: "'viewer', 'editor', 'owner'"
// Only includes relations that can be resolved via tuple lookup (SimpleClosureRelations).
// Complex closure relations (with exclusions, etc.) are handled separately via check_permission_internal.
func buildRelationList(a RelationAnalysis) string {
	// Build relation list from self + simple closure relations
	// Complex closure relations are handled via function calls, not tuple lookup
	relations := []string{a.Relation}
	relations = append(relations, a.SimpleClosureRelations...)

	// Fallback to satisfying relations only if no partition was computed at all
	// (for backwards compatibility when closure relations not yet partitioned).
	// If ComplexClosureRelations is non-empty, the partition was computed and
	// we should use only the simple relations (even if that's just self).
	if len(a.SimpleClosureRelations) == 0 && len(a.ComplexClosureRelations) == 0 && len(a.SatisfyingRelations) > 0 {
		relations = a.SatisfyingRelations
	}

	quoted := make([]string, len(relations))
	for i, r := range relations {
		quoted[i] = fmt.Sprintf("'%s'", r)
	}
	return strings.Join(quoted, ", ")
}

// buildSubjectIDCheck builds the SQL fragment for checking subject_id.
// When hasWildcard is true, also matches wildcard tuples (subject_id = '*').
func buildSubjectIDCheck(hasWildcard bool) string {
	if hasWildcard {
		return "(t.subject_id = p_subject_id OR t.subject_id = '*')"
	}
	// Exclude wildcard tuples when model doesn't allow wildcards
	return "t.subject_id = p_subject_id AND t.subject_id != '*'"
}

// buildAllowedSubjectTypes builds a SQL-formatted list of allowed subject types.
// This enforces model type restrictions in list queries.
func buildAllowedSubjectTypes(a RelationAnalysis) string {
	// Use AllowedSubjectTypes if available (computed from closure)
	types := a.AllowedSubjectTypes
	if len(types) == 0 {
		// Fallback to DirectSubjectTypes
		types = a.DirectSubjectTypes
	}
	if len(types) == 0 {
		// No types - return empty which will cause no matches
		return "''"
	}

	quoted := make([]string, len(types))
	for i, t := range types {
		quoted[i] = fmt.Sprintf("'%s'", t)
	}
	return strings.Join(quoted, ", ")
}
