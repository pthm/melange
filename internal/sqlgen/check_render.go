package sqlgen

func RenderCheckFunction(plan CheckPlan, blocks CheckBlocks) (string, error) {
	switch plan.DetermineCheckFunctionType() {
	case "direct":
		return renderCheckDirectFunctionFromBlocks(plan, blocks)
	case "intersection":
		return renderCheckIntersectionFunctionFromBlocks(plan, blocks)
	case "recursive":
		return renderCheckRecursiveFunctionFromBlocks(plan, blocks)
	default:
		return renderCheckRecursiveIntersectionFunctionFromBlocks(plan, blocks)
	}
}

func renderCheckDirectFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	body := buildUsersetSubjectStmts(plan, blocks)
	accessExpr := buildAccessCheckExpr(blocks, Visited)

	if plan.HasExclusion {
		exclusionExpr := blocks.ExclusionCheck
		if exclusionExpr == nil {
			exclusionExpr = Bool(false)
		}
		body = append(body, If{
			Cond: accessExpr,
			Then: []Stmt{
				If{
					Cond: exclusionExpr,
					Then: []Stmt{ReturnInt{Value: 0}},
					Else: []Stmt{ReturnInt{Value: 1}},
				},
			},
			Else: []Stmt{ReturnInt{Value: 0}},
		})
	} else {
		body = append(body, If{
			Cond: accessExpr,
			Then: []Stmt{ReturnInt{Value: 1}},
			Else: []Stmt{ReturnInt{Value: 0}},
		})
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    checkFunctionArgs(),
		Returns: "INTEGER",
		Decls:   []Decl{{Name: "v_userset_check", Type: "INTEGER := 0"}},
		Body:    body,
		Header:  checkFunctionHeader(plan),
	}

	return fn.SQL() + "\n", nil
}

func renderCheckIntersectionFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	body := buildUsersetSubjectStmts(plan, blocks)

	if blocks.HasStandaloneAccess {
		accessExpr := buildAccessCheckExpr(blocks, Visited)
		body = append(body,
			Comment{Text: "Non-intersection access paths"},
			If{
				Cond: accessExpr,
				Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
			},
		)
	}

	body = append(body, buildIntersectionGroupStmts(plan, blocks, Visited)...)
	body = append(body, buildExclusionWithAccessStmts(plan, blocks)...)
	body = append(body, ReturnInt{Value: 0})

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    checkFunctionArgs(),
		Returns: "INTEGER",
		Decls: []Decl{
			{Name: "v_userset_check", Type: "INTEGER := 0"},
			{Name: "v_has_access", Type: "BOOLEAN := FALSE"},
		},
		Body:   body,
		Header: checkFunctionHeader(plan),
	}

	return fn.SQL() + "\n", nil
}

func renderCheckRecursiveFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	visitedWithKey := Raw("p_visited || ARRAY[v_key]")

	body := buildCycleDetectionStmts()
	body = append(body, Assign{Name: "v_has_access", Value: Bool(false)})
	body = append(body, buildUsersetSubjectStmts(plan, blocks)...)
	body = append(body, buildStandaloneAccessPathStmts(plan, blocks, visitedWithKey)...)
	body = append(body, buildExclusionWithAccessStmts(plan, blocks)...)
	body = append(body, ReturnInt{Value: 0})

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    checkFunctionArgs(),
		Returns: "INTEGER",
		Decls:   recursiveCheckDecls(plan),
		Body:    body,
		Header:  checkFunctionHeader(plan),
	}

	return fn.SQL() + "\n", nil
}

func renderCheckRecursiveIntersectionFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	visitedWithKey := Raw("p_visited || ARRAY[v_key]")

	body := buildCycleDetectionStmts()
	body = append(body, Assign{Name: "v_has_access", Value: Bool(false)})
	body = append(body, buildUsersetSubjectStmts(plan, blocks)...)
	body = append(body, Comment{Text: "Relation has intersection; only render standalone paths if HasStandaloneAccess is true"})

	if blocks.HasStandaloneAccess {
		body = append(body, buildStandaloneAccessPathStmts(plan, blocks, visitedWithKey)...)
	}

	body = append(body, buildIntersectionGroupStmts(plan, blocks, visitedWithKey)...)
	body = append(body, buildExclusionWithAccessStmts(plan, blocks)...)
	body = append(body, ReturnInt{Value: 0})

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    checkFunctionArgs(),
		Returns: "INTEGER",
		Decls:   recursiveCheckDecls(plan),
		Body:    body,
		Header:  checkFunctionHeader(plan),
	}

	return fn.SQL() + "\n", nil
}

func checkFunctionArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
	}
}

func checkFunctionHeader(plan CheckPlan) []string {
	return []string{
		"Generated check function for " + plan.ObjectType + "." + plan.Relation,
		"Features: " + plan.FeaturesString,
	}
}

func recursiveCheckDecls(plan CheckPlan) []Decl {
	vKeyExpr := Concat{Parts: []Expr{
		Lit(plan.ObjectType + ":"),
		ObjectID,
		Lit(":" + plan.Relation),
	}}
	return []Decl{
		{Name: "v_has_access", Type: "BOOLEAN := FALSE"},
		{Name: "v_key", Type: "TEXT := " + vKeyExpr.SQL()},
		{Name: "v_userset_check", Type: "INTEGER := 0"},
	}
}

func buildUsersetSubjectStmts(plan CheckPlan, blocks CheckBlocks) []Stmt {
	var exclusionStmts []Stmt
	if plan.HasExclusion && blocks.ExclusionCheck != nil {
		exclusionStmts = []Stmt{
			If{
				Cond: blocks.ExclusionCheck,
				Then: []Stmt{ReturnInt{Value: 0}},
			},
		}
	}

	selfRefCond := AndExpr{Exprs: []Expr{
		Eq{Left: SubjectType, Right: Lit(plan.ObjectType)},
		Eq{
			Left: Substring{
				Source: SubjectID,
				From:   Int(1),
				For: Sub{
					Left:  Position{Needle: Lit("#"), Haystack: SubjectID},
					Right: Int(1),
				},
			},
			Right: ObjectID,
		},
	}}

	case1Body := []Stmt{
		SelectInto{Query: blocks.UsersetSubjectSelfCheck, Variable: "v_userset_check"},
		If{
			Cond: Eq{Left: Raw("v_userset_check"), Right: Int(1)},
			Then: []Stmt{ReturnInt{Value: 1}},
		},
	}

	case2SelectInto := SelectInto{Query: blocks.UsersetSubjectComputedCheck, Variable: "v_userset_check"}
	case2ResultCheck := If{
		Cond: Eq{Left: Raw("v_userset_check"), Right: Int(1)},
		Then: append(exclusionStmts, ReturnInt{Value: 1}),
	}

	return []Stmt{
		Comment{Text: "Userset subject handling"},
		If{
			Cond: Gt{Left: Position{Needle: Lit("#"), Haystack: SubjectID}, Right: Int(0)},
			Then: []Stmt{
				Comment{Text: "Case 1: Self-referential userset check"},
				If{Cond: selfRefCond, Then: case1Body},
				Comment{Text: "Case 2: Computed userset matching"},
				case2SelectInto,
				case2ResultCheck,
			},
		},
	}
}

func buildAccessCheckExpr(blocks CheckBlocks, visitedExpr Expr) Expr {
	var parts []Expr
	if blocks.DirectCheck != nil {
		parts = append(parts, blocks.DirectCheck)
	}
	if blocks.UsersetCheck != nil {
		parts = append(parts, blocks.UsersetCheck)
	}
	for _, call := range blocks.ImpliedFunctionCalls {
		checkCall := SpecializedCheckCall(call.FunctionName, SubjectType, SubjectID, ObjectID, visitedExpr)
		parts = append(parts, Raw(checkCall.SQL()))
	}

	switch len(parts) {
	case 0:
		return Bool(false)
	case 1:
		return parts[0]
	default:
		return OrExpr{Exprs: parts}
	}
}

func buildCycleDetectionStmts() []Stmt {
	return []Stmt{
		Comment{Text: "Cycle detection"},
		If{
			Cond: ArrayContains{Value: Raw("v_key"), Array: Visited},
			Then: []Stmt{ReturnInt{Value: 0}},
		},
		If{
			Cond: Gte{Left: ArrayLength{Array: Visited}, Right: Int(25)},
			Then: []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}},
		},
	}
}

func buildStandaloneAccessPathStmts(plan CheckPlan, blocks CheckBlocks, visitedExpr Expr) []Stmt {
	var stmts []Stmt

	if blocks.DirectCheck != nil {
		stmts = append(stmts,
			Comment{Text: "Direct/Implied access path"},
			If{
				Cond: blocks.DirectCheck,
				Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
			},
		)
	}

	if blocks.UsersetCheck != nil {
		stmts = append(stmts, accessPathCheck("Userset access path", blocks.UsersetCheck))
	}

	for _, call := range blocks.ImpliedFunctionCalls {
		checkCall := SpecializedCheckCall(call.FunctionName, SubjectType, SubjectID, ObjectID, visitedExpr)
		stmts = append(stmts, accessPathCheck("Implied access path via "+call.FunctionName, Raw(checkCall.SQL())))
	}

	for _, parent := range blocks.ParentRelationBlocks {
		existsSQL := renderParentRelationExistsFromBlocks(plan, parent, visitedExpr.SQL())
		comment := "Recursive access path via " + parent.LinkingRelation + " -> " + parent.ParentRelation
		stmts = append(stmts, accessPathCheck(comment, Raw(existsSQL)))
	}

	return stmts
}

func accessPathCheck(comment string, cond Expr) If {
	return If{
		Cond: NotExpr{Expr: Raw("v_has_access")},
		Then: []Stmt{
			Comment{Text: comment},
			If{
				Cond: cond,
				Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
			},
		},
	}
}

func renderParentRelationExistsFromBlocks(plan CheckPlan, parent ParentRelationBlock, visitedExpr string) string {
	checkCall := InternalCheckCall(
		SubjectType, SubjectID, parent.ParentRelation,
		Col{Table: "link", Column: "subject_type"},
		Col{Table: "link", Column: "subject_id"},
		Raw(visitedExpr),
	)

	q := Tuples("link").
		ObjectType(plan.ObjectType).
		Relations(parent.LinkingRelation).
		Where(
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: Raw("p_object_id")},
			Raw(checkCall.SQL()),
		)

	if len(parent.AllowedLinkingTypes) > 0 {
		q.Where(In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypes})
	}

	return q.ExistsSQL()
}

func buildIntersectionGroupStmts(plan CheckPlan, blocks CheckBlocks, visitedExpr Expr) []Stmt {
	if len(blocks.IntersectionGroups) == 0 {
		return nil
	}

	stmts := []Stmt{Comment{Text: "Intersection groups (OR'd together, parts within group AND'd)"}}

	for _, group := range blocks.IntersectionGroups {
		partExprs := make([]Expr, len(group.Parts))
		for i, part := range group.Parts {
			partExprs[i] = part.Check
		}

		groupCond := andExprs(partExprs)
		innerStmts := buildIntersectionInnerStmts(plan, group.Parts, visitedExpr)

		stmts = append(stmts, If{
			Cond: NotExpr{Expr: Raw("v_has_access")},
			Then: []Stmt{If{Cond: groupCond, Then: innerStmts}},
		})
	}

	return stmts
}

func andExprs(exprs []Expr) Expr {
	if len(exprs) == 1 {
		return exprs[0]
	}
	return AndExpr{Exprs: exprs}
}

func buildIntersectionInnerStmts(plan CheckPlan, parts []IntersectionPartCheck, visitedExpr Expr) []Stmt {
	hasExclusions := false
	for _, part := range parts {
		if part.ExcludedRelation != "" {
			hasExclusions = true
			break
		}
	}

	if !hasExclusions {
		return []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}}
	}

	innermost := []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}}
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part.ExcludedRelation == "" {
			continue
		}
		checkCall := InternalCheckCall(
			SubjectType, SubjectID, part.ExcludedRelation,
			Lit(plan.ObjectType), ObjectID, visitedExpr,
		)
		innermost = []Stmt{
			Comment{Text: "Check exclusion for part " + part.ExcludedRelation},
			If{
				Cond: Raw(checkCall.SQL()),
				Then: []Stmt{Comment{Text: "Excluded, this group fails"}},
				Else: innermost,
			},
		}
	}
	return innermost
}

func buildExclusionWithAccessStmts(plan CheckPlan, blocks CheckBlocks) []Stmt {
	if !plan.HasExclusion {
		return []Stmt{
			If{
				Cond: Raw("v_has_access"),
				Then: []Stmt{ReturnInt{Value: 1}},
			},
		}
	}

	exclusionExpr := blocks.ExclusionCheck
	if exclusionExpr == nil {
		exclusionExpr = Bool(false)
	}

	return []Stmt{
		Comment{Text: "Exclusion check"},
		If{
			Cond: Raw("v_has_access"),
			Then: []Stmt{
				If{
					Cond: exclusionExpr,
					Then: []Stmt{ReturnInt{Value: 0}},
				},
				ReturnInt{Value: 1},
			},
		},
	}
}
