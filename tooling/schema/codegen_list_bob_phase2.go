package schema

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/tooling/schema/sqlgen"
	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
)

func generateListObjectsRecursiveFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listObjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	allowWildcard := a.Features.HasWildcard
	complexClosure := filterComplexClosureRelations(a)
	parentRelations := buildListParentRelations(a)
	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	selfRefRelations := dequoteList(selfRefSQL)

	baseBlocks, err := buildListObjectsRecursiveBaseBlocks(a, inline, relationList, allowedSubjectTypes, allowWildcard, complexClosure)
	if err != nil {
		return "", err
	}

	var recursiveBlock string
	if len(selfRefRelations) > 0 {
		recursiveExclusions := buildExclusionInput(a, "child.object_id", "p_subject_type", "p_subject_id")
		recursiveSQL, err := sqlgen.ListObjectsRecursiveTTUQuery(sqlgen.ListObjectsRecursiveTTUInput{
			ObjectType:       a.ObjectType,
			LinkingRelations: selfRefRelations,
			Exclusions:       recursiveExclusions,
		})
		if err != nil {
			return "", err
		}
		recursiveBlock = formatQueryBlock(
			[]string{
				"-- Self-referential TTU: follow linking relations to accessible parents",
				"-- Combined all self-referential TTU patterns into single recursive term",
			},
			recursiveSQL,
		)
	}

	cteSQL, err := buildAccessibleObjectsCTE(a, baseBlocks, recursiveBlock)
	if err != nil {
		return "", err
	}

	selfCandidateSQL, err := sqlgen.ListObjectsSelfCandidateQuery(sqlgen.ListObjectsSelfCandidateInput{
		ObjectType:    a.ObjectType,
		Relation:      a.Relation,
		ClosureValues: inline.ClosureValues,
	})
	if err != nil {
		return "", err
	}

	query := joinUnionBlocks([]string{
		cteSQL,
		formatQueryBlock(
			[]string{
				"-- Self-candidate: when subject is a userset on the same object type",
				"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
				"-- The object 'document:1' should be considered as a candidate",
				"-- No type guard here - validity comes from the closure check below",
			},
			selfCandidateSQL,
		),
	})

	depthCheck := buildDepthCheckSQL(a.ObjectType, selfRefRelations)
	functionSQL := fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
DECLARE
    v_max_depth INTEGER;
BEGIN
%s
    IF v_max_depth >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    RETURN QUERY
%s;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		depthCheck,
		query,
	)

	return functionSQL, nil
}

func buildListObjectsRecursiveBaseBlocks(a RelationAnalysis, inline InlineSQLData, relationList, allowedSubjectTypes []string, allowWildcard bool, complexClosure []string) ([]string, error) {
	var blocks []string
	baseExclusions := buildExclusionInput(a, "t.object_id", "p_subject_type", "p_subject_id")

	directSQL, err := sqlgen.ListObjectsDirectQuery(sqlgen.ListObjectsDirectInput{
		ObjectType:          a.ObjectType,
		Relations:           relationList,
		AllowedSubjectTypes: allowedSubjectTypes,
		AllowWildcard:       allowWildcard,
		Exclusions:          baseExclusions,
	})
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, formatQueryBlock(
		[]string{
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		wrapQueryWithDepth(directSQL, "0", "direct_base"),
	))

	for _, rel := range complexClosure {
		complexSQL, err := sqlgen.ListObjectsComplexClosureQuery(sqlgen.ListObjectsComplexClosureInput{
			ObjectType:          a.ObjectType,
			Relation:            rel,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          baseExclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Complex closure relation: %s", rel),
			},
			wrapQueryWithDepth(complexSQL, "0", "complex_base"),
		))
	}

	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_objects", a.ObjectType, rel)
		closureSQL, err := sqlgen.ListObjectsIntersectionClosureQuery(functionName)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			wrapQueryWithDepth(closureSQL, "0", "intersection_base"),
		))
	}

	for _, pattern := range buildListUsersetPatternInputs(a) {
		if pattern.IsComplex {
			patternSQL, err := sqlgen.ListObjectsUsersetPatternComplexQuery(sqlgen.ListObjectsUsersetPatternComplexInput{
				ObjectType:       a.ObjectType,
				SubjectType:      pattern.SubjectType,
				SubjectRelation:  pattern.SubjectRelation,
				SourceRelations:  pattern.SourceRelations,
				IsClosurePattern: pattern.IsClosurePattern,
				SourceRelation:   pattern.SourceRelation,
				Exclusions:       baseExclusions,
			})
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
					"-- Complex userset: use check_permission_internal for membership",
				},
				wrapQueryWithDepth(patternSQL, "0", "userset_complex"),
			))
			continue
		}

		patternSQL, err := sqlgen.ListObjectsUsersetPatternSimpleQuery(sqlgen.ListObjectsUsersetPatternSimpleInput{
			ObjectType:          a.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       pattern.HasWildcard,
			IsClosurePattern:    pattern.IsClosurePattern,
			SourceRelation:      pattern.SourceRelation,
			Exclusions:          baseExclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
				"-- Simple userset: JOIN with membership tuples",
			},
			wrapQueryWithDepth(patternSQL, "0", "userset_simple"),
		))
	}

	for _, parent := range buildListParentRelations(a) {
		if !parent.HasCrossTypeLinks {
			continue
		}
		crossExclusions := buildExclusionInput(a, "child.object_id", "p_subject_type", "p_subject_id")
		crossSQL, err := sqlgen.ListObjectsCrossTypeTTUQuery(sqlgen.ListObjectsCrossTypeTTUInput{
			ObjectType:      a.ObjectType,
			LinkingRelation: parent.LinkingRelation,
			Relation:        parent.Relation,
			CrossTypes:      dequoteList(parent.CrossTypeLinkingTypes),
			Exclusions:      crossExclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Cross-type TTU: %s -> %s on non-self types", parent.LinkingRelation, parent.Relation),
				"-- Find objects whose linking relation points to a parent where subject has relation",
				"-- This is non-recursive (uses check_permission_internal, not CTE reference)",
			},
			wrapQueryWithDepth(crossSQL, "0", "cross_ttu"),
		))
	}

	return blocks, nil
}

func buildAccessibleObjectsCTE(a RelationAnalysis, baseBlocks []string, recursiveBlock string) (string, error) {
	cteBody := joinUnionBlocks(baseBlocks)
	if recursiveBlock != "" {
		cteBody = cteBody + "\n    UNION ALL\n" + recursiveBlock
	}

	finalExclusions := buildExclusionInput(a, "acc.object_id", "p_subject_type", "p_subject_id")
	exclusionPreds := sqlgen.ExclusionPredicatesDSL(finalExclusions)

	var whereExpr dsl.Expr
	if len(exclusionPreds) > 0 {
		// Prepend TRUE to ensure valid AND expression when there are exclusions
		allPreds := append([]dsl.Expr{dsl.Bool(true)}, exclusionPreds...)
		whereExpr = dsl.And(allPreds...)
	}

	finalStmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"acc.object_id"},
		From:     "accessible",
		Alias:    "acc",
		Where:    whereExpr,
	}
	finalSQL := finalStmt.SQL()

	return fmt.Sprintf(`WITH RECURSIVE accessible(object_id, depth) AS (
%s
)
%s`, cteBody, finalSQL), nil
}

func buildDepthCheckSQL(objectType string, linkingRelations []string) string {
	if len(linkingRelations) == 0 {
		return "    v_max_depth := 0;\n"
	}
	return fmt.Sprintf(`    -- Check for excessive recursion depth before running the query
    -- This matches check_permission behavior with M2002 error
    -- Only self-referential TTUs contribute to recursion depth (cross-type are one-hop)
    WITH RECURSIVE depth_check(object_id, depth) AS (
        -- Base case: seed with empty set (we just need depth tracking)
        SELECT NULL::TEXT, 0
        WHERE FALSE

        UNION ALL
        -- Track depth through all self-referential linking relations
        SELECT t.object_id, d.depth + 1
        FROM depth_check d
        JOIN melange_tuples t
          ON t.object_type = '%s'
          AND t.relation IN (%s)
          AND t.subject_type = '%s'
        WHERE d.depth < 26  -- Allow one extra to detect overflow
    )
    SELECT MAX(depth) INTO v_max_depth FROM depth_check;
`, objectType, formatSQLStringList(linkingRelations), objectType)
}

func wrapQueryWithDepth(sql string, depthExpr string, alias string) string {
	return fmt.Sprintf("SELECT DISTINCT %s.object_id, %s AS depth\nFROM (\n%s\n) AS %s", alias, depthExpr, sql, alias)
}

func dequoteList(sqlList string) []string {
	if strings.TrimSpace(sqlList) == "" {
		return nil
	}
	parts := strings.Split(sqlList, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(strings.Trim(part, "'"))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func generateListObjectsIntersectionFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listObjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	allowWildcard := a.Features.HasWildcard
	complexClosure := filterComplexClosureRelations(a)
	parentRelations := buildListParentRelations(a)
	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	selfRefRelations := dequoteList(selfRefSQL)
	hasStandalone := computeListHasStandaloneAccess(a)

	var blocks []string
	if hasStandalone {
		standaloneExclusions := buildSimpleComplexExclusionInput(a, "t.object_id", "p_subject_type", "p_subject_id")
		directSQL, err := sqlgen.ListObjectsDirectQuery(sqlgen.ListObjectsDirectInput{
			ObjectType:          a.ObjectType,
			Relations:           relationList,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          standaloneExclusions,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Direct/Implied standalone access via closure relations",
			},
			directSQL,
		))

		for _, rel := range complexClosure {
			complexSQL, err := sqlgen.ListObjectsComplexClosureQuery(sqlgen.ListObjectsComplexClosureInput{
				ObjectType:          a.ObjectType,
				Relation:            rel,
				AllowedSubjectTypes: allowedSubjectTypes,
				AllowWildcard:       allowWildcard,
				Exclusions:          standaloneExclusions,
			})
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Complex closure relation: %s", rel),
				},
				complexSQL,
			))
		}

		for _, rel := range a.IntersectionClosureRelations {
			functionName := fmt.Sprintf("list_%s_%s_objects", a.ObjectType, rel)
			closureSQL, err := sqlgen.ListObjectsIntersectionClosureQuery(functionName)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
				},
				closureSQL,
			))
		}

		for _, pattern := range buildListUsersetPatternInputs(a) {
			if pattern.IsComplex {
				patternSQL, err := sqlgen.ListObjectsUsersetPatternComplexQuery(sqlgen.ListObjectsUsersetPatternComplexInput{
					ObjectType:       a.ObjectType,
					SubjectType:      pattern.SubjectType,
					SubjectRelation:  pattern.SubjectRelation,
					SourceRelations:  pattern.SourceRelations,
					IsClosurePattern: pattern.IsClosurePattern,
					SourceRelation:   pattern.SourceRelation,
					Exclusions:       standaloneExclusions,
				})
				if err != nil {
					return "", err
				}
				blocks = append(blocks, formatQueryBlock(
					[]string{
						fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
					},
					patternSQL,
				))
				continue
			}

			patternSQL, err := sqlgen.ListObjectsUsersetPatternSimpleQuery(sqlgen.ListObjectsUsersetPatternSimpleInput{
				ObjectType:          a.ObjectType,
				SubjectType:         pattern.SubjectType,
				SubjectRelation:     pattern.SubjectRelation,
				SourceRelations:     pattern.SourceRelations,
				SatisfyingRelations: pattern.SatisfyingRelations,
				AllowedSubjectTypes: allowedSubjectTypes,
				AllowWildcard:       pattern.HasWildcard,
				IsClosurePattern:    pattern.IsClosurePattern,
				SourceRelation:      pattern.SourceRelation,
				Exclusions:          standaloneExclusions,
			})
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
				},
				patternSQL,
			))
		}

		for _, parent := range parentRelations {
			if !parent.HasCrossTypeLinks {
				continue
			}
			crossExclusions := buildSimpleComplexExclusionInput(a, "child.object_id", "p_subject_type", "p_subject_id")
			crossSQL, err := sqlgen.ListObjectsCrossTypeTTUQuery(sqlgen.ListObjectsCrossTypeTTUInput{
				ObjectType:      a.ObjectType,
				LinkingRelation: parent.LinkingRelation,
				Relation:        parent.Relation,
				CrossTypes:      dequoteList(parent.CrossTypeLinkingTypes),
				Exclusions:      crossExclusions,
			})
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Cross-type TTU: %s -> %s", parent.LinkingRelation, parent.Relation),
				},
				crossSQL,
			))
		}
	}

	for idx, group := range a.IntersectionGroups {
		groupSQL, err := buildObjectsIntersectionGroupSQL(a, idx, group, true)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, groupSQL)
	}

	var recursiveBlock string
	if len(selfRefRelations) > 0 {
		recursiveSQL, err := buildObjectsIntersectionRecursiveSQL(a, relationList, allowedSubjectTypes, selfRefRelations, hasStandalone)
		if err != nil {
			return "", err
		}
		recursiveBlock = recursiveSQL
	}

	selfCandidateSQL, err := sqlgen.ListObjectsSelfCandidateQuery(sqlgen.ListObjectsSelfCandidateInput{
		ObjectType:    a.ObjectType,
		Relation:      a.Relation,
		ClosureValues: inline.ClosureValues,
	})
	if err != nil {
		return "", err
	}

	query := joinUnionBlocks(blocks)
	if recursiveBlock != "" {
		query = query + "\n    UNION ALL\n" + recursiveBlock
	}
	query = query + "\n    UNION\n" + formatQueryBlock(
		[]string{
			"-- Self-candidate: when subject is a userset on the same object type",
		},
		selfCandidateSQL,
	)

	depthCheck := buildDepthCheckSQL(a.ObjectType, selfRefRelations)
	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
DECLARE
    v_max_depth INTEGER;
BEGIN
%s
    IF v_max_depth >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    RETURN QUERY
%s;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		depthCheck,
		query,
	), nil
}

func buildObjectsIntersectionGroupSQL(a RelationAnalysis, idx int, group IntersectionGroupInfo, applyExclusions bool) (string, error) {
	partQueries := make([]string, 0, len(group.Parts))
	for partIdx, part := range group.Parts {
		partSQL, err := buildObjectsIntersectionPartSQL(a, partIdx, part)
		if err != nil {
			return "", err
		}
		partQueries = append(partQueries, partSQL)
	}
	intersectSQL := strings.Join(partQueries, "\n        INTERSECT\n")
	groupSQL := fmt.Sprintf("    -- Intersection group %d\n    SELECT ig_%d.object_id FROM (\n%s\n    ) AS ig_%d",
		idx,
		idx,
		indentLines(intersectSQL, "        "),
		idx,
	)

	if !applyExclusions {
		return groupSQL, nil
	}

	exclusions := buildSimpleComplexExclusionInput(a, fmt.Sprintf("ig_%d.object_id", idx), "p_subject_type", "p_subject_id")
	exclusionPreds := sqlgen.ExclusionPredicatesDSL(exclusions)
	if len(exclusionPreds) == 0 {
		return groupSQL, nil
	}

	groupSQL = groupSQL + "\n    WHERE " + dsl.And(exclusionPreds...).SQL()
	return groupSQL, nil
}

func buildObjectsIntersectionPartSQL(a RelationAnalysis, partIdx int, part IntersectionPart) (string, error) {
	switch {
	case part.IsThis:
		q := dsl.Tuples("t").
			ObjectType(a.ObjectType).
			Relations(a.Relation).
			Select("t.object_id").
			WhereSubjectType(dsl.SubjectType).
			WhereSubjectID(dsl.SubjectID, part.HasWildcard).
			Distinct()

		if part.ExcludedRelation != "" {
			q = q.Where(sqlgen.CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ExcludedRelation, fmt.Sprintf("'%s'", a.ObjectType), "t.object_id", false))
		}
		return q.SQL(), nil
	case part.ParentRelation != nil:
		q := dsl.Tuples("child").
			ObjectType(a.ObjectType).
			Relations(part.ParentRelation.LinkingRelation).
			Select("child.object_id").
			Where(sqlgen.CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ParentRelation.Relation, "child.subject_type", "child.subject_id", true)).
			Distinct()

		if part.ExcludedRelation != "" {
			q = q.Where(sqlgen.CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ExcludedRelation, fmt.Sprintf("'%s'", a.ObjectType), "child.object_id", false))
		}
		return q.SQL(), nil
	default:
		q := dsl.Tuples("t").
			ObjectType(a.ObjectType).
			Select("t.object_id").
			Where(sqlgen.CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.Relation, fmt.Sprintf("'%s'", a.ObjectType), "t.object_id", true)).
			Distinct()

		if part.ExcludedRelation != "" {
			q = q.Where(sqlgen.CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ExcludedRelation, fmt.Sprintf("'%s'", a.ObjectType), "t.object_id", false))
		}
		return q.SQL(), nil
	}
}

func buildObjectsIntersectionRecursiveSQL(a RelationAnalysis, relationList, allowedSubjectTypes, selfRefRelations []string, hasStandalone bool) (string, error) {
	var seedBlocks []string
	if hasStandalone && len(relationList) > 0 {
		q := dsl.Tuples("t").
			ObjectType(a.ObjectType).
			Relations(relationList...).
			Select("t.object_id").
			WhereSubjectType(dsl.SubjectType).
			Where(dsl.In{Expr: dsl.SubjectType, Values: allowedSubjectTypes}).
			WhereSubjectID(dsl.SubjectID, a.Features.HasWildcard)
		seedBlocks = append(seedBlocks, q.SQL())
	}

	for idx, group := range a.IntersectionGroups {
		groupSQL, err := buildObjectsIntersectionGroupSQL(a, idx, group, false)
		if err != nil {
			return "", err
		}
		seedBlocks = append(seedBlocks, strings.ReplaceAll(groupSQL, "    ", ""))
	}

	seedSQL := strings.Join(seedBlocks, "\n            UNION\n")
	recursiveSQL := fmt.Sprintf(`    -- Self-referential TTU: recursive expansion from accessible parents
    -- Note: WITH RECURSIVE must be wrapped in a subquery when used after UNION
    SELECT rec.object_id FROM (
        WITH RECURSIVE accessible_rec(object_id, depth) AS (
        -- Seed: all objects from above queries
        SELECT DISTINCT seed.object_id, 0
        FROM (
%s
        ) AS seed

        UNION ALL

        SELECT DISTINCT child.object_id, a.depth + 1
        FROM accessible_rec a
        JOIN melange_tuples child
          ON child.object_type = '%s'
          AND child.relation IN (%s)
          AND child.subject_type = '%s'
          AND child.subject_id = a.object_id
        WHERE a.depth < 25
        )
        SELECT DISTINCT object_id FROM accessible_rec
    ) AS rec`,
		indentLines(seedSQL, "            "),
		a.ObjectType,
		formatSQLStringList(selfRefRelations),
		a.ObjectType,
	)

	return recursiveSQL, nil
}

func buildSimpleComplexExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr string) sqlgen.ExclusionInput {
	return sqlgen.ExclusionInput{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
	}
}

func generateListSubjectsRecursiveFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allSatisfyingRelations := buildAllSatisfyingRelationsList(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	excludeWildcard := !a.Features.HasWildcard
	complexClosure := filterComplexClosureRelations(a)

	usersetFilterBlocks, usersetSelfBlock, err := buildListSubjectsRecursiveUsersetFilterBlocks(a, inline, allSatisfyingRelations)
	if err != nil {
		return "", err
	}

	regularQuery, err := buildListSubjectsRecursiveRegularQuery(a, inline, relationList, complexClosure, allowedSubjectTypes, excludeWildcard)
	if err != nil {
		return "", err
	}

	regularQuery = trimTrailingSemicolon(regularQuery)
	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_filter_type TEXT;
    v_filter_relation TEXT;
BEGIN
    -- Check if p_subject_type is a userset filter (contains '#')
    IF position('#' in p_subject_type) > 0 THEN
        -- Parse userset filter
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);

        -- Userset filter: find userset tuples that match and return normalized references
        RETURN QUERY
%s;
    ELSE
        -- Regular subject type: find direct subjects and expand usersets
        RETURN QUERY
%s;
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		joinUnionBlocks(append(usersetFilterBlocks, usersetSelfBlock)),
		regularQuery,
	), nil
}

func buildListSubjectsRecursiveUsersetFilterBlocks(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string) ([]string, string, error) {
	var blocks []string
	checkExprSQL := sqlgen.CheckPermissionExprDSL("check_permission", "v_filter_type", "t.subject_id", a.Relation, fmt.Sprintf("'%s'", a.ObjectType), "p_object_id", true).SQL()
	baseSQL, err := sqlgen.ListSubjectsUsersetFilterQuery(sqlgen.ListSubjectsUsersetFilterInput{
		ObjectType:          a.ObjectType,
		RelationList:        allSatisfyingRelations,
		ObjectIDExpr:        "p_object_id",
		FilterTypeExpr:      "v_filter_type",
		FilterRelationExpr:  "v_filter_relation",
		ClosureValues:       inline.ClosureValues,
		UseTypeGuard:        false,
		ExtraPredicatesSQL:  []string{checkExprSQL},
	})
	if err != nil {
		return nil, "", err
	}
	blocks = append(blocks, formatQueryBlock(
		[]string{
			"-- Direct userset tuples on this object",
		},
		baseSQL,
	))

	for _, parent := range buildListParentRelations(a) {
		ttuSQL, err := buildUsersetFilterTTUQuery(a, inline, parent)
		if err != nil {
			return nil, "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- TTU path: userset subjects via %s -> %s", parent.LinkingRelation, parent.Relation),
			},
			ttuSQL,
		))

		intermediateSQL, err := buildUsersetFilterTTUIntermediateQuery(a, inline, parent)
		if err != nil {
			return nil, "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- TTU intermediate object: return the parent object itself as a userset reference",
			},
			intermediateSQL,
		))

		nestedSQL, err := buildUsersetFilterTTUNestedQuery(a.ObjectType, parent)
		if err != nil {
			return nil, "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- TTU nested intermediate objects: recursively resolve multi-hop TTU chains",
			},
			nestedSQL,
		))
	}

	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		closureSQL, err := sqlgen.ListSubjectsIntersectionClosureQuery(functionName, "v_filter_type || '#' || v_filter_relation")
		if err != nil {
			return nil, "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			closureSQL,
		))
	}

	selfSQL, err := sqlgen.ListSubjectsSelfCandidateQuery(sqlgen.ListSubjectsSelfCandidateInput{
		ObjectType:         a.ObjectType,
		Relation:           a.Relation,
		ObjectIDExpr:       "p_object_id",
		FilterTypeExpr:     "v_filter_type",
		FilterRelationExpr: "v_filter_relation",
		ClosureValues:      inline.ClosureValues,
	})
	if err != nil {
		return nil, "", err
	}

	return blocks, formatQueryBlock(
		[]string{
			"-- Self-referential userset",
		},
		selfSQL,
	), nil
}

func buildListSubjectsRecursiveRegularQuery(a RelationAnalysis, inline InlineSQLData, relationList, complexClosure, allowedSubjectTypes []string, excludeWildcard bool) (string, error) {
	baseExclusions := buildExclusionInput(a, "p_object_id", "p_subject_type", "t.subject_id")

	subjectPoolSQL, err := buildSubjectPoolSQL(allowedSubjectTypes, excludeWildcard)
	if err != nil {
		return "", err
	}

	var baseBlocks []string
	directSQL, err := sqlgen.ListSubjectsDirectQuery(sqlgen.ListSubjectsDirectInput{
		ObjectType:      a.ObjectType,
		RelationList:    relationList,
		ObjectIDExpr:    "p_object_id",
		SubjectTypeExpr: "p_subject_type",
		ExcludeWildcard: excludeWildcard,
		Exclusions:      baseExclusions,
	})
	if err != nil {
		return "", err
	}
	baseBlocks = append(baseBlocks, formatQueryBlock(
		[]string{
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		directSQL,
	))

	for _, rel := range complexClosure {
		complexSQL, err := sqlgen.ListSubjectsComplexClosureQuery(sqlgen.ListSubjectsComplexClosureInput{
			ObjectType:      a.ObjectType,
			Relation:        rel,
			ObjectIDExpr:    "p_object_id",
			SubjectTypeExpr: "p_subject_type",
			ExcludeWildcard: excludeWildcard,
			Exclusions:      baseExclusions,
		})
		if err != nil {
			return "", err
		}
		baseBlocks = append(baseBlocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Complex closure relation: %s", rel),
			},
			complexSQL,
		))
	}

	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		closureSQL, err := sqlgen.ListSubjectsIntersectionClosureQuery(functionName, "p_subject_type")
		if err != nil {
			return "", err
		}
		baseBlocks = append(baseBlocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			closureSQL,
		))
	}

	for _, pattern := range buildListUsersetPatternInputs(a) {
		usersetExclusions := buildExclusionInput(a, "p_object_id", "m.subject_type", "m.subject_id")
		simpleUsersetExclusions := buildExclusionInput(a, "p_object_id", "s.subject_type", "s.subject_id")
		if pattern.IsComplex {
			patternSQL, err := sqlgen.ListSubjectsUsersetPatternRecursiveComplexQuery(sqlgen.ListSubjectsUsersetPatternRecursiveComplexInput{
				ObjectType:          a.ObjectType,
				SubjectType:         pattern.SubjectType,
				SubjectRelation:     pattern.SubjectRelation,
				SourceRelations:     pattern.SourceRelations,
				ObjectIDExpr:        "p_object_id",
				SubjectTypeExpr:     "p_subject_type",
				AllowedSubjectTypes: allowedSubjectTypes,
				ExcludeWildcard:     excludeWildcard,
				IsClosurePattern:    pattern.IsClosurePattern,
				SourceRelation:      pattern.SourceRelation,
				Exclusions:          usersetExclusions,
			})
			if err != nil {
				return "", err
			}
			baseBlocks = append(baseBlocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Userset path: Via %s#%s", pattern.SubjectType, pattern.SubjectRelation),
				},
				patternSQL,
			))
			continue
		}

		patternSQL, err := sqlgen.ListSubjectsUsersetPatternSimpleQuery(sqlgen.ListSubjectsUsersetPatternSimpleInput{
			ObjectType:          a.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			ObjectIDExpr:        "p_object_id",
			SubjectTypeExpr:     "p_subject_type",
			AllowedSubjectTypes: allowedSubjectTypes,
			ExcludeWildcard:     excludeWildcard,
			IsClosurePattern:    pattern.IsClosurePattern,
			SourceRelation:      pattern.SourceRelation,
			Exclusions:          simpleUsersetExclusions,
		})
		if err != nil {
			return "", err
		}
		baseBlocks = append(baseBlocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Userset path: Via %s#%s", pattern.SubjectType, pattern.SubjectRelation),
			},
			patternSQL,
		))
	}

	for _, parent := range buildListParentRelations(a) {
		ttuSQL, err := buildSubjectsTTUPathQuery(a, parent)
		if err != nil {
			return "", err
		}
		baseBlocks = append(baseBlocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- TTU path: subjects via %s -> %s", parent.LinkingRelation, parent.Relation),
			},
			ttuSQL,
		))
	}

	baseResultsSQL := indentLines(joinUnionBlocks(baseBlocks), "        ")
	return fmt.Sprintf(`WITH subject_pool AS (
%s
        ),
        base_results AS (
%s
        ),
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard
        )
%s`, indentLines(subjectPoolSQL, "        "), baseResultsSQL, renderUsersetWildcardTail(a)), nil
}

func buildSubjectPoolSQL(allowedSubjectTypes []string, excludeWildcard bool) (string, error) {
	q := dsl.Tuples("t").
		Select("t.subject_id").
		WhereSubjectType(dsl.SubjectType).
		Where(dsl.In{Expr: dsl.SubjectType, Values: allowedSubjectTypes}).
		Distinct()

	if excludeWildcard {
		q = q.Where(dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")})
	}
	return q.SQL(), nil
}

func buildSubjectsTTUPathQuery(a RelationAnalysis, parent ListParentRelationData) (string, error) {
	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(parent.LinkingRelation)},
		sqlgen.CheckPermissionInternalExprDSL("p_subject_type", "sp.subject_id", parent.Relation, "link.subject_type", "link.subject_id", true),
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, dsl.Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	exclusions := buildExclusionInput(a, "p_object_id", "p_subject_type", "sp.subject_id")
	exclusionPreds := sqlgen.ExclusionPredicatesDSL(exclusions)
	conditions = append(conditions, exclusionPreds...)

	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"sp.subject_id"},
		From:     "subject_pool",
		Alias:    "sp",
		Joins: []dsl.JoinClause{{
			Type:  "CROSS",
			Table: "melange_tuples",
			Alias: "link",
			On:    dsl.Bool(true), // CROSS JOIN has ON TRUE (always matches)
		}},
		Where: dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterTTUQuery(a RelationAnalysis, inline InlineSQLData, parent ListParentRelationData) (string, error) {
	closureRelStmt := dsl.SelectStmt{
		Columns: []string{"c.satisfying_relation"},
		From:    fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", inline.ClosureValues),
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.Lit(parent.Relation)},
		),
	}
	closureRelSQL := closureRelStmt.SQL()

	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", inline.ClosureValues),
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "object_type"}, Right: dsl.Raw("v_filter_type")},
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "relation"}, Right: dsl.Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)")},
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "satisfying_relation"}, Right: dsl.Raw("v_filter_relation")},
		),
	}

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(parent.LinkingRelation)},
		dsl.Eq{Left: dsl.Col{Table: "pt", Column: "subject_type"}, Right: dsl.Raw("v_filter_type")},
		dsl.Gt{Left: dsl.Raw("position('#' in pt.subject_id)"), Right: dsl.Int(0)},
		dsl.Or(
			dsl.Eq{Left: dsl.Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)"), Right: dsl.Raw("v_filter_relation")},
			dsl.Exists{Query: closureExistsStmt},
		),
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, dsl.Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []dsl.JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: dsl.And(
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_id"}, Right: dsl.Raw("link.subject_id")},
				dsl.Raw("pt.relation IN ("+closureRelSQL+")"),
			),
		}},
		Where: dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterTTUIntermediateQuery(a RelationAnalysis, inline InlineSQLData, parent ListParentRelationData) (string, error) {
	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", inline.ClosureValues),
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "relation"}, Right: dsl.Lit(parent.Relation)},
			dsl.Eq{Left: dsl.Col{Table: "c", Column: "satisfying_relation"}, Right: dsl.Raw("v_filter_relation")},
		),
	}

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(parent.LinkingRelation)},
		dsl.Eq{Left: dsl.Raw("link.subject_type"), Right: dsl.Raw("v_filter_type")},
		dsl.Exists{Query: closureExistsStmt},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, dsl.Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"link.subject_id || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Where:    dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterTTUNestedQuery(objectType string, parent ListParentRelationData) (string, error) {
	lateralCall := fmt.Sprintf("LATERAL list_accessible_subjects(link.subject_type, link.subject_id, '%s', p_subject_type)", parent.Relation)

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(objectType)},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(parent.LinkingRelation)},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, dsl.Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := dsl.SelectStmt{
		Columns: []string{"nested.subject_id"},
		From:    "melange_tuples",
		Alias:   "link",
		Joins: []dsl.JoinClause{{
			Type:  "CROSS",
			Table: lateralCall,
			Alias: "nested",
		}},
		Where: dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func generateListSubjectsIntersectionFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	allSatisfyingRelations := buildAllSatisfyingRelationsList(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	excludeWildcard := !a.Features.HasWildcard

	usersetCandidatesSQL, err := buildUsersetIntersectionCandidates(a, inline, allSatisfyingRelations)
	if err != nil {
		return "", err
	}
	usersetSelfSQL, err := sqlgen.ListSubjectsSelfCandidateQuery(sqlgen.ListSubjectsSelfCandidateInput{
		ObjectType:         a.ObjectType,
		Relation:           a.Relation,
		ObjectIDExpr:       "p_object_id",
		FilterTypeExpr:     "v_filter_type",
		FilterRelationExpr: "v_filter_relation",
		ClosureValues:      inline.ClosureValues,
	})
	if err != nil {
		return "", err
	}

	regularCandidatesSQL, err := buildRegularIntersectionCandidates(a, inline, allSatisfyingRelations, excludeWildcard)
	if err != nil {
		return "", err
	}
	regularQuery := fmt.Sprintf(`WITH subject_candidates AS (
%s
        ),
        filtered_candidates AS (
            SELECT DISTINCT c.subject_id
            FROM subject_candidates c
            WHERE check_permission(p_subject_type, c.subject_id, '%s', '%s', p_object_id) = 1
        )%s`,
		indentLines(regularCandidatesSQL, "        "),
		a.Relation,
		a.ObjectType,
		renderIntersectionWildcardTail(a),
	)

	regularQuery = trimTrailingSemicolon(regularQuery)
	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_filter_type TEXT;
    v_filter_relation TEXT;
BEGIN
    -- Check if p_subject_type is a userset filter (contains '#')
    IF position('#' in p_subject_type) > 0 THEN
        -- Parse userset filter
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);

        -- Userset filter: find userset tuples and filter with check_permission
        RETURN QUERY
        WITH userset_candidates AS (
%s
        )
        SELECT DISTINCT c.subject_id
        FROM userset_candidates c
        WHERE check_permission(v_filter_type, c.subject_id, '%s', '%s', p_object_id) = 1

        UNION

%s;
    ELSE
        -- Regular subject type: gather candidates and filter with check_permission
        -- Guard: return empty if subject type is not allowed by the model
        IF p_subject_type NOT IN (%s) THEN
            RETURN;
        END IF;

        RETURN QUERY
%s;
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		usersetCandidatesSQL,
		a.Relation,
		a.ObjectType,
		formatQueryBlock(
			[]string{"-- Self-referential userset"},
			usersetSelfSQL,
		),
		formatSQLStringList(allowedSubjectTypes),
		regularQuery,
	), nil
}

func buildUsersetIntersectionCandidates(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string) (string, error) {
	var blocks []string
	baseSQL, err := sqlgen.ListSubjectsUsersetFilterQuery(sqlgen.ListSubjectsUsersetFilterInput{
		ObjectType:         a.ObjectType,
		RelationList:       allSatisfyingRelations,
		ObjectIDExpr:       "p_object_id",
		FilterTypeExpr:     "v_filter_type",
		FilterRelationExpr: "v_filter_relation",
		ClosureValues:      inline.ClosureValues,
		UseTypeGuard:       false,
	})
	if err != nil {
		return "", err
	}
	blocks = append(blocks, baseSQL)

	for _, group := range a.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			partSQL, err := buildUsersetIntersectionPartCandidates(a, inline, part)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, partSQL)
		}
	}

	for _, parent := range buildListParentRelations(a) {
		ttuSQL, err := buildUsersetIntersectionTTUCandidates(a, inline, parent)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, ttuSQL)
	}

	return strings.Join(blocks, "\n        UNION\n"), nil
}

func buildUsersetIntersectionPartCandidates(a RelationAnalysis, inline InlineSQLData, part IntersectionPart) (string, error) {
	if part.ParentRelation != nil {
		relationMatch := buildUsersetFilterRelationMatchExprDSL("pt.subject_id", inline.ClosureValues)
		conditions := []dsl.Expr{
			dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
			dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
			dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(part.ParentRelation.LinkingRelation)},
			dsl.Eq{Left: dsl.Col{Table: "pt", Column: "subject_type"}, Right: dsl.Raw("v_filter_type")},
			dsl.Gt{Left: dsl.Raw("position('#' in pt.subject_id)"), Right: dsl.Int(0)},
			relationMatch,
		}
		stmt := dsl.SelectStmt{
			Distinct: true,
			Columns:  []string{"substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
			From:     "melange_tuples",
			Alias:    "link",
			Joins: []dsl.JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "pt",
				On: dsl.And(
					dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
					dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_id"}, Right: dsl.Raw("link.subject_id")},
				),
			}},
			Where: dsl.And(conditions...),
		}
		return stmt.SQL(), nil
	}

	relationMatch := buildUsersetFilterRelationMatchExprDSL("t.subject_id", inline.ClosureValues)
	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "relation"}, Right: dsl.Lit(part.Relation)},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.Raw("v_filter_type")},
		dsl.Gt{Left: dsl.Raw("position('#' in t.subject_id)"), Right: dsl.Int(0)},
		relationMatch,
	}
	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetIntersectionTTUCandidates(a RelationAnalysis, inline InlineSQLData, parent ListParentRelationData) (string, error) {
	relationMatch := buildUsersetFilterRelationMatchExprDSL("pt.subject_id", inline.ClosureValues)
	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(parent.LinkingRelation)},
		dsl.Eq{Left: dsl.Col{Table: "pt", Column: "subject_type"}, Right: dsl.Raw("v_filter_type")},
		dsl.Gt{Left: dsl.Raw("position('#' in pt.subject_id)"), Right: dsl.Int(0)},
		relationMatch,
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, dsl.Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []dsl.JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: dsl.And(
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_id"}, Right: dsl.Raw("link.subject_id")},
			),
		}},
		Where: dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterRelationMatchExprDSL(subjectIDExpr, closureValues string) dsl.Expr {
	closureExistsStmt := dsl.SelectStmt{
		Columns: []string{"1"},
		From:    fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", closureValues),
		Where: dsl.And(
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "object_type"}, Right: dsl.Raw("v_filter_type")},
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "relation"}, Right: dsl.Raw("substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)")},
			dsl.Eq{Left: dsl.Col{Table: "subj_c", Column: "satisfying_relation"}, Right: dsl.Raw("v_filter_relation")},
		),
	}
	return dsl.Or(
		dsl.Eq{Left: dsl.Raw("substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)"), Right: dsl.Raw("v_filter_relation")},
		dsl.Exists{Query: closureExistsStmt},
	)
}

func buildRegularIntersectionCandidates(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string, excludeWildcard bool) (string, error) {
	var blocks []string

	// Base query
	baseConditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.In{Expr: dsl.Col{Table: "t", Column: "relation"}, Values: allSatisfyingRelations},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
	}
	if excludeWildcard {
		baseConditions = append(baseConditions, dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")})
	}
	baseStmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    dsl.And(baseConditions...),
	}
	blocks = append(blocks, baseStmt.SQL())

	for _, group := range a.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			partSQL, err := buildRegularIntersectionPartCandidates(a, part, excludeWildcard)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, partSQL)
		}
	}

	for _, pattern := range buildListUsersetPatternInputs(a) {
		patternSQL, err := sqlgen.ListSubjectsUsersetPatternSimpleQuery(sqlgen.ListSubjectsUsersetPatternSimpleInput{
			ObjectType:          a.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			ObjectIDExpr:        "p_object_id",
			SubjectTypeExpr:     "p_subject_type",
			AllowedSubjectTypes: buildAllowedSubjectTypesList(a),
			ExcludeWildcard:     excludeWildcard,
			IsClosurePattern:    pattern.IsClosurePattern,
			SourceRelation:      pattern.SourceRelation,
			Exclusions:          sqlgen.ExclusionInput{},
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, patternSQL)
	}

	for _, parent := range buildListParentRelations(a) {
		ttuSQL, err := buildRegularIntersectionTTUCandidates(a, parent, excludeWildcard)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, ttuSQL)
	}

	// Pool query - subject pool for intersection filtering
	poolConditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
	}
	if excludeWildcard {
		poolConditions = append(poolConditions, dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")})
	}
	poolStmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    dsl.And(poolConditions...),
	}
	blocks = append(blocks, poolStmt.SQL())

	return strings.Join(blocks, "\n            UNION\n"), nil
}

func buildRegularIntersectionPartCandidates(a RelationAnalysis, part IntersectionPart, excludeWildcard bool) (string, error) {
	if part.ParentRelation != nil {
		conditions := []dsl.Expr{
			dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
			dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
			dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(part.ParentRelation.LinkingRelation)},
			dsl.Eq{Left: dsl.Col{Table: "pt", Column: "subject_type"}, Right: dsl.SubjectType},
		}
		if excludeWildcard {
			conditions = append(conditions, dsl.Ne{Left: dsl.Col{Table: "pt", Column: "subject_id"}, Right: dsl.Lit("*")})
		}
		stmt := dsl.SelectStmt{
			Distinct: true,
			Columns:  []string{"pt.subject_id"},
			From:     "melange_tuples",
			Alias:    "link",
			Joins: []dsl.JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "pt",
				On: dsl.And(
					dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
					dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_id"}, Right: dsl.Raw("link.subject_id")},
				),
			}},
			Where: dsl.And(conditions...),
		}
		return stmt.SQL(), nil
	}

	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "relation"}, Right: dsl.Lit(part.Relation)},
		dsl.Eq{Left: dsl.Col{Table: "t", Column: "subject_type"}, Right: dsl.SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, dsl.Ne{Left: dsl.Col{Table: "t", Column: "subject_id"}, Right: dsl.Lit("*")})
	}
	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildRegularIntersectionTTUCandidates(a RelationAnalysis, parent ListParentRelationData, excludeWildcard bool) (string, error) {
	conditions := []dsl.Expr{
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_type"}, Right: dsl.Lit(a.ObjectType)},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "object_id"}, Right: dsl.ObjectID},
		dsl.Eq{Left: dsl.Col{Table: "link", Column: "relation"}, Right: dsl.Lit(parent.LinkingRelation)},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, dsl.Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	if excludeWildcard {
		conditions = append(conditions, dsl.Ne{Left: dsl.Col{Table: "pt", Column: "subject_id"}, Right: dsl.Lit("*")})
	}
	stmt := dsl.SelectStmt{
		Distinct: true,
		Columns:  []string{"pt.subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []dsl.JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: dsl.And(
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_type"}, Right: dsl.Raw("link.subject_type")},
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "object_id"}, Right: dsl.Raw("link.subject_id")},
				dsl.Eq{Left: dsl.Col{Table: "pt", Column: "subject_type"}, Right: dsl.SubjectType},
			),
		}},
		Where: dsl.And(conditions...),
	}
	return stmt.SQL(), nil
}

func renderIntersectionWildcardTail(a RelationAnalysis) string {
	if !a.Features.HasWildcard {
		return "\n        SELECT fc.subject_id FROM filtered_candidates fc;"
	}
	return fmt.Sprintf(`,
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM filtered_candidates fc WHERE fc.subject_id = '*') AS has_wildcard
        )
        SELECT fc.subject_id
        FROM filtered_candidates fc
        CROSS JOIN has_wildcard hw
        WHERE (NOT hw.has_wildcard)
           OR (fc.subject_id = '*')
           OR (
               fc.subject_id != '*'
               AND check_permission_no_wildcard(
                   p_subject_type,
                   fc.subject_id,
                   '%s',
                   '%s',
                   p_object_id
               ) = 1
           );`, a.Relation, a.ObjectType)
}

func trimTrailingSemicolon(input string) string {
	trimmed := strings.TrimSpace(input)
	if strings.HasSuffix(trimmed, ";") {
		trimmed = strings.TrimSuffix(trimmed, ";")
	}
	return trimmed
}

