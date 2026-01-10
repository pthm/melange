package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
)

type UsersetSubjectSelfCheckInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
}

func UsersetSubjectSelfCheckQuery(input UsersetSubjectSelfCheckInput) (string, error) {
	closureTable := fmt.Sprintf("(VALUES %s)", input.ClosureValues)

	stmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    closureTable,
		Alias:   "c(object_type, relation, satisfying_relation)",
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.Lit(input.Relation)},
			dsl.Eq{
				Left:  dsl.Col{Table: "c", Column: "satisfying_relation"},
				Right: dsl.SubstringUsersetRelation{Source: dsl.SubjectID},
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

	stmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    "melange_tuples",
		Alias:   "t",
		Joins: []dsl.JoinClause{
			{
				Type:  "INNER",
				Table: closureTable,
				On: dsl.And(
					dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
					dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.Lit(input.Relation)},
					dsl.Eq{Left: dsl.Col{Table: "c", Column: "satisfying_relation"}, Right: dsl.Col{Table: "t", Column: "relation"}},
				),
			},
			{
				Type:  "INNER",
				Table: usersetTable,
				On: dsl.And(
					dsl.Eq{Left: dsl.Col{Table: "m", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
					dsl.Eq{Left: dsl.Col{Table: "m", Column: "relation"}, Right: dsl.Col{Table: "c", Column: "satisfying_relation"}},
					dsl.Eq{Left: dsl.Col{Table: "m", Column: "subject_type"}, Right: dsl.Col{Table: "t", Column: "subject_type"}},
				),
			},
			{
				Type:  "INNER",
				Table: subjClosureTable,
				On: dsl.And(
					dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "object_type"}, Right: dsl.Col{Table: "t", Column: "subject_type"}},
					dsl.Eq{
						Left:  dsl.Col{Table: "subj_c", Column: "relation"},
						Right: dsl.SubstringUsersetRelation{Source: dsl.Col{Table: "t", Column: "subject_id"}},
					},
					dsl.Eq{
						Left:  dsl.Col{Table: "subj_c", Column: "satisfying_relation"},
						Right: dsl.SubstringUsersetRelation{Source: dsl.SubjectID},
					},
				),
			},
		},
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_type"}, Right: dsl.Lit(input.ObjectType)},
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: dsl.ObjectID},
			dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")},
			dsl.HasUserset{Source: dsl.Col{Table: "t", Column: "subject_id"}},
			dsl.Eq{
				Left:  dsl.Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1)"),
				Right: dsl.Raw("substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)"),
			},
		),
		Limit: 1,
	}
	return stmt.SQL(), nil
}
