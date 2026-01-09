package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

type UsersetSubjectSelfCheckInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
}

func UsersetSubjectSelfCheckQuery(input UsersetSubjectSelfCheckInput) (string, error) {
	closureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues))
	where := []bob.Expression{
		psql.Quote("c", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("c", "relation").EQ(psql.S(input.Relation)),
		psql.Quote("c", "satisfying_relation").EQ(psql.Raw("substring(p_subject_id from position('#' in p_subject_id) + 1)")),
	}
	query := psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From(closureTable),
		sm.Where(psql.And(where...)),
		sm.Limit(1),
	)
	return renderQuery(query)
}

type UsersetSubjectComputedCheckInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
	UsersetValues string
}

func UsersetSubjectComputedCheckQuery(input UsersetSubjectComputedCheckInput) (string, error) {
	closureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", input.ClosureValues))
	usersetTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS m(object_type, relation, subject_type, subject_relation)", input.UsersetValues))
	subjClosureTable := psql.Raw(fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", input.ClosureValues))

	joinConditions := []bob.Expression{
		psql.Quote("c", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("c", "relation").EQ(psql.S(input.Relation)),
		psql.Quote("c", "satisfying_relation").EQ(psql.Quote("t", "relation")),
	}
	usersetJoinConditions := []bob.Expression{
		psql.Quote("m", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("m", "relation").EQ(psql.Quote("c", "satisfying_relation")),
		psql.Quote("m", "subject_type").EQ(psql.Quote("t", "subject_type")),
	}
	subjClosureJoinConditions := []bob.Expression{
		psql.Quote("subj_c", "object_type").EQ(psql.Quote("t", "subject_type")),
		psql.Quote("subj_c", "relation").EQ(psql.Raw("substring(t.subject_id from position('#' in t.subject_id) + 1)")),
		psql.Quote("subj_c", "satisfying_relation").EQ(psql.Raw("substring(p_subject_id from position('#' in p_subject_id) + 1)")),
	}

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw("p_object_id")),
		psql.Quote("t", "subject_type").EQ(psql.Raw("p_subject_type")),
		psql.Quote("t", "subject_id").NE(psql.S("*")),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1)").EQ(
			psql.Raw("substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)")),
	}

	query := psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From("melange_tuples").As("t"),
		sm.InnerJoin(closureTable).On(joinConditions...),
		sm.InnerJoin(usersetTable).On(usersetJoinConditions...),
		sm.InnerJoin(subjClosureTable).On(subjClosureJoinConditions...),
		sm.Where(psql.And(where...)),
		sm.Limit(1),
	)
	return renderQuery(query)
}
