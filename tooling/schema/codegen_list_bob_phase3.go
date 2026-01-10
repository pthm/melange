package schema

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/tooling/schema/sqlgen"
	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

func generateListObjectsDepthExceededFunctionBob(a RelationAnalysis) string {
	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
-- DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    -- This relation has userset chain depth %d which exceeds the 25 level limit.
    -- Raise M2002 immediately without any computation.
    RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		a.MaxUsersetDepth,
		listObjectsFunctionName(a.ObjectType, a.Relation),
		a.MaxUsersetDepth,
	)
}

func generateListSubjectsDepthExceededFunctionBob(a RelationAnalysis) string {
	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
-- DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
BEGIN
    -- This relation has userset chain depth %d which exceeds the 25 level limit.
    -- Raise M2002 immediately without any computation.
    RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		a.MaxUsersetDepth,
		listSubjectsFunctionName(a.ObjectType, a.Relation),
		a.MaxUsersetDepth,
	)
}

func generateListObjectsSelfRefUsersetFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listObjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	allowWildcard := a.Features.HasWildcard
	complexClosure := filterComplexClosureRelations(a)

	baseBlocks, err := buildListObjectsSelfRefBaseBlocks(a, relationList, allowedSubjectTypes, allowWildcard, complexClosure)
	if err != nil {
		return "", err
	}

	recursiveSQL, err := buildListObjectsSelfRefRecursiveBlock(a, relationList)
	if err != nil {
		return "", err
	}

	recursiveBlock := formatQueryBlock(
		[]string{
			"-- Self-referential userset expansion",
			"-- For patterns like [group#member] on group.member",
		},
		recursiveSQL,
	)

	cteBody := joinUnionBlocks(baseBlocks)
	cteBody = cteBody + "\n    UNION ALL\n" + recursiveBlock

	finalSQL, err := buildListObjectsSelfRefFinalQuery(a)
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

	query := fmt.Sprintf(`WITH RECURSIVE member_expansion(object_id, depth) AS (
%s
)
%s
UNION
%s`, cteBody, finalSQL, selfCandidateSQL)

	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s (self-referential userset)
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
%s;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		indentLines(query, "    "),
	), nil
}

func buildListObjectsSelfRefBaseBlocks(a RelationAnalysis, relationList, allowedSubjectTypes []string, allowWildcard bool, complexClosure []string) ([]string, error) {
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
			AllowWildcard:       true,
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
		if pattern.IsSelfReferential {
			continue
		}
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

	return blocks, nil
}

func buildListObjectsSelfRefRecursiveBlock(a RelationAnalysis, relationList []string) (string, error) {
	baseExclusions := buildExclusionInput(a, "t.object_id", "p_subject_type", "p_subject_id")
	exclusionPreds := exclusionPredicates(baseExclusions)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Raw("t.relation IN (" + formatSQLStringList(relationList) + ")"),
		psql.Quote("t", "subject_type").EQ(psql.S(a.ObjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(a.Relation)),
		psql.Raw("me.depth < 25"),
	}
	where = append(where, exclusionPreds...)

	query := psql.Select(
		sm.Columns(
			psql.Raw("t.object_id"),
			psql.Raw("me.depth + 1 AS depth"),
		),
		sm.From("member_expansion").As("me"),
		sm.InnerJoin("melange_tuples").As("t").On(
			psql.Raw("split_part(t.subject_id, '#', 1)").EQ(psql.Quote("me", "object_id")),
		),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildListObjectsSelfRefFinalQuery(a RelationAnalysis) (string, error) {
	finalExclusions := buildExclusionInput(a, "me.object_id", "p_subject_type", "p_subject_id")
	exclusionPreds := exclusionPredicates(finalExclusions)

	query := psql.Select(
		sm.Columns(psql.Raw("me.object_id")),
		sm.From("member_expansion").As("me"),
		sm.Distinct(),
	)
	if len(exclusionPreds) > 0 {
		query = psql.Select(
			sm.Columns(psql.Raw("me.object_id")),
			sm.From("member_expansion").As("me"),
			sm.Where(psql.And(exclusionPreds...)),
			sm.Distinct(),
		)
	}
	return sqlgen.RenderQuery(query)
}

func generateListSubjectsSelfRefUsersetFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allSatisfyingRelations := buildAllSatisfyingRelationsList(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	excludeWildcard := !a.Features.HasWildcard
	complexClosure := filterComplexClosureRelations(a)

	usersetFilterQuery, err := buildListSubjectsSelfRefUsersetFilterQuery(a, inline, allSatisfyingRelations)
	if err != nil {
		return "", err
	}

	regularQuery, err := buildListSubjectsSelfRefRegularQuery(a, inline, relationList, complexClosure, allowedSubjectTypes, excludeWildcard)
	if err != nil {
		return "", err
	}
	usersetFilterQuery = trimTrailingSemicolon(usersetFilterQuery)
	regularQuery = trimTrailingSemicolon(regularQuery)

	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s (self-referential userset)
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
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		indentLines(usersetFilterQuery, "        "),
		indentLines(regularQuery, "        "),
	), nil
}

func buildListSubjectsSelfRefUsersetFilterQuery(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string) (string, error) {
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
		return "", err
	}

	baseWrapped := fmt.Sprintf(`SELECT DISTINCT split_part(u.subject_id, '#', 1) AS userset_object_id, 0 AS depth
FROM (
%s
) AS u`, baseSQL)

	recursiveSQL, err := buildListSubjectsSelfRefUsersetRecursiveQuery()
	if err != nil {
		return "", err
	}

	cte := fmt.Sprintf(`WITH RECURSIVE userset_expansion(userset_object_id, depth) AS (
%s
    UNION ALL
%s
)`, indentLines(baseWrapped, "        "), indentLines(recursiveSQL, "        "))

	var blocks []string
	blocks = append(blocks, formatQueryBlock(
		[]string{
			"-- Userset filter: return normalized userset references",
		},
		`SELECT DISTINCT ue.userset_object_id || '#' || v_filter_relation AS subject_id
FROM userset_expansion ue`,
	))

	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		closureSQL, err := sqlgen.ListSubjectsIntersectionClosureQuery(functionName, "v_filter_type || '#' || v_filter_relation")
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

	selfSQL, err := sqlgen.ListSubjectsSelfCandidateQuery(sqlgen.ListSubjectsSelfCandidateInput{
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
	blocks = append(blocks, formatQueryBlock(
		[]string{
			"-- Self-referential: when filter type matches object type",
		},
		selfSQL,
	))

	return fmt.Sprintf(`%s
%s`, cte, joinUnionBlocks(blocks)), nil
}

func buildListSubjectsSelfRefUsersetRecursiveQuery() (string, error) {
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.Raw("v_filter_type")),
		psql.Quote("t", "object_id").EQ(psql.Quote("ue", "userset_object_id")),
		psql.Quote("t", "relation").EQ(psql.Raw("v_filter_relation")),
		psql.Quote("t", "subject_type").EQ(psql.Raw("v_filter_type")),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.Raw("v_filter_relation")),
		psql.Raw("ue.depth < 25"),
	}

	query := psql.Select(
		sm.Columns(
			psql.Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"),
			psql.Raw("ue.depth + 1 AS depth"),
		),
		sm.From("userset_expansion").As("ue"),
		sm.InnerJoin("melange_tuples").As("t").On(
			psql.Quote("t", "object_id").EQ(psql.Quote("ue", "userset_object_id")),
		),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildListSubjectsSelfRefRegularQuery(a RelationAnalysis, inline InlineSQLData, relationList, complexClosure, allowedSubjectTypes []string, excludeWildcard bool) (string, error) {
	baseExclusions := buildSimpleComplexExclusionInput(a, "p_object_id", "p_subject_type", "t.subject_id")

	usersetObjectsBaseSQL, err := buildListSubjectsSelfRefUsersetObjectsBaseQuery(a, relationList)
	if err != nil {
		return "", err
	}

	usersetObjectsRecursiveSQL, err := buildListSubjectsSelfRefUsersetObjectsRecursiveQuery(a)
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
			"-- Path 1: Direct tuple lookup on the object itself",
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

	usersetObjectsSQL, err := buildListSubjectsSelfRefUsersetObjectsExpansionQuery(a, relationList, allowedSubjectTypes, excludeWildcard, baseExclusions)
	if err != nil {
		return "", err
	}
	baseBlocks = append(baseBlocks, formatQueryBlock(
		[]string{
			"-- Path 2: Expand userset subjects from all reachable userset objects",
		},
		usersetObjectsSQL,
	))

	for _, pattern := range buildListUsersetPatternInputs(a) {
		if pattern.IsSelfReferential {
			continue
		}
		patternExclusions := buildSimpleComplexExclusionInput(a, "p_object_id", "p_subject_type", "s.subject_id")
		if pattern.IsComplex {
			patternSQL, err := sqlgen.ListSubjectsUsersetPatternComplexQuery(sqlgen.ListSubjectsUsersetPatternComplexInput{
				ObjectType:       a.ObjectType,
				SubjectType:      pattern.SubjectType,
				SubjectRelation:  pattern.SubjectRelation,
				SourceRelations:  pattern.SourceRelations,
				ObjectIDExpr:     "p_object_id",
				SubjectTypeExpr:  "p_subject_type",
				IsClosurePattern: pattern.IsClosurePattern,
				SourceRelation:   pattern.SourceRelation,
				Exclusions:       patternExclusions,
			})
			if err != nil {
				return "", err
			}
			baseBlocks = append(baseBlocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Non-self userset expansion: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
					"-- Complex userset: use LATERAL list function",
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
			Exclusions:          patternExclusions,
		})
		if err != nil {
			return "", err
		}
		baseBlocks = append(baseBlocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Non-self userset expansion: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
				"-- Simple userset: JOIN with membership tuples",
			},
			patternSQL,
		))
	}

	baseResultsSQL := indentLines(joinUnionBlocks(baseBlocks), "        ")
	return fmt.Sprintf(`WITH RECURSIVE
        userset_objects(userset_object_id, depth) AS (
%s
            UNION ALL
%s
        ),
        base_results AS (
%s
        ),
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard
        )
%s`,
		indentLines(usersetObjectsBaseSQL, "            "),
		indentLines(usersetObjectsRecursiveSQL, "            "),
		baseResultsSQL,
		renderUsersetWildcardTail(a),
	), nil
}

func buildListSubjectsSelfRefUsersetObjectsBaseQuery(a RelationAnalysis, relationList []string) (string, error) {
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw("p_object_id")),
		psql.Raw("t.relation IN (" + formatSQLStringList(relationList) + ")"),
		psql.Quote("t", "subject_type").EQ(psql.S(a.ObjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(a.Relation)),
	}

	query := psql.Select(
		sm.Columns(
			psql.Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"),
			psql.Raw("0 AS depth"),
		),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildListSubjectsSelfRefUsersetObjectsRecursiveQuery(a RelationAnalysis) (string, error) {
	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Quote("uo", "userset_object_id")),
		psql.Quote("t", "relation").EQ(psql.S(a.Relation)),
		psql.Quote("t", "subject_type").EQ(psql.S(a.ObjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(a.Relation)),
		psql.Raw("uo.depth < 25"),
	}

	query := psql.Select(
		sm.Columns(
			psql.Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"),
			psql.Raw("uo.depth + 1 AS depth"),
		),
		sm.From("userset_objects").As("uo"),
		sm.InnerJoin("melange_tuples").As("t").On(
			psql.Quote("t", "object_id").EQ(psql.Quote("uo", "userset_object_id")),
		),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildListSubjectsSelfRefUsersetObjectsExpansionQuery(a RelationAnalysis, relationList, allowedSubjectTypes []string, excludeWildcard bool, exclusions sqlgen.ExclusionInput) (string, error) {
	exclusionPreds := exclusionPredicates(exclusions)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Quote("uo", "userset_object_id")),
		psql.Raw("t.relation IN (" + formatSQLStringList(relationList) + ")"),
		psql.Quote("t", "subject_type").EQ(psql.Raw("p_subject_type")),
		psql.Raw("p_subject_type IN (" + formatSQLStringList(allowedSubjectTypes) + ")"),
	}
	if excludeWildcard {
		where = append(where, psql.Quote("t", "subject_id").NE(psql.S("*")))
	}
	where = append(where, exclusionPreds...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.subject_id")),
		sm.From("userset_objects").As("uo"),
		sm.InnerJoin("melange_tuples").As("t").On(
			psql.Quote("t", "object_id").EQ(psql.Quote("uo", "userset_object_id")),
		),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func generateListObjectsComposedFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listObjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	anchor := buildListIndirectAnchorData(a)
	if anchor == nil || len(anchor.Path) == 0 {
		return "", fmt.Errorf("missing indirect anchor data for %s.%s", a.ObjectType, a.Relation)
	}

	selfSQL, err := sqlgen.ListObjectsSelfCandidateQuery(sqlgen.ListObjectsSelfCandidateInput{
		ObjectType:    a.ObjectType,
		Relation:      a.Relation,
		ClosureValues: inline.ClosureValues,
	})
	if err != nil {
		return "", err
	}

	querySQL, err := buildListObjectsComposedQuery(a, anchor, relationList)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
-- Indirect anchor: %s.%s via %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    -- Self-candidate check: when subject is a userset on the same object type
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
%s;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		anchor.AnchorType,
		anchor.AnchorRelation,
		anchor.Path[0].Type,
		functionName,
		indentLines(selfSQL, "        "),
		indentLines(selfSQL, "        "),
		formatSQLStringList(allowedSubjectTypes),
		indentLines(querySQL, "    "),
	), nil
}

func buildListObjectsComposedQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, relationList []string) (string, error) {
	if anchor == nil || len(anchor.Path) == 0 {
		return "SELECT NULL::TEXT WHERE FALSE", nil
	}

	firstStep := anchor.Path[0]
	exclusions := buildSimpleComplexExclusionInput(a, "t.object_id", "p_subject_type", "p_subject_id")

	var blocks []string
	switch firstStep.Type {
	case "ttu":
		for _, targetType := range firstStep.AllTargetTypes {
			query, err := buildComposedTTUObjectsQuery(a, anchor, targetType, exclusions)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- TTU composition: %s -> %s", firstStep.LinkingRelation, targetType),
				},
				query,
			))
		}

		for _, recursiveType := range firstStep.RecursiveTypes {
			query, err := buildComposedRecursiveTTUObjectsQuery(a, anchor, recursiveType, exclusions)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Recursive TTU: %s -> %s", firstStep.LinkingRelation, recursiveType),
				},
				query,
			))
		}
	case "userset":
		query, err := buildComposedUsersetObjectsQuery(a, anchor, firstStep, relationList, exclusions)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Userset composition: %s#%s", firstStep.SubjectType, firstStep.SubjectRelation),
			},
			query,
		))
	default:
		return "SELECT NULL::TEXT WHERE FALSE", nil
	}

	return joinUnionBlocks(blocks), nil
}

func buildComposedTTUObjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, targetType string, exclusions sqlgen.ExclusionInput) (string, error) {
	exclusionPreds := exclusionPredicates(exclusions)

	targetFunction := fmt.Sprintf("list_%s_%s_objects", targetType, anchor.Path[0].TargetRelation)
	subquery := fmt.Sprintf("SELECT obj.object_id FROM %s(p_subject_type, p_subject_id) obj", targetFunction)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("t", "relation").EQ(psql.S(anchor.Path[0].LinkingRelation)),
		psql.Quote("t", "subject_type").EQ(psql.S(targetType)),
		psql.Raw("t.subject_id IN (" + subquery + ")"),
	}
	where = append(where, exclusionPreds...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildComposedRecursiveTTUObjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, recursiveType string, exclusions sqlgen.ExclusionInput) (string, error) {
	exclusionPreds := exclusionPredicates(exclusions)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("t", "relation").EQ(psql.S(anchor.Path[0].LinkingRelation)),
		psql.Quote("t", "subject_type").EQ(psql.S(recursiveType)),
		sqlgen.CheckPermissionInternalExpr("p_subject_type", "p_subject_id", anchor.Path[0].TargetRelation, fmt.Sprintf("'%s'", recursiveType), "t.subject_id", true),
	}
	where = append(where, exclusionPreds...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildComposedUsersetObjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, firstStep ListAnchorPathStepData, relationList []string, exclusions sqlgen.ExclusionInput) (string, error) {
	exclusionPreds := exclusionPredicates(exclusions)

	targetFunction := anchor.FirstStepTargetFunctionName
	subquery := fmt.Sprintf("SELECT obj.object_id FROM %s(p_subject_type, p_subject_id) obj", targetFunction)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Raw("t.relation IN (" + formatSQLStringList(relationList) + ")"),
		psql.Quote("t", "subject_type").EQ(psql.S(firstStep.SubjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(firstStep.SubjectRelation)),
		psql.Or(
			psql.Raw("split_part(t.subject_id, '#', 1) IN ("+subquery+")"),
			sqlgen.CheckPermissionInternalExpr(
				"p_subject_type",
				"p_subject_id",
				firstStep.SubjectRelation,
				fmt.Sprintf("'%s'", firstStep.SubjectType),
				"split_part(t.subject_id, '#', 1)",
				true,
			),
		),
	}
	where = append(where, exclusionPreds...)

	query := psql.Select(
		sm.Columns(psql.Raw("t.object_id")),
		sm.From("melange_tuples").As("t"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func generateListSubjectsComposedFunctionBob(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	anchor := buildListIndirectAnchorData(a)
	if anchor == nil || len(anchor.Path) == 0 {
		return "", fmt.Errorf("missing indirect anchor data for %s.%s", a.ObjectType, a.Relation)
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
		return "", err
	}

	usersetFilterSQL, err := buildListSubjectsComposedUsersetFilterQuery(a, anchor)
	if err != nil {
		return "", err
	}

	regularSQL, err := buildListSubjectsComposedRegularQuery(a, anchor)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
-- Indirect anchor: %s.%s via %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_is_userset_filter BOOLEAN;
    v_filter_type TEXT;
    v_filter_relation TEXT;
BEGIN
    v_is_userset_filter := position('#' in p_subject_type) > 0;
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
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		anchor.AnchorType,
		anchor.AnchorRelation,
		anchor.Path[0].Type,
		functionName,
		a.ObjectType,
		indentLines(selfSQL, "                "),
		indentLines(selfSQL, "                "),
		indentLines(usersetFilterSQL, "        "),
		formatSQLStringList(allowedSubjectTypes),
		indentLines(regularSQL, "        "),
	), nil
}

func buildListSubjectsComposedUsersetFilterQuery(a RelationAnalysis, anchor *ListIndirectAnchorData) (string, error) {
	candidateBlocks, err := buildComposedSubjectsCandidateBlocks(a, anchor, "p_subject_type")
	if err != nil {
		return "", err
	}
	candidates := indentLines(joinUnionBlocks(candidateBlocks), "        ")
	return fmt.Sprintf(`WITH subject_candidates AS (
%s
)
SELECT DISTINCT sc.subject_id
FROM subject_candidates sc
WHERE check_permission_internal(v_filter_type, sc.subject_id, '%s', '%s', p_object_id, ARRAY[]::TEXT[]) = 1`,
		candidates,
		a.Relation,
		a.ObjectType,
	), nil
}

func buildListSubjectsComposedRegularQuery(a RelationAnalysis, anchor *ListIndirectAnchorData) (string, error) {
	candidateBlocks, err := buildComposedSubjectsCandidateBlocks(a, anchor, "p_subject_type")
	if err != nil {
		return "", err
	}
	candidates := indentLines(joinUnionBlocks(candidateBlocks), "        ")

	exclusions := buildSimpleComplexExclusionInput(a, "p_object_id", "p_subject_type", "sc.subject_id")
	exclusionPreds := exclusionPredicates(exclusions)

	whereClause := ""
	if len(exclusionPreds) > 0 {
		whereSQL, err := renderWhereClause(exclusionPreds)
		if err != nil {
			return "", err
		}
		whereClause = "\nWHERE " + whereSQL
	}

	return fmt.Sprintf(`WITH subject_candidates AS (
%s
)
SELECT DISTINCT sc.subject_id
FROM subject_candidates sc%s`,
		candidates,
		whereClause,
	), nil
}

func buildComposedSubjectsCandidateBlocks(a RelationAnalysis, anchor *ListIndirectAnchorData, subjectTypeExpr string) ([]string, error) {
	if anchor == nil || len(anchor.Path) == 0 {
		return []string{"SELECT NULL::TEXT WHERE FALSE"}, nil
	}

	firstStep := anchor.Path[0]
	var blocks []string
	switch firstStep.Type {
	case "ttu":
		for _, targetType := range firstStep.AllTargetTypes {
			query, err := buildComposedTTUSubjectsQuery(a, anchor, targetType, subjectTypeExpr)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- From %s parents", targetType),
				},
				query,
			))
		}

		for _, recursiveType := range firstStep.RecursiveTypes {
			query, err := buildComposedTTUSubjectsQuery(a, anchor, recursiveType, subjectTypeExpr)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- From %s parents (recursive)", recursiveType),
				},
				query,
			))
		}
	case "userset":
		query, err := buildComposedUsersetSubjectsQuery(a, firstStep, subjectTypeExpr)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Userset: %s#%s grants", firstStep.SubjectType, firstStep.SubjectRelation),
			},
			query,
		))
	default:
		return []string{"SELECT NULL::TEXT WHERE FALSE"}, nil
	}

	return blocks, nil
}

func buildComposedTTUSubjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, targetType, subjectTypeExpr string) (string, error) {
	listFunction := fmt.Sprintf("list_%s_%s_subjects(link.subject_id, %s)", targetType, anchor.Path[0].TargetRelation, subjectTypeExpr)

	where := []bob.Expression{
		psql.Quote("link", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("link", "object_id").EQ(psql.Raw("p_object_id")),
		psql.Quote("link", "relation").EQ(psql.S(anchor.Path[0].LinkingRelation)),
		psql.Quote("link", "subject_type").EQ(psql.S(targetType)),
	}

	query := psql.Select(
		sm.Columns(psql.Raw("s.subject_id")),
		sm.From("melange_tuples").As("link"),
		sm.CrossJoin(psql.Raw("LATERAL "+listFunction)).As("s"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func buildComposedUsersetSubjectsQuery(a RelationAnalysis, firstStep ListAnchorPathStepData, subjectTypeExpr string) (string, error) {
	listFunction := fmt.Sprintf("list_%s_%s_subjects(split_part(t.subject_id, '#', 1), %s)", firstStep.SubjectType, firstStep.SubjectRelation, subjectTypeExpr)

	where := []bob.Expression{
		psql.Quote("t", "object_type").EQ(psql.S(a.ObjectType)),
		psql.Quote("t", "object_id").EQ(psql.Raw("p_object_id")),
		psql.Raw("t.relation IN (" + formatSQLStringList(buildTupleLookupRelations(a)) + ")"),
		psql.Quote("t", "subject_type").EQ(psql.S(firstStep.SubjectType)),
		psql.Raw("position('#' in t.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(t.subject_id, '#', 2)").EQ(psql.S(firstStep.SubjectRelation)),
	}

	query := psql.Select(
		sm.Columns(psql.Raw("s.subject_id")),
		sm.From("melange_tuples").As("t"),
		sm.CrossJoin(psql.Raw("LATERAL "+listFunction)).As("s"),
		sm.Where(psql.And(where...)),
		sm.Distinct(),
	)
	return sqlgen.RenderQuery(query)
}

func generateListObjectsDispatcherBob(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	var buf strings.Builder
	buf.WriteString("-- Generated dispatcher for list_accessible_objects\n")
	buf.WriteString("-- Routes to specialized functions for all type/relation pairs\n")
	buf.WriteString("CREATE OR REPLACE FUNCTION list_accessible_objects(\n")
	buf.WriteString("    p_subject_type TEXT,\n")
	buf.WriteString("    p_subject_id TEXT,\n")
	buf.WriteString("    p_relation TEXT,\n")
	buf.WriteString("    p_object_type TEXT\n")
	buf.WriteString(") RETURNS TABLE (object_id TEXT) AS $$\n")
	buf.WriteString("BEGIN\n")
	if len(cases) > 0 {
		buf.WriteString("    -- Route to specialized functions for all type/relation pairs\n")
		for _, c := range cases {
			buf.WriteString("    IF p_object_type = '")
			buf.WriteString(c.ObjectType)
			buf.WriteString("' AND p_relation = '")
			buf.WriteString(c.Relation)
			buf.WriteString("' THEN\n")
			buf.WriteString("        RETURN QUERY SELECT * FROM ")
			buf.WriteString(c.FunctionName)
			buf.WriteString("(p_subject_type, p_subject_id);\n")
			buf.WriteString("        RETURN;\n")
			buf.WriteString("    END IF;\n")
		}
	}
	buf.WriteString("\n")
	buf.WriteString("    -- Unknown type/relation pair - return empty result (relation not defined in model)\n")
	buf.WriteString("    -- This matches check_permission behavior for unknown relations (returns 0/denied)\n")
	buf.WriteString("    RETURN;\n")
	buf.WriteString("END;\n")
	buf.WriteString("$$ LANGUAGE plpgsql STABLE;\n")
	return buf.String(), nil
}

func generateListSubjectsDispatcherBob(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	var buf strings.Builder
	buf.WriteString("-- Generated dispatcher for list_accessible_subjects\n")
	buf.WriteString("-- Routes to specialized functions for all type/relation pairs\n")
	buf.WriteString("CREATE OR REPLACE FUNCTION list_accessible_subjects(\n")
	buf.WriteString("    p_object_type TEXT,\n")
	buf.WriteString("    p_object_id TEXT,\n")
	buf.WriteString("    p_relation TEXT,\n")
	buf.WriteString("    p_subject_type TEXT\n")
	buf.WriteString(") RETURNS TABLE (subject_id TEXT) AS $$\n")
	buf.WriteString("BEGIN\n")
	if len(cases) > 0 {
		buf.WriteString("    -- Route to specialized functions for all type/relation pairs\n")
		for _, c := range cases {
			buf.WriteString("    IF p_object_type = '")
			buf.WriteString(c.ObjectType)
			buf.WriteString("' AND p_relation = '")
			buf.WriteString(c.Relation)
			buf.WriteString("' THEN\n")
			buf.WriteString("        RETURN QUERY SELECT * FROM ")
			buf.WriteString(c.FunctionName)
			buf.WriteString("(p_object_id, p_subject_type);\n")
			buf.WriteString("        RETURN;\n")
			buf.WriteString("    END IF;\n")
		}
	}
	buf.WriteString("\n")
	buf.WriteString("    -- Unknown type/relation pair - return empty result (relation not defined in model)\n")
	buf.WriteString("    -- This matches check_permission behavior for unknown relations (returns 0/denied)\n")
	buf.WriteString("    RETURN;\n")
	buf.WriteString("END;\n")
	buf.WriteString("$$ LANGUAGE plpgsql STABLE;\n")
	return buf.String(), nil
}
