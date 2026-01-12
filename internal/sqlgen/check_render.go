package sqlgen

import (
	"strings"
)

// =============================================================================
// Check Render Layer
// =============================================================================
//
// This file implements the Render layer for check function generation.
// The Render layer produces SQL/PLpgSQL strings from Plan and CheckBlocks data.
//
// Architecture: Plan → Blocks → Render
// - Plan: compute flags and normalized inputs
// - Blocks: build typed DSL expressions (CheckBlocks)
// - Render: produce SQL/PLpgSQL strings (this file)
//
// The render layer is the ONLY place in the check generation pipeline that
// produces SQL strings. All other layers work with typed DSL structures.

// RenderCheckFunction renders a complete check function from plan and blocks.
func RenderCheckFunction(plan CheckPlan, blocks CheckBlocks) (string, error) {
	// Determine function type and route to appropriate renderer
	switch plan.DetermineCheckFunctionType() {
	case "direct":
		return renderCheckDirectFunctionFromBlocks(plan, blocks)
	case "intersection":
		return renderCheckIntersectionFunctionFromBlocks(plan, blocks)
	case "recursive":
		return renderCheckRecursiveFunctionFromBlocks(plan, blocks)
	default: // "recursive_intersection"
		return renderCheckRecursiveIntersectionFunctionFromBlocks(plan, blocks)
	}
}

// =============================================================================
// Direct Check Function (no recursion, no intersection)
// =============================================================================

func renderCheckDirectFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	// Build function body using plpgsql types
	var body []Stmt

	// Add userset subject handling
	body = append(body, buildUsersetSubjectStmts(plan, blocks)...)

	// Build access check expression
	accessExpr := buildAccessCheckExpr(blocks, Visited)

	// Build the final access check with optional exclusion
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
		Name: plan.FunctionName,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
			{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		},
		Returns: "INTEGER",
		Decls:   []Decl{{Name: "v_userset_check", Type: "INTEGER := 0"}},
		Body:    body,
		Header: []string{
			"Generated check function for " + plan.ObjectType + "." + plan.Relation,
			"Features: " + plan.FeaturesString,
		},
	}

	return fn.SQL() + "\n", nil
}

// =============================================================================
// Intersection Check Function (intersection patterns, no recursion)
// =============================================================================

func renderCheckIntersectionFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	// Build function body using plpgsql types
	var body []Stmt

	// Add userset subject handling
	body = append(body, buildUsersetSubjectStmts(plan, blocks)...)

	// Non-intersection access paths (if any)
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

	// Add intersection group checks
	body = append(body, buildIntersectionGroupStmts(plan, blocks, Visited)...)

	// Add exclusion check with access
	body = append(body, buildExclusionWithAccessStmts(plan, blocks)...)

	// Final return
	body = append(body, ReturnInt{Value: 0})

	fn := PlpgsqlFunction{
		Name: plan.FunctionName,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
			{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		},
		Returns: "INTEGER",
		Decls: []Decl{
			{Name: "v_userset_check", Type: "INTEGER := 0"},
			{Name: "v_has_access", Type: "BOOLEAN := FALSE"},
		},
		Body: body,
		Header: []string{
			"Generated check function for " + plan.ObjectType + "." + plan.Relation,
			"Features: " + plan.FeaturesString,
		},
	}

	return fn.SQL() + "\n", nil
}

// =============================================================================
// Recursive Check Function (recursion, no intersection)
// =============================================================================

func renderCheckRecursiveFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	// Build function body using plpgsql types
	var body []Stmt

	// Cycle detection
	body = append(body, buildCycleDetectionStmts()...)

	// Initialize v_has_access
	body = append(body, Assign{Name: "v_has_access", Value: Bool(false)})

	// Add userset subject handling
	body = append(body, buildUsersetSubjectStmts(plan, blocks)...)

	// Build visited expression: p_visited || ARRAY[v_key]
	visitedWithKey := Raw("p_visited || ARRAY[v_key]")

	// Add standalone access paths
	body = append(body, buildStandaloneAccessPathStmts(plan, blocks, visitedWithKey)...)

	// Add exclusion check with access
	body = append(body, buildExclusionWithAccessStmts(plan, blocks)...)

	// Final return
	body = append(body, ReturnInt{Value: 0})

	// Build visited key expression for declaration
	vKeyExpr := Concat{Parts: []Expr{
		Lit(plan.ObjectType + ":"),
		ObjectID,
		Lit(":" + plan.Relation),
	}}

	fn := PlpgsqlFunction{
		Name: plan.FunctionName,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
			{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		},
		Returns: "INTEGER",
		Decls: []Decl{
			{Name: "v_has_access", Type: "BOOLEAN := FALSE"},
			{Name: "v_key", Type: "TEXT := " + vKeyExpr.SQL()},
			{Name: "v_userset_check", Type: "INTEGER := 0"},
		},
		Body: body,
		Header: []string{
			"Generated check function for " + plan.ObjectType + "." + plan.Relation,
			"Features: " + plan.FeaturesString,
		},
	}

	return fn.SQL() + "\n", nil
}

// =============================================================================
// Recursive Intersection Check Function (both recursion and intersection)
// =============================================================================

func renderCheckRecursiveIntersectionFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	// Build function body using plpgsql types
	var body []Stmt

	// Cycle detection
	body = append(body, buildCycleDetectionStmts()...)

	// Initialize v_has_access
	body = append(body, Assign{Name: "v_has_access", Value: Bool(false)})

	// Add userset subject handling
	body = append(body, buildUsersetSubjectStmts(plan, blocks)...)

	// Build visited expression: p_visited || ARRAY[v_key]
	visitedWithKey := Raw("p_visited || ARRAY[v_key]")

	// Comment about intersection handling
	body = append(body, Comment{Text: "Relation has intersection; only render standalone paths if HasStandaloneAccess is true"})

	// Add standalone access paths (if any)
	if blocks.HasStandaloneAccess {
		body = append(body, buildStandaloneAccessPathStmts(plan, blocks, visitedWithKey)...)
	}

	// Add intersection group checks
	body = append(body, buildIntersectionGroupStmts(plan, blocks, visitedWithKey)...)

	// Add exclusion check with access
	body = append(body, buildExclusionWithAccessStmts(plan, blocks)...)

	// Final return
	body = append(body, ReturnInt{Value: 0})

	// Build visited key expression for declaration
	vKeyExpr := Concat{Parts: []Expr{
		Lit(plan.ObjectType + ":"),
		ObjectID,
		Lit(":" + plan.Relation),
	}}

	fn := PlpgsqlFunction{
		Name: plan.FunctionName,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
			{Name: "p_object_id", Type: "TEXT"},
			{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		},
		Returns: "INTEGER",
		Decls: []Decl{
			{Name: "v_has_access", Type: "BOOLEAN := FALSE"},
			{Name: "v_key", Type: "TEXT := " + vKeyExpr.SQL()},
			{Name: "v_userset_check", Type: "INTEGER := 0"},
		},
		Body: body,
		Header: []string{
			"Generated check function for " + plan.ObjectType + "." + plan.Relation,
			"Features: " + plan.FeaturesString,
		},
	}

	return fn.SQL() + "\n", nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// buildUsersetSubjectStmts builds the userset subject handling statements.
// This handles the case where the subject itself is a userset (e.g., group#member).
func buildUsersetSubjectStmts(plan CheckPlan, blocks CheckBlocks) []Stmt {
	// Build the exclusion check if needed
	var exclusionStmts []Stmt
	if plan.HasExclusion && blocks.ExclusionCheck != nil {
		exclusionStmts = []Stmt{
			If{
				Cond: blocks.ExclusionCheck,
				Then: []Stmt{ReturnInt{Value: 0}},
			},
		}
	}

	// Case 1: Self-referential userset check
	// IF p_subject_type = 'objectType' AND substring(...) = p_object_id
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

	// Case 1 body: SELECT INTO, then check result
	case1Body := []Stmt{
		SelectInto{Query: blocks.UsersetSubjectSelfCheck, Variable: "v_userset_check"},
		If{
			Cond: Eq{Left: Raw("v_userset_check"), Right: Int(1)},
			Then: []Stmt{ReturnInt{Value: 1}},
		},
	}

	// Case 2 body: computed userset matching
	case2Body := []Stmt{
		SelectInto{Query: blocks.UsersetSubjectComputedCheck, Variable: "v_userset_check"},
	}

	// Build case 2 return with optional exclusion
	case2ReturnStmts := append(exclusionStmts, ReturnInt{Value: 1})
	case2Body = append(case2Body, If{
		Cond: Eq{Left: Raw("v_userset_check"), Right: Int(1)},
		Then: case2ReturnStmts,
	})

	return []Stmt{
		Comment{Text: "Userset subject handling"},
		If{
			Cond: Gt{Left: Position{Needle: Lit("#"), Haystack: SubjectID}, Right: Int(0)},
			Then: []Stmt{
				Comment{Text: "Case 1: Self-referential userset check"},
				If{
					Cond: selfRefCond,
					Then: case1Body,
				},
				Comment{Text: "Case 2: Computed userset matching"},
				case2Body[0], // SelectInto
				case2Body[1], // If check
			},
		},
	}
}

// buildAccessCheckExpr builds an OR expression for all access paths.
// Returns nil if there are no access checks, or an Expr for the combined condition.
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

	if len(parts) == 0 {
		return Bool(false)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return OrExpr{Exprs: parts}
}

// buildCycleDetectionStmts builds the cycle detection statements for recursive functions.
// Pattern: check if v_key is in p_visited, check depth limit.
func buildCycleDetectionStmts() []Stmt {
	return []Stmt{
		Comment{Text: "Cycle detection"},
		If{
			Cond: ArrayContains{Value: Raw("v_key"), Array: Visited},
			Then: []Stmt{ReturnInt{Value: 0}},
		},
		If{
			Cond: Gte{Left: ArrayLength{Array: Visited}, Right: Int(25)},
			Then: []Stmt{
				Raise{Message: "resolution too complex", ErrCode: "M2002"},
			},
		},
	}
}

// buildStandaloneAccessPathStmts builds statements for standalone (non-intersection) access paths.
// Used in recursive check functions to check direct, userset, implied, and parent relation paths.
func buildStandaloneAccessPathStmts(plan CheckPlan, blocks CheckBlocks, visitedExpr Expr) []Stmt {
	var stmts []Stmt

	// Direct/Implied access path
	if blocks.DirectCheck != nil {
		stmts = append(stmts,
			Comment{Text: "Direct/Implied access path"},
			If{
				Cond: blocks.DirectCheck,
				Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
			},
		)
	}

	// Userset access path (only if not already has access)
	if blocks.UsersetCheck != nil {
		stmts = append(stmts,
			Comment{Text: "Userset access path"},
			If{
				Cond: NotExpr{Expr: Raw("v_has_access")},
				Then: []Stmt{
					If{
						Cond: blocks.UsersetCheck,
						Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
					},
				},
			},
		)
	}

	// Implied function calls
	for _, call := range blocks.ImpliedFunctionCalls {
		checkCall := SpecializedCheckCall(call.FunctionName, SubjectType, SubjectID, ObjectID, visitedExpr)
		stmts = append(stmts,
			Comment{Text: "Implied access path via " + call.FunctionName},
			If{
				Cond: NotExpr{Expr: Raw("v_has_access")},
				Then: []Stmt{
					If{
						Cond: Raw(checkCall.SQL()),
						Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
					},
				},
			},
		)
	}

	// Parent relation checks (TTU)
	for _, parent := range blocks.ParentRelationBlocks {
		existsSQL := renderParentRelationExistsFromBlocks(plan, parent, visitedExpr.SQL())
		stmts = append(stmts,
			Comment{Text: "Recursive access path via " + parent.LinkingRelation + " -> " + parent.ParentRelation},
			If{
				Cond: NotExpr{Expr: Raw("v_has_access")},
				Then: []Stmt{
					If{
						Cond: Raw(existsSQL),
						Then: []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}},
					},
				},
			},
		)
	}

	return stmts
}

func renderParentRelationExistsFromBlocks(plan CheckPlan, parent ParentRelationBlock, visitedExpr string) string {
	// Use InternalCheckCall DSL instead of fmt.Sprintf
	checkCall := InternalCheckCall(
		SubjectType,
		SubjectID,
		parent.ParentRelation,
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

// buildIntersectionGroupStmts builds statements for intersection group checks.
// Each group is AND'd together, groups are OR'd (first match wins).
func buildIntersectionGroupStmts(plan CheckPlan, blocks CheckBlocks, visitedExpr Expr) []Stmt {
	if len(blocks.IntersectionGroups) == 0 {
		return nil
	}

	stmts := []Stmt{Comment{Text: "Intersection groups (OR'd together, parts within group AND'd)"}}

	for _, group := range blocks.IntersectionGroups {
		// Build the AND condition for all parts
		var partExprs []Expr
		for _, part := range group.Parts {
			partExprs = append(partExprs, part.Check)
		}

		// Build the condition: all parts must be true
		var groupCond Expr
		if len(partExprs) == 1 {
			groupCond = partExprs[0]
		} else {
			groupCond = AndExpr{Exprs: partExprs}
		}

		// Build inner statements: exclusion checks + set v_has_access
		var innerStmts []Stmt

		// Handle exclusions - wrap in nested IFs
		// For each part with exclusion, we need IF exclusion THEN (fail) ELSE (continue)
		var hasExclusions bool
		for _, part := range group.Parts {
			if part.ExcludedRelation != "" {
				hasExclusions = true
				break
			}
		}

		if hasExclusions {
			// Build nested exclusion checks
			// This is complex: we need to build inside-out
			// Start with the innermost action (set v_has_access)
			innermost := []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}}

			// Wrap from last to first exclusion
			for i := len(group.Parts) - 1; i >= 0; i-- {
				part := group.Parts[i]
				if part.ExcludedRelation != "" {
					checkCall := InternalCheckCall(
						SubjectType,
						SubjectID,
						part.ExcludedRelation,
						Lit(plan.ObjectType),
						ObjectID,
						visitedExpr,
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
			}
			innerStmts = innermost
		} else {
			innerStmts = []Stmt{Assign{Name: "v_has_access", Value: Bool(true)}}
		}

		// Wrap in: IF NOT v_has_access THEN IF groupCond THEN ... END IF; END IF;
		stmts = append(stmts, If{
			Cond: NotExpr{Expr: Raw("v_has_access")},
			Then: []Stmt{
				If{
					Cond: groupCond,
					Then: innerStmts,
				},
			},
		})
	}

	return stmts
}

// buildExclusionWithAccessStmts builds the final exclusion check statements.
// Returns plpgsql statements for the "if has access, check exclusion, return" pattern.
func buildExclusionWithAccessStmts(plan CheckPlan, blocks CheckBlocks) []Stmt {
	if plan.HasExclusion {
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
	return []Stmt{
		If{
			Cond: Raw("v_has_access"),
			Then: []Stmt{ReturnInt{Value: 1}},
		},
	}
}

// =============================================================================
// Dispatcher Rendering
// =============================================================================

// RenderCheckDispatcher renders the check_permission dispatcher function.
func RenderCheckDispatcher(analyses []RelationAnalysis, noWildcard bool) (string, error) {
	functionName := "check_permission"
	if noWildcard {
		functionName = "check_permission_no_wildcard"
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

	var buf strings.Builder
	if len(cases) > 0 {
		buf.WriteString("-- Generated internal dispatcher for ")
		buf.WriteString(functionName)
		buf.WriteString("_internal\n")
		buf.WriteString("-- Routes to specialized functions with p_visited for cycle detection in TTU patterns\n")
		buf.WriteString("-- Enforces depth limit of 25 to prevent stack overflow from deep permission chains\n")
		buf.WriteString("-- Phase 5: All relations use specialized functions - no generic fallback\n")
		buf.WriteString("CREATE OR REPLACE FUNCTION ")
		buf.WriteString(functionName)
		buf.WriteString("_internal (\n")
		buf.WriteString("p_subject_type TEXT,\n")
		buf.WriteString("p_subject_id TEXT,\n")
		buf.WriteString("p_relation TEXT,\n")
		buf.WriteString("p_object_type TEXT,\n")
		buf.WriteString("p_object_id TEXT,\n")
		buf.WriteString("p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]\n")
		buf.WriteString(") RETURNS INTEGER AS $$\n")
		buf.WriteString("BEGIN\n")
		buf.WriteString("    -- Depth limit check: prevent excessively deep permission resolution chains\n")
		buf.WriteString("    -- This catches both recursive TTU patterns and long userset chains\n")
		buf.WriteString("    IF array_length(p_visited, 1) >= 25 THEN\n")
		buf.WriteString("        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';\n")
		buf.WriteString("    END IF;\n\n")
		buf.WriteString("    RETURN (SELECT CASE\n")
		for _, c := range cases {
			buf.WriteString("        WHEN p_object_type = '")
			buf.WriteString(c.ObjectType)
			buf.WriteString("' AND p_relation = '")
			buf.WriteString(c.Relation)
			buf.WriteString("' THEN ")
			buf.WriteString(c.CheckFunctionName)
			buf.WriteString("(p_subject_type, p_subject_id, p_object_id, p_visited)\n")
		}
		buf.WriteString("        -- Unknown type/relation: deny by default (no generic fallback)\n")
		buf.WriteString("        ELSE 0\n")
		buf.WriteString("    END);\n")
		buf.WriteString("END;\n")
		buf.WriteString("$$ LANGUAGE plpgsql STABLE;\n\n")
		buf.WriteString("-- Generated dispatcher for ")
		buf.WriteString(functionName)
		buf.WriteString("\n")
		buf.WriteString("-- Routes to specialized functions for all known type/relation pairs\n")
		buf.WriteString("CREATE OR REPLACE FUNCTION ")
		buf.WriteString(functionName)
		buf.WriteString(" (\n")
		buf.WriteString("p_subject_type TEXT,\n")
		buf.WriteString("p_subject_id TEXT,\n")
		buf.WriteString("p_relation TEXT,\n")
		buf.WriteString("p_object_type TEXT,\n")
		buf.WriteString("p_object_id TEXT\n")
		buf.WriteString(") RETURNS INTEGER AS $$\n")
		buf.WriteString("    SELECT ")
		buf.WriteString(functionName)
		buf.WriteString("_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[]);\n")
		buf.WriteString("$$ LANGUAGE sql STABLE;\n")
		return buf.String(), nil
	}

	// No cases - generate empty dispatcher
	buf.WriteString("-- Generated dispatcher for ")
	buf.WriteString(functionName)
	buf.WriteString(" (no relations defined)\n")
	buf.WriteString("-- Phase 5: Returns 0 (deny) for all requests - no generic fallback\n")
	buf.WriteString("CREATE OR REPLACE FUNCTION ")
	buf.WriteString(functionName)
	buf.WriteString("_internal (\n")
	buf.WriteString("p_subject_type TEXT,\n")
	buf.WriteString("p_subject_id TEXT,\n")
	buf.WriteString("p_relation TEXT,\n")
	buf.WriteString("p_object_type TEXT,\n")
	buf.WriteString("p_object_id TEXT,\n")
	buf.WriteString("p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]\n")
	buf.WriteString(") RETURNS INTEGER AS $$\n")
	buf.WriteString("    SELECT 0;\n")
	buf.WriteString("$$ LANGUAGE sql STABLE;\n\n")
	buf.WriteString("CREATE OR REPLACE FUNCTION ")
	buf.WriteString(functionName)
	buf.WriteString(" (\n")
	buf.WriteString("p_subject_type TEXT,\n")
	buf.WriteString("p_subject_id TEXT,\n")
	buf.WriteString("p_relation TEXT,\n")
	buf.WriteString("p_object_type TEXT,\n")
	buf.WriteString("p_object_id TEXT\n")
	buf.WriteString(") RETURNS INTEGER AS $$\n")
	buf.WriteString("    SELECT 0;\n")
	buf.WriteString("$$ LANGUAGE sql STABLE;\n")
	return buf.String(), nil
}
