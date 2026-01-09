package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

type ListObjectsDirectInput struct {
	ObjectType          string
	Relations           []string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          ExclusionInput
}

func ListObjectsDirectQuery(input ListObjectsDirectInput) (string, error) {
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "relation").In(literalList(input.Relations)...),
		psql.Quote("t", "subject_type").EQ(param("p_subject_type")),
		param("p_subject_type").In(literalList(input.AllowedSubjectTypes)...),
		subjectIDCheckExpr(psql.Quote("t", "subject_id"), input.AllowWildcard),
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

type ListObjectsUsersetSubjectInput struct {
	ObjectType    string
	Relations     []string
	ClosureValues string
	Exclusions    ExclusionInput
}

func ListObjectsUsersetSubjectQuery(input ListObjectsUsersetSubjectInput) (string, error) {
	closureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues))
	closureExists, err := existsExpr(psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From(closureTable),
		sm.Where(psql.And(
			psql.Quote("c", "object_type").EQ(param("p_subject_type")),
			psql.Quote("c", "relation").EQ(psql.Raw("split_part(t.subject_id, '#', 2)")),
			psql.Quote("c", "satisfying_relation").EQ(psql.Raw("substring(p_subject_id from position('#' in p_subject_id) + 1)")),
		)),
	))
	if err != nil {
		return "", err
	}

	subjectMatch := psql.Or(
		psql.Quote("t", "subject_id").EQ(param("p_subject_id")),
		psql.And(
			psql.Raw("split_part(t.subject_id, '#', 1)").EQ(psql.Raw("split_part(p_subject_id, '#', 1)")),
			closureExists,
		),
	)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "relation").In(literalList(input.Relations)...),
		psql.Quote("t", "subject_type").EQ(param("p_subject_type")),
		psql.Raw("position('#' in p_subject_id)").GT(psql.Raw("0")),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		subjectMatch,
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

type ListObjectsComplexClosureInput struct {
	ObjectType          string
	Relation            string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          ExclusionInput
}

func ListObjectsComplexClosureQuery(input ListObjectsComplexClosureInput) (string, error) {
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "relation").EQ(psql.S(input.Relation)),
		psql.Quote("t", "subject_type").EQ(param("p_subject_type")),
		param("p_subject_type").In(literalList(input.AllowedSubjectTypes)...),
		subjectIDCheckExpr(psql.Quote("t", "subject_id"), input.AllowWildcard),
		CheckPermissionInternalExpr(
			"p_subject_type",
			"p_subject_id",
			input.Relation,
			fmt.Sprintf("'%s'", input.ObjectType),
			"t.object_id",
			true,
		),
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

func ListObjectsIntersectionClosureQuery(functionName string) (string, error) {
	query := psql.Select(
		sm.Columns(psql.Raw("*")),
		sm.From(psql.Raw(functionName+"(p_subject_type, p_subject_id)")),
	)
	return renderQuery(query)
}

func ListObjectsIntersectionClosureValidatedQuery(objectType, relation, functionName string) (string, error) {
	where := CheckPermissionInternalExpr(
		"p_subject_type",
		"p_subject_id",
		relation,
		fmt.Sprintf("'%s'", objectType),
		"icr.object_id",
		true,
	)
	query := psql.Select(
		sm.Columns(psql.Raw("icr.object_id")),
		sm.From(psql.Raw(functionName+"(p_subject_type, p_subject_id)")).As("icr"),
		sm.Where(where),
		sm.Distinct(),
	)
	return renderQuery(query)
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
	Exclusions          ExclusionInput
}

func ListObjectsUsersetPatternSimpleQuery(input ListObjectsUsersetPatternSimpleInput) (string, error) {
	joinConditions := []bob.Expression{
		psql.Quote("m", "object_type").EQ(psql.S(input.SubjectType)),
		psql.Quote("m", "object_id").EQ(psql.Raw("split_part(t.subject_id, '#', 1)")),
		psql.Quote("m", "relation").In(literalList(input.SatisfyingRelations)...),
		psql.Quote("m", "subject_type").EQ(param("p_subject_type")),
		param("p_subject_type").In(literalList(input.AllowedSubjectTypes)...),
		subjectIDCheckExpr(psql.Quote("m", "subject_id"), input.AllowWildcard),
	}

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "relation").In(literalList(input.SourceRelations)...),
		psql.Quote("t", "subject_type").EQ(psql.S(input.SubjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(input.SubjectRelation)),
	}
	if input.IsClosurePattern {
		where = append(where, CheckPermissionInternalExpr(
			"p_subject_type",
			"p_subject_id",
			input.SourceRelation,
			fmt.Sprintf("'%s'", input.ObjectType),
			"t.object_id",
			true,
		))
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.InnerJoin("melange_tuples").As("m").On(joinConditions...),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

type ListObjectsUsersetPatternComplexInput struct {
	ObjectType       string
	SubjectType      string
	SubjectRelation  string
	SourceRelations  []string
	IsClosurePattern bool
	SourceRelation   string
	Exclusions       ExclusionInput
}

func ListObjectsUsersetPatternComplexQuery(input ListObjectsUsersetPatternComplexInput) (string, error) {
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "relation").In(literalList(input.SourceRelations)...),
		psql.Quote("t", "subject_type").EQ(psql.S(input.SubjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(input.SubjectRelation)),
		CheckPermissionInternalExpr(
			"p_subject_type",
			"p_subject_id",
			input.SubjectRelation,
			fmt.Sprintf("'%s'", input.SubjectType),
			"split_part(t.subject_id, '#', 1)",
			true,
		),
	}
	if input.IsClosurePattern {
		where = append(where, CheckPermissionInternalExpr(
			"p_subject_type",
			"p_subject_id",
			input.SourceRelation,
			fmt.Sprintf("'%s'", input.ObjectType),
			"t.object_id",
			true,
		))
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

type ListObjectsSelfCandidateInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
}

func ListObjectsSelfCandidateQuery(input ListObjectsSelfCandidateInput) (string, error) {
	closureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues))
	closureExists, err := existsExpr(psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From(closureTable),
		sm.Where(psql.And(
			psql.Quote("c", "object_type").EQ(psql.S(input.ObjectType)),
			psql.Quote("c", "relation").EQ(psql.S(input.Relation)),
			psql.Quote("c", "satisfying_relation").EQ(psql.Raw("substring(p_subject_id from position('#' in p_subject_id) + 1)")),
		)),
	))
	if err != nil {
		return "", err
	}

	where := psql.And(
		psql.Raw("position('#' in p_subject_id)").GT(psql.Raw("0")),
		param("p_subject_type").EQ(psql.S(input.ObjectType)),
		closureExists,
	)

	query := psql.Select(
		sm.Columns(psql.Raw("split_part(p_subject_id, '#', 1) AS object_id")),
		sm.Where(where),
	)

	return renderQuery(query)
}
