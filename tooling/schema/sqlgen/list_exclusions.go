package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

type ExcludedParentRelation struct {
	Relation            string
	LinkingRelation     string
	AllowedLinkingTypes []string
}

type ExcludedIntersectionPart struct {
	Relation         string
	ExcludedRelation string
	ParentRelation   *ExcludedParentRelation
}

type ExcludedIntersectionGroup struct {
	Parts []ExcludedIntersectionPart
}

type ExclusionInput struct {
	ObjectType string

	ObjectIDExpr    string
	SubjectTypeExpr string
	SubjectIDExpr   string

	SimpleExcludedRelations  []string
	ComplexExcludedRelations []string
	ExcludedParentRelations  []ExcludedParentRelation
	ExcludedIntersection     []ExcludedIntersectionGroup
}

func ExclusionPredicates(input ExclusionInput) ([]bob.Expression, error) {
	if len(input.SimpleExcludedRelations) == 0 &&
		len(input.ComplexExcludedRelations) == 0 &&
		len(input.ExcludedParentRelations) == 0 &&
		len(input.ExcludedIntersection) == 0 {
		return nil, nil
	}

	objectIDExpr := rawExpr(input.ObjectIDExpr)
	subjectTypeExpr := rawExpr(input.SubjectTypeExpr)
	subjectIDExpr := rawExpr(input.SubjectIDExpr)

	var predicates []bob.Expression

	for _, rel := range input.SimpleExcludedRelations {
		subquery := psql.Select(
			sm.Columns(psql.Raw("1")),
			sm.From("melange_tuples").As("excl"),
			sm.Where(psql.And(
				psql.Quote("excl", "object_type").EQ(psql.S(input.ObjectType)),
				psql.Quote("excl", "object_id").EQ(objectIDExpr),
				psql.Quote("excl", "relation").EQ(psql.S(rel)),
				psql.Quote("excl", "subject_type").EQ(subjectTypeExpr),
				psql.Or(
					psql.Quote("excl", "subject_id").EQ(subjectIDExpr),
					psql.Quote("excl", "subject_id").EQ(psql.S("*")),
				),
			)),
		)
		notExists, err := notExistsExpr(subquery)
		if err != nil {
			return nil, fmt.Errorf("building simple exclusion for %s: %w", rel, err)
		}
		predicates = append(predicates, notExists)
	}

	for _, rel := range input.ComplexExcludedRelations {
		predicates = append(predicates, CheckPermissionInternalExpr(
			input.SubjectTypeExpr,
			input.SubjectIDExpr,
			rel,
			fmt.Sprintf("'%s'", input.ObjectType),
			input.ObjectIDExpr,
			false,
		))
	}

	for _, rel := range input.ExcludedParentRelations {
		where := []bob.Expression{
			psql.Quote("link", "object_type").EQ(psql.S(input.ObjectType)),
			psql.Quote("link", "object_id").EQ(objectIDExpr),
			psql.Quote("link", "relation").EQ(psql.S(rel.LinkingRelation)),
			CheckPermissionInternalExpr(
				input.SubjectTypeExpr,
				input.SubjectIDExpr,
				rel.Relation,
				"link.subject_type",
				"link.subject_id",
				true,
			),
		}
		if len(rel.AllowedLinkingTypes) > 0 {
			where = append(where, psql.Quote("link", "subject_type").In(literalList(rel.AllowedLinkingTypes)...))
		}
		subquery := psql.Select(
			sm.Columns(psql.Raw("1")),
			sm.From("melange_tuples").As("link"),
			sm.Where(psql.And(where...)),
		)
		notExists, err := notExistsExpr(subquery)
		if err != nil {
			return nil, fmt.Errorf("building TTU exclusion for %s: %w", rel.Relation, err)
		}
		predicates = append(predicates, notExists)
	}

	for _, group := range input.ExcludedIntersection {
		var parts []bob.Expression
		for _, part := range group.Parts {
			switch {
			case part.ParentRelation != nil:
				where := []bob.Expression{
					psql.Quote("link", "object_type").EQ(psql.S(input.ObjectType)),
					psql.Quote("link", "object_id").EQ(objectIDExpr),
					psql.Quote("link", "relation").EQ(psql.S(part.ParentRelation.LinkingRelation)),
					CheckPermissionInternalExpr(
						input.SubjectTypeExpr,
						input.SubjectIDExpr,
						part.ParentRelation.Relation,
						"link.subject_type",
						"link.subject_id",
						true,
					),
				}
				if len(part.ParentRelation.AllowedLinkingTypes) > 0 {
					where = append(where, psql.Quote("link", "subject_type").In(literalList(part.ParentRelation.AllowedLinkingTypes)...))
				}
				subquery := psql.Select(
					sm.Columns(psql.Raw("1")),
					sm.From("melange_tuples").As("link"),
					sm.Where(psql.And(where...)),
				)
				exists, err := existsExpr(subquery)
				if err != nil {
					return nil, fmt.Errorf("building intersection TTU exclusion for %s: %w", part.ParentRelation.Relation, err)
				}
				parts = append(parts, exists)
			default:
				partExpr := CheckPermissionInternalExpr(
					input.SubjectTypeExpr,
					input.SubjectIDExpr,
					part.Relation,
					fmt.Sprintf("'%s'", input.ObjectType),
					input.ObjectIDExpr,
					true,
				)
				if part.ExcludedRelation != "" {
					partExpr = psql.And(
						partExpr,
						CheckPermissionInternalExpr(
							input.SubjectTypeExpr,
							input.SubjectIDExpr,
							part.ExcludedRelation,
							fmt.Sprintf("'%s'", input.ObjectType),
							input.ObjectIDExpr,
							false,
						),
					)
				}
				parts = append(parts, partExpr)
			}
		}
		if len(parts) == 0 {
			continue
		}
		predicates = append(predicates, psql.Not(psql.And(parts...)))
	}

	return predicates, nil
}
