package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
)

type ListSubjectsUsersetFilterInput struct {
	ObjectType          string
	RelationList        []string
	AllowedSubjectTypes []string
	ObjectIDExpr        string
	FilterTypeExpr      string
	FilterRelationExpr  string
	ClosureValues       string
	UseTypeGuard        bool
	ExtraPredicatesSQL  []string // Raw SQL predicate strings
}

func ListSubjectsUsersetFilterQuery(input ListSubjectsUsersetFilterInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", input.ClosureValues)

	filterTypeExpr := stringToDSLExpr(input.FilterTypeExpr)
	filterRelationExpr := stringToDSLExpr(input.FilterRelationExpr)
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)

	// Build the closure EXISTS subquery
	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "object_type"}, Right: filterTypeExpr},
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "relation"}, Right: dsl.SubstringUsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}}},
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "satisfying_relation"}, Right: filterRelationExpr},
		),
	}

	// Normalized subject expression
	normalizedSubject := fmt.Sprintf("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || %s AS subject_id", input.FilterRelationExpr)

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: filterTypeExpr},
		dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
		dsl.Or(
			dsl.Eq{Left: dsl.SubstringUsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}}, Right: filterRelationExpr},
			dsl.Raw(closureExistsStmt.Exists()),
		),
	}

	if input.UseTypeGuard {
		conditions = append(conditions, dsl.In{Expr: filterTypeExpr, Values: input.AllowedSubjectTypes})
	}

	for _, sql := range input.ExtraPredicatesSQL {
		conditions = append(conditions, dsl.Raw(sql))
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.RelationList...).
		Where(conditions...).
		Select(normalizedSubject).
		Distinct()

	return q.SQL(), nil
}

type ListSubjectsSelfCandidateInput struct {
	ObjectType         string
	Relation           string
	ObjectIDExpr       string
	FilterTypeExpr     string
	FilterRelationExpr string
	ClosureValues      string
	ExtraPredicatesSQL []string // Raw SQL predicate strings
}

func ListSubjectsSelfCandidateQuery(input ListSubjectsSelfCandidateInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues)

	filterTypeExpr := stringToDSLExpr(input.FilterTypeExpr)
	filterRelationExpr := stringToDSLExpr(input.FilterRelationExpr)

	// Build the closure EXISTS subquery
	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.Lit(input.Relation)},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "satisfying_relation"}, Right: filterRelationExpr},
		),
	}

	conditions := []dsl.Expr{
		dsl.Eq{Left: filterTypeExpr, Right: dsl.Lit(input.ObjectType)},
		dsl.Raw(closureExistsStmt.Exists()),
	}

	for _, sql := range input.ExtraPredicatesSQL {
		conditions = append(conditions, dsl.Raw(sql))
	}

	// Subject ID output: object_id + '#' + filter_relation
	subjectIDExpr := fmt.Sprintf("%s || '#' || %s AS subject_id", input.ObjectIDExpr, input.FilterRelationExpr)

	stmt := dsl.SelectStmt{
		Columns: []string{subjectIDExpr},
		Where:   dsl.And(conditions...),
	}

	return stmt.SQL(), nil
}

type ListSubjectsDirectInput struct {
	ObjectType      string
	RelationList    []string
	ObjectIDExpr    string
	SubjectTypeExpr string
	ExcludeWildcard bool
	Exclusions      ExclusionInput
}

func ListSubjectsDirectQuery(input ListSubjectsDirectInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: subjectTypeExpr},
	}

	if input.ExcludeWildcard {
		conditions = append(conditions, dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")})
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.RelationList...).
		Where(conditions...).
		SelectCol("subject_id").
		Distinct()

	// Add exclusion predicates
	exclusionConfig := toDSLExclusionConfig(input.Exclusions)
	for _, pred := range exclusionConfig.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListSubjectsComplexClosureInput struct {
	ObjectType      string
	Relation        string
	ObjectIDExpr    string
	SubjectTypeExpr string
	ExcludeWildcard bool
	Exclusions      ExclusionInput
}

func ListSubjectsComplexClosureQuery(input ListSubjectsComplexClosureInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: subjectTypeExpr},
		dsl.CheckPermission{
			Subject: dsl.SubjectRef{
				Type: subjectTypeExpr,
				ID:   dsl.Col{Table: "t", Column: "subject_id"},
			},
			Relation:    input.Relation,
			Object:      dsl.LiteralObject(input.ObjectType, objectIDExpr),
			ExpectAllow: true,
		},
	}

	if input.ExcludeWildcard {
		conditions = append(conditions, dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")})
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(conditions...).
		SelectCol("subject_id").
		Distinct()

	// Add exclusion predicates
	exclusionConfig := toDSLExclusionConfig(input.Exclusions)
	for _, pred := range exclusionConfig.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

func ListSubjectsIntersectionClosureQuery(functionName, subjectTypeExpr string) (string, error) {
	stmt := dsl.SelectStmt{
		Columns: []string{"*"},
		From:    functionName + "(p_object_id, " + subjectTypeExpr + ")",
	}
	return stmt.SQL(), nil
}

func ListSubjectsIntersectionClosureValidatedQuery(objectType, relation, functionName, functionSubjectTypeExpr, checkSubjectTypeExpr, objectIDExpr string) (string, error) {
	checkSubjectType := stringToDSLExpr(checkSubjectTypeExpr)
	objectID := stringToDSLExpr(objectIDExpr)

	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"ics.subject_id"},
		From:     functionName + "(" + objectIDExpr + ", " + functionSubjectTypeExpr + ")",
		Alias:    "ics",
		Where: dsl.CheckPermissionCall{
			FunctionName: "check_permission",
			Subject: dsl.SubjectRef{
				Type: checkSubjectType,
				ID:   dsl.Col{Table: "ics", Column: "subject_id"},
			},
			Relation:    relation,
			Object:      dsl.LiteralObject(objectType, objectID),
			ExpectAllow: true,
		},
	}
	return stmt.SQL(), nil
}

type ListSubjectsUsersetPatternSimpleInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	SatisfyingRelations []string
	ObjectIDExpr        string
	SubjectTypeExpr     string
	AllowedSubjectTypes []string
	ExcludeWildcard     bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionInput
}

func ListSubjectsUsersetPatternSimpleQuery(input ListSubjectsUsersetPatternSimpleInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	// Join conditions for the userset membership table
	joinConditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "s", Column: "object_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.Eq{Left: dsl.Col{Table: "s", Column: "object_id"}, Right: dsl.UsersetObjectID{Source: dsl.Col{Table: "t", Column: "subject_id"}}},
		dsl.In{Expr: dsl.Col{Table: "s", Column: "relation"}, Values: input.SatisfyingRelations},
		dsl.Eq{Left: dsl.Col{Table: "s", Column: "subject_type"}, Right: subjectTypeExpr},
		dsl.In{Expr: subjectTypeExpr, Values: input.AllowedSubjectTypes},
	}

	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, dsl.Ne{Left: dsl.Col{Table: "s", Column: "subject_id"}, Right: dsl.Lit("*")})
	}

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
		dsl.Eq{Left: dsl.UsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}}, Right: dsl.Lit(input.SubjectRelation)},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, dsl.CheckPermission{
			Subject: dsl.SubjectRef{
				Type: subjectTypeExpr,
				ID:   dsl.Col{Table: "s", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      dsl.LiteralObject(input.ObjectType, objectIDExpr),
			ExpectAllow: true,
		})
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("s", joinConditions...).
		Select("s.subject_id").
		Distinct()

	// Add exclusion predicates
	exclusionConfig := toDSLExclusionConfig(input.Exclusions)
	for _, pred := range exclusionConfig.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListSubjectsUsersetPatternComplexInput struct {
	ObjectType       string
	SubjectType      string
	SubjectRelation  string
	SourceRelations  []string
	ObjectIDExpr     string
	SubjectTypeExpr  string
	IsClosurePattern bool
	SourceRelation   string
	Exclusions       ExclusionInput
}

func ListSubjectsUsersetPatternComplexQuery(input ListSubjectsUsersetPatternComplexInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
		dsl.Eq{Left: dsl.UsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}}, Right: dsl.Lit(input.SubjectRelation)},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, dsl.CheckPermission{
			Subject: dsl.SubjectRef{
				Type: subjectTypeExpr,
				ID:   dsl.Col{Table: "s", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      dsl.LiteralObject(input.ObjectType, objectIDExpr),
			ExpectAllow: true,
		})
	}

	// Use lateral join with list function
	listFunction := fmt.Sprintf("list_%s_%s_subjects(split_part(t.subject_id, '#', 1), %s)", input.SubjectType, input.SubjectRelation, input.SubjectTypeExpr)

	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"s.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Joins: []dsl.JoinClause{
			{
				Type:  "CROSS",
				Table: "LATERAL " + listFunction,
				Alias: "s",
				On:    dsl.Raw("TRUE"),
			},
		},
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
			dsl.In{Expr: dsl.Col{Table: "t", Column: "relation"}, Values: input.SourceRelations},
			dsl.And(conditions...),
		),
	}

	// Add exclusion predicates to WHERE
	exclusionConfig := toDSLExclusionConfig(input.Exclusions)
	predicates := exclusionConfig.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]dsl.Expr{stmt.Where}, predicates...)
		stmt.Where = dsl.And(allPredicates...)
	}

	return stmt.SQL(), nil
}

type ListSubjectsUsersetPatternRecursiveComplexInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	ObjectIDExpr        string
	SubjectTypeExpr     string
	AllowedSubjectTypes []string
	ExcludeWildcard     bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionInput
}

func ListSubjectsUsersetPatternRecursiveComplexQuery(input ListSubjectsUsersetPatternRecursiveComplexInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	// Join conditions for the membership table
	joinConditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "m", Column: "object_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.Eq{Left: dsl.Col{Table: "m", Column: "object_id"}, Right: dsl.UsersetObjectID{Source: dsl.Col{Table: "t", Column: "subject_id"}}},
		dsl.Eq{Left: dsl.Col{Table: "m", Column: "subject_type"}, Right: subjectTypeExpr},
		dsl.In{Expr: subjectTypeExpr, Values: input.AllowedSubjectTypes},
	}

	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, dsl.Ne{Left: dsl.Col{Table: "m", Column: "subject_id"}, Right: dsl.Lit("*")})
	}

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.Lit(input.SubjectType)},
		dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
		dsl.Eq{Left: dsl.UsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}}, Right: dsl.Lit(input.SubjectRelation)},
		dsl.CheckPermission{
			Subject: dsl.SubjectRef{
				Type: subjectTypeExpr,
				ID:   dsl.Col{Table: "m", Column: "subject_id"},
			},
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
			Subject: dsl.SubjectRef{
				Type: subjectTypeExpr,
				ID:   dsl.Col{Table: "m", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      dsl.LiteralObject(input.ObjectType, objectIDExpr),
			ExpectAllow: true,
		})
	}

	q := dsl.Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("m", joinConditions...).
		Select("m.subject_id").
		Distinct()

	// Add exclusion predicates
	exclusionConfig := toDSLExclusionConfig(input.Exclusions)
	for _, pred := range exclusionConfig.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}
