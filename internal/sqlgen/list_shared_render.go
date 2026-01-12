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
// Uses strings.Builder to avoid direct string concatenation with +.
func wrapQueryWithDepthForRender(sql, depthExpr, alias string) string {
	var sb strings.Builder
	sb.WriteString("SELECT DISTINCT ")
	sb.WriteString(alias)
	sb.WriteString(".object_id, ")
	sb.WriteString(depthExpr)
	sb.WriteString(" AS depth\nFROM (\n")
	sb.WriteString(sql)
	sb.WriteString("\n) AS ")
	sb.WriteString(alias)
	return sb.String()
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

// joinUnionAllBlocksSQL joins multiple SQL blocks with UNION ALL.
// Used for recursive CTE bodies where duplicate elimination is handled elsewhere.
func joinUnionAllBlocksSQL(blocks []string) string {
	return strings.Join(blocks, "\n    UNION ALL\n")
}

// appendUnionAll appends a part to existing SQL using UNION ALL separator.
// Avoids direct string concatenation for SQL clause construction.
func appendUnionAll(base, additional string) string {
	return joinUnionAllBlocksSQL([]string{base, additional})
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

	// Build CTE body as UNION ALL of base and recursive cases using typed DSL
	cteBody := UnionAll{
		Queries: []SQLer{
			CommentedSQL{Comment: "Base case: seed with empty set (we just need depth tracking)", Query: baseCase},
			CommentedSQL{Comment: "Track depth through all self-referential linking relations", Query: recursiveCase},
		},
	}

	// Build the final SELECT
	finalQuery := Raw("SELECT MAX(depth) FROM depth_check")

	// Build the CTE
	cteQuery := RecursiveCTE("depth_check", []string{"object_id", "depth"}, cteBody, finalQuery)

	// Wrap with SELECT INTO for PL/pgSQL variable assignment
	selectInto := SelectIntoVar{Query: cteQuery, Variable: "v_max_depth"}

	// Wrap with explanatory comments
	commentedQuery := MultiLineComment([]string{
		"Check for excessive recursion depth before running the query",
		"This matches check_permission behavior with M2002 error",
		"Only self-referential TTUs contribute to recursion depth (cross-type are one-hop)",
	}, selectInto)

	return IndentLines(commentedQuery.SQL(), "    ") + ";\n"
}

// dispatcherCallArgs defines typed arguments for dispatcher function calls.
var (
	listObjectsCallArgs = []Expr{
		Param("p_subject_type"),
		Param("p_subject_id"),
		Param("p_limit"),
		Param("p_after"),
	}
	listSubjectsCallArgs = []Expr{
		Param("p_object_id"),
		Param("p_subject_type"),
		Param("p_limit"),
		Param("p_after"),
	}
)

func renderListDispatcher(functionName string, args []FuncArg, returns string, cases []ListDispatcherCase) string {
	// Build the body with routing cases
	var bodyStmts []Stmt
	if len(cases) > 0 {
		for _, c := range cases {
			// Arguments depend on whether this is list_objects or list_subjects
			var callArgs []Expr
			if strings.Contains(functionName, "objects") {
				callArgs = listObjectsCallArgs
			} else {
				callArgs = listSubjectsCallArgs
			}

			// Build SELECT * FROM func_name(args) using typed DSL
			query := SelectStmt{
				Columns: []string{"*"},
				FromExpr: FunctionCallExpr{
					Name: c.FunctionName,
					Args: callArgs,
				},
			}

			bodyStmts = append(bodyStmts, If{
				Cond: And(
					Eq{Left: ObjectType, Right: Lit(c.ObjectType)},
					Eq{Left: Param("p_relation"), Right: Lit(c.Relation)},
				),
				Then: []Stmt{
					ReturnQuery{Query: query.SQL()},
					Return{},
				},
			})
		}
	}
	bodyStmts = append(bodyStmts,
		Comment{Text: "Unknown type/relation: return empty result"},
		Return{},
	)

	fn := PlpgsqlFunction{
		Name:    functionName,
		Args:    args,
		Returns: returns,
		Header: []string{
			"Generated dispatcher for " + functionName,
			"Routes to specialized functions for known type/relation pairs",
		},
		Body: bodyStmts,
	}
	return fn.SQL() + "\n"
}
