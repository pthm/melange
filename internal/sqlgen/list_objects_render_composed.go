package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// Composed Strategy Render Functions (List Objects)
// =============================================================================
// RenderListObjectsComposedFunction renders a list_objects function for composed access.
// Composed functions handle indirect anchor patterns (TTU and userset composition).
func RenderListObjectsComposedFunction(plan ListPlan, blocks ComposedObjectsBlockSet) (string, error) {
	// Render self-candidate query
	var selfSQL string
	if blocks.SelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.SelfBlock)
		selfSQL = qb.Query.SQL()
	}

	// Render main query blocks
	mainBlocksSQL := make([]string, 0, len(blocks.MainBlocks))
	for _, block := range blocks.MainBlocks {
		qb := renderTypedQueryBlock(block)
		mainBlocksSQL = append(mainBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
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
