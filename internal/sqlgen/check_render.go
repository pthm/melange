package sqlgen

import (
	"fmt"
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
	usersetBlock, err := renderCheckUsersetSubjectBlockFromBlocks(plan, blocks)
	if err != nil {
		return "", err
	}

	accessCondition := renderAccessChecksFromBlocks(blocks, "p_visited")

	var buf strings.Builder
	writeCheckHeaderFromPlan(&buf, plan)
	buf.WriteString("\nDECLARE\n    v_userset_check INTEGER := 0;\nBEGIN\n")
	buf.WriteString(usersetBlock)

	if plan.HasExclusion {
		exclusionSQL := renderExclusionCheckFromBlocks(blocks)
		buf.WriteString("\n    IF " + accessCondition + " THEN\n")
		buf.WriteString("        IF " + exclusionSQL + " THEN\n")
		buf.WriteString("            RETURN 0;\n")
		buf.WriteString("        ELSE\n")
		buf.WriteString("            RETURN 1;\n")
		buf.WriteString("        END IF;\n")
		buf.WriteString("    ELSE\n")
		buf.WriteString("        RETURN 0;\n")
		buf.WriteString("    END IF;\n")
	} else {
		buf.WriteString("\n    IF " + accessCondition + " THEN\n")
		buf.WriteString("        RETURN 1;\n")
		buf.WriteString("    ELSE\n")
		buf.WriteString("        RETURN 0;\n")
		buf.WriteString("    END IF;\n")
	}

	buf.WriteString("END;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

// =============================================================================
// Intersection Check Function (intersection patterns, no recursion)
// =============================================================================

func renderCheckIntersectionFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlockFromBlocks(plan, blocks)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	writeCheckHeaderFromPlan(&buf, plan)
	buf.WriteString("\nDECLARE\n    v_userset_check INTEGER := 0;\n    v_has_access BOOLEAN := FALSE;\nBEGIN\n")
	buf.WriteString(usersetBlock)

	if blocks.HasStandaloneAccess {
		accessCondition := renderAccessChecksFromBlocks(blocks, "p_visited")
		buf.WriteString("\n    -- Non-intersection access paths\n")
		buf.WriteString("    IF " + accessCondition + " THEN\n")
		buf.WriteString("        v_has_access := TRUE;\n")
		buf.WriteString("    END IF;\n")
	}

	intersectionBlock, err := renderIntersectionGroupsFromBlocks(plan, blocks, "p_visited")
	if err != nil {
		return "", err
	}
	buf.WriteString(intersectionBlock)
	buf.WriteString(renderExclusionWithAccessFromBlocks(plan, blocks))
	buf.WriteString("\n    RETURN 0;\nEND;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

// =============================================================================
// Recursive Check Function (recursion, no intersection)
// =============================================================================

func renderCheckRecursiveFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlockFromBlocks(plan, blocks)
	if err != nil {
		return "", err
	}

	standaloneBlock, err := renderRecursiveStandalonePathsFromBlocks(plan, blocks, "p_visited || v_key")
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	writeCheckHeaderFromPlan(&buf, plan)
	buf.WriteString("\nDECLARE\n    v_has_access BOOLEAN := FALSE;\n    v_key TEXT := '")
	buf.WriteString(plan.ObjectType)
	buf.WriteString(":' || p_object_id || ':")
	buf.WriteString(plan.Relation)
	buf.WriteString("';\n    v_userset_check INTEGER := 0;\nBEGIN\n")
	buf.WriteString("    -- Cycle detection\n")
	buf.WriteString("    IF v_key = ANY(p_visited) THEN RETURN 0; END IF;\n")
	buf.WriteString("    IF array_length(p_visited, 1) >= 25 THEN\n")
	buf.WriteString("        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';\n")
	buf.WriteString("    END IF;\n\n")
	buf.WriteString("    v_has_access := FALSE;\n\n")
	buf.WriteString(usersetBlock)
	buf.WriteString("\n")
	buf.WriteString(standaloneBlock)
	buf.WriteString(renderExclusionWithAccessFromBlocks(plan, blocks))
	buf.WriteString("\n    RETURN 0;\nEND;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

// =============================================================================
// Recursive Intersection Check Function (both recursion and intersection)
// =============================================================================

func renderCheckRecursiveIntersectionFunctionFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlockFromBlocks(plan, blocks)
	if err != nil {
		return "", err
	}

	var standaloneBlock string
	if blocks.HasStandaloneAccess {
		standaloneBlock, err = renderRecursiveStandalonePathsFromBlocks(plan, blocks, "p_visited || v_key")
		if err != nil {
			return "", err
		}
	}

	intersectionBlock, err := renderIntersectionGroupsFromBlocks(plan, blocks, "p_visited || v_key")
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	writeCheckHeaderFromPlan(&buf, plan)
	buf.WriteString("\nDECLARE\n    v_has_access BOOLEAN := FALSE;\n    v_key TEXT := '")
	buf.WriteString(plan.ObjectType)
	buf.WriteString(":' || p_object_id || ':")
	buf.WriteString(plan.Relation)
	buf.WriteString("';\n    v_userset_check INTEGER := 0;\nBEGIN\n")
	buf.WriteString("    -- Cycle detection\n")
	buf.WriteString("    IF v_key = ANY(p_visited) THEN RETURN 0; END IF;\n")
	buf.WriteString("    IF array_length(p_visited, 1) >= 25 THEN\n")
	buf.WriteString("        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';\n")
	buf.WriteString("    END IF;\n\n")
	buf.WriteString("    v_has_access := FALSE;\n\n")
	buf.WriteString(usersetBlock)
	buf.WriteString("\n    -- Relation has intersection; only render standalone paths if HasStandaloneAccess is true\n")
	if standaloneBlock != "" {
		buf.WriteString(standaloneBlock)
	}
	buf.WriteString(intersectionBlock)
	buf.WriteString(renderExclusionWithAccessFromBlocks(plan, blocks))
	buf.WriteString("\n    RETURN 0;\nEND;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

// =============================================================================
// Helper Functions
// =============================================================================

func writeCheckHeaderFromPlan(buf *strings.Builder, plan CheckPlan) {
	buf.WriteString("-- Generated check function for ")
	buf.WriteString(plan.ObjectType)
	buf.WriteString(".")
	buf.WriteString(plan.Relation)
	buf.WriteString("\n-- Features: ")
	buf.WriteString(plan.FeaturesString)
	buf.WriteString("\nCREATE OR REPLACE FUNCTION ")
	buf.WriteString(plan.FunctionName)
	buf.WriteString(" (\n")
	buf.WriteString("p_subject_type TEXT,\n")
	buf.WriteString("p_subject_id TEXT,\n")
	buf.WriteString("p_object_id TEXT,\n")
	buf.WriteString("p_visited TEXT [] DEFAULT ARRAY []::TEXT []\n")
	buf.WriteString(") RETURNS INTEGER AS $$")
}

func renderCheckUsersetSubjectBlockFromBlocks(plan CheckPlan, blocks CheckBlocks) (string, error) {
	selfQuery := blocks.UsersetSubjectSelfCheck.SQL()
	selfQuery, err := selectInto(selfQuery, "v_userset_check")
	if err != nil {
		return "", err
	}

	computedQuery := blocks.UsersetSubjectComputedCheck.SQL()
	computedQuery, err = selectInto(computedQuery, "v_userset_check")
	if err != nil {
		return "", err
	}

	var exclusionBlock string
	if plan.HasExclusion {
		exclusionSQL := renderExclusionCheckFromBlocks(blocks)
		exclusionBlock = "            IF " + exclusionSQL + " THEN\n" +
			"                RETURN 0;\n" +
			"            END IF;\n"
	}

	var block strings.Builder
	block.WriteString("    -- Userset subject handling\n")
	block.WriteString("    IF position('#' in p_subject_id) > 0 THEN\n")
	block.WriteString("        -- Case 1: Self-referential userset check\n")
	block.WriteString("        IF p_subject_type = '")
	block.WriteString(plan.ObjectType)
	block.WriteString("' AND\n")
	block.WriteString("           substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id THEN\n")
	block.WriteString(indentLines(selfQuery, "            "))
	block.WriteString(";\n")
	block.WriteString("            IF v_userset_check = 1 THEN\n")
	block.WriteString("                RETURN 1;\n")
	block.WriteString("            END IF;\n")
	block.WriteString("        END IF;\n\n")
	block.WriteString("        -- Case 2: Computed userset matching\n")
	block.WriteString(indentLines(computedQuery, "        "))
	block.WriteString(";\n")
	block.WriteString("        IF v_userset_check = 1 THEN\n")
	if exclusionBlock != "" {
		block.WriteString(exclusionBlock)
	}
	block.WriteString("            RETURN 1;\n")
	block.WriteString("        END IF;\n")
	block.WriteString("    END IF;\n")
	return block.String(), nil
}

func renderAccessChecksFromBlocks(blocks CheckBlocks, visitedExpr string) string {
	condition := ""
	if blocks.DirectCheck != nil {
		condition = blocks.DirectCheck.SQL()
	}
	if blocks.UsersetCheck != nil {
		if condition != "" {
			condition += " OR "
		}
		condition += blocks.UsersetCheck.SQL()
	}

	for _, call := range blocks.ImpliedFunctionCalls {
		if condition != "" {
			condition += " OR "
		}
		condition += fmt.Sprintf("%s(p_subject_type, p_subject_id, p_object_id, %s) = 1",
			call.FunctionName, visitedExpr)
	}

	if condition == "" {
		condition = "FALSE"
	}
	return condition
}

func renderExclusionCheckFromBlocks(blocks CheckBlocks) string {
	if blocks.ExclusionCheck == nil {
		return "FALSE"
	}
	return blocks.ExclusionCheck.SQL()
}

func renderRecursiveStandalonePathsFromBlocks(plan CheckPlan, blocks CheckBlocks, visitedExpr string) (string, error) {
	var parts []string

	// Direct/Implied access path
	if blocks.DirectCheck != nil {
		parts = append(parts, fmt.Sprintf(`
    -- Direct/Implied access path
    IF %s THEN
        v_has_access := TRUE;
    END IF;`, blocks.DirectCheck.SQL()))
	}

	// Userset access path
	if blocks.UsersetCheck != nil {
		parts = append(parts, fmt.Sprintf(`
    -- Userset access path
    IF NOT v_has_access THEN
        IF %s THEN
            v_has_access := TRUE;
        END IF;
    END IF;`, blocks.UsersetCheck.SQL()))
	}

	// Implied function calls
	for _, call := range blocks.ImpliedFunctionCalls {
		parts = append(parts, fmt.Sprintf(`
    -- Implied access path via %s
    IF NOT v_has_access THEN
        IF %s(p_subject_type, p_subject_id, p_object_id, %s) = 1 THEN
            v_has_access := TRUE;
        END IF;
    END IF;`, call.FunctionName, call.FunctionName, visitedExpr))
	}

	// Parent relation checks (TTU)
	for _, parent := range blocks.ParentRelationBlocks {
		existsSQL := renderParentRelationExistsFromBlocks(plan, parent, visitedExpr)
		parts = append(parts, fmt.Sprintf(`
    -- Recursive access path via %s -> %s
    IF NOT v_has_access THEN
        IF %s THEN
            v_has_access := TRUE;
        END IF;
    END IF;`, parent.LinkingRelation, parent.ParentRelation, existsSQL))
	}

	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, "\n"), nil
}

func renderParentRelationExistsFromBlocks(plan CheckPlan, parent ParentRelationBlock, visitedExpr string) string {
	q := Tuples("link").
		ObjectType(plan.ObjectType).
		Relations(parent.LinkingRelation).
		Where(
			Eq{Col{Table: "link", Column: "object_id"}, Raw("p_object_id")},
			Raw(fmt.Sprintf(
				"%s(p_subject_type, p_subject_id, '%s', link.subject_type, link.subject_id, %s) = 1",
				plan.InternalCheckFunctionName,
				parent.ParentRelation,
				visitedExpr,
			)),
		)

	if len(parent.AllowedLinkingTypes) > 0 {
		q.Where(In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypes})
	}

	return q.ExistsSQL()
}

func renderIntersectionGroupsFromBlocks(plan CheckPlan, blocks CheckBlocks, visitedExpr string) (string, error) {
	if len(blocks.IntersectionGroups) == 0 {
		return "", nil
	}

	var buf strings.Builder
	buf.WriteString("\n    -- Intersection groups (OR'd together, parts within group AND'd)\n")

	for _, group := range blocks.IntersectionGroups {
		partExprs := make([]string, 0, len(group.Parts))
		var exclusionBlocks []string
		var exclusionClosers []string

		for partIdx, part := range group.Parts {
			partExprs = append(partExprs, part.Check.SQL())

			if part.ExcludedRelation != "" {
				exclusionBlocks = append(exclusionBlocks, fmt.Sprintf(
					"            -- Check exclusion for part %d\n            IF %s(p_subject_type, p_subject_id, '%s', '%s', p_object_id, %s) = 1 THEN\n                -- Excluded, this group fails\n            ELSE",
					partIdx,
					plan.InternalCheckFunctionName,
					part.ExcludedRelation,
					plan.ObjectType,
					visitedExpr,
				))
				exclusionClosers = append(exclusionClosers, "            END IF;")
			}
		}

		groupCondition := strings.Join(partExprs, " AND ")
		buf.WriteString("    IF NOT v_has_access THEN\n")
		buf.WriteString("        IF " + groupCondition + " THEN\n")
		for _, exclusionBlock := range exclusionBlocks {
			buf.WriteString(exclusionBlock + "\n")
		}
		buf.WriteString("            v_has_access := TRUE;\n")
		for _, closer := range exclusionClosers {
			buf.WriteString(closer + "\n")
		}
		buf.WriteString("        END IF;\n")
		buf.WriteString("    END IF;\n")
	}

	return buf.String(), nil
}

func renderExclusionWithAccessFromBlocks(plan CheckPlan, blocks CheckBlocks) string {
	if plan.HasExclusion {
		exclusionSQL := renderExclusionCheckFromBlocks(blocks)
		return "\n    -- Exclusion check\n" +
			"    IF v_has_access THEN\n" +
			"        IF " + exclusionSQL + " THEN\n" +
			"            RETURN 0;\n" +
			"        END IF;\n" +
			"        RETURN 1;\n" +
			"    END IF;\n"
	}
	return "\n    IF v_has_access THEN RETURN 1; END IF;\n"
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
