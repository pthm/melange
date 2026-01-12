package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// Composed Strategy Render Functions (List Subjects)
// =============================================================================
// RenderListSubjectsComposedFunction renders a list_subjects function for composed access.
func RenderListSubjectsComposedFunction(plan ListPlan, blocks ComposedSubjectsBlockSet) (string, error) {
	// Render self-candidate query
	var selfSQL string
	if blocks.SelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.SelfBlock)
		selfSQL = qb.Query.SQL()
	}

	// Render userset filter candidate blocks
	usersetFilterBlocksSQL := make([]string, 0, len(blocks.UsersetFilterBlocks))
	for _, block := range blocks.UsersetFilterBlocks {
		qb := renderTypedQueryBlock(block)
		usersetFilterBlocksSQL = append(usersetFilterBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}
	usersetFilterCandidates := strings.Join(usersetFilterBlocksSQL, "\n    UNION\n")

	// Render regular candidate blocks
	regularBlocksSQL := make([]string, 0, len(blocks.RegularBlocks))
	for _, block := range blocks.RegularBlocks {
		qb := renderTypedQueryBlock(block)
		regularBlocksSQL = append(regularBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
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
