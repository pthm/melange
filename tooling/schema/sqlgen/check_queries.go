package sqlgen

import (
	"fmt"
)

// =============================================================================
// Direct Check Queries
// =============================================================================

type DirectCheckInput struct {
	ObjectType    string
	Relations     []string
	SubjectTypes  []string
	AllowWildcard bool
}

func DirectCheck(input DirectCheckInput) (string, error) {
	if len(input.Relations) == 0 {
		return "", fmt.Errorf("direct check requires at least one relation")
	}
	if len(input.SubjectTypes) == 0 {
		return "", fmt.Errorf("direct check requires at least one subject type")
	}

	q := Tuples("").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Column: "subject_type"}, Values: input.SubjectTypes},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}

// =============================================================================
// Exclusion Check Queries
// =============================================================================

type ExclusionCheckInput struct {
	ObjectType       string
	ExcludedRelation string
	AllowWildcard    bool
}

func ExclusionCheck(input ExclusionCheckInput) (string, error) {
	q := Tuples("").
		ObjectType(input.ObjectType).
		Relations(input.ExcludedRelation).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}

// =============================================================================
// Userset Check Queries
// =============================================================================

type UsersetCheckInput struct {
	ObjectType          string
	Relation            string
	SubjectType         string
	SubjectRelation     string
	SatisfyingRelations []string
	AllowWildcard       bool
}

func UsersetCheck(input UsersetCheckInput) (string, error) {
	if len(input.SatisfyingRelations) == 0 {
		return "", fmt.Errorf("userset check requires at least one satisfying relation")
	}

	q := Tuples("grant_tuple").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(
			Eq{Left: Col{Table: "grant_tuple", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "grant_tuple", Column: "subject_type"}, Right: Lit(input.SubjectType)},
			HasUserset{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
				Right: Lit(input.SubjectRelation),
			},
		).
		JoinTuples("membership",
			Eq{Left: Col{Table: "membership", Column: "object_type"}, Right: Lit(input.SubjectType)},
			Eq{
				Left:  Col{Table: "membership", Column: "object_id"},
				Right: UsersetObjectID{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
			},
			In{Expr: Col{Table: "membership", Column: "relation"}, Values: input.SatisfyingRelations},
			Eq{Left: Col{Table: "membership", Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Table: "membership", Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}

// =============================================================================
// Userset Subject Check Queries
// =============================================================================

type UsersetSubjectSelfCheckInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
}

func UsersetSubjectSelfCheckQuery(input UsersetSubjectSelfCheckInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s)", input.ClosureValues)

	stmt := SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Alias:   "c(object_type, relation, satisfying_relation)",
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
			Eq{
				Left:  Col{Table: "c", Column: "satisfying_relation"},
				Right: SubstringUsersetRelation{Source: SubjectID},
			},
		),
		Limit: 1,
	}
	return stmt.SQL(), nil
}

type UsersetSubjectComputedCheckInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
	UsersetValues string
}

func UsersetSubjectComputedCheckQuery(input UsersetSubjectComputedCheckInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues)
	usersetTable := fmt.Sprintf("(VALUES %s) AS m(object_type, relation, subject_type, subject_relation)", input.UsersetValues)
	subjClosureTable := fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", input.ClosureValues)

	stmt := SelectStmt{
		Columns: []string{"1"},
		From:    "melange_tuples",
		Alias:   "t",
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: closureTable,
				On: And(
					Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
					Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
					Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Col{Table: "t", Column: "relation"}},
				),
			},
			{
				Type:  "INNER",
				Table: usersetTable,
				On: And(
					Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(input.ObjectType)},
					Eq{Left: Col{Table: "m", Column: "relation"}, Right: Col{Table: "c", Column: "satisfying_relation"}},
					Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Col{Table: "t", Column: "subject_type"}},
				),
			},
			{
				Type:  "INNER",
				Table: subjClosureTable,
				On: And(
					Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Col{Table: "t", Column: "subject_type"}},
					Eq{
						Left:  Col{Table: "subj_c", Column: "relation"},
						Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
					},
					Eq{
						Left:  Col{Table: "subj_c", Column: "satisfying_relation"},
						Right: SubstringUsersetRelation{Source: SubjectID},
					},
				),
			},
		},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(input.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				Right: UsersetObjectID{Source: SubjectID},
			},
		),
		Limit: 1,
	}
	return stmt.SQL(), nil
}

// =============================================================================
// Check Permission Expression Helpers
// =============================================================================

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
