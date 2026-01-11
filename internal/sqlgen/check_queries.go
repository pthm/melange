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
	ClosureValues string      // Deprecated: use ClosureRows
	ClosureRows   []ValuesRow // Typed closure rows (preferred)
}

func UsersetSubjectSelfCheckQuery(input UsersetSubjectSelfCheckInput) (string, error) {
	stmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(input.ClosureRows, input.ClosureValues, "c"),
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
	ClosureValues string      // Deprecated: use ClosureRows
	ClosureRows   []ValuesRow // Typed closure rows (preferred)
	UsersetValues string      // Deprecated: use UsersetRows
	UsersetRows   []ValuesRow // Typed userset rows (preferred)
}

func UsersetSubjectComputedCheckQuery(input UsersetSubjectComputedCheckInput) (string, error) {
	stmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{
			{
				Type:      "INNER",
				TableExpr: ClosureTable(input.ClosureRows, input.ClosureValues, "c"),
				On: And(
					Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
					Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
					Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Col{Table: "t", Column: "relation"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: UsersetTable(input.UsersetRows, input.UsersetValues, "m"),
				On: And(
					Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(input.ObjectType)},
					Eq{Left: Col{Table: "m", Column: "relation"}, Right: Col{Table: "c", Column: "satisfying_relation"}},
					Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Col{Table: "t", Column: "subject_type"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: ClosureTable(input.ClosureRows, input.ClosureValues, "subj_c"),
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

// CheckPermissionExpr returns a typed expression for a check_permission call.
// Uses CheckPermissionCall with the existing typed Subject/Object refs.
func CheckPermissionExpr(functionName string, subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermissionCall{
		FunctionName: functionName,
		Subject:      subject,
		Relation:     relation,
		Object:       object,
		ExpectAllow:  expect,
	}
}

// CheckPermissionInternalExpr returns a typed expression for check_permission_internal.
// Uses CheckPermission with the existing typed Subject/Object refs.
func CheckPermissionInternalExpr(subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermission{
		Subject:     subject,
		Relation:    relation,
		Object:      object,
		ExpectAllow: expect,
	}
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
