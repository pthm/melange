package sqlgen

import (
	"fmt"
	"strings"
)

// listUsersetPatternInput contains data for building userset pattern blocks.
// Used by both list_objects and list_subjects block builders.
type listUsersetPatternInput struct {
	SubjectType         string
	SubjectRelation     string
	SatisfyingRelations []string
	SourceRelations     []string
	SourceRelation      string
	IsClosurePattern    bool
	HasWildcard         bool
	IsComplex           bool
	IsSelfReferential   bool
}

// filterComplexClosureRelations returns complex closure relations excluding those
// that are already handled by intersection closure blocks.
func filterComplexClosureRelations(a RelationAnalysis) []string {
	intersectionSet := make(map[string]bool)
	for _, rel := range a.IntersectionClosureRelations {
		intersectionSet[rel] = true
	}
	var complexRels []string
	for _, rel := range a.ComplexClosureRelations {
		if !intersectionSet[rel] {
			complexRels = append(complexRels, rel)
		}
	}
	return complexRels
}

// buildAllowedSubjectTypesList returns the list of allowed subject types for a relation.
// Falls back to DirectSubjectTypes if AllowedSubjectTypes is not populated.
func buildAllowedSubjectTypesList(a RelationAnalysis) []string {
	subjectTypes := a.AllowedSubjectTypes
	if len(subjectTypes) == 0 {
		subjectTypes = a.DirectSubjectTypes
	}
	if len(subjectTypes) == 0 {
		return []string{""}
	}
	return subjectTypes
}

// buildAllSatisfyingRelationsList returns all relations that satisfy the given relation.
// Falls back to just the relation itself if SatisfyingRelations is not populated.
func buildAllSatisfyingRelationsList(a RelationAnalysis) []string {
	relations := a.SatisfyingRelations
	if len(relations) == 0 {
		relations = []string{a.Relation}
	}
	return relations
}

// buildListUsersetPatternInputs builds template data for userset pattern expansion.
// Combines both direct UsersetPatterns and ClosureUsersetPatterns.
func buildListUsersetPatternInputs(a RelationAnalysis) []listUsersetPatternInput {
	if len(a.UsersetPatterns) == 0 && len(a.ClosureUsersetPatterns) == 0 {
		return nil
	}
	directRelationList := buildTupleLookupRelations(a)
	var patterns []listUsersetPatternInput

	for _, p := range a.UsersetPatterns {
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}
		patterns = append(patterns, listUsersetPatternInput{
			SubjectType:         p.SubjectType,
			SubjectRelation:     p.SubjectRelation,
			SatisfyingRelations: satisfying,
			SourceRelations:     directRelationList,
			SourceRelation:      "",
			IsClosurePattern:    false,
			HasWildcard:         p.HasWildcard,
			IsComplex:           p.IsComplex,
			IsSelfReferential:   p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
		})
	}

	for _, p := range a.ClosureUsersetPatterns {
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}
		patterns = append(patterns, listUsersetPatternInput{
			SubjectType:         p.SubjectType,
			SubjectRelation:     p.SubjectRelation,
			SatisfyingRelations: satisfying,
			SourceRelations:     []string{p.SourceRelation},
			SourceRelation:      p.SourceRelation,
			IsClosurePattern:    true,
			HasWildcard:         p.HasWildcard,
			IsComplex:           p.IsComplex,
			IsSelfReferential:   p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
		})
	}

	return patterns
}

// buildExclusionInput creates an ExclusionConfig from a RelationAnalysis.
// This configures exclusion predicates for SQL query generation.
func buildExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
		ExcludedParentRelations:  convertParentRelations(a.ExcludedParentRelations),
		ExcludedIntersection:     convertIntersectionGroups(a.ExcludedIntersectionGroups),
	}
}

// convertParentRelations converts ParentRelationInfo slice to ExcludedParentRelation slice.
func convertParentRelations(relations []ParentRelationInfo) []ExcludedParentRelation {
	if len(relations) == 0 {
		return nil
	}
	result := make([]ExcludedParentRelation, 0, len(relations))
	for _, rel := range relations {
		result = append(result, ExcludedParentRelation(rel))
	}
	return result
}

// convertIntersectionGroups converts IntersectionGroupInfo slice to ExcludedIntersectionGroup slice.
func convertIntersectionGroups(groups []IntersectionGroupInfo) []ExcludedIntersectionGroup {
	if len(groups) == 0 {
		return nil
	}
	result := make([]ExcludedIntersectionGroup, 0, len(groups))
	for _, group := range groups {
		parts := make([]ExcludedIntersectionPart, 0, len(group.Parts))
		for _, part := range group.Parts {
			if part.ParentRelation != nil {
				parts = append(parts, ExcludedIntersectionPart{
					ParentRelation: &ExcludedParentRelation{
						Relation:            part.ParentRelation.Relation,
						LinkingRelation:     part.ParentRelation.LinkingRelation,
						AllowedLinkingTypes: part.ParentRelation.AllowedLinkingTypes,
					},
				})
				continue
			}
			parts = append(parts, ExcludedIntersectionPart{
				Relation:         part.Relation,
				ExcludedRelation: part.ExcludedRelation,
			})
		}
		result = append(result, ExcludedIntersectionGroup{Parts: parts})
	}
	return result
}

// buildSimpleComplexExclusionInput creates an ExclusionConfig with only simple and complex
// exclusions (no TTU or intersection exclusions).
func buildSimpleComplexExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
	}
}

// formatQueryBlock formats a query block with comments and indentation.
// This function is retained for compatibility with some render functions.
//
// Deprecated: Use QueryBlock and RenderBlocks/RenderUnionBlocks in sql.go instead.
func formatQueryBlock(comments []string, sql string) string {
	lines := make([]string, 0, len(comments)+1)
	for _, comment := range comments {
		lines = append(lines, "    "+comment)
	}
	lines = append(lines, indentLines(sql, "    "))
	return strings.Join(lines, "\n")
}

// trimTrailingSemicolon removes a trailing semicolon from a SQL string.
func trimTrailingSemicolon(input string) string {
	trimmed := strings.TrimSpace(input)
	return strings.TrimSuffix(trimmed, ";")
}

// renderUsersetWildcardTail renders the wildcard handling tail for list_subjects functions.
func renderUsersetWildcardTail(a RelationAnalysis) string {
	if a.Features.HasWildcard {
		return fmt.Sprintf(`
        -- Wildcard handling: when wildcard exists, filter non-wildcard subjects
        -- to only those with explicit (non-wildcard-derived) access
        SELECT br.subject_id
        FROM base_results br
        CROSS JOIN has_wildcard hw
        WHERE (NOT hw.has_wildcard)
           OR (br.subject_id = '*')
           OR (
               br.subject_id != '*'
               AND check_permission_no_wildcard(
                   p_subject_type,
                   br.subject_id,
                   '%s',
                   '%s',
                   p_object_id
               ) = 1
           );`, a.Relation, a.ObjectType)
	}

	return "        SELECT br.subject_id FROM base_results br;"
}

// generateListObjectsDispatcher generates the list_accessible_objects dispatcher function.
func generateListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	// Build the body with routing cases
	var bodyBuf strings.Builder
	if len(cases) > 0 {
		bodyBuf.WriteString("-- Route to specialized functions for all type/relation pairs\n")
		for _, c := range cases {
			bodyBuf.WriteString("IF p_object_type = '")
			bodyBuf.WriteString(c.ObjectType)
			bodyBuf.WriteString("' AND p_relation = '")
			bodyBuf.WriteString(c.Relation)
			bodyBuf.WriteString("' THEN\n")
			bodyBuf.WriteString("    RETURN QUERY SELECT * FROM ")
			bodyBuf.WriteString(c.FunctionName)
			bodyBuf.WriteString("(p_subject_type, p_subject_id, p_limit, p_after);\n")
			bodyBuf.WriteString("    RETURN;\n")
			bodyBuf.WriteString("END IF;\n")
		}
	}
	bodyBuf.WriteString("\n")
	bodyBuf.WriteString("-- Unknown type/relation pair - return empty result (relation not defined in model)\n")
	bodyBuf.WriteString("-- This matches check_permission behavior for unknown relations (returns 0/denied)\n")
	bodyBuf.WriteString("RETURN;")

	fn := PlpgsqlFunction{
		Name:    "list_accessible_objects",
		Args:    ListObjectsDispatcherArgs(),
		Returns: "TABLE (object_id TEXT, next_cursor TEXT)",
		Header: []string{
			"Generated dispatcher for list_accessible_objects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: []Stmt{
			RawStmt{SQLText: bodyBuf.String()},
		},
	}
	return fn.SQL(), nil
}

// generateListSubjectsDispatcher generates the list_accessible_subjects dispatcher function.
func generateListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	// Build the body with routing cases
	var bodyBuf strings.Builder
	if len(cases) > 0 {
		bodyBuf.WriteString("-- Route to specialized functions for all type/relation pairs\n")
		for _, c := range cases {
			bodyBuf.WriteString("IF p_object_type = '")
			bodyBuf.WriteString(c.ObjectType)
			bodyBuf.WriteString("' AND p_relation = '")
			bodyBuf.WriteString(c.Relation)
			bodyBuf.WriteString("' THEN\n")
			bodyBuf.WriteString("    RETURN QUERY SELECT * FROM ")
			bodyBuf.WriteString(c.FunctionName)
			bodyBuf.WriteString("(p_object_id, p_subject_type, p_limit, p_after);\n")
			bodyBuf.WriteString("    RETURN;\n")
			bodyBuf.WriteString("END IF;\n")
		}
	}
	bodyBuf.WriteString("\n")
	bodyBuf.WriteString("-- Unknown type/relation pair - return empty result (relation not defined in model)\n")
	bodyBuf.WriteString("-- This matches check_permission behavior for unknown relations (returns 0/denied)\n")
	bodyBuf.WriteString("RETURN;")

	fn := PlpgsqlFunction{
		Name:    "list_accessible_subjects",
		Args:    ListSubjectsDispatcherArgs(),
		Returns: "TABLE (subject_id TEXT, next_cursor TEXT)",
		Header: []string{
			"Generated dispatcher for list_accessible_subjects",
			"Routes to specialized functions for all type/relation pairs",
		},
		Body: []Stmt{
			RawStmt{SQLText: bodyBuf.String()},
		},
	}
	return fn.SQL(), nil
}
