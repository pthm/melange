package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
	"github.com/stephenafamo/bob/dialect/psql"
)

// CheckPermissionInternalExpr returns a Bob expression for a check_permission_internal call.
// This is used in places where Bob query builders need Bob expressions directly.
func CheckPermissionInternalExpr(subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr string, expect bool) psql.Expression {
	result := "1"
	if !expect {
		result = "0"
	}
	return psql.Raw(fmt.Sprintf(
		"check_permission_internal(%s, %s, '%s', %s, %s, ARRAY[]::TEXT[]) = %s",
		subjectTypeExpr,
		subjectIDExpr,
		relation,
		objectTypeExpr,
		objectIDExpr,
		result,
	))
}

// toDSLExclusionConfig converts ExclusionInput to dsl.ExclusionConfig
func toDSLExclusionConfig(input ExclusionInput) dsl.ExclusionConfig {
	// Convert string expressions to dsl.Expr
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)
	subjectIDExpr := stringToDSLExpr(input.SubjectIDExpr)

	// Convert ExcludedParentRelation types
	var parentRels []dsl.ExcludedParentRelation
	for _, rel := range input.ExcludedParentRelations {
		parentRels = append(parentRels, dsl.ExcludedParentRelation{
			Relation:            rel.Relation,
			LinkingRelation:     rel.LinkingRelation,
			AllowedLinkingTypes: rel.AllowedLinkingTypes,
		})
	}

	// Convert ExcludedIntersectionGroup types
	var intersectionGroups []dsl.ExcludedIntersectionGroup
	for _, group := range input.ExcludedIntersection {
		var parts []dsl.ExcludedIntersectionPart
		for _, part := range group.Parts {
			dslPart := dsl.ExcludedIntersectionPart{
				Relation:         part.Relation,
				ExcludedRelation: part.ExcludedRelation,
			}
			if part.ParentRelation != nil {
				dslPart.ParentRelation = &dsl.ExcludedParentRelation{
					Relation:            part.ParentRelation.Relation,
					LinkingRelation:     part.ParentRelation.LinkingRelation,
					AllowedLinkingTypes: part.ParentRelation.AllowedLinkingTypes,
				}
			}
			parts = append(parts, dslPart)
		}
		intersectionGroups = append(intersectionGroups, dsl.ExcludedIntersectionGroup{Parts: parts})
	}

	return dsl.ExclusionConfig{
		ObjectType:               input.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  input.SimpleExcludedRelations,
		ComplexExcludedRelations: input.ComplexExcludedRelations,
		ExcludedParentRelations:  parentRels,
		ExcludedIntersection:     intersectionGroups,
	}
}

// stringToDSLExpr converts a string expression to dsl.Expr
// Recognizes common parameter names and converts them to DSL constants
func stringToDSLExpr(s string) dsl.Expr {
	if s == "" {
		return nil
	}
	switch s {
	case "p_subject_type":
		return dsl.SubjectType
	case "p_subject_id":
		return dsl.SubjectID
	case "p_object_type":
		return dsl.ObjectType
	case "p_object_id":
		return dsl.ObjectID
	default:
		return dsl.Raw(s)
	}
}

// CheckPermissionExprDSL returns a DSL expression for a check_permission call
func CheckPermissionExprDSL(functionName, subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr string, expect bool) dsl.Expr {
	result := "1"
	if !expect {
		result = "0"
	}
	return dsl.Raw(fmt.Sprintf(
		"%s(%s, %s, '%s', %s, %s) = %s",
		functionName,
		subjectTypeExpr,
		subjectIDExpr,
		relation,
		objectTypeExpr,
		objectIDExpr,
		result,
	))
}

// CheckPermissionInternalExprDSL returns a DSL expression for a check_permission_internal call
func CheckPermissionInternalExprDSL(subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr string, expect bool) dsl.Expr {
	result := "1"
	if !expect {
		result = "0"
	}
	return dsl.Raw(fmt.Sprintf(
		"check_permission_internal(%s, %s, '%s', %s, %s, ARRAY[]::TEXT[]) = %s",
		subjectTypeExpr,
		subjectIDExpr,
		relation,
		objectTypeExpr,
		objectIDExpr,
		result,
	))
}

// ExclusionPredicatesDSL converts an ExclusionInput to a slice of DSL expressions
func ExclusionPredicatesDSL(input ExclusionInput) []dsl.Expr {
	config := toDSLExclusionConfig(input)
	return config.BuildPredicates()
}

// RenderDSLExprs converts a slice of DSL expressions to SQL strings
func RenderDSLExprs(exprs []dsl.Expr) []string {
	result := make([]string, 0, len(exprs))
	for _, expr := range exprs {
		if expr != nil {
			result = append(result, expr.SQL())
		}
	}
	return result
}
