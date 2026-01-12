package sqlgen

import "strings"

// =============================================================================
// List Render Shared Helpers
// =============================================================================
// =============================================================================
// Block Rendering Helpers
// =============================================================================

// renderTypedQueryBlocks converts TypedQueryBlocks to QueryBlocks with rendered SQL.
func renderTypedQueryBlocks(blocks []TypedQueryBlock) []QueryBlock {
	result := make([]QueryBlock, len(blocks))
	for i, block := range blocks {
		result[i] = renderTypedQueryBlock(block)
	}
	return result
}

// renderTypedQueryBlock converts a TypedQueryBlock to QueryBlock.
// Since both now use SelectStmt, this is a direct copy.
func renderTypedQueryBlock(block TypedQueryBlock) QueryBlock {
	return QueryBlock{
		Comments: block.Comments,
		Query:    block.Query,
	}
}

// wrapQueryWithDepthForRender wraps a query to include depth column.
func wrapQueryWithDepthForRender(sql, depthExpr, alias string) string {
	return "SELECT DISTINCT " + alias + ".object_id, " + depthExpr + " AS depth\nFROM (\n" + sql + "\n) AS " + alias
}

// formatQueryBlockSQL formats a query block with comments.
func formatQueryBlockSQL(comments []string, sql string) string {
	lines := make([]string, 0, len(comments)+1)
	for _, comment := range comments {
		lines = append(lines, "    "+comment)
	}
	lines = append(lines, indentLines(sql, "    "))
	return strings.Join(lines, "\n")
}

// joinUnionBlocksSQL joins multiple SQL blocks with UNION.
func joinUnionBlocksSQL(blocks []string) string {
	return strings.Join(blocks, "\n    UNION\n")
}

// buildDepthCheckSQLForRender builds the depth check SQL for recursive functions.
func buildDepthCheckSQLForRender(objectType string, linkingRelations []string) string {
	if len(linkingRelations) == 0 {
		return "    v_max_depth := 0;\n"
	}
	return "    -- Check for excessive recursion depth before running the query\n" +
		"    -- This matches check_permission behavior with M2002 error\n" +
		"    -- Only self-referential TTUs contribute to recursion depth (cross-type are one-hop)\n" +
		"    WITH RECURSIVE depth_check(object_id, depth) AS (\n" +
		"        -- Base case: seed with empty set (we just need depth tracking)\n" +
		"        SELECT NULL::TEXT, 0\n" +
		"        WHERE FALSE\n" +
		"\n" +
		"        UNION ALL\n" +
		"        -- Track depth through all self-referential linking relations\n" +
		"        SELECT t.object_id, d.depth + 1\n" +
		"        FROM depth_check d\n" +
		"        JOIN melange_tuples t\n" +
		"          ON t.object_type = '" + objectType + "'\n" +
		"          AND t.relation IN (" + formatSQLStringList(linkingRelations) + ")\n" +
		"          AND t.subject_type = '" + objectType + "'\n" +
		"        WHERE d.depth < 26  -- Allow one extra to detect overflow\n" +
		"    )\n" +
		"    SELECT MAX(depth) INTO v_max_depth FROM depth_check;\n"
}

func renderListDispatcher(functionName string, args []FuncArg, returns string, cases []ListDispatcherCase) string {
	var buf strings.Builder

	buf.WriteString("-- Generated dispatcher for ")
	buf.WriteString(functionName)
	buf.WriteString("\n")
	buf.WriteString("-- Routes to specialized functions for known type/relation pairs\n")
	buf.WriteString("CREATE OR REPLACE FUNCTION ")
	buf.WriteString(functionName)
	buf.WriteString("(\n")

	for i, arg := range args {
		buf.WriteString("    ")
		buf.WriteString(arg.Name)
		buf.WriteString(" ")
		buf.WriteString(arg.Type)
		if arg.Default != nil {
			buf.WriteString(" DEFAULT ")
			buf.WriteString(arg.Default.SQL())
		}
		if i < len(args)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}

	buf.WriteString(") RETURNS ")
	buf.WriteString(returns)
	buf.WriteString(" AS $$\n")
	buf.WriteString("BEGIN\n")

	if len(cases) > 0 {
		for _, c := range cases {
			buf.WriteString("    IF p_object_type = '")
			buf.WriteString(c.ObjectType)
			buf.WriteString("' AND p_relation = '")
			buf.WriteString(c.Relation)
			buf.WriteString("' THEN\n")
			buf.WriteString("        RETURN QUERY SELECT * FROM ")
			buf.WriteString(c.FunctionName)
			buf.WriteString("(")
			// Arguments depend on whether this is list_objects or list_subjects
			if strings.Contains(functionName, "objects") {
				buf.WriteString("p_subject_type, p_subject_id, p_limit, p_after")
			} else {
				buf.WriteString("p_object_id, p_subject_type, p_limit, p_after")
			}
			buf.WriteString(");\n")
			buf.WriteString("        RETURN;\n")
			buf.WriteString("    END IF;\n")
		}
	}

	buf.WriteString("    -- Unknown type/relation: return empty result\n")
	buf.WriteString("    RETURN;\n")
	buf.WriteString("END;\n")
	buf.WriteString("$$ LANGUAGE plpgsql STABLE;\n")

	return buf.String()
}
