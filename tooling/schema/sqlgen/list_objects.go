package sqlgen

import (
	"fmt"
)

type ListObjectsDirectInput struct {
	ObjectType          string
	Relations           []string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          ExclusionConfig
}

func ListObjectsDirectQuery(input ListObjectsDirectInput) (string, error) {
	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: input.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, input.AllowWildcard),
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
	Exclusions    ExclusionConfig
}

func ListObjectsUsersetSubjectQuery(input ListObjectsUsersetSubjectInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues)

	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: SubjectType},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	// Subject match: either exact match or userset object ID match with closure exists
	subjectMatch := Or(
		Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: SubjectID},
		And(
			Eq{
				Left:  UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				Right: UsersetObjectID{Source: SubjectID},
			},
			Raw(closureExistsStmt.Exists()),
		),
	)

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			HasUserset{Source: SubjectID},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
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
	Exclusions          ExclusionConfig
}

func ListObjectsComplexClosureQuery(input ListObjectsComplexClosureInput) (string, error) {
	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: input.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, input.AllowWildcard),
			CheckPermission{
				Subject:     SubjectParams(),
				Relation:    input.Relation,
				Object:      LiteralObject(input.ObjectType, Col{Table: "t", Column: "object_id"}),
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
	stmt := SelectStmt{
		Columns: []string{"*"},
		From:    functionName + "(p_subject_type, p_subject_id)",
	}
	return stmt.SQL(), nil
}

func ListObjectsIntersectionClosureValidatedQuery(objectType, relation, functionName string) (string, error) {
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"icr.object_id"},
		From:     functionName + "(p_subject_type, p_subject_id)",
		Alias:    "icr",
		Where: CheckPermission{
			Subject:     SubjectParams(),
			Relation:    relation,
			Object:      LiteralObject(objectType, Col{Table: "icr", Column: "object_id"}),
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
	Exclusions          ExclusionConfig
}

func ListObjectsUsersetPatternSimpleQuery(input ListObjectsUsersetPatternSimpleInput) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{
			Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
			Right: Lit(input.SubjectRelation),
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject:     SubjectParams(),
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, Col{Table: "t", Column: "object_id"}),
			ExpectAllow: true,
		})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("m",
			Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(input.SubjectType)},
			Eq{
				Left:  Col{Table: "m", Column: "object_id"},
				Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			},
			In{Expr: Col{Table: "m", Column: "relation"}, Values: input.SatisfyingRelations},
			Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: input.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "m", Column: "subject_id"}, SubjectID, input.AllowWildcard),
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
	Exclusions       ExclusionConfig
}

func ListObjectsUsersetPatternComplexQuery(input ListObjectsUsersetPatternComplexInput) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{
			Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
			Right: Lit(input.SubjectRelation),
		},
		CheckPermission{
			Subject:  SubjectParams(),
			Relation: input.SubjectRelation,
			Object: ObjectRef{
				Type: Lit(input.SubjectType),
				ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			},
			ExpectAllow: true,
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject:     SubjectParams(),
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, Col{Table: "t", Column: "object_id"}),
			ExpectAllow: true,
		})
	}

	q := Tuples("t").
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
	closureExistsStmt := SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	stmt := SelectStmt{
		Columns: []string{"split_part(p_subject_id, '#', 1) AS object_id"},
		Where: And(
			HasUserset{Source: SubjectID},
			Eq{Left: SubjectType, Right: Lit(input.ObjectType)},
			Raw(closureExistsStmt.Exists()),
		),
	}

	return stmt.SQL(), nil
}

type ListObjectsCrossTypeTTUInput struct {
	ObjectType      string
	LinkingRelation string
	Relation        string
	CrossTypes      []string
	Exclusions      ExclusionConfig
}

func ListObjectsCrossTypeTTUQuery(input ListObjectsCrossTypeTTUInput) (string, error) {
	q := Tuples("child").
		ObjectType(input.ObjectType).
		Relations(input.LinkingRelation).
		Where(
			In{Expr: Col{Table: "child", Column: "subject_type"}, Values: input.CrossTypes},
			CheckPermission{
				Subject:  SubjectParams(),
				Relation: input.Relation,
				Object: ObjectRef{
					Type: Col{Table: "child", Column: "subject_type"},
					ID:   Col{Table: "child", Column: "subject_id"},
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
	Exclusions       ExclusionConfig
}

func ListObjectsRecursiveTTUQuery(input ListObjectsRecursiveTTUInput) (string, error) {
	// This is a CTE recursive query pattern - uses 'accessible' as the source table
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth"},
		From:     "accessible",
		Alias:    "a",
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "child",
				On: And(
					Eq{Left: Col{Table: "child", Column: "object_type"}, Right: Lit(input.ObjectType)},
					In{Expr: Col{Table: "child", Column: "relation"}, Values: input.LinkingRelations},
					Eq{Left: Col{Table: "child", Column: "subject_type"}, Right: Lit(input.ObjectType)},
					Eq{Left: Col{Table: "child", Column: "subject_id"}, Right: Col{Table: "a", Column: "object_id"}},
				),
			},
		},
		Where: Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)},
	}

	// Add exclusion predicates to WHERE
	predicates := input.Exclusions.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]Expr{stmt.Where}, predicates...)
		stmt.Where = And(allPredicates...)
	}

	return stmt.SQL(), nil
}
