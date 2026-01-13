package sqlgen

// CheckBlocks contains all the DSL blocks needed to generate a check function.
type CheckBlocks struct {
	DirectCheck             Expr       // EXISTS check for direct/implied tuple lookup
	UsersetCheck            Expr       // EXISTS check for userset membership
	ExclusionCheck          Expr       // EXISTS check for exclusion (denial)
	UsersetSubjectSelfCheck SelectStmt // Validates when subject IS a userset

	// UsersetSubjectComputedCheck validates userset subject via tuple join
	UsersetSubjectComputedCheck SelectStmt

	ParentRelationBlocks []ParentRelationBlock    // TTU pattern checks
	ImpliedFunctionCalls []ImpliedFunctionCheck   // Complex implied relation checks
	IntersectionGroups   []IntersectionGroupCheck // AND groups for intersection patterns
	HasStandaloneAccess  bool                     // Access paths outside intersections exist
}

// ParentRelationBlock represents a TTU check through a parent relation.
type ParentRelationBlock struct {
	LinkingRelation     string
	ParentRelation      string
	AllowedLinkingTypes []string
	Query               SelectStmt
}

// ImpliedFunctionCheck represents a check that calls another check function.
type ImpliedFunctionCheck struct {
	Relation     string
	FunctionName string
	Check        Expr
}

// IntersectionGroupCheck represents an AND group where all checks must pass.
type IntersectionGroupCheck struct {
	Parts []IntersectionPartCheck
}

// IntersectionPartCheck represents one part of an intersection check.
type IntersectionPartCheck struct {
	Relation         string
	ExcludedRelation string
	IsParent         bool
	ParentRelation   string
	LinkingRelation  string
	Check            Expr
}

// BuildCheckBlocks builds all DSL blocks for a check function.
func BuildCheckBlocks(plan CheckPlan) (CheckBlocks, error) {
	blocks := CheckBlocks{
		HasStandaloneAccess: plan.HasStandaloneAccess,
	}

	if plan.HasDirect || plan.HasImplied {
		blocks.DirectCheck = buildDirectCheck(plan)
	}
	if plan.HasUserset {
		blocks.UsersetCheck = buildUsersetCheck(plan)
	}
	if plan.HasExclusion {
		blocks.ExclusionCheck = buildExclusionCheck(plan)
	}

	blocks.UsersetSubjectSelfCheck, blocks.UsersetSubjectComputedCheck = buildUsersetSubjectChecks(plan)

	if plan.HasParentRelations {
		blocks.ParentRelationBlocks = buildParentRelationBlocks(plan)
	}
	if plan.HasImpliedFunctionCall {
		blocks.ImpliedFunctionCalls = buildImpliedFunctionCalls(plan)
	}
	if plan.HasIntersection {
		blocks.IntersectionGroups = buildIntersectionGroups(plan)
	}

	return blocks, nil
}

func buildDirectCheck(plan CheckPlan) Expr {
	q := Tuples("").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Column: "subject_type"}, Values: plan.AllowedSubjectTypes},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return Exists{Query: q}
}

func buildUsersetCheck(plan CheckPlan) Expr {
	patterns := plan.Analysis.UsersetPatterns
	if len(patterns) == 0 {
		return nil
	}

	visitedWithKey := VisitedWithKey(plan.ObjectType, plan.Relation, ObjectID)
	checks := make([]Expr, 0, len(patterns))

	for _, pattern := range patterns {
		checks = append(checks, buildUsersetPatternCheck(plan, pattern, visitedWithKey))
	}

	if len(checks) == 1 {
		return checks[0]
	}
	return Or(checks...)
}

func buildUsersetPatternCheck(plan CheckPlan, pattern UsersetPattern, visitedWithKey Expr) Expr {
	grantTuple := Col{Table: "grant_tuple", Column: "subject_id"}

	baseWhere := []Expr{
		Eq{Left: Col{Table: "grant_tuple", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "grant_tuple", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: grantTuple},
		Eq{Left: UsersetRelation{Source: grantTuple}, Right: Lit(pattern.SubjectRelation)},
	}

	if pattern.IsComplex {
		// Complex pattern: use check_permission_internal for recursive membership verification
		q := Tuples("grant_tuple").
			ObjectType(plan.ObjectType).
			Relations(plan.Relation).
			Where(append(baseWhere, CheckPermission{
				Subject:  SubjectParams(),
				Relation: pattern.SubjectRelation,
				Object: ObjectRef{
					Type: Lit(pattern.SubjectType),
					ID:   UsersetObjectID{Source: grantTuple},
				},
				Visited:     visitedWithKey,
				ExpectAllow: true,
			})...).
			Select("1").
			Limit(1)
		return Exists{Query: q}
	}

	// Simple pattern: use tuple JOIN for membership lookup
	q := Tuples("grant_tuple").
		ObjectType(plan.ObjectType).
		Relations(plan.Relation).
		Where(baseWhere...).
		JoinTuples("membership",
			Eq{Left: Col{Table: "membership", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
			Eq{Left: Col{Table: "membership", Column: "object_id"}, Right: UsersetObjectID{Source: grantTuple}},
			In{Expr: Col{Table: "membership", Column: "relation"}, Values: pattern.SatisfyingRelations},
			Eq{Left: Col{Table: "membership", Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Table: "membership", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		Select("1").
		Limit(1)
	return Exists{Query: q}
}

func buildExclusionCheck(plan CheckPlan) Expr {
	if !plan.Exclusions.HasExclusions() {
		return nil
	}

	var checks []Expr
	obj := LiteralObject(plan.ObjectType, ObjectID)

	// Simple exclusions: EXISTS (excluded tuple)
	for _, rel := range plan.Exclusions.SimpleExcludedRelations {
		q := Tuples("excl").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "excl", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "excl", Column: "subject_type"}, Right: SubjectType},
				Or(
					Eq{Left: Col{Table: "excl", Column: "subject_id"}, Right: SubjectID},
					IsWildcard{Source: Col{Table: "excl", Column: "subject_id"}},
				),
			).
			Select("1").
			Limit(1)
		checks = append(checks, Exists{Query: q})
	}

	// Complex exclusions: check_permission_internal
	for _, rel := range plan.Exclusions.ComplexExcludedRelations {
		checks = append(checks, checkPermissionAllow(rel, obj))
	}

	// TTU exclusions
	for _, rel := range plan.Exclusions.ExcludedParentRelations {
		checks = append(checks, buildTTUExclusionCheck(plan.ObjectType, rel))
	}

	// Intersection exclusions
	for _, group := range plan.Exclusions.ExcludedIntersection {
		if expr := buildIntersectionExclusionCheck(plan.ObjectType, group); expr != nil {
			checks = append(checks, expr)
		}
	}

	return orExprs(checks)
}

func buildTTUExclusionCheck(objectType string, rel ExcludedParentRelation) Expr {
	linkQuery := Tuples("link").
		ObjectType(objectType).
		Relations(rel.LinkingRelation).
		Where(
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			CheckPermission{
				Subject:  SubjectParams(),
				Relation: rel.Relation,
				Object: ObjectRef{
					Type: Col{Table: "link", Column: "subject_type"},
					ID:   Col{Table: "link", Column: "subject_id"},
				},
				Visited:     Visited,
				ExpectAllow: true,
			},
		).
		Select("1").
		Limit(1)

	if len(rel.AllowedLinkingTypes) > 0 {
		linkQuery.WhereSubjectTypeIn(rel.AllowedLinkingTypes...)
	}
	return Exists{Query: linkQuery}
}

func buildIntersectionExclusionCheck(objectType string, group ExcludedIntersectionGroup) Expr {
	obj := LiteralObject(objectType, ObjectID)
	parts := make([]Expr, 0, len(group.Parts))

	for _, part := range group.Parts {
		switch {
		case part.ParentRelation != nil:
			parts = append(parts, buildTTUExclusionCheck(objectType, *part.ParentRelation))
		case part.ExcludedRelation != "":
			parts = append(parts, And(
				checkPermissionAllow(part.Relation, obj),
				checkPermissionDeny(part.ExcludedRelation, obj),
			))
		default:
			parts = append(parts, checkPermissionAllow(part.Relation, obj))
		}
	}

	if len(parts) == 0 {
		return nil
	}
	return And(parts...)
}

func checkPermissionAllow(relation string, obj ObjectRef) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      obj,
		Visited:     Visited,
		ExpectAllow: true,
	}
}

func checkPermissionDeny(relation string, obj ObjectRef) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      obj,
		Visited:     Visited,
		ExpectAllow: false,
	}
}

func orExprs(exprs []Expr) Expr {
	switch len(exprs) {
	case 0:
		return nil
	case 1:
		return exprs[0]
	default:
		return Or(exprs...)
	}
}

func buildUsersetSubjectChecks(plan CheckPlan) (selfCheck, computedCheck SelectStmt) {
	closureTable := ClosureTable(plan.Inline.ClosureRows, "c")

	selfCheck = SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    closureTable,
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
		Limit: 1,
	}

	computedCheck = SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{
			{
				Type:      "INNER",
				TableExpr: closureTable,
				On: And(
					Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
					Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Col{Table: "t", Column: "relation"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: UsersetTable(plan.Inline.UsersetRows, "m"),
				On: And(
					Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "m", Column: "relation"}, Right: Col{Table: "c", Column: "satisfying_relation"}},
					Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Col{Table: "t", Column: "subject_type"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: ClosureTable(plan.Inline.ClosureRows, "subj_c"),
				On: And(
					Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Col{Table: "t", Column: "subject_type"}},
					Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
					Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
				),
			},
		},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{Left: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, Right: UsersetObjectID{Source: SubjectID}},
		),
		Limit: 1,
	}

	return selfCheck, computedCheck
}

func buildParentRelationBlocks(plan CheckPlan) []ParentRelationBlock {
	blocks := make([]ParentRelationBlock, 0, len(plan.Analysis.ParentRelations))

	for _, parent := range plan.Analysis.ParentRelations {
		q := Tuples("link").
			ObjectType(plan.ObjectType).
			Relations(parent.LinkingRelation).
			Where(Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID}).
			Select("link.subject_type", "link.subject_id")

		if len(parent.AllowedLinkingTypes) > 0 {
			q.WhereSubjectTypeIn(parent.AllowedLinkingTypes...)
		}

		blocks = append(blocks, ParentRelationBlock{
			LinkingRelation:     parent.LinkingRelation,
			ParentRelation:      parent.Relation,
			AllowedLinkingTypes: parent.AllowedLinkingTypes,
			Query:               q.Build(),
		})
	}

	return blocks
}

func buildImpliedFunctionCalls(plan CheckPlan) []ImpliedFunctionCheck {
	calls := make([]ImpliedFunctionCheck, 0, len(plan.Analysis.ComplexClosureRelations))

	for _, rel := range plan.Analysis.ComplexClosureRelations {
		funcName := functionName(plan.ObjectType, rel)
		if plan.NoWildcard {
			funcName = functionNameNoWildcard(plan.ObjectType, rel)
		}

		calls = append(calls, ImpliedFunctionCheck{
			Relation:     rel,
			FunctionName: funcName,
			Check: CheckPermissionCall{
				FunctionName: funcName,
				Subject:      SubjectParams(),
				Relation:     rel,
				Object:       LiteralObject(plan.ObjectType, ObjectID),
				ExpectAllow:  true,
			},
		})
	}

	return calls
}

func buildIntersectionGroups(plan CheckPlan) []IntersectionGroupCheck {
	groups := make([]IntersectionGroupCheck, 0, len(plan.Analysis.IntersectionGroups))
	visitedWithKey := VisitedWithKey(plan.ObjectType, plan.Relation, ObjectID)

	for _, group := range plan.Analysis.IntersectionGroups {
		parts := make([]IntersectionPartCheck, 0, len(group.Parts))
		for _, part := range group.Parts {
			parts = append(parts, buildIntersectionPartCheck(plan, part, visitedWithKey))
		}
		groups = append(groups, IntersectionGroupCheck{Parts: parts})
	}

	return groups
}

func buildIntersectionPartCheck(plan CheckPlan, part IntersectionPart, visitedWithKey Expr) IntersectionPartCheck {
	pc := IntersectionPartCheck{
		Relation:         part.Relation,
		ExcludedRelation: part.ExcludedRelation,
	}

	switch {
	case part.ParentRelation != nil:
		pc.IsParent = true
		pc.ParentRelation = part.ParentRelation.Relation
		pc.LinkingRelation = part.ParentRelation.LinkingRelation
		pc.Check = buildParentCheck(plan.ObjectType, part.ParentRelation, visitedWithKey)

	case part.IsThis:
		pc.Check = buildThisCheck(plan, part)

	default:
		pc.Check = CheckPermission{
			Subject:     SubjectParams(),
			Relation:    part.Relation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			Visited:     visitedWithKey,
			ExpectAllow: true,
		}
	}

	if part.ExcludedRelation != "" {
		exclusionCheck := CheckPermission{
			Subject:     SubjectParams(),
			Relation:    part.ExcludedRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			Visited:     visitedWithKey,
			ExpectAllow: false,
		}
		pc.Check = And(pc.Check, exclusionCheck)
	}

	return pc
}

func buildParentCheck(objectType string, parent *ParentRelationInfo, visitedWithKey Expr) Expr {
	q := Tuples("link").
		ObjectType(objectType).
		Relations(parent.LinkingRelation).
		Where(
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			CheckPermission{
				Subject:  SubjectParams(),
				Relation: parent.Relation,
				Object: ObjectRef{
					Type: Col{Table: "link", Column: "subject_type"},
					ID:   Col{Table: "link", Column: "subject_id"},
				},
				Visited:     visitedWithKey,
				ExpectAllow: true,
			},
		).
		Select("1").
		Limit(1)

	if len(parent.AllowedLinkingTypes) > 0 {
		q.WhereSubjectTypeIn(parent.AllowedLinkingTypes...)
	}
	return Exists{Query: q}
}

func buildThisCheck(plan CheckPlan, part IntersectionPart) Expr {
	allowWildcard := part.HasWildcard && plan.AllowWildcard
	subjectCol := Col{Table: "t", Column: "subject_id"}

	var subjectCheck Expr
	if allowWildcard {
		subjectCheck = Or(
			Eq{Left: subjectCol, Right: SubjectID},
			IsWildcard{Source: subjectCol},
		)
	} else {
		subjectCheck = And(
			Eq{Left: subjectCol, Right: SubjectID},
			NotExpr{Expr: IsWildcard{Source: subjectCol}},
		)
	}

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(part.Relation).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			subjectCheck,
		).
		Select("1").
		Limit(1)
	return Exists{Query: q}
}
