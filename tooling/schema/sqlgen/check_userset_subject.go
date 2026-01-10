package sqlgen

import (
	"fmt"
)

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
				Left:  Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1)"),
				Right: Raw("substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)"),
			},
		),
		Limit: 1,
	}
	return stmt.SQL(), nil
}
