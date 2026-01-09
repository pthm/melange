package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
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
	ExtraPredicates     []bob.Expression
}

func ListSubjectsUsersetFilterQuery(input ListSubjectsUsersetFilterInput) (string, error) {
	closureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", input.ClosureValues))
	closureExists, err := existsExpr(psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From(closureTable),
		sm.Where(psql.And(
			psql.Quote("subj_c", "object_type").EQ(psql.Raw(input.FilterTypeExpr)),
			psql.Quote("subj_c", "relation").EQ(psql.Raw("substring(t.subject_id from position('#' in t.subject_id) + 1)")),
			psql.Quote("subj_c", "satisfying_relation").EQ(psql.Raw(input.FilterRelationExpr)),
		)),
	))
	if err != nil {
		return "", err
	}

	normalizedSubject := "substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || " + input.FilterRelationExpr
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw(input.ObjectIDExpr)),
		psql.Quote("t", "relation").In(literalList(input.RelationList)...),
		psql.Quote("t", "subject_type").EQ(psql.Raw(input.FilterTypeExpr)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Or(
			psql.Raw("substring(t.subject_id from position('#' in t.subject_id) + 1)").EQ(psql.Raw(input.FilterRelationExpr)),
			closureExists,
		),
	}
	if input.UseTypeGuard {
		where = append(where, psql.Raw(input.FilterTypeExpr).In(literalList(input.AllowedSubjectTypes)...))
	}
	where = append(where, input.ExtraPredicates...)

	query := psql.Select(
		sm.Columns(psql.Raw(normalizedSubject+" AS subject_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

type ListSubjectsSelfCandidateInput struct {
	ObjectType         string
	Relation           string
	ObjectIDExpr       string
	FilterTypeExpr     string
	FilterRelationExpr string
	ClosureValues      string
	ExtraPredicates    []bob.Expression
}

func ListSubjectsSelfCandidateQuery(input ListSubjectsSelfCandidateInput) (string, error) {
	closureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues))
	closureExists, err := existsExpr(psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From(closureTable),
		sm.Where(psql.And(
			psql.Quote("c", "object_type").EQ(psql.S(input.ObjectType)),
			psql.Quote("c", "relation").EQ(psql.S(input.Relation)),
			psql.Quote("c", "satisfying_relation").EQ(psql.Raw(input.FilterRelationExpr)),
		)),
	))
	if err != nil {
		return "", err
	}

	where := []bob.Expression{
		psql.Raw(input.FilterTypeExpr).EQ(psql.S(input.ObjectType)),
		closureExists,
	}
	where = append(where, input.ExtraPredicates...)

	query := psql.Select(
		sm.Columns(psql.Raw(input.ObjectIDExpr+" || '#' || "+input.FilterRelationExpr+" AS subject_id")),
		sm.Where(psql.And(where...)),
	)

	return renderQuery(query)
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
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw(input.ObjectIDExpr)),
		psql.Quote("t", "relation").In(literalList(input.RelationList)...),
		psql.Quote("t", "subject_type").EQ(psql.Raw(input.SubjectTypeExpr)),
	}
	if input.ExcludeWildcard {
		where = append(where, psql.Quote("t", "subject_id").NE(psql.S("*")))
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.subject_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
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
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw(input.ObjectIDExpr)),
		psql.Quote("t", "relation").EQ(psql.S(input.Relation)),
		psql.Quote("t", "subject_type").EQ(psql.Raw(input.SubjectTypeExpr)),
		CheckPermissionInternalExpr(
			input.SubjectTypeExpr,
			"t.subject_id",
			input.Relation,
			fmt.Sprintf("'%s'", input.ObjectType),
			input.ObjectIDExpr,
			true,
		),
	}
	if input.ExcludeWildcard {
		where = append(where, psql.Quote("t", "subject_id").NE(psql.S("*")))
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.subject_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}

func ListSubjectsIntersectionClosureQuery(functionName, subjectTypeExpr string) (string, error) {
	query := psql.Select(
		sm.Columns(psql.Raw("*")),
		sm.From(psql.Raw(functionName+"(p_object_id, "+subjectTypeExpr+")")),
	)
	return renderQuery(query)
}

func ListSubjectsIntersectionClosureValidatedQuery(objectType, relation, functionName, functionSubjectTypeExpr, checkSubjectTypeExpr, objectIDExpr string) (string, error) {
	where := CheckPermissionExpr(
		"check_permission",
		checkSubjectTypeExpr,
		"ics.subject_id",
		relation,
		fmt.Sprintf("'%s'", objectType),
		objectIDExpr,
		true,
	)
	query := psql.Select(
		sm.Columns(psql.Raw("ics.subject_id")),
		sm.From(psql.Raw(functionName+"("+objectIDExpr+", "+functionSubjectTypeExpr+")")).As("ics"),
		sm.Where(where),
		sm.Distinct(),
	)
	return renderQuery(query)
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
	joinConditions := []bob.Expression{
		psql.Quote("s", "object_type").EQ(psql.S(input.SubjectType)),
		psql.Quote("s", "object_id").EQ(psql.Raw("split_part(t.subject_id, '#', 1)")),
		psql.Quote("s", "relation").In(literalList(input.SatisfyingRelations)...),
		psql.Quote("s", "subject_type").EQ(psql.Raw(input.SubjectTypeExpr)),
		psql.Raw(input.SubjectTypeExpr).In(literalList(input.AllowedSubjectTypes)...),
	}
	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, psql.Quote("s", "subject_id").NE(psql.S("*")))
	}

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw(input.ObjectIDExpr)),
		psql.Quote("t", "relation").In(literalList(input.SourceRelations)...),
		psql.Quote("t", "subject_type").EQ(psql.S(input.SubjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(input.SubjectRelation)),
	}
	if input.IsClosurePattern {
		where = append(where, CheckPermissionInternalExpr(
			input.SubjectTypeExpr,
			"s.subject_id",
			input.SourceRelation,
			fmt.Sprintf("'%s'", input.ObjectType),
			input.ObjectIDExpr,
			true,
		))
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	query := psql.Select(
		sm.Columns(psql.Raw("s.subject_id")),
		sm.From("melange_tuples").As("t"),
		sm.InnerJoin("melange_tuples").As("s").On(joinConditions...),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
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
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw(input.ObjectIDExpr)),
		psql.Quote("t", "relation").In(literalList(input.SourceRelations)...),
		psql.Quote("t", "subject_type").EQ(psql.S(input.SubjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(input.SubjectRelation)),
	}
	if input.IsClosurePattern {
		where = append(where, CheckPermissionInternalExpr(
			input.SubjectTypeExpr,
			"s.subject_id",
			input.SourceRelation,
			fmt.Sprintf("'%s'", input.ObjectType),
			input.ObjectIDExpr,
			true,
		))
	}
	exclusions, err := ExclusionPredicates(input.Exclusions)
	if err != nil {
		return "", err
	}
	where = append(where, exclusions...)

	listFunction := fmt.Sprintf("list_%s_%s_subjects(split_part(t.subject_id, '#', 1), %s)", input.SubjectType, input.SubjectRelation, input.SubjectTypeExpr)
	query := psql.Select(
		sm.Columns(psql.Raw("s.subject_id")),
		sm.From("melange_tuples").As("t"),
		sm.CrossJoin(psql.Raw("LATERAL "+listFunction)).As("s"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)

	return renderQuery(query)
}
