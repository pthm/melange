package sqlgen

import (
	"fmt"
)

// stringToDSLExpr converts a string expression to Expr.
// Recognizes common parameter names and converts them to DSL constants.
func stringToDSLExpr(s string) Expr {
	if s == "" {
		return nil
	}
	switch s {
	case "p_subject_type":
		return SubjectType
	case "p_subject_id":
		return SubjectID
	case "p_object_type":
		return ObjectType
	case "p_object_id":
		return ObjectID
	default:
		return Raw(s)
	}
}

// CheckPermissionExprDSL returns a DSL expression for a check_permission call.
func CheckPermissionExprDSL(functionName, subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr string, expect bool) Expr {
	result := "1"
	if !expect {
		result = "0"
	}
	return Raw(fmt.Sprintf(
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

// CheckPermissionInternalExprDSL returns a DSL expression for a check_permission_internal call.
func CheckPermissionInternalExprDSL(subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr string, expect bool) Expr {
	result := "1"
	if !expect {
		result = "0"
	}
	return Raw(fmt.Sprintf(
		"check_permission_internal(%s, %s, '%s', %s, %s, ARRAY[]::TEXT[]) = %s",
		subjectTypeExpr,
		subjectIDExpr,
		relation,
		objectTypeExpr,
		objectIDExpr,
		result,
	))
}

// RenderDSLExprs converts a slice of DSL expressions to SQL strings.
func RenderDSLExprs(exprs []Expr) []string {
	result := make([]string, 0, len(exprs))
	for _, expr := range exprs {
		if expr != nil {
			result = append(result, expr.SQL())
		}
	}
	return result
}
