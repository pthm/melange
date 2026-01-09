package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
)

func rawExpr(expr string) psql.Expression {
	return psql.Raw(expr)
}

func existsExpr(q bob.Query) (bob.Expression, error) {
	sql, err := renderQuery(q)
	if err != nil {
		return nil, err
	}
	return psql.Raw("EXISTS (\n" + sql + "\n)"), nil
}

func notExistsExpr(q bob.Query) (bob.Expression, error) {
	sql, err := renderQuery(q)
	if err != nil {
		return nil, err
	}
	return psql.Raw("NOT EXISTS (\n" + sql + "\n)"), nil
}

func CheckPermissionExpr(functionName, subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr string, expect bool) psql.Expression {
	result := "1"
	if !expect {
		result = "0"
	}
	return psql.Raw(fmt.Sprintf(
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
