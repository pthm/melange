package sqlgen

import (
	"fmt"
	"strings"
)

func generateCheckFunction(a RelationAnalysis, inline InlineSQLData, noWildcard bool) (string, error) {
	// NOTE: The Plan → Blocks → Render architecture (BuildCheckPlan, BuildCheckBlocks,
	// RenderCheckFunction) exists but has subtle differences from the legacy approach
	// that cause test failures. The legacy buildCheckFunctionData path is used until
	// these differences are resolved.
	data, err := buildCheckFunctionData(a, inline, noWildcard)
	if err != nil {
		return "", fmt.Errorf("building check function data for %s.%s: %w", a.ObjectType, a.Relation, err)
	}

	needsPLpgSQL := a.Features.NeedsPLpgSQL() || a.HasComplexUsersetPatterns
	switch {
	case !needsPLpgSQL && !a.Features.HasIntersection:
		return renderCheckDirectFunction(data)
	case !needsPLpgSQL && a.Features.HasIntersection:
		return renderCheckIntersectionFunction(data)
	case needsPLpgSQL && !a.Features.HasIntersection:
		return renderCheckRecursiveFunction(data)
	default:
		return renderCheckRecursiveIntersectionFunction(data)
	}
}

func renderCheckDirectFunction(data CheckFunctionData) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlock(data)
	if err != nil {
		return "", err
	}
	accessCondition := renderAccessChecks(data, "p_visited")

	var buf strings.Builder
	writeCheckHeader(&buf, data)
	buf.WriteString("\nDECLARE\n    v_userset_check INTEGER := 0;\nBEGIN\n")
	buf.WriteString(usersetBlock)
	if data.HasExclusion {
		buf.WriteString("\n    IF " + accessCondition + " THEN\n")
		buf.WriteString("        IF " + data.ExclusionCheck + " THEN\n")
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

func renderCheckIntersectionFunction(data CheckFunctionData) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlock(data)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	writeCheckHeader(&buf, data)
	buf.WriteString("\nDECLARE\n    v_userset_check INTEGER := 0;\n    v_has_access BOOLEAN := FALSE;\nBEGIN\n")
	buf.WriteString(usersetBlock)

	if data.HasStandaloneAccess {
		accessCondition := renderAccessChecks(data, "p_visited")
		buf.WriteString("\n    -- Non-intersection access paths\n")
		buf.WriteString("    IF " + accessCondition + " THEN\n")
		buf.WriteString("        v_has_access := TRUE;\n")
		buf.WriteString("    END IF;\n")
	}

	intersectionBlock, err := renderIntersectionGroups(data, "p_visited")
	if err != nil {
		return "", err
	}
	buf.WriteString(intersectionBlock)
	buf.WriteString(renderExclusionWithAccess(data))
	buf.WriteString("\n    RETURN 0;\nEND;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

func renderCheckRecursiveFunction(data CheckFunctionData) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlock(data)
	if err != nil {
		return "", err
	}

	standaloneBlock, err := renderRecursiveStandalonePaths(data, "p_visited || v_key")
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	writeCheckHeader(&buf, data)
	buf.WriteString("\nDECLARE\n    v_has_access BOOLEAN := FALSE;\n    v_key TEXT := '")
	buf.WriteString(data.ObjectType)
	buf.WriteString(":' || p_object_id || ':")
	buf.WriteString(data.Relation)
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
	buf.WriteString(renderExclusionWithAccess(data))
	buf.WriteString("\n    RETURN 0;\nEND;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

func renderCheckRecursiveIntersectionFunction(data CheckFunctionData) (string, error) {
	usersetBlock, err := renderCheckUsersetSubjectBlock(data)
	if err != nil {
		return "", err
	}

	var standaloneBlock string
	if data.HasStandaloneAccess {
		standaloneBlock, err = renderRecursiveStandalonePaths(data, "p_visited || v_key")
		if err != nil {
			return "", err
		}
	}

	intersectionBlock, err := renderIntersectionGroups(data, "p_visited || v_key")
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	writeCheckHeader(&buf, data)
	buf.WriteString("\nDECLARE\n    v_has_access BOOLEAN := FALSE;\n    v_key TEXT := '")
	buf.WriteString(data.ObjectType)
	buf.WriteString(":' || p_object_id || ':")
	buf.WriteString(data.Relation)
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
	buf.WriteString(renderExclusionWithAccess(data))
	buf.WriteString("\n    RETURN 0;\nEND;\n$$ LANGUAGE plpgsql STABLE ;\n")
	return buf.String(), nil
}

func writeCheckHeader(buf *strings.Builder, data CheckFunctionData) {
	buf.WriteString("-- Generated check function for ")
	buf.WriteString(data.ObjectType)
	buf.WriteString(".")
	buf.WriteString(data.Relation)
	buf.WriteString("\n-- Features: ")
	buf.WriteString(data.FeaturesString)
	buf.WriteString("\nCREATE OR REPLACE FUNCTION ")
	buf.WriteString(data.FunctionName)
	buf.WriteString(" (\n")
	buf.WriteString("p_subject_type TEXT,\n")
	buf.WriteString("p_subject_id TEXT,\n")
	buf.WriteString("p_object_id TEXT,\n")
	buf.WriteString("p_visited TEXT [] DEFAULT ARRAY []::TEXT []\n")
	buf.WriteString(") RETURNS INTEGER AS $$")
}

func renderCheckUsersetSubjectBlock(data CheckFunctionData) (string, error) {
	selfQuery, err := UsersetSubjectSelfCheckQuery(UsersetSubjectSelfCheckInput{
		ObjectType:    data.ObjectType,
		Relation:      data.Relation,
		ClosureValues: data.ClosureValues,
	})
	if err != nil {
		return "", err
	}
	selfQuery, err = selectInto(selfQuery, "v_userset_check")
	if err != nil {
		return "", err
	}

	computedQuery, err := UsersetSubjectComputedCheckQuery(UsersetSubjectComputedCheckInput{
		ObjectType:    data.ObjectType,
		Relation:      data.Relation,
		ClosureValues: data.ClosureValues,
		UsersetValues: data.UsersetValues,
	})
	if err != nil {
		return "", err
	}
	computedQuery, err = selectInto(computedQuery, "v_userset_check")
	if err != nil {
		return "", err
	}

	var exclusionBlock string
	if data.HasExclusion {
		exclusionBlock = "            IF " + data.ExclusionCheck + " THEN\n" +
			"                RETURN 0;\n" +
			"            END IF;\n"
	}

	var block strings.Builder
	block.WriteString("    -- Userset subject handling\n")
	block.WriteString("    IF position('#' in p_subject_id) > 0 THEN\n")
	block.WriteString("        -- Case 1: Self-referential userset check\n")
	block.WriteString("        IF p_subject_type = '")
	block.WriteString(data.ObjectType)
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

func renderAccessChecks(data CheckFunctionData, visitedExpr string) string {
	condition := data.AccessChecks
	if condition == "" {
		condition = "FALSE"
	}
	for _, call := range data.ImpliedFunctionCalls {
		condition += fmt.Sprintf(" OR %s(p_subject_type, p_subject_id, p_object_id, %s) = 1", call.FunctionName, visitedExpr)
	}
	return condition
}

func renderRecursiveStandalonePaths(data CheckFunctionData, visitedExpr string) (string, error) {
	var blocks []string

	if data.HasDirect || data.HasImplied {
		blocks = append(blocks, fmt.Sprintf(`
    -- Direct/Implied access path
    IF %s THEN
        v_has_access := TRUE;
    END IF;`, data.DirectCheck))
	}

	if data.HasUserset {
		blocks = append(blocks, fmt.Sprintf(`
    -- Userset access path
    IF NOT v_has_access THEN
        IF %s THEN
            v_has_access := TRUE;
        END IF;
    END IF;`, data.UsersetCheck))
	}

	for _, call := range data.ImpliedFunctionCalls {
		blocks = append(blocks, fmt.Sprintf(`
    -- Implied access path via %s
    IF NOT v_has_access THEN
        IF %s(p_subject_type, p_subject_id, p_object_id, %s) = 1 THEN
            v_has_access := TRUE;
        END IF;
    END IF;`, call.FunctionName, call.FunctionName, visitedExpr))
	}

	for _, parent := range data.ParentRelations {
		existsSQL, err := buildParentRelationExists(data, parent, visitedExpr)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, fmt.Sprintf(`
    -- Recursive access path via %s -> %s
    IF NOT v_has_access THEN
        IF %s THEN
            v_has_access := TRUE;
        END IF;
    END IF;`, parent.LinkingRelation, parent.ParentRelation, existsSQL))
	}

	if len(blocks) == 0 {
		return "", nil
	}
	return strings.Join(blocks, "\n"), nil
}

func renderIntersectionGroups(data CheckFunctionData, visitedExpr string) (string, error) {
	if len(data.IntersectionGroups) == 0 {
		return "", nil
	}

	var buf strings.Builder
	buf.WriteString("\n    -- Intersection groups (OR'd together, parts within group AND'd)\n")

	for _, group := range data.IntersectionGroups {
		partExprs := make([]string, 0, len(group.Parts))
		var exclusionBlocks []string
		var exclusionClosers []string
		for partIdx, part := range group.Parts {
			switch {
			case part.IsThis:
				existsSQL, err := buildIntersectionThisExists(data, part.ThisHasWildcard)
				if err != nil {
					return "", err
				}
				partExprs = append(partExprs, existsSQL)
			case part.IsTTU:
				existsSQL, err := buildIntersectionTTUExists(data, part, visitedExpr)
				if err != nil {
					return "", err
				}
				partExprs = append(partExprs, existsSQL)
			default:
				partExprs = append(partExprs, fmt.Sprintf("%s(p_subject_type, p_subject_id, p_object_id, %s) = 1", part.FunctionName, visitedExpr))
			}

			if part.HasExclusion {
				exclusionBlocks = append(exclusionBlocks, fmt.Sprintf(
					"            -- Check exclusion for part %d\n            IF %s(p_subject_type, p_subject_id, '%s', '%s', p_object_id, %s) = 1 THEN\n                -- Excluded, this group fails\n            ELSE",
					partIdx,
					data.InternalCheckFunctionName,
					part.ExcludedRelation,
					data.ObjectType,
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

func renderExclusionWithAccess(data CheckFunctionData) string {
	if data.HasExclusion {
		return "\n    -- Exclusion check\n" +
			"    IF v_has_access THEN\n" +
			"        IF " + data.ExclusionCheck + " THEN\n" +
			"            RETURN 0;\n" +
			"        END IF;\n" +
			"        RETURN 1;\n" +
			"    END IF;\n"
	}
	return "\n    IF v_has_access THEN RETURN 1; END IF;\n"
}

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

func buildComplexUsersetCheck(a RelationAnalysis, pattern UsersetPattern, internalCheckFn string) (string, error) {
	visitedExpr := fmt.Sprintf("p_visited || ARRAY['%s:' || p_object_id || ':%s']", a.ObjectType, a.Relation)
	q := Tuples("grant_tuple").
		ObjectType(a.ObjectType).
		Relations(a.Relation).
		Where(
			Eq{Col{Table: "grant_tuple", Column: "object_id"}, Raw("p_object_id")},
			Eq{Col{Table: "grant_tuple", Column: "subject_type"}, Lit(pattern.SubjectType)},
			HasUserset{Col{Table: "grant_tuple", Column: "subject_id"}},
			Eq{UsersetRelation{Col{Table: "grant_tuple", Column: "subject_id"}}, Lit(pattern.SubjectRelation)},
			Raw(fmt.Sprintf(
				"%s(p_subject_type, p_subject_id, '%s', '%s', split_part(grant_tuple.subject_id, '#', 1), %s) = 1",
				internalCheckFn,
				pattern.SubjectRelation,
				pattern.SubjectType,
				visitedExpr,
			)),
		).
		Limit(1)
	return q.ExistsSQL(), nil
}

func buildComplexExclusionCheck(objectType, excludedRelation, internalCheckFn string) string {
	return fmt.Sprintf(
		"%s(p_subject_type, p_subject_id, '%s', '%s', p_object_id, p_visited) = 1",
		internalCheckFn,
		excludedRelation,
		objectType,
	)
}

func buildTTUExclusionCheck(objectType string, rel ParentRelationInfo, internalCheckFn string) (string, error) {
	q := Tuples("link").
		ObjectType(objectType).
		Relations(rel.LinkingRelation).
		Where(
			Eq{Col{Table: "link", Column: "object_id"}, Raw("p_object_id")},
			Raw(fmt.Sprintf(
				"%s(p_subject_type, p_subject_id, '%s', link.subject_type, link.subject_id, p_visited) = 1",
				internalCheckFn,
				rel.Relation,
			)),
		)
	if len(rel.AllowedLinkingTypes) > 0 {
		q.Where(In{Expr: Col{Table: "link", Column: "subject_type"}, Values: rel.AllowedLinkingTypes})
	}
	return q.ExistsSQL(), nil
}

func buildParentRelationExists(data CheckFunctionData, parent ParentRelationData, visitedExpr string) (string, error) {
	q := Tuples("link").
		ObjectType(data.ObjectType).
		Relations(parent.LinkingRelation).
		Where(
			Eq{Col{Table: "link", Column: "object_id"}, Raw("p_object_id")},
			Raw(fmt.Sprintf(
				"%s(p_subject_type, p_subject_id, '%s', link.subject_type, link.subject_id, %s) = 1",
				data.InternalCheckFunctionName,
				parent.ParentRelation,
				visitedExpr,
			)),
		)
	if parent.AllowedLinkingTypes != "" {
		q.Where(Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	return q.ExistsSQL(), nil
}

func buildIntersectionThisExists(data CheckFunctionData, allowWildcard bool) (string, error) {
	subjectIDCol := Col{Table: "t", Column: "subject_id"}
	var subjectCheck Expr
	if allowWildcard {
		subjectCheck = Or(
			Eq{subjectIDCol, Raw("p_subject_id")},
			Eq{subjectIDCol, Lit("*")},
		)
	} else {
		subjectCheck = And(
			Eq{subjectIDCol, Raw("p_subject_id")},
			Ne{subjectIDCol, Lit("*")},
		)
	}
	q := Tuples("t").
		ObjectType(data.ObjectType).
		Relations(data.Relation).
		Where(
			Eq{Col{Table: "t", Column: "object_id"}, Raw("p_object_id")},
			Eq{Col{Table: "t", Column: "subject_type"}, Raw("p_subject_type")},
			subjectCheck,
		)
	return q.ExistsSQL(), nil
}

func buildIntersectionTTUExists(data CheckFunctionData, part IntersectionPartData, visitedExpr string) (string, error) {
	q := Tuples("link").
		ObjectType(data.ObjectType).
		Relations(part.TTULinkingRelation).
		Where(
			Eq{Col{Table: "link", Column: "object_id"}, Raw("p_object_id")},
			Raw(fmt.Sprintf(
				"%s(p_subject_type, p_subject_id, '%s', link.subject_type, link.subject_id, %s) = 1",
				data.InternalCheckFunctionName,
				part.TTURelation,
				visitedExpr,
			)),
		)
	return q.ExistsSQL(), nil
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
