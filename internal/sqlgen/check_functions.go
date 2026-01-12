package sqlgen

import "fmt"

func generateCheckFunction(a RelationAnalysis, inline InlineSQLData, noWildcard bool) (string, error) {
	// Use Plan → Blocks → Render architecture
	plan := BuildCheckPlan(a, inline, noWildcard)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		return "", fmt.Errorf("building check blocks for %s.%s: %w", a.ObjectType, a.Relation, err)
	}
	return RenderCheckFunction(plan, blocks)
}

func generateDispatcher(analyses []RelationAnalysis, noWildcard bool) (string, error) {
	fnName := "check_permission"
	if noWildcard {
		fnName = "check_permission_no_wildcard"
	}

	var cases []DispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed {
			continue
		}
		checkFn := functionNameForDispatcher(a, noWildcard)
		cases = append(cases, DispatcherCase{
			ObjectType:        a.ObjectType,
			Relation:          a.Relation,
			CheckFunctionName: checkFn,
		})
	}

	if len(cases) > 0 {
		return renderDispatcherWithCases(fnName, cases), nil
	}
	return renderEmptyDispatcher(fnName), nil
}

// renderDispatcherWithCases renders the internal and public dispatcher functions
// when there are specialized check functions to route to.
func renderDispatcherWithCases(fnName string, cases []DispatcherCase) string {
	// Build CASE WHEN clauses for routing
	var whens []CaseWhen
	for _, c := range cases {
		// Condition: p_object_type = 'type' AND p_relation = 'relation'
		cond := AndExpr{Exprs: []Expr{
			Eq{Left: ObjectType, Right: Lit(c.ObjectType)},
			Eq{Left: Raw("p_relation"), Right: Lit(c.Relation)},
		}}
		// Result: call specialized function
		result := Func{
			Name: c.CheckFunctionName,
			Args: []Expr{SubjectType, SubjectID, ObjectID, Visited},
		}
		whens = append(whens, CaseWhen{Cond: cond, Result: result})
	}

	caseExpr := CaseExpr{
		Whens: whens,
		Else:  Int(0), // Unknown type/relation: deny by default
	}

	// Build internal dispatcher function (PL/pgSQL with depth limit)
	internalFn := PlpgsqlFunction{
		Name: fnName + "_internal",
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_relation", Type: "TEXT"},
			{Name: "p_object_type", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
			{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		},
		Returns: "INTEGER",
		Body: []Stmt{
			Comment{Text: "Depth limit check: prevent excessively deep permission resolution chains"},
			Comment{Text: "This catches both recursive TTU patterns and long userset chains"},
			If{
				Cond: Gte{Left: ArrayLength{Array: Visited}, Right: Int(25)},
				Then: []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}},
			},
			ReturnValue{Value: Raw("(SELECT " + caseExpr.SQL() + ")")},
		},
		Header: []string{
			"Generated internal dispatcher for " + fnName + "_internal",
			"Routes to specialized functions with p_visited for cycle detection in TTU patterns",
			"Enforces depth limit of 25 to prevent stack overflow from deep permission chains",
			"Phase 5: All relations use specialized functions - no generic fallback",
		},
	}

	// Build public dispatcher function (simple SQL wrapper)
	publicFn := SqlFunction{
		Name: fnName,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_relation", Type: "TEXT"},
			{Name: "p_object_type", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
		},
		Returns: "INTEGER",
		Body: Raw("SELECT " + fnName + "_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[])"),
		Header: []string{
			"Generated dispatcher for " + fnName,
			"Routes to specialized functions for all known type/relation pairs",
		},
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

// renderEmptyDispatcher renders dispatcher functions when no relations are defined.
// Both functions simply return 0 (deny all).
func renderEmptyDispatcher(fnName string) string {
	// Internal function returns 0 for all requests
	internalFn := SqlFunction{
		Name: fnName + "_internal",
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_relation", Type: "TEXT"},
			{Name: "p_object_type", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
			{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		},
		Returns: "INTEGER",
		Body:    Raw("SELECT 0"),
		Header: []string{
			"Generated dispatcher for " + fnName + " (no relations defined)",
			"Phase 5: Returns 0 (deny) for all requests - no generic fallback",
		},
	}

	// Public function also returns 0
	publicFn := SqlFunction{
		Name: fnName,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_relation", Type: "TEXT"},
			{Name: "p_object_type", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
		},
		Returns: "INTEGER",
		Body:    Raw("SELECT 0"),
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

func functionNameForDispatcher(a RelationAnalysis, noWildcard bool) string {
	if noWildcard {
		return functionNameNoWildcard(a.ObjectType, a.Relation)
	}
	return functionName(a.ObjectType, a.Relation)
}
