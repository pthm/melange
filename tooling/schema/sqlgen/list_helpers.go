package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
)

// stringToDSLExpr converts a string expression to dsl.Expr.
// Recognizes common parameter names and converts them to DSL constants.
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

// CheckPermissionExprDSL returns a DSL expression for a check_permission call.
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

// CheckPermissionInternalExprDSL returns a DSL expression for a check_permission_internal call.
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

// RenderDSLExprs converts a slice of DSL expressions to SQL strings.
func RenderDSLExprs(exprs []dsl.Expr) []string {
	result := make([]string, 0, len(exprs))
	for _, expr := range exprs {
		if expr != nil {
			result = append(result, expr.SQL())
		}
	}
	return result
}
