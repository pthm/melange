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

	// Build the base case: seed with empty set (we just need depth tracking)
	baseCase := SelectStmt{
		ColumnExprs: []Expr{Raw("NULL::TEXT"), Int(0)},
		Where:       Raw("FALSE"),
	}

	// Build the recursive case: track depth through all self-referential linking relations
	recursiveCase := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}, Add{Left: Col{Table: "d", Column: "depth"}, Right: Int(1)}},
		FromExpr:    TableAs("depth_check", "d"),
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "t",
				On: And(
					Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(objectType)},
					In{Expr: Col{Table: "t", Column: "relation"}, Values: linkingRelations},
					Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(objectType)},
				),
			},
		},
		Where: Lt{Left: Col{Table: "d", Column: "depth"}, Right: Int(26)},
	}

	// Build CTE body as UNION ALL of base and recursive cases
	cteBody := "-- Base case: seed with empty set (we just need depth tracking)\n" +
		IndentLines(baseCase.SQL(), "    ") + "\n\n" +
		"    UNION ALL\n" +
		"    -- Track depth through all self-referential linking relations\n" +
		IndentLines(recursiveCase.SQL(), "    ")

	// Build the final SELECT INTO
	finalQuery := Raw("SELECT MAX(depth) FROM depth_check")

	// Build the CTE
	cteQuery := RecursiveCTE("depth_check", []string{"object_id", "depth"}, Raw(cteBody), finalQuery)

	return "    -- Check for excessive recursion depth before running the query\n" +
		"    -- This matches check_permission behavior with M2002 error\n" +
		"    -- Only self-referential TTUs contribute to recursion depth (cross-type are one-hop)\n" +
		IndentLines(cteQuery.SQL(), "    ") + " INTO v_max_depth;\n"
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
