package sqlgen

import "strings"

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

// Pagination helpers are defined in list_builders.go:
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
