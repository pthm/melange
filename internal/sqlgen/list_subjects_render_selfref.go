package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// Self-Referential Userset Render Functions (List Subjects)
// =============================================================================
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
	baseBlocksSQL := make([]string, 0, len(blocks.UsersetFilterBlocks))
	for _, block := range blocks.UsersetFilterBlocks {
		qb := renderTypedQueryBlock(block)
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}

	cteBody := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Add recursive block
	if blocks.UsersetFilterRecursiveBlock != nil {
		qb := renderTypedQueryBlock(*blocks.UsersetFilterRecursiveBlock)
		recursiveSQL := formatQueryBlockSQL(qb.Comments, qb.Query.SQL())
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
		resultBlocks = append(resultBlocks, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}

	return mainCTE + "\n" + strings.Join(resultBlocks, "\nUNION\n")
}

// renderSelfRefUsersetRegularQuery renders the regular path query with userset_objects CTE.
func renderSelfRefUsersetRegularQuery(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) string {
	// Build userset_objects CTE
	var usersetObjectsCTE string
	if blocks.UsersetObjectsBaseBlock != nil {
		baseQB := renderTypedQueryBlock(*blocks.UsersetObjectsBaseBlock)
		baseSQL := formatQueryBlockSQL(baseQB.Comments, baseQB.Query.SQL())

		usersetObjectsCTE = baseSQL
		if blocks.UsersetObjectsRecursiveBlock != nil {
			recursiveQB := renderTypedQueryBlock(*blocks.UsersetObjectsRecursiveBlock)
			recursiveSQL := formatQueryBlockSQL(recursiveQB.Comments, recursiveQB.Query.SQL())
			usersetObjectsCTE = usersetObjectsCTE + "\n            UNION ALL\n" + recursiveSQL
		}
	}

	// Render regular blocks
	baseBlocksSQL := make([]string, 0, len(blocks.RegularBlocks))
	for _, block := range blocks.RegularBlocks {
		qb := renderTypedQueryBlock(block)
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
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
