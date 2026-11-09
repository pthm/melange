package sqlgen

import (
	"fmt"
	"slices"
)

// SubjectsIntersectionBlockSet contains blocks for an intersection list_subjects function.
// Unlike recursive which uses check_permission_internal within queries, intersection
// gathers candidates then filters with check_permission at the end.
type SubjectsIntersectionBlockSet struct {
	RegularCandidateBlocks       []TypedQueryBlock
	UsersetFilterCandidateBlocks []TypedQueryBlock
	UsersetFilterSelfBlock       *TypedQueryBlock
}

// buildDirectSubjectSelectStmt creates a SELECT DISTINCT subject_id FROM melange_tuples t.
func buildDirectSubjectSelectStmt(conditions []Expr) SelectStmt {
	return SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
		FromExpr:    TableAs("", "melange_tuples", "t"),
		Where:       And(conditions...),
	}
}

// buildTTUSubjectSelectStmt creates a TTU join query selecting subject_id from pt.
func buildTTUSubjectSelectStmt(conditions []Expr) SelectStmt {
	return SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "pt", Column: "subject_id"}},
		FromExpr:    TableAs("", "melange_tuples", "link"),
		Joins:       []JoinClause{ttuJoin()},
		Where:       And(conditions...),
	}
}

// ttuJoin returns the standard TTU join clause.
func ttuJoin() JoinClause {
	return JoinClause{
		Type:   "INNER",
		Schema: "",
		Table:  "melange_tuples",
		Alias:  "pt",
		On: And(
			Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
		),
	}
}

// buildUsersetFilterTTUSelectStmt creates a userset filter TTU query.
func buildUsersetFilterTTUSelectStmt(objectType, linkingRelation string, subjectExpr, relationMatch Expr) SelectStmt {
	return SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("", "melange_tuples", "link"),
		Joins:       []JoinClause{ttuJoin()},
		Where: And(
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(objectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(linkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Param("v_filter_type")},
			Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
			relationMatch,
		),
	}
}

func BuildListSubjectsIntersectionBlocks(plan ListPlan) SubjectsIntersectionBlockSet {
	return SubjectsIntersectionBlockSet{
		RegularCandidateBlocks:       buildListSubjectsIntersectionRegularCandidates(plan),
		UsersetFilterCandidateBlocks: buildListSubjectsIntersectionUsersetCandidates(plan),
		UsersetFilterSelfBlock:       buildListSubjectsUsersetFilterSelfBlock(plan),
	}
}

// buildListSubjectsIntersectionRegularCandidates returns the candidate blocks
// for a plain subject type. When every group has an anchor, the anchors and
// the base block (which lists This anchors) cover every subject the groups
// grant, so the per-part and TTU blocks are left out: they add no subjects,
// and the check filter over their tuples can be pushed into scans of every
// tuples-view branch they join. The userset-pattern blocks stay: a direct
// userset grant outside the groups ([user, group#member] or (a and b)) is
// listed by nothing else.
func buildListSubjectsIntersectionRegularCandidates(plan ListPlan) []TypedQueryBlock {
	excludeWildcard := plan.ExcludeWildcard()

	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildListSubjectsIntersectionBaseBlock(plan, excludeWildcard))
	if _, ok := intersectionAnchors(plan); ok {
		blocks = append(blocks, buildListSubjectsIntersectionUsersetPatternBlocks(plan, excludeWildcard)...)
		return append(blocks, buildListSubjectsIntersectionPoolBlocks(plan, excludeWildcard)...)
	}
	blocks = append(blocks, buildListSubjectsIntersectionPartBlocks(plan, excludeWildcard)...)
	blocks = append(blocks, buildListSubjectsIntersectionUsersetPatternBlocks(plan, excludeWildcard)...)
	blocks = append(blocks, buildListSubjectsIntersectionTTUBlocks(plan, excludeWildcard)...)
	return append(blocks, buildListSubjectsIntersectionPoolBlocks(plan, excludeWildcard)...)
}

// intersectionAnchor is one intersection part whose subjects can be
// enumerated: a relation on the same object (Relation, listed by its own
// list_subjects function), the relation's direct tuples (This), or a parent
// relation (Parent, listed by the parent type's list_subjects function for
// each linked object).
type intersectionAnchor struct {
	Relation string
	This     bool
	Parent   *ParentRelationInfo
}

// intersectionAnchors picks, for each intersection group, one part whose
// subjects can be enumerated: every subject a group grants holds every part
// of that group, so the anchors' subjects are a superset of the
// group-granted subjects, and the candidates' check filter keeps the result
// exact. It prefers the relation's own direct tuples, which the base block
// already lists, then parts that cannot grant through a wildcard, then simple
// relations, then parent relations. ok is false when some group has no
// enumerable part, or when the relation has implied or parent paths outside
// its groups, which the candidate blocks would then have to enumerate as
// well.
func intersectionAnchors(plan ListPlan) (anchors []intersectionAnchor, ok bool) {
	a := plan.Analysis
	if len(a.IntersectionGroups) == 0 || a.Features.HasImplied || len(a.ParentRelations) > 0 {
		return nil, false
	}
	for _, group := range a.IntersectionGroups {
		var best intersectionAnchor
		bestScore := -1
		for _, part := range group.Parts {
			anchor, score, eligible := intersectionAnchorFor(plan, part)
			if eligible && (bestScore < 0 || score < bestScore) {
				best, bestScore = anchor, score
			}
		}
		if bestScore < 0 {
			return nil, false
		}
		if !slices.ContainsFunc(anchors, func(x intersectionAnchor) bool { return sameAnchor(x, best) }) {
			anchors = append(anchors, best)
		}
	}
	return anchors, true
}

// listsWildcard reports whether the list_subjects function of
// (objectType, relation) can return '*': the relation grants a wildcard
// directly or reaches one through its references (reachesWildcard), which
// Features.HasWildcard does not cover.
func listsWildcard(plan ListPlan, objectType, relation string) bool {
	a := plan.AnalysisLookup[objectType+"."+relation]
	return a == nil || a.Features.HasWildcard || plan.graph().reachesWildcard(objectType, relation)
}

// intersectionAnchorFor reports whether part can anchor its group, and its
// cost score (lower is cheaper).
//
//   - This: the direct tuples are exactly the part's subjects when the
//     relation grants no userset or wildcard subjects directly.
//   - A parent relation: every linking type's relation needs a composable
//     list_subjects function that cannot return '*' (listsWildcard), so each
//     linked object's subjects are listed exactly.
//   - A relation on the object: its composable list_subjects function.
func intersectionAnchorFor(plan ListPlan, part IntersectionPart) (intersectionAnchor, int, bool) {
	switch {
	case part.IsThis:
		if part.HasWildcard || plan.Analysis.Features.HasUserset {
			return intersectionAnchor{}, 0, false
		}
		return intersectionAnchor{This: true}, 0, true
	case part.ParentRelation != nil:
		pr := part.ParentRelation
		if len(pr.AllowedLinkingTypes) == 0 {
			return intersectionAnchor{}, 0, false
		}
		score := 4
		for _, ptype := range pr.AllowedLinkingTypes {
			target := plan.AnalysisLookup[ptype+"."+pr.Relation]
			if target == nil || listsWildcard(plan, ptype, pr.Relation) || !composableListSubjectsTarget(plan, ptype, pr.Relation) {
				return intersectionAnchor{}, 0, false
			}
			if target.ListStrategy != ListStrategyDirect {
				score = 5
			}
		}
		return intersectionAnchor{Parent: pr}, score, true
	case part.Relation != "" && composableListSubjectsTarget(plan, plan.ObjectType, part.Relation):
		score := 1
		if listsWildcard(plan, plan.ObjectType, part.Relation) {
			score += 2
		}
		if !part.IsSimple {
			score++
		}
		return intersectionAnchor{Relation: part.Relation}, score, true
	}
	return intersectionAnchor{}, 0, false
}

// sameAnchor reports whether x and y name the same part, comparing parent
// relations by value.
func sameAnchor(x, y intersectionAnchor) bool {
	if x.Parent != nil || y.Parent != nil {
		return x.Parent != nil && y.Parent != nil && x.Parent.LinkingRelation == y.Parent.LinkingRelation &&
			x.Parent.Relation == y.Parent.Relation && slices.Equal(x.Parent.AllowedLinkingTypes, y.Parent.AllowedLinkingTypes)
	}
	return x == y
}

// relationAnchors returns the anchors' relations when every anchor is a
// relation on the object, for the userset filter, which lists only those.
func relationAnchors(anchors []intersectionAnchor) ([]string, bool) {
	rels := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		if anchor.Relation == "" {
			return nil, false
		}
		rels = append(rels, anchor.Relation)
	}
	return rels, true
}

// buildListSubjectsIntersectionPoolBlocks returns the candidates that cover
// the intersection groups: the anchors' subjects when every group has an
// anchor, else the subject pool. A This anchor needs no block of its own:
// the base block lists the relation's direct tuples. A relation anchor that
// can grant through a wildcard may return '*' (every subject holds it); the
// pool is then still offered, gated on that anchor actually returning '*'
// for this object.
func buildListSubjectsIntersectionPoolBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	anchors, ok := intersectionAnchors(plan)
	if !ok {
		return []TypedQueryBlock{buildListSubjectsIntersectionPoolBlock(plan, excludeWildcard)}
	}
	blocks := make([]TypedQueryBlock, 0, len(anchors)+1)
	var wildcardGates []Expr
	for _, anchor := range anchors {
		switch {
		case anchor.Parent != nil:
			blocks = append(blocks, intersectionParentAnchorBlocks(plan, *anchor.Parent)...)
		case anchor.Relation != "":
			rel := anchor.Relation
			fn := listSubjectsFunctionName(plan.ObjectType, rel)
			stmt := SelectStmt{
				ColumnExprs: []Expr{Col{Table: "a", Column: "subject_id"}},
				FromExpr: FunctionCallExpr{
					Schema: plan.DatabaseSchema, Name: fn, Args: []Expr{ObjectID, SubjectType}, Alias: "a",
				},
			}
			if excludeWildcard {
				stmt.Where = Ne{Left: Col{Table: "a", Column: "subject_id"}, Right: Lit("*")}
			}
			blocks = append(blocks, TypedQueryBlock{
				Comments: []string{"-- Intersection anchor: subjects of " + rel},
				Query:    stmt,
			})
			if listsWildcard(plan, plan.ObjectType, rel) {
				wildcardGates = append(wildcardGates, InFunctionSelect{
					Expr: Lit("*"), Schema: plan.DatabaseSchema, FuncName: fn,
					Args: []Expr{ObjectID, SubjectType}, Alias: "w", SelectCol: "subject_id",
				})
			}
		}
	}
	if len(wildcardGates) > 0 {
		pool := buildListSubjectsIntersectionPoolBlock(plan, excludeWildcard)
		pool.Comments = []string{"-- Subject pool, only when an anchor grants every subject ('*')"}
		pool.Query.Where = And(pool.Query.Where, Or(wildcardGates...))
		blocks = append(blocks, pool)
	}
	return blocks
}

// intersectionParentAnchorBlocks lists a parent-relation anchor's subjects:
// for each linking tuple to a parent object of an allowed type, the subjects
// of the parent relation on it, from the parent type's list_subjects
// function.
func intersectionParentAnchorBlocks(plan ListPlan, pr ParentRelationInfo) []TypedQueryBlock {
	blocks := make([]TypedQueryBlock, 0, len(pr.AllowedLinkingTypes))
	for _, ptype := range pr.AllowedLinkingTypes {
		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection anchor: subjects of %s on the %s via %s", pr.Relation, ptype, pr.LinkingRelation)},
			Query: SelectStmt{
				ColumnExprs: []Expr{Col{Table: "a", Column: "subject_id"}},
				FromExpr:    TableAs("", "melange_tuples", "link"),
				Joins: []JoinClause{{
					Type: "CROSS",
					TableExpr: LateralFunction{
						Schema: plan.DatabaseSchema,
						Name:   listSubjectsFunctionName(ptype, pr.Relation),
						Args:   []Expr{Col{Table: "link", Column: "subject_id"}, SubjectType},
						Alias:  "a",
					},
				}},
				Where: And(
					Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
					Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(pr.LinkingRelation)},
					Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Lit(ptype)},
				),
			},
		})
	}
	return blocks
}

// buildListSubjectsIntersectionBaseBlock builds the base tuple lookup block.
func buildListSubjectsIntersectionBaseBlock(plan ListPlan, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
		FromExpr:    TableAs("", "melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- Base: direct tuple lookup"},
		Query:    stmt,
	}
}

func buildListSubjectsIntersectionPartBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	var blocks []TypedQueryBlock
	for _, group := range plan.Analysis.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			blocks = append(blocks, buildListSubjectsIntersectionPartBlock(plan, part, excludeWildcard))
		}
	}
	return blocks
}

func buildListSubjectsIntersectionPartBlock(plan ListPlan, part IntersectionPart, excludeWildcard bool) TypedQueryBlock {
	if part.ParentRelation != nil {
		conditions := []Expr{
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(part.ParentRelation.LinkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
		}
		if excludeWildcard {
			conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
		}

		return TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection part: via %s", part.ParentRelation.LinkingRelation)},
			Query:    buildTTUSubjectSelectStmt(conditions),
		}
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Intersection part: %s", part.Relation)},
		Query:    buildDirectSubjectSelectStmt(conditions),
	}
}

func buildListSubjectsIntersectionUsersetPatternBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(patterns))
	for _, pattern := range patterns {
		blocks = append(blocks, buildListSubjectsIntersectionUsersetPatternBlock(plan, pattern, excludeWildcard))
	}
	return blocks
}

func buildListSubjectsIntersectionUsersetPatternBlock(plan ListPlan, pattern listUsersetPatternInput, excludeWildcard bool) TypedQueryBlock {
	const grantAlias = "g"
	const memberAlias = "s"

	joinCond := And(
		Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
		In{Expr: Col{Table: memberAlias, Column: "relation"}, Values: pattern.SatisfyingRelations},
	)

	whereConditions := []Expr{
		Eq{Left: Col{Table: grantAlias, Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: grantAlias, Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: grantAlias, Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: grantAlias, Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: grantAlias, Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: grantAlias, Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
		Eq{Left: Col{Table: memberAlias, Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
		NoUserset{Source: Col{Table: memberAlias, Column: "subject_id"}},
	}
	if excludeWildcard {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset pattern: %s#%s", pattern.SubjectType, pattern.SubjectRelation)},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: memberAlias, Column: "subject_id"}},
			FromExpr:    TableAs("", "melange_tuples", grantAlias),
			Joins: []JoinClause{{
				Type:   "INNER",
				Schema: "",
				Table:  "melange_tuples",
				Alias:  memberAlias,
				On:     joinCond,
			}},
			Where: And(whereConditions...),
		},
	}
}

func buildListSubjectsIntersectionTTUBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(parentRelations))
	for _, parent := range parentRelations {
		blocks = append(blocks, buildListSubjectsIntersectionTTUBlock(plan, parent, excludeWildcard))
	}
	return blocks
}

func buildListSubjectsIntersectionTTUBlock(plan ListPlan, parent ListParentRelationData, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
	}
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		conditions = append(conditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU: subjects via %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    buildTTUSubjectSelectStmt(conditions),
	}
}

func buildListSubjectsIntersectionPoolBlock(plan ListPlan, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{"-- Subject pool: all subjects of requested type"},
		Query:    buildDirectSubjectSelectStmt(conditions),
	}
}

func buildListSubjectsIntersectionUsersetCandidates(plan ListPlan) []TypedQueryBlock {
	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterBaseBlock(plan))
	blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterPartBlocks(plan)...)
	blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterTTUBlocks(plan)...)
	return append(blocks,
		buildListSubjectsIntersectionUsersetAnchorOrPool(plan),
		buildListSubjectsIntersectionSelfCandidateBlock(plan),
	)
}

// buildListSubjectsIntersectionUsersetAnchorOrPool is the userset-filter
// form of buildListSubjectsIntersectionPoolBlocks: the anchors' list
// functions under the filter (a userset is never granted by a wildcard), or
// the userset pool when some group has no anchor.
func buildListSubjectsIntersectionUsersetAnchorOrPool(plan ListPlan) TypedQueryBlock {
	anchors, ok := intersectionAnchors(plan)
	rels, onlyRelations := relationAnchors(anchors)
	if !ok || !onlyRelations {
		return buildListSubjectsIntersectionUsersetPoolBlock()
	}
	filter := Concat{Parts: []Expr{Param("v_filter_type"), Lit("#"), Param("v_filter_relation")}}
	queries := make([]SelectStmt, 0, len(rels))
	for _, rel := range rels {
		queries = append(queries, SelectStmt{
			ColumnExprs: []Expr{Col{Table: "a", Column: "subject_id"}},
			FromExpr: FunctionCallExpr{
				Schema: plan.DatabaseSchema, Name: listSubjectsFunctionName(plan.ObjectType, rel),
				Args: []Expr{ObjectID, filter}, Alias: "a",
			},
		})
	}
	return TypedQueryBlock{
		Comments: []string{"-- Intersection anchors under the userset filter"},
		Query: SelectStmt{
			ColumnExprs: []Expr{Col{Table: "an", Column: "subject_id"}},
			FromExpr:    UnionSubquery{Queries: queries, Alias: "an"},
		},
	}
}

// buildListSubjectsIntersectionUsersetPoolBlock is the userset-filter
// counterpart of the subject pool: every userset of the filter type named in
// any tuple, as filter_type:id#filter_relation. A group part can grant a
// userset through paths the per-part blocks do not follow (a complex part's
// own exclusions or parent lookups); the check_permission filter on the
// candidates keeps the result exact.
func buildListSubjectsIntersectionUsersetPoolBlock() TypedQueryBlock {
	return TypedQueryBlock{
		Comments: []string{"-- Userset pool: every userset of the filter type, checked like the others"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{Alias{
				Expr: NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, Param("v_filter_relation")),
				Name: "subject_id",
			}},
			FromExpr: TableAs("", "melange_tuples", "t"),
			Where: And(
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
				HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			),
		},
	}
}

// buildListSubjectsIntersectionSelfCandidateBlock offers object_id#filter_relation
// as a candidate. The unfiltered self block covers SatisfyingRelations; a
// relation reached only through an intersection group (e.g. the editor in
// `writer or (editor but not owner)`) needs the check_permission filter the
// candidates get.
func buildListSubjectsIntersectionSelfCandidateBlock(plan ListPlan) TypedQueryBlock {
	return TypedQueryBlock{
		Comments: []string{"-- Userset filter: self-candidate, checked like the others"},
		Query: SelectStmt{
			ColumnExprs: []Expr{Alias{
				Expr: Concat{Parts: []Expr{ObjectID, Lit("#"), Param("v_filter_relation")}},
				Name: "subject_id",
			}},
			Where: And(
				Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
				NotIn{Expr: Param("v_filter_relation"), Values: plan.Analysis.SatisfyingRelations},
			),
		},
	}
}

func buildListSubjectsIntersectionUsersetFilterBaseBlock(plan ListPlan) TypedQueryBlock {
	relationMatch := buildUsersetFilterRelationMatchExpr("t.subject_id")
	subjectExpr := Alias{Expr: NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, Param("v_filter_relation")), Name: "subject_id"}

	return TypedQueryBlock{
		Comments: []string{"-- Userset filter: direct userset tuples"},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{subjectExpr},
			FromExpr:    TableAs("", "melange_tuples", "t"),
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
				Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
				relationMatch,
			),
		},
	}
}

func buildUsersetFilterRelationMatchExpr(subjectIDExpr string) Expr {
	relationExtract := UsersetRelation{Source: Raw(subjectIDExpr)}
	// The closure table is hoisted into a per-function `closure` CTE (Finding 7).
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: closureCTERef("subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: relationExtract},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}
	return Or(
		Eq{Left: relationExtract, Right: Param("v_filter_relation")},
		Exists{Query: closureExistsStmt},
	)
}

func buildListSubjectsIntersectionUsersetFilterPartBlocks(plan ListPlan) []TypedQueryBlock {
	var blocks []TypedQueryBlock
	for _, group := range plan.Analysis.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterPartBlock(plan, part))
		}
	}
	return blocks
}

func buildListSubjectsIntersectionUsersetFilterPartBlock(plan ListPlan, part IntersectionPart) TypedQueryBlock {
	if part.ParentRelation != nil {
		relationMatch := buildUsersetFilterRelationMatchExpr("pt.subject_id")
		subjectExpr := Alias{Expr: NormalizedUsersetSubject(Col{Table: "pt", Column: "subject_id"}, Param("v_filter_relation")), Name: "subject_id"}

		return TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Userset filter intersection part: via %s", part.ParentRelation.LinkingRelation)},
			Query:    buildUsersetFilterTTUSelectStmt(plan.ObjectType, part.ParentRelation.LinkingRelation, subjectExpr, relationMatch),
		}
	}

	relationMatch := buildUsersetFilterRelationMatchExpr("t.subject_id")
	subjectExpr := Alias{Expr: NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, Param("v_filter_relation")), Name: "subject_id"}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset filter intersection part: %s", part.Relation)},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{subjectExpr},
			FromExpr:    TableAs("", "melange_tuples", "t"),
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
				Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
				relationMatch,
			),
		},
	}
}

func buildListSubjectsIntersectionUsersetFilterTTUBlocks(plan ListPlan) []TypedQueryBlock {
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(parentRelations))
	for _, parent := range parentRelations {
		blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterTTUBlock(plan, parent))
	}
	return blocks
}

func buildListSubjectsIntersectionUsersetFilterTTUBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	relationMatch := buildUsersetFilterRelationMatchExpr("pt.subject_id")
	subjectExpr := Alias{Expr: NormalizedUsersetSubject(Col{Table: "pt", Column: "subject_id"}, Param("v_filter_relation")), Name: "subject_id"}

	stmt := buildUsersetFilterTTUSelectStmt(plan.ObjectType, parent.LinkingRelation, subjectExpr, relationMatch)
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		// Add type restriction to existing WHERE clause
		stmt.Where = And(stmt.Where, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset filter TTU: via %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    stmt,
	}
}
