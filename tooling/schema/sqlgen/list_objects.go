package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
)

type ListObjectsDirectInput struct {
	ObjectType          string
	Relations           []string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          dsl.ExclusionConfig
}

func ListObjectsDirectQuery(input ListObjectsDirectInput) (string, error) {
	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.In{Expr: dsl.SubjectType, Values: input.AllowedSubjectTypes},
			dsl.SubjectIDMatch(dsl.Col{Table: "t", Column: "subject_id"}, dsl.SubjectID, input.AllowWildcard),
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsUsersetSubjectInput struct {
	ObjectType    string
	Relations     []string
	ClosureValues string
	Exclusions    dsl.ExclusionConfig
}

func ListObjectsUsersetSubjectQuery(input ListObjectsUsersetSubjectInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues)

	// Build the closure EXISTS subquery
	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.SubjectType},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.UsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}}},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "satisfying_relation"}, Right: dsl.SubstringUsersetRelation{Source: dsl.SubjectID}},
		),
	}

	// Subject match: either exact match or userset object ID match with closure exists
	subjectMatch := dsl.Or(
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.SubjectID},
		dsl.And(
			dsl.Eq{
				Left:  dsl.UsersetObjectID{Source: dsl.Col{Table: "t", Column: "subject_id"}},
				Right: dsl.UsersetObjectID{Source: dsl.SubjectID},
			},
			dsl.Raw(closureExistsStmt.Exists()),
		),
	)

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.HasUserset{Source: dsl.SubjectID},
			dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
			subjectMatch,
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsComplexClosureInput struct {
	ObjectType          string
	Relation            string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          dsl.ExclusionConfig
}

func ListObjectsComplexClosureQuery(input ListObjectsComplexClosureInput) (string, error) {
	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.In{Expr: dsl.SubjectType, Values: input.AllowedSubjectTypes},
			dsl.SubjectIDMatch(dsl.Col{Table: "t", Column: "subject_id"}, dsl.SubjectID, input.AllowWildcard),
			dsl.CheckPermission{
				Subject:     dsl.SubjectParams(),
				Relation:    input.Relation,
				Object:      dsl.LiteralObject(input.ObjectType, dsl.Col{Table: "t", Column: "object_id"}),
				ExpectAllow: true,
			},
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

func ListObjectsIntersectionClosureQuery(functionName string) (string, error) {
	stmt := dsl.SelectStmt{
		Columns: []string{"*"},
		From:    functionName + "(p_subject_type, p_subject_id)",
	}
	return stmt.SQL(), nil
}

func ListObjectsIntersectionClosureValidatedQuery(objectType, relation, functionName string) (string, error) {
	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"icr.object_id"},
		From:     functionName + "(p_subject_type, p_subject_id)",
		Alias:    "icr",
		Where: dsl.CheckPermission{
			Subject:     dsl.SubjectParams(),
			Relation:    relation,
			Object:      dsl.LiteralObject(objectType, dsl.Col{Table: "icr", Column: "object_id"}),
			ExpectAllow: true,
		},
	}
	return stmt.SQL(), nil
}

type ListObjectsUsersetPatternSimpleInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	SatisfyingRelations []string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          dsl.ExclusionConfig
}

func ListObjectsUsersetPatternSimpleQuery(input ListObjectsUsersetPatternSimpleInput) (string, error) {
	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
		dsl.Eq{
			Left:  dsl.UsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}},
			Right: dsl.Lit(input.SubjectRelation),
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, dsl.CheckPermission{
			Subject:     dsl.SubjectParams(),
			Relation:    input.SourceRelation,
			Object:      dsl.LiteralObject(input.ObjectType, dsl.Col{Table: "t", Column: "object_id"}),
			ExpectAllow: true,
		})
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("m",
			dsl.Eq{Left: dsl.Col{Table: "m", Column: "object_type"}, Right: dsl.Lit(input.SubjectType)},
			dsl.Eq{
				Left:  dsl.Col{Table: "m", Column: "object_id"},
				Right: dsl.UsersetObjectID{Source: dsl.Col{Table: "t", Column: "subject_id"}},
			},
			dsl.In{Expr: dsl.Col{Table: "m", Column: "relation"}, Values: input.SatisfyingRelations},
			dsl.Eq{Left: dsl.Col{Table: "m", Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.In{Expr: dsl.SubjectType, Values: input.AllowedSubjectTypes},
			dsl.SubjectIDMatch(dsl.Col{Table: "m", Column: "subject_id"}, dsl.SubjectID, input.AllowWildcard),
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsUsersetPatternComplexInput struct {
	ObjectType       string
	SubjectType      string
	SubjectRelation  string
	SourceRelations  []string
	IsClosurePattern bool
	SourceRelation   string
	Exclusions       dsl.ExclusionConfig
}

func ListObjectsUsersetPatternComplexQuery(input ListObjectsUsersetPatternComplexInput) (string, error) {
	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
		dsl.Eq{
			Left:  dsl.UsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}},
			Right: dsl.Lit(input.SubjectRelation),
		},
		dsl.CheckPermission{
			Subject:  dsl.SubjectParams(),
			Relation: input.SubjectRelation,
			Object: dsl.ObjectRef{
				Type: dsl.Lit(input.SubjectType),
				ID:   dsl.UsersetObjectID{Source: dsl.Col{Table: "t", Column: "subject_id"}},
			},
			ExpectAllow: true,
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, dsl.CheckPermission{
			Subject:     dsl.SubjectParams(),
			Relation:    input.SourceRelation,
			Object:      dsl.LiteralObject(input.ObjectType, dsl.Col{Table: "t", Column: "object_id"}),
			ExpectAllow: true,
		})
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsSelfCandidateInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
}

func ListObjectsSelfCandidateQuery(input ListObjectsSelfCandidateInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues)

	// Build the closure EXISTS subquery
	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.Lit(input.Relation)},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "satisfying_relation"}, Right: dsl.SubstringUsersetRelation{Source: dsl.SubjectID}},
		),
	}

	stmt := dsl.SelectStmt{
		Columns: []string{"split_part(p_subject_id, '#', 1) AS object_id"},
		Where: dsl.And(
			dsl.HasUserset{Source: dsl.SubjectID},
			dsl.Eq{Left: dsl.SubjectType, Right: dsl.Lit(input.ObjectType)},
			dsl.Raw(closureExistsStmt.Exists()),
		),
	}

	return stmt.SQL(), nil
}

type ListObjectsCrossTypeTTUInput struct {
	ObjectType      string
	LinkingRelation string
	Relation        string
	CrossTypes      []string
	Exclusions      dsl.ExclusionConfig
}

func ListObjectsCrossTypeTTUQuery(input ListObjectsCrossTypeTTUInput) (string, error) {
	q := dsl.Tuples("child").
		ObjectType(input.ObjectType).
		Relations(input.LinkingRelation).
		Where(
			dsl.In{Expr: dsl.Col{Table: "child", Column: "subject_type"}, Values: input.CrossTypes},
			dsl.CheckPermission{
				Subject:  dsl.SubjectParams(),
				Relation: input.Relation,
				Object: dsl.ObjectRef{
					Type: dsl.Col{Table: "child", Column: "subject_type"},
					ID:   dsl.Col{Table: "child", Column: "subject_id"},
				},
				ExpectAllow: true,
			},
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsRecursiveTTUInput struct {
	ObjectType       string
	LinkingRelations []string
	Exclusions       dsl.ExclusionConfig
}

func ListObjectsRecursiveTTUQuery(input ListObjectsRecursiveTTUInput) (string, error) {
	// This is a CTE recursive query pattern - uses 'accessible' as the source table
	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth"},
		From:     "accessible",
		Alias:    "a",
		Joins: []dsl.JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "child",
				On: dsl.And(
					dsl.Eq{Left: dsl.Col{Table: "child", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
					dsl.In{Expr: dsl.Col{Table: "child", Column: "relation"}, Values: input.LinkingRelations},
					dsl.Eq{Left: dsl.Col{Table: "child", Column: "subject_type"}, Right: dsl.Lit(input.ObjectType)},
					dsl.Eq{Left: dsl.Col{Table: "child", Column: "subject_id"}, Right: dsl.Col{Table: "a", Column: "object_id"}},
				),
			},
		},
		Where: dsl.Lt{Left: dsl.Col{Table: "a", Column: "depth"}, Right: dsl.Int(25)},
	}

	// Add exclusion predicates to WHERE
	predicates := input.Exclusions.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]dsl.Expr{stmt.Where}, predicates...)
		stmt.Where = dsl.And(allPredicates...)
	}

	return stmt.SQL(), nil
}
