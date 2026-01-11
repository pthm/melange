package sqlgen

import (
	"fmt"
	"strings"
)

func generateCheckFunction(a RelationAnalysis, inline InlineSQLData, noWildcard bool) (string, error) {
	// Use Plan → Blocks → Render architecture
	plan := BuildCheckPlan(a, inline, noWildcard)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		return "", fmt.Errorf("building check blocks for %s.%s: %w", a.ObjectType, a.Relation, err)
	}
	return RenderCheckFunction(plan, blocks)
}

// selectInto transforms a SELECT query to use SELECT INTO syntax.
// Used to capture query results into PL/pgSQL variables.
func selectInto(query, target string) (string, error) {
	trimmed := strings.TrimSpace(query)
	if !strings.HasPrefix(strings.ToUpper(trimmed), "SELECT") {
		return "", fmt.Errorf("expected query to start with SELECT: %s", trimmed)
	}
	i := len("SELECT")
	for i < len(trimmed) && trimmed[i] <= ' ' {
		i++
	}
	if i >= len(trimmed) || trimmed[i] != '1' {
		return "", fmt.Errorf("expected SELECT 1 prefix: %s", trimmed)
	}
	return trimmed[:i+1] + " INTO " + target + trimmed[i+1:], nil
}

func generateDispatcher(analyses []RelationAnalysis, noWildcard bool) (string, error) {
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

func functionNameForDispatcher(a RelationAnalysis, noWildcard bool) string {
	if noWildcard {
		return functionNameNoWildcard(a.ObjectType, a.Relation)
	}
	return functionName(a.ObjectType, a.Relation)
}
