package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// List Render Layer
// =============================================================================
//
// This file implements the Render layer for list function generation.
// The Render layer produces SQL/PLpgSQL strings from Plan and BlockSet data.
//
// Architecture: Plan → Blocks → Render
// - Plan: compute flags and normalized inputs
// - Blocks: build typed query structures using DSL
// - Render: produce SQL/PLpgSQL strings (this file)
//
// The render layer is the ONLY place in the list generation pipeline that
// produces SQL strings. All other layers work with typed DSL structures.

// RenderListObjectsFunction renders a complete list_objects function from plan and blocks.
func RenderListObjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	// Convert typed blocks to QueryBlocks with rendered SQL
	queryBlocks := renderTypedQueryBlocks(blocks.Primary)

	// Render the UNION of all primary blocks
	query := RenderUnionBlocks(queryBlocks)

	// Build the function using PlpgsqlFunction
	return renderListObjectsFunctionSQL(plan, query), nil
}

// RenderListSubjectsFunction renders a complete list_subjects function from plan and blocks.
func RenderListSubjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	// Convert typed blocks to QueryBlocks with rendered SQL
	primaryBlocks := renderTypedQueryBlocks(blocks.Primary)
	secondaryBlocks := renderTypedQueryBlocks(blocks.Secondary)

	// Build userset filter path query (when p_subject_type contains '#')
	var usersetFilterPaginatedQuery string
	if len(secondaryBlocks) > 0 || blocks.SecondarySelf != nil {
		parts := append([]QueryBlock{}, secondaryBlocks...)
		if blocks.SecondarySelf != nil {
			parts = append(parts, renderTypedQueryBlock(*blocks.SecondarySelf))
		}
		usersetFilterQuery := RenderUnionBlocks(parts)
		usersetFilterPaginatedQuery = wrapWithPaginationWildcardFirst(usersetFilterQuery)
	}

	// Render regular path query
	regularQuery := RenderUnionBlocks(primaryBlocks)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := renderRegularSubjectElseBranch(plan, regularPaginatedQuery)

	// Build main IF statement: check if subject_type is a userset filter
	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: thenBranch,
		Else: elseBranch,
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls: []Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			Comment{Text: "Check if subject_type is a userset filter (e.g., \"document#viewer\")"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}

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

// renderTypedQueryBlock converts a single TypedQueryBlock to QueryBlock with rendered SQL.
func renderTypedQueryBlock(block TypedQueryBlock) QueryBlock {
	return QueryBlock{
		Comments: block.Comments,
		SQL:      block.Query.SQL(),
	}
}

// renderListObjectsFunctionSQL builds the complete list_objects function.
func renderListObjectsFunctionSQL(plan ListPlan, query string) string {
	paginatedQuery := wrapWithPagination(query, "object_id")
	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Body: []Stmt{
			ReturnQuery{Query: paginatedQuery},
		},
	}
	return fn.SQL()
}

// RenderListObjectsRecursiveFunction renders a recursive list_objects function from plan and blocks.
// This handles TTU patterns with depth tracking and recursive CTEs.
func RenderListObjectsRecursiveFunction(plan ListPlan, blocks RecursiveBlockSet) (string, error) {
	// Build CTE body from base blocks
	cteBody := renderRecursiveCTEBody(blocks)

	// Build final exclusion predicates for the CTE result
	finalExclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "acc", Column: "object_id"},
		SubjectType,
		SubjectID,
	)
	exclusionPreds := finalExclusions.BuildPredicates()

	var whereExpr Expr
	if len(exclusionPreds) > 0 {
		allPreds := append([]Expr{Bool(true)}, exclusionPreds...)
		whereExpr = And(allPreds...)
	}

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "acc", Column: "object_id"}},
		FromExpr:    TableAs("accessible", "acc"),
		Where:       whereExpr,
	}

	// Build the CTE SQL
	cteSQL := "WITH RECURSIVE accessible(object_id, depth) AS (\n" + cteBody + "\n)\n" + finalStmt.SQL()

	// Build self-candidate SQL
	var selfCandidateSQL string
	if blocks.SelfCandidateBlock != nil {
		selfCandidateSQL = renderTypedQueryBlock(*blocks.SelfCandidateBlock).SQL
	}

	// Combine CTE and self-candidate with UNION
	var query string
	if selfCandidateSQL != "" {
		query = joinUnionBlocksSQL([]string{cteSQL, selfCandidateSQL})
	} else {
		query = cteSQL
	}

	// Build depth check SQL
	depthCheck := buildDepthCheckSQLForRender(plan.ObjectType, blocks.SelfRefLinkingRelations)
	paginatedQuery := wrapWithPagination(query, "object_id")

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls: []Decl{
			{Name: "v_max_depth", Type: "INTEGER"},
		},
		Body: []Stmt{
			RawStmt{SQLText: strings.TrimSpace(depthCheck)},
			If{
				Cond: Raw("v_max_depth >= 25"),
				Then: []Stmt{
					Raise{Message: "resolution too complex", ErrCode: "M2002"},
				},
			},
			ReturnQuery{Query: paginatedQuery},
		},
	}

	return fn.SQL(), nil
}

// renderRecursiveCTEBody renders the CTE body from base and recursive blocks.
func renderRecursiveCTEBody(blocks RecursiveBlockSet) string {
	// Render base blocks with depth wrapping
	var baseBlocksSQL []string
	for _, block := range blocks.BaseBlocks {
		qb := renderTypedQueryBlock(block)
		wrappedSQL := wrapQueryWithDepthForRender(qb.SQL, "0", "base")
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, wrappedSQL))
	}

	// Join base blocks with UNION
	cteBody := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Add recursive block with UNION ALL if present
	if blocks.RecursiveBlock != nil {
		qb := renderTypedQueryBlock(*blocks.RecursiveBlock)
		recursiveSQL := formatQueryBlockSQL(qb.Comments, qb.SQL)
		cteBody = cteBody + "\n    UNION ALL\n" + recursiveSQL
	}

	return cteBody
}

// wrapQueryWithDepthForRender wraps a query to include depth column.
func wrapQueryWithDepthForRender(sql, depthExpr, alias string) string {
	return "SELECT DISTINCT " + alias + ".object_id, " + depthExpr + " AS depth\nFROM (\n" + sql + "\n) AS " + alias
}

// formatQueryBlockSQL formats a query block with comments.
func formatQueryBlockSQL(comments []string, sql string) string {
	var lines []string
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

// renderUsersetFilterThenBranch builds the THEN branch statements for userset filter path.
func renderUsersetFilterThenBranch(usersetFilterPaginatedQuery string) []Stmt {
	// If there are no userset filter blocks, just return empty results
	if usersetFilterPaginatedQuery == "" {
		return []Stmt{Return{}}
	}

	// v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1)
	filterTypeAssign := Assign{
		Name: "v_filter_type",
		Value: Substring{
			Source: SubjectType,
			From:   Int(1),
			For: Sub{
				Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
				Right: Int(1),
			},
		},
	}

	// v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1)
	filterRelationAssign := Assign{
		Name: "v_filter_relation",
		Value: Substring{
			Source: SubjectType,
			From: Add{
				Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
				Right: Int(1),
			},
		},
	}

	return []Stmt{
		filterTypeAssign,
		filterRelationAssign,
		ReturnQuery{Query: usersetFilterPaginatedQuery},
	}
}

// renderRegularSubjectElseBranch builds the ELSE branch statements for regular subject type path.
func renderRegularSubjectElseBranch(plan ListPlan, regularPaginatedQuery string) []Stmt {
	var stmts []Stmt

	// Add type guard for non-userset templates
	if !plan.HasUsersetPatterns {
		typeGuard := If{
			Cond: NotIn{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
			Then: []Stmt{Return{}},
		}
		stmts = append(stmts,
			Comment{Text: "Guard: return empty if subject type is not allowed by the model"},
			typeGuard,
			Comment{Text: "Regular subject type (no userset filter)"},
		)
	}

	stmts = append(stmts, ReturnQuery{Query: regularPaginatedQuery})
	return stmts
}

// RenderListSubjectsRecursiveFunction renders a recursive list_subjects function from plan and blocks.
// This handles TTU patterns with subject_pool CTE and check_permission_internal calls.
func RenderListSubjectsRecursiveFunction(plan ListPlan, blocks SubjectsRecursiveBlockSet) (string, error) {
	// Render userset filter path query
	usersetFilterBlocks := renderTypedQueryBlocks(blocks.UsersetFilterBlocks)
	var usersetFilterParts []QueryBlock
	usersetFilterParts = append(usersetFilterParts, usersetFilterBlocks...)
	if blocks.UsersetFilterSelfBlock != nil {
		usersetFilterParts = append(usersetFilterParts, renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock))
	}
	usersetFilterQuery := RenderUnionBlocks(usersetFilterParts)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)

	// Render regular path blocks
	regularBlocks := renderTypedQueryBlocks(blocks.RegularBlocks)
	ttuBlocks := renderTypedQueryBlocks(blocks.RegularTTUBlocks)

	// Build the regular query with subject_pool and base_results CTEs
	regularQuery := buildSubjectsRecursiveRegularQuery(plan, regularBlocks, ttuBlocks)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := []Stmt{
		Comment{Text: "Regular subject type: find direct subjects and expand usersets"},
		ReturnQuery{Query: regularPaginatedQuery},
	}

	// Build main IF statement: check if subject_type is a userset filter
	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: thenBranch,
		Else: elseBranch,
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls: []Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			Comment{Text: "Check if p_subject_type is a userset filter (contains '#')"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}

// buildSubjectsRecursiveRegularQuery builds the regular path query with subject_pool and base_results CTEs.
func buildSubjectsRecursiveRegularQuery(plan ListPlan, regularBlocks, ttuBlocks []QueryBlock) string {
	// Build subject_pool CTE - pool of subjects matching the type constraint
	subjectPoolSQL := buildSubjectPoolCTESQL(plan)

	// Join all base blocks with UNION
	baseBlocksSQL := RenderUnionBlocks(regularBlocks)

	// Add TTU blocks to base results
	if len(ttuBlocks) > 0 {
		ttuBlocksSQL := RenderUnionBlocks(ttuBlocks)
		baseBlocksSQL = baseBlocksSQL + "\n    UNION\n" + ttuBlocksSQL
	}

	// Build the full CTE query with wildcard handling
	wildcardTailSQL := renderSubjectsWildcardTail(plan)

	return "WITH subject_pool AS (\n" +
		indentLines(subjectPoolSQL, "        ") + "\n" +
		"        ),\n" +
		"        base_results AS (\n" +
		indentLines(baseBlocksSQL, "        ") + "\n" +
		"        ),\n" +
		"        has_wildcard AS (\n" +
		"            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard\n" +
		"        )\n" +
		wildcardTailSQL
}

// buildSubjectPoolCTESQL builds the subject_pool CTE SQL.
func buildSubjectPoolCTESQL(plan ListPlan) string {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples("t").
		Select("t.subject_id").
		WhereSubjectType(SubjectType).
		Where(In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}).
		Distinct()

	if excludeWildcard {
		q = q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	return q.SQL()
}

// renderSubjectsWildcardTail renders the final SELECT with wildcard handling.
// Note: No trailing semicolon - this gets wrapped in pagination CTEs.
func renderSubjectsWildcardTail(plan ListPlan) string {
	if plan.AllowWildcard {
		return "        -- Wildcard handling: when wildcard exists, filter non-wildcard subjects\n" +
			"        -- to only those with explicit (non-wildcard-derived) access\n" +
			"        SELECT br.subject_id\n" +
			"        FROM base_results br\n" +
			"        CROSS JOIN has_wildcard hw\n" +
			"        WHERE (NOT hw.has_wildcard)\n" +
			"           OR (br.subject_id = '*')\n" +
			"           OR (\n" +
			"               br.subject_id != '*'\n" +
			"               AND check_permission_no_wildcard(\n" +
			"                   p_subject_type,\n" +
			"                   br.subject_id,\n" +
			"                   '" + plan.Relation + "',\n" +
			"                   '" + plan.ObjectType + "',\n" +
			"                   p_object_id\n" +
			"               ) = 1\n" +
			"           )"
	}
	return "        SELECT br.subject_id FROM base_results br"
}

// RenderListSubjectsIntersectionFunction renders an intersection list_subjects function from plan and blocks.
// Intersection gathers candidates then filters with check_permission at the end.
func RenderListSubjectsIntersectionFunction(plan ListPlan, blocks SubjectsIntersectionBlockSet) (string, error) {
	// Render regular candidate blocks
	regularCandidateBlocks := renderTypedQueryBlocks(blocks.RegularCandidateBlocks)
	regularCandidatesSQL := RenderUnionBlocks(regularCandidateBlocks)

	// Build regular query with check_permission filter
	regularQuery := buildIntersectionRegularQuery(plan, regularCandidatesSQL)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Render userset filter candidate blocks
	usersetCandidateBlocks := renderTypedQueryBlocks(blocks.UsersetFilterCandidateBlocks)
	usersetCandidatesSQL := RenderUnionBlocks(usersetCandidateBlocks)

	// Build userset filter query with check_permission filter and self block
	usersetFilterQuery := buildIntersectionUsersetFilterQuery(plan, usersetCandidatesSQL, blocks.UsersetFilterSelfBlock)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := renderIntersectionRegularElseBranch(plan, regularPaginatedQuery)

	// Build main IF statement
	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: thenBranch,
		Else: elseBranch,
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls: []Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			Comment{Text: "Check if p_subject_type is a userset filter (contains '#')"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}

// buildIntersectionRegularQuery builds the regular path query for intersection.
// It wraps candidates in a CTE and filters with check_permission.
func buildIntersectionRegularQuery(plan ListPlan, candidatesSQL string) string {
	wildcardTail := renderSubjectsIntersectionWildcardTail(plan)

	return fmt.Sprintf(`WITH subject_candidates AS (
%s
        ),
        filtered_candidates AS (
            SELECT DISTINCT c.subject_id
            FROM subject_candidates c
            WHERE check_permission(p_subject_type, c.subject_id, '%s', '%s', p_object_id) = 1
        )%s`,
		indentLines(candidatesSQL, "        "),
		plan.Relation,
		plan.ObjectType,
		wildcardTail,
	)
}

// renderSubjectsIntersectionWildcardTail renders the wildcard handling for intersection.
// Unlike simple wildcard relations, intersections require all parts to be satisfied.
// The check_permission filter already correctly handles intersection logic, so we
// return filtered_candidates directly without additional wildcard filtering.
// For example: `viewer: [user:*] and allowed` - a user who gets viewer via the
// wildcard AND is in allowed should be returned, even though check_permission_no_wildcard
// would fail (since there's no direct viewer tuple for that user).
func renderSubjectsIntersectionWildcardTail(_ ListPlan) string {
	// For intersections, return all filtered candidates directly.
	// The check_permission filter in filtered_candidates already handles
	// intersection logic correctly, including wildcard components.
	return "\n        SELECT fc.subject_id FROM filtered_candidates fc"
}

// buildIntersectionUsersetFilterQuery builds the userset filter path query for intersection.
func buildIntersectionUsersetFilterQuery(plan ListPlan, candidatesSQL string, selfBlock *TypedQueryBlock) string {
	var selfSQL string
	if selfBlock != nil {
		rendered := renderTypedQueryBlock(*selfBlock)
		selfSQL = fmt.Sprintf(`

        UNION

%s`,
			formatQueryBlock(rendered.Comments, rendered.SQL))
	}

	return fmt.Sprintf(`WITH userset_candidates AS (
%s
        )
        SELECT DISTINCT c.subject_id
        FROM userset_candidates c
        WHERE check_permission(v_filter_type, c.subject_id, '%s', '%s', p_object_id) = 1%s`,
		candidatesSQL,
		plan.Relation,
		plan.ObjectType,
		selfSQL,
	)
}

// renderIntersectionRegularElseBranch builds the ELSE branch for intersection regular path.
func renderIntersectionRegularElseBranch(plan ListPlan, regularPaginatedQuery string) []Stmt {
	var stmts []Stmt

	// Add type guard
	typeGuard := If{
		Cond: NotIn{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
		Then: []Stmt{Return{}},
	}
	stmts = append(stmts,
		Comment{Text: "Regular subject type: gather candidates and filter with check_permission"},
		Comment{Text: "Guard: return empty if subject type is not allowed by the model"},
		typeGuard,
	)

	stmts = append(stmts, ReturnQuery{Query: regularPaginatedQuery})
	return stmts
}

// Pagination helpers are defined in sql.go:
// - wrapWithPagination(query, orderColumn string) string
// - wrapWithPaginationWildcardFirst(query string) string

// =============================================================================
// Dispatcher Rendering
// =============================================================================

// RenderListObjectsDispatcher renders the list_accessible_objects dispatcher function.
func RenderListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	return renderListDispatcher("list_accessible_objects", ListObjectsDispatcherArgs(), ListObjectsReturns(), cases), nil
}

// RenderListSubjectsDispatcher renders the list_accessible_subjects dispatcher function.
func RenderListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	return renderListDispatcher("list_accessible_subjects", ListSubjectsDispatcherArgs(), ListSubjectsReturns(), cases), nil
}

// renderListDispatcher renders a list dispatcher function with the given cases.
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

// =============================================================================
// Depth Exceeded Render Functions
// =============================================================================
//
// These render functions handle relations that exceed the userset depth limit.
// They generate simple functions that immediately raise M2002 without computation.

// RenderListObjectsDepthExceededFunction renders a list_objects function for a relation
// that exceeds the userset depth limit. The generated function raises M2002 immediately.
func RenderListObjectsDepthExceededFunction(plan ListPlan) string {
	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_objects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit", plan.Analysis.MaxUsersetDepth),
		},
		Body: []Stmt{
			Comment{Text: fmt.Sprintf("This relation has userset chain depth %d which exceeds the 25 level limit.", plan.Analysis.MaxUsersetDepth)},
			Comment{Text: "Raise M2002 immediately without any computation."},
			Raise{Message: "resolution too complex", ErrCode: "M2002"},
		},
	}
	return fn.SQL()
}

// RenderListSubjectsDepthExceededFunction renders a list_subjects function for a relation
// that exceeds the userset depth limit. The generated function raises M2002 immediately.
func RenderListSubjectsDepthExceededFunction(plan ListPlan) string {
	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_subjects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit", plan.Analysis.MaxUsersetDepth),
		},
		Body: []Stmt{
			Comment{Text: fmt.Sprintf("This relation has userset chain depth %d which exceeds the 25 level limit.", plan.Analysis.MaxUsersetDepth)},
			Comment{Text: "Raise M2002 immediately without any computation."},
			Raise{Message: "resolution too complex", ErrCode: "M2002"},
		},
	}
	return fn.SQL()
}

// =============================================================================
// Self-Referential Userset Render Functions
// =============================================================================
//
// These render functions handle self-referential userset patterns like
// [group#member] on group.member, which require recursive CTE expansion.

// RenderListObjectsSelfRefUsersetFunction renders a list_objects function for self-referential userset patterns.
func RenderListObjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetBlockSet) (string, error) {
	// Build CTE body from base blocks with depth wrapping
	cteBody := renderSelfRefUsersetCTEBody(blocks)

	// Build final exclusion predicates for the CTE result
	finalExclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "me", Column: "object_id"},
		SubjectType,
		SubjectID,
	)
	exclusionPreds := finalExclusions.BuildPredicates()

	var whereExpr Expr
	if len(exclusionPreds) > 0 {
		whereExpr = And(exclusionPreds...)
	}

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "me", Column: "object_id"}},
		FromExpr:    TableAs("member_expansion", "me"),
		Where:       whereExpr,
	}

	// Build the CTE SQL
	cteSQL := "WITH RECURSIVE member_expansion(object_id, depth) AS (\n" + cteBody + "\n)\n" + finalStmt.SQL()

	// Build self-candidate SQL
	var selfCandidateSQL string
	if blocks.SelfCandidateBlock != nil {
		selfCandidateSQL = renderTypedQueryBlock(*blocks.SelfCandidateBlock).SQL
	}

	// Combine CTE and self-candidate with UNION
	var query string
	if selfCandidateSQL != "" {
		query = joinUnionBlocksSQL([]string{cteSQL, selfCandidateSQL})
	} else {
		query = cteSQL
	}

	paginatedQuery := wrapWithPagination(query, "object_id")

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_objects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s (self-referential userset)", plan.FeaturesString()),
		},
		Body: []Stmt{
			ReturnQuery{Query: paginatedQuery},
		},
	}

	return fn.SQL(), nil
}

// renderSelfRefUsersetCTEBody renders the CTE body from base and recursive blocks.
func renderSelfRefUsersetCTEBody(blocks SelfRefUsersetBlockSet) string {
	// Render base blocks with depth wrapping
	var baseBlocksSQL []string
	for _, block := range blocks.BaseBlocks {
		qb := renderTypedQueryBlock(block)
		wrappedSQL := wrapQueryWithDepthForRender(qb.SQL, "0", "base")
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, wrappedSQL))
	}

	// Join base blocks with UNION
	cteBody := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Add recursive block with UNION ALL if present
	if blocks.RecursiveBlock != nil {
		qb := renderTypedQueryBlock(*blocks.RecursiveBlock)
		recursiveSQL := formatQueryBlockSQL(qb.Comments, qb.SQL)
		cteBody = cteBody + "\n    UNION ALL\n" + recursiveSQL
	}

	return cteBody
}

// RenderListSubjectsSelfRefUsersetFunction renders a list_subjects function for self-referential userset patterns.
func RenderListSubjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) (string, error) {
	// Build userset filter path query
	usersetFilterQuery := renderSelfRefUsersetFilterQuery(blocks)
	usersetFilterQuery = trimTrailingSemicolon(usersetFilterQuery)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)

	// Build regular path query
	regularQuery := renderSelfRefUsersetRegularQuery(plan, blocks)
	regularQuery = trimTrailingSemicolon(regularQuery)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the IF/ELSE body
	bodySQL := fmt.Sprintf(`-- Check if p_subject_type is a userset filter (contains '#')
IF position('#' in p_subject_type) > 0 THEN
    v_filter_type := split_part(p_subject_type, '#', 1);
    v_filter_relation := split_part(p_subject_type, '#', 2);

    -- Userset filter case: find userset tuples and recursively expand
    -- Returns normalized references like 'group:1#member'
    RETURN QUERY
    %s;
ELSE
    -- Regular subject type: find individual subjects via recursive userset expansion
    RETURN QUERY
    %s;
END IF;`, usersetFilterPaginatedQuery, regularPaginatedQuery)

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_subjects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s (self-referential userset)", plan.FeaturesString()),
		},
		Decls: []Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			RawStmt{SQLText: bodySQL},
		},
	}

	return fn.SQL(), nil
}

// renderSelfRefUsersetFilterQuery renders the userset filter path query with recursive CTE.
func renderSelfRefUsersetFilterQuery(blocks SelfRefUsersetSubjectsBlockSet) string {
	// Render base blocks for userset filter path
	var baseBlocksSQL []string
	for _, block := range blocks.UsersetFilterBlocks {
		qb := renderTypedQueryBlock(block)
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.SQL))
	}

	cteBody := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Add recursive block
	if blocks.UsersetFilterRecursiveBlock != nil {
		qb := renderTypedQueryBlock(*blocks.UsersetFilterRecursiveBlock)
		recursiveSQL := formatQueryBlockSQL(qb.Comments, qb.SQL)
		cteBody = cteBody + "\n    UNION ALL\n" + recursiveSQL
	}

	// Build main CTE query
	mainCTE := fmt.Sprintf(`WITH RECURSIVE userset_expansion(userset_object_id, depth) AS (
%s
)`, indentLines(cteBody, "        "))

	// Build result blocks
	var resultBlocks []string

	// Userset filter returns normalized references
	resultBlocks = append(resultBlocks, formatQueryBlockSQL(
		[]string{"-- Userset filter: return normalized userset references"},
		`SELECT DISTINCT ue.userset_object_id || '#' || v_filter_relation AS subject_id
FROM userset_expansion ue`,
	))

	// Add self-candidate block if present
	if blocks.UsersetFilterSelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock)
		resultBlocks = append(resultBlocks, formatQueryBlockSQL(qb.Comments, qb.SQL))
	}

	return mainCTE + "\n" + strings.Join(resultBlocks, "\nUNION\n")
}

// renderSelfRefUsersetRegularQuery renders the regular path query with userset_objects CTE.
func renderSelfRefUsersetRegularQuery(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) string {
	// Build userset_objects CTE
	var usersetObjectsCTE string
	if blocks.UsersetObjectsBaseBlock != nil {
		baseQB := renderTypedQueryBlock(*blocks.UsersetObjectsBaseBlock)
		baseSQL := formatQueryBlockSQL(baseQB.Comments, baseQB.SQL)

		usersetObjectsCTE = baseSQL
		if blocks.UsersetObjectsRecursiveBlock != nil {
			recursiveQB := renderTypedQueryBlock(*blocks.UsersetObjectsRecursiveBlock)
			recursiveSQL := formatQueryBlockSQL(recursiveQB.Comments, recursiveQB.SQL)
			usersetObjectsCTE = usersetObjectsCTE + "\n            UNION ALL\n" + recursiveSQL
		}
	}

	// Render regular blocks
	var baseBlocksSQL []string
	for _, block := range blocks.RegularBlocks {
		qb := renderTypedQueryBlock(block)
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.SQL))
	}

	baseResultsSQL := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Build full CTE with has_wildcard
	wildcardTailSQL := renderUsersetWildcardTail(plan.Analysis)

	return fmt.Sprintf(`WITH RECURSIVE
        userset_objects(userset_object_id, depth) AS (
%s
        ),
        base_results AS (
%s
        ),
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard
        )
%s`,
		indentLines(usersetObjectsCTE, "            "),
		indentLines(baseResultsSQL, "        "),
		wildcardTailSQL,
	)
}

// =============================================================================
// Composed Strategy Render Functions
// =============================================================================

// RenderListObjectsComposedFunction renders a list_objects function for composed access.
// Composed functions handle indirect anchor patterns (TTU and userset composition).
func RenderListObjectsComposedFunction(plan ListPlan, blocks ComposedObjectsBlockSet) (string, error) {
	// Render self-candidate query
	var selfSQL string
	if blocks.SelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.SelfBlock)
		selfSQL = qb.SQL
	}

	// Render main query blocks
	var mainBlocksSQL []string
	for _, block := range blocks.MainBlocks {
		qb := renderTypedQueryBlock(block)
		mainBlocksSQL = append(mainBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.SQL))
	}
	mainQuery := strings.Join(mainBlocksSQL, "\n    UNION\n")

	// Wrap with pagination
	selfPaginatedSQL := wrapWithPagination(selfSQL, "object_id")
	mainPaginatedSQL := wrapWithPagination(mainQuery, "object_id")

	// Build the body
	bodySQL := fmt.Sprintf(`-- Self-candidate check: when subject is a userset on the same object type
IF EXISTS (
%s
) THEN
    RETURN QUERY
    %s;
    RETURN;
END IF;

-- Type guard: only return results if subject type is allowed
-- Skip the guard for userset subjects since composed inner calls handle userset subjects
IF position('#' in p_subject_id) = 0 AND p_subject_type NOT IN (%s) THEN
    RETURN;
END IF;

RETURN QUERY
%s;`,
		indentLines(selfSQL, "    "),
		selfPaginatedSQL,
		formatSQLStringList(blocks.AllowedSubjectTypes),
		mainPaginatedSQL,
	)

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_objects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("Indirect anchor: %s.%s via %s", blocks.AnchorType, blocks.AnchorRelation, blocks.FirstStepType),
		},
		Body: []Stmt{
			RawStmt{SQLText: bodySQL},
		},
	}
	return fn.SQL(), nil
}

// RenderListSubjectsComposedFunction renders a list_subjects function for composed access.
func RenderListSubjectsComposedFunction(plan ListPlan, blocks ComposedSubjectsBlockSet) (string, error) {
	// Render self-candidate query
	var selfSQL string
	if blocks.SelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.SelfBlock)
		selfSQL = qb.SQL
	}

	// Render userset filter candidate blocks
	var usersetFilterBlocksSQL []string
	for _, block := range blocks.UsersetFilterBlocks {
		qb := renderTypedQueryBlock(block)
		usersetFilterBlocksSQL = append(usersetFilterBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.SQL))
	}
	usersetFilterCandidates := strings.Join(usersetFilterBlocksSQL, "\n    UNION\n")

	// Render regular candidate blocks
	var regularBlocksSQL []string
	for _, block := range blocks.RegularBlocks {
		qb := renderTypedQueryBlock(block)
		regularBlocksSQL = append(regularBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.SQL))
	}
	regularCandidates := strings.Join(regularBlocksSQL, "\n    UNION\n")

	// Build userset filter query
	usersetFilterQuery := fmt.Sprintf(`WITH subject_candidates AS (
%s
)
SELECT DISTINCT sc.subject_id
FROM subject_candidates sc
WHERE check_permission_internal(v_filter_type, sc.subject_id, '%s', '%s', p_object_id, ARRAY[]::TEXT[]) = 1`,
		indentLines(usersetFilterCandidates, "        "),
		plan.Relation,
		plan.ObjectType,
	)

	// Build regular query (with exclusions if needed)
	var regularQuery string
	if blocks.HasExclusions {
		exclusions := buildSimpleComplexExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})
		exclusionPreds := exclusions.BuildPredicates()
		whereClause := ""
		if len(exclusionPreds) > 0 {
			whereClause = "\nWHERE " + And(exclusionPreds...).SQL()
		}
		regularQuery = fmt.Sprintf(`WITH subject_candidates AS (
%s
)
SELECT DISTINCT sc.subject_id
FROM subject_candidates sc%s`,
			indentLines(regularCandidates, "        "),
			whereClause,
		)
	} else {
		regularQuery = fmt.Sprintf(`WITH subject_candidates AS (
%s
)
SELECT DISTINCT sc.subject_id
FROM subject_candidates sc`,
			indentLines(regularCandidates, "        "),
		)
	}

	// Wrap queries with pagination
	selfPaginatedSQL := wrapWithPaginationWildcardFirst(selfSQL)
	usersetFilterPaginatedSQL := wrapWithPaginationWildcardFirst(usersetFilterQuery)
	regularPaginatedSQL := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the body
	bodySQL := fmt.Sprintf(`v_is_userset_filter := position('#' in p_subject_type) > 0;
IF v_is_userset_filter THEN
    v_filter_type := split_part(p_subject_type, '#', 1);
    v_filter_relation := split_part(p_subject_type, '#', 2);

    -- Self-candidate: when filter type matches object type
    IF v_filter_type = '%s' THEN
        IF EXISTS (
%s
        ) THEN
            RETURN QUERY
            %s;
            RETURN;
        END IF;
    END IF;

    -- Userset filter case
    RETURN QUERY
    %s;
ELSE
    -- Direct subject type case
    IF p_subject_type NOT IN (%s) THEN
        RETURN;
    END IF;

    RETURN QUERY
    %s;
END IF;`,
		plan.ObjectType,
		indentLines(selfSQL, "            "),
		selfPaginatedSQL,
		usersetFilterPaginatedSQL,
		formatSQLStringList(blocks.AllowedSubjectTypes),
		regularPaginatedSQL,
	)

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_subjects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("Indirect anchor: %s.%s via %s", blocks.AnchorType, blocks.AnchorRelation, blocks.FirstStepType),
		},
		Decls: []Decl{
			{Name: "v_is_userset_filter", Type: "BOOLEAN"},
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			RawStmt{SQLText: bodySQL},
		},
	}
	return fn.SQL(), nil
}
