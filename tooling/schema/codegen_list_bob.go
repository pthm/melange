package schema

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/tooling/schema/sqlgen"
	"github.com/stephenafamo/bob"
)

type listUsersetPatternInput struct {
	SubjectType         string
	SubjectRelation     string
	SatisfyingRelations []string
	SourceRelations     []string
	SourceRelation      string
	IsClosurePattern    bool
	HasWildcard         bool
	IsComplex           bool
}

func generateListObjectsFunctionBob(a RelationAnalysis, inline InlineSQLData, templateName string) (string, error) {
	functionName := listObjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	allowWildcard := a.Features.HasWildcard
	exclusions := buildExclusionInput(a, "t.object_id", "p_subject_type", "p_subject_id")
	complexClosure := filterComplexClosureRelations(a)

	var blocks []string
	switch templateName {
	case "list_objects_direct.tpl.sql":
		baseSQL, err := sqlgen.ListObjectsDirectQuery(sqlgen.ListObjectsDirectInput{
			ObjectType:          a.ObjectType,
			Relations:           relationList,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          sqlgen.ExclusionInput{},
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Direct tuple lookup with simple closure relations",
				"-- Type guard: only return results if subject type is in allowed subject types",
			},
			baseSQL,
		))

		complexBlocks, err := buildListObjectsComplexClosureBlocks(a, complexClosure, allowedSubjectTypes, allowWildcard, sqlgen.ExclusionInput{})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, complexBlocks...)
		intersectionBlocks, err := buildListObjectsIntersectionBlocks(a, false)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, intersectionBlocks...)
		selfSQL, err := sqlgen.ListObjectsSelfCandidateQuery(sqlgen.ListObjectsSelfCandidateInput{
			ObjectType:    a.ObjectType,
			Relation:      a.Relation,
			ClosureValues: inline.ClosureValues,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Self-candidate: when subject is a userset on the same object type",
				"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
				"-- The object 'document:1' should be considered as a candidate",
				"-- No type guard here - validity comes from the closure check below",
			},
			selfSQL,
		))
	case "list_objects_exclusion.tpl.sql":
		baseSQL, err := sqlgen.ListObjectsDirectQuery(sqlgen.ListObjectsDirectInput{
			ObjectType:          a.ObjectType,
			Relations:           relationList,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          exclusions,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Direct tuple lookup with closure-inlined relations",
				"-- Type guard: only return results if subject type is in allowed subject types",
			},
			baseSQL,
		))
		complexBlocks, err := buildListObjectsComplexClosureBlocks(a, complexClosure, allowedSubjectTypes, allowWildcard, exclusions)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, complexBlocks...)
		intersectionBlocks, err := buildListObjectsIntersectionBlocks(a, true)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, intersectionBlocks...)
		selfSQL, err := sqlgen.ListObjectsSelfCandidateQuery(sqlgen.ListObjectsSelfCandidateInput{
			ObjectType:    a.ObjectType,
			Relation:      a.Relation,
			ClosureValues: inline.ClosureValues,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Self-candidate: when subject is a userset on the same object type",
				"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
				"-- The object 'document:1' should be considered as a candidate",
				"-- No type guard here - validity comes from the closure check below",
				"-- No exclusion checks for self-candidate - this is a structural validity check",
			},
			selfSQL,
		))
	case "list_objects_userset.tpl.sql":
		baseSQL, err := sqlgen.ListObjectsDirectQuery(sqlgen.ListObjectsDirectInput{
			ObjectType:          a.ObjectType,
			Relations:           relationList,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          exclusions,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Path 1: Direct tuple lookup with simple closure relations",
				"-- Type guard: only return results if subject type is in allowed subject types",
			},
			baseSQL,
		))

		usersetSubjectSQL, err := sqlgen.ListObjectsUsersetSubjectQuery(sqlgen.ListObjectsUsersetSubjectInput{
			ObjectType:    a.ObjectType,
			Relations:     relationList,
			ClosureValues: inline.ClosureValues,
			Exclusions:    exclusions,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Direct userset subject matching: when the subject IS a userset (e.g., group:fga#member)",
				"-- and there's a tuple with that userset (or a satisfying relation) as the subject",
				"-- This handles cases like: tuple(document:1, viewer, group:fga#member_c4) queried by group:fga#member",
				"-- where member satisfies member_c4 via the closure (member → member_c1 → ... → member_c4)",
				"-- No type guard - we're matching userset subjects via closure",
			},
			usersetSubjectSQL,
		))

		complexBlocks, err := buildListObjectsComplexClosureBlocks(a, complexClosure, allowedSubjectTypes, allowWildcard, sqlgen.ExclusionInput{})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, complexBlocks...)
		intersectionBlocks, err := buildListObjectsIntersectionBlocks(a, false)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, intersectionBlocks...)

		for _, pattern := range buildListUsersetPatternInputs(a) {
			if pattern.IsComplex {
				patternSQL, err := sqlgen.ListObjectsUsersetPatternComplexQuery(sqlgen.ListObjectsUsersetPatternComplexInput{
					ObjectType:       a.ObjectType,
					SubjectType:      pattern.SubjectType,
					SubjectRelation:  pattern.SubjectRelation,
					SourceRelations:  pattern.SourceRelations,
					IsClosurePattern: pattern.IsClosurePattern,
					SourceRelation:   pattern.SourceRelation,
					Exclusions:       exclusions,
				})
				if err != nil {
					return "", err
				}
				blocks = append(blocks, formatQueryBlock(
					[]string{
						fmt.Sprintf("-- Path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
						"-- Complex userset: use check_permission_internal for membership verification",
						"-- Note: No type guard needed here because check_permission_internal handles all validation",
						"-- including userset self-referential checks (e.g., group:1#member checking member on group:1)",
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
				Exclusions:          exclusions,
			})
			if err != nil {
				return "", err
			}
			blocks = append(blocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
					"-- Simple userset: JOIN with membership tuples",
				},
				patternSQL,
			))
		}

		selfSQL, err := sqlgen.ListObjectsSelfCandidateQuery(sqlgen.ListObjectsSelfCandidateInput{
			ObjectType:    a.ObjectType,
			Relation:      a.Relation,
			ClosureValues: inline.ClosureValues,
		})
		if err != nil {
			return "", err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Self-candidate: when subject is a userset on the same object type",
				"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
				"-- The object 'document:1' should be considered as a candidate",
				"-- No type guard here - validity comes from the closure check below",
				"-- No exclusion checks for self-candidate - this is a structural validity check",
			},
			selfSQL,
		))
	default:
		return "", fmt.Errorf("unexpected list_objects template %s", templateName)
	}

	query := joinUnionBlocks(blocks)
	return buildListObjectsFunctionSQL(functionName, a, query), nil
}

func generateListSubjectsFunctionBob(a RelationAnalysis, inline InlineSQLData, templateName string) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allSatisfyingRelations := buildAllSatisfyingRelationsList(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	excludeWildcard := !a.Features.HasWildcard
	complexClosure := filterComplexClosureRelations(a)

	var usersetFilterBlocks []string
	var usersetFilterSelfBlock string
	var regularBlocks []string
	var filterBlocks []string
	var complexBlocks []string
	var intersectionBlocks []string

	switch templateName {
	case "list_subjects_direct.tpl.sql":
		usersetBaseSQL, err := sqlgen.ListSubjectsUsersetFilterQuery(sqlgen.ListSubjectsUsersetFilterInput{
			ObjectType:          a.ObjectType,
			RelationList:        relationList,
			AllowedSubjectTypes: allowedSubjectTypes,
			ObjectIDExpr:        "p_object_id",
			FilterTypeExpr:      "v_filter_type",
			FilterRelationExpr:  "v_filter_relation",
			ClosureValues:       inline.ClosureValues,
			UseTypeGuard:        true,
		})
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, formatQueryBlock(
			[]string{
				"-- Direct tuple lookup with simple closure relations",
				"-- Normalize results to use the filter relation (e.g., group:1#admin -> group:1#member if admin implies member)",
				"-- Type guard: only return results if filter type is in allowed subject types",
			},
			usersetBaseSQL,
		))

		filterBlocks, err = buildListSubjectsComplexClosureFilterBlocks(
			a,
			complexClosure,
			allowedSubjectTypes,
			inline.ClosureValues,
			false,
		)
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, filterBlocks...)
		intersectionBlocks, err = buildListSubjectsIntersectionBlocks(
			a,
			false,
			"v_filter_type || '#' || v_filter_relation",
			"",
		)
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, intersectionBlocks...)

		selfBlock, err := sqlgen.ListSubjectsSelfCandidateQuery(sqlgen.ListSubjectsSelfCandidateInput{
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
		usersetFilterSelfBlock = formatQueryBlock(
			[]string{
				"-- Self-candidate: when filter type matches object type",
				"-- e.g., querying document:1.viewer with filter document#writer",
				"-- should return document:1#writer if writer satisfies the relation",
				"-- No type guard here - validity comes from the closure check below",
			},
			selfBlock,
		)

		regularBaseSQL, err := sqlgen.ListSubjectsDirectQuery(sqlgen.ListSubjectsDirectInput{
			ObjectType:      a.ObjectType,
			RelationList:    relationList,
			ObjectIDExpr:    "p_object_id",
			SubjectTypeExpr: "p_subject_type",
			ExcludeWildcard: excludeWildcard,
			Exclusions:      sqlgen.ExclusionInput{},
		})
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, formatQueryBlock(nil, regularBaseSQL))
		complexBlocks, err = buildListSubjectsComplexClosureBlocks(
			a,
			complexClosure,
			"p_subject_type",
			excludeWildcard,
			sqlgen.ExclusionInput{},
		)
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, complexBlocks...)
		intersectionBlocks, err = buildListSubjectsIntersectionBlocks(
			a,
			false,
			"p_subject_type",
			"",
		)
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, intersectionBlocks...)
	case "list_subjects_exclusion.tpl.sql":
		usersetNormalized := "substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation"
		usersetExclusions := buildExclusionInput(a, "p_object_id", "v_filter_type", usersetNormalized)

		usersetPreds, err := exclusionPredicates(usersetExclusions)
		if err != nil {
			return "", err
		}
		usersetBaseSQL, err := sqlgen.ListSubjectsUsersetFilterQuery(sqlgen.ListSubjectsUsersetFilterInput{
			ObjectType:          a.ObjectType,
			RelationList:        relationList,
			AllowedSubjectTypes: allowedSubjectTypes,
			ObjectIDExpr:        "p_object_id",
			FilterTypeExpr:      "v_filter_type",
			FilterRelationExpr:  "v_filter_relation",
			ClosureValues:       inline.ClosureValues,
			UseTypeGuard:        true,
			ExtraPredicates:     usersetPreds,
		})
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, formatQueryBlock(
			[]string{
				"-- Direct tuple lookup with closure-inlined relations",
				"-- Normalize results to use the filter relation (e.g., group:1#admin -> group:1#member if admin implies member)",
				"-- Type guard: only return results if filter type is in allowed subject types",
			},
			usersetBaseSQL,
		))

		filterBlocks, err = buildListSubjectsComplexClosureFilterBlocks(
			a,
			complexClosure,
			allowedSubjectTypes,
			inline.ClosureValues,
			true,
		)
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, filterBlocks...)
		intersectionBlocks, err = buildListSubjectsIntersectionBlocks(
			a,
			true,
			"v_filter_type || '#' || v_filter_relation",
			"v_filter_type",
		)
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, intersectionBlocks...)

		selfExclusions := buildExclusionInput(a, "p_object_id", fmt.Sprintf("'%s'", a.ObjectType), "p_object_id || '#' || v_filter_relation")
		selfPreds, err := exclusionPredicates(selfExclusions)
		if err != nil {
			return "", err
		}
		selfBlock, err := sqlgen.ListSubjectsSelfCandidateQuery(sqlgen.ListSubjectsSelfCandidateInput{
			ObjectType:         a.ObjectType,
			Relation:           a.Relation,
			ObjectIDExpr:       "p_object_id",
			FilterTypeExpr:     "v_filter_type",
			FilterRelationExpr: "v_filter_relation",
			ClosureValues:      inline.ClosureValues,
			ExtraPredicates:    selfPreds,
		})
		if err != nil {
			return "", err
		}
		usersetFilterSelfBlock = formatQueryBlock(
			[]string{
				"-- Self-candidate: when filter type matches object type",
				"-- e.g., querying document:1.viewer with filter document#writer",
				"-- should return document:1#writer if writer satisfies the relation",
				"-- No type guard here - validity comes from the closure check below",
			},
			selfBlock,
		)

		regularExclusions := buildExclusionInput(a, "p_object_id", "p_subject_type", "t.subject_id")
		regularBaseSQL, err := sqlgen.ListSubjectsDirectQuery(sqlgen.ListSubjectsDirectInput{
			ObjectType:      a.ObjectType,
			RelationList:    relationList,
			ObjectIDExpr:    "p_object_id",
			SubjectTypeExpr: "p_subject_type",
			ExcludeWildcard: excludeWildcard,
			Exclusions:      regularExclusions,
		})
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, formatQueryBlock(nil, regularBaseSQL))
		complexBlocks, err = buildListSubjectsComplexClosureBlocks(
			a,
			complexClosure,
			"p_subject_type",
			excludeWildcard,
			regularExclusions,
		)
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, complexBlocks...)
		intersectionBlocks, err = buildListSubjectsIntersectionBlocks(
			a,
			true,
			"p_subject_type",
			"p_subject_type",
		)
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, intersectionBlocks...)
	case "list_subjects_userset.tpl.sql":
		usersetBaseSQL, err := sqlgen.ListSubjectsUsersetFilterQuery(sqlgen.ListSubjectsUsersetFilterInput{
			ObjectType:         a.ObjectType,
			RelationList:       allSatisfyingRelations,
			ObjectIDExpr:       "p_object_id",
			FilterTypeExpr:     "v_filter_type",
			FilterRelationExpr: "v_filter_relation",
			ClosureValues:      inline.ClosureValues,
			UseTypeGuard:       false,
			ExtraPredicates: []bob.Expression{
				sqlgen.CheckPermissionExpr("check_permission", "v_filter_type", "t.subject_id", a.Relation, fmt.Sprintf("'%s'", a.ObjectType), "p_object_id", true),
			},
		})
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, formatQueryBlock(
			[]string{
				"-- Userset filter: find userset tuples that match and return normalized references",
			},
			usersetBaseSQL,
		))

		intersectionBlocks, err = buildListSubjectsIntersectionBlocks(
			a,
			false,
			"v_filter_type || '#' || v_filter_relation",
			"",
		)
		if err != nil {
			return "", err
		}
		usersetFilterBlocks = append(usersetFilterBlocks, intersectionBlocks...)

		selfBlock, err := sqlgen.ListSubjectsSelfCandidateQuery(sqlgen.ListSubjectsSelfCandidateInput{
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
		usersetFilterSelfBlock = formatQueryBlock(
			[]string{
				"-- Self-referential userset: when object_type matches filter_type and filter_relation",
				"-- satisfies the requested relation, the userset reference object_id#filter_relation has access",
				"-- e.g., for group:1.member with filter group#member, return 1#member (= group:1#member)",
				"-- NOTE: Exclusions don't apply to self-referential userset checks (structural validity)",
			},
			selfBlock,
		)

		baseExclusions := buildExclusionInput(a, "p_object_id", "p_subject_type", "t.subject_id")
		regularBaseSQL, err := sqlgen.ListSubjectsDirectQuery(sqlgen.ListSubjectsDirectInput{
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
		regularBlocks = append(regularBlocks, formatQueryBlock(
			[]string{
				"-- Path 1: Direct tuple lookup with simple closure relations",
			},
			regularBaseSQL,
		))

		complexBlocks, err = buildListSubjectsComplexClosureBlocks(
			a,
			complexClosure,
			"p_subject_type",
			excludeWildcard,
			baseExclusions,
		)
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, complexBlocks...)
		intersectionBlocks, err = buildListSubjectsIntersectionBlocks(
			a,
			false,
			"p_subject_type",
			"",
		)
		if err != nil {
			return "", err
		}
		regularBlocks = append(regularBlocks, intersectionBlocks...)

		for _, pattern := range buildListUsersetPatternInputs(a) {
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
					Exclusions:       baseExclusions,
				})
				if err != nil {
					return "", err
				}
				regularBlocks = append(regularBlocks, formatQueryBlock(
					[]string{
						fmt.Sprintf("-- Path: Via %s#%s - expand group membership to return individual subjects", pattern.SubjectType, pattern.SubjectRelation),
						"-- Complex userset: use LATERAL join with userset's list_subjects function",
						"-- This handles userset-to-userset chains where there are no direct subject tuples",
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
				Exclusions:          baseExclusions,
			})
			if err != nil {
				return "", err
			}
			regularBlocks = append(regularBlocks, formatQueryBlock(
				[]string{
					fmt.Sprintf("-- Path: Via %s#%s - expand group membership to return individual subjects", pattern.SubjectType, pattern.SubjectRelation),
					"-- Simple userset: JOIN with membership tuples",
				},
				patternSQL,
			))
		}
	default:
		return "", fmt.Errorf("unexpected list_subjects template %s", templateName)
	}

	return buildListSubjectsFunctionSQL(functionName, a, usersetFilterBlocks, usersetFilterSelfBlock, regularBlocks, templateName), nil
}

func buildListObjectsComplexClosureBlocks(a RelationAnalysis, relations []string, allowedSubjectTypes []string, allowWildcard bool, exclusions sqlgen.ExclusionInput) ([]string, error) {
	var blocks []string
	for _, rel := range relations {
		blockSQL, err := sqlgen.ListObjectsComplexClosureQuery(sqlgen.ListObjectsComplexClosureInput{
			ObjectType:          a.ObjectType,
			Relation:            rel,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          exclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
				"-- These relations have exclusions or other complex features that require full permission check",
			},
			blockSQL,
		))
	}
	return blocks, nil
}

func buildListObjectsIntersectionBlocks(a RelationAnalysis, validate bool) ([]string, error) {
	var blocks []string
	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_objects", a.ObjectType, rel)
		var blockSQL string
		var err error
		if validate {
			blockSQL, err = sqlgen.ListObjectsIntersectionClosureValidatedQuery(a.ObjectType, a.Relation, functionName)
		} else {
			blockSQL, err = sqlgen.ListObjectsIntersectionClosureQuery(functionName)
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			blockSQL,
		))
	}
	return blocks, nil
}

func buildListSubjectsComplexClosureFilterBlocks(a RelationAnalysis, relations []string, allowedSubjectTypes []string, closureValues string, applyExclusions bool) ([]string, error) {
	var blocks []string
	normalized := "substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation"
	for _, rel := range relations {
		exclusions := sqlgen.ExclusionInput{}
		if applyExclusions {
			exclusions = buildExclusionInput(a, "p_object_id", "t.subject_type", normalized)
		}
		exclusionPreds, err := exclusionPredicates(exclusions)
		if err != nil {
			return nil, err
		}
		blockSQL, err := sqlgen.ListSubjectsUsersetFilterQuery(sqlgen.ListSubjectsUsersetFilterInput{
			ObjectType:          a.ObjectType,
			RelationList:        []string{rel},
			AllowedSubjectTypes: allowedSubjectTypes,
			ObjectIDExpr:        "p_object_id",
			FilterTypeExpr:      "v_filter_type",
			FilterRelationExpr:  "v_filter_relation",
			ClosureValues:       closureValues,
			UseTypeGuard:        true,
			ExtraPredicates: append(
				exclusionPreds,
				sqlgen.CheckPermissionInternalExpr("t.subject_type", "t.subject_id", rel, fmt.Sprintf("'%s'", a.ObjectType), "p_object_id", true),
			),
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
			},
			blockSQL,
		))
	}
	return blocks, nil
}

func buildListSubjectsComplexClosureBlocks(a RelationAnalysis, relations []string, subjectTypeExpr string, excludeWildcard bool, exclusions sqlgen.ExclusionInput) ([]string, error) {
	var blocks []string
	for _, rel := range relations {
		blockSQL, err := sqlgen.ListSubjectsComplexClosureQuery(sqlgen.ListSubjectsComplexClosureInput{
			ObjectType:      a.ObjectType,
			Relation:        rel,
			ObjectIDExpr:    "p_object_id",
			SubjectTypeExpr: subjectTypeExpr,
			ExcludeWildcard: excludeWildcard,
			Exclusions:      exclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
			},
			blockSQL,
		))
	}
	return blocks, nil
}

func buildListSubjectsIntersectionBlocks(a RelationAnalysis, validate bool, functionSubjectTypeExpr, checkSubjectTypeExpr string) ([]string, error) {
	var blocks []string
	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		var blockSQL string
		var err error
		if validate {
			blockSQL, err = sqlgen.ListSubjectsIntersectionClosureValidatedQuery(a.ObjectType, a.Relation, functionName, functionSubjectTypeExpr, checkSubjectTypeExpr, "p_object_id")
		} else {
			blockSQL, err = sqlgen.ListSubjectsIntersectionClosureQuery(functionName, functionSubjectTypeExpr)
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, formatQueryBlock(
			[]string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			blockSQL,
		))
	}
	return blocks, nil
}

func buildListObjectsFunctionSQL(functionName string, a RelationAnalysis, query string) string {
	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
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
		query,
	)
}

func buildListSubjectsFunctionSQL(functionName string, a RelationAnalysis, usersetFilterBlocks []string, usersetFilterSelfBlock string, regularBlocks []string, templateName string) string {
	var usersetFilterQuery string
	if len(usersetFilterBlocks) > 0 {
		parts := append([]string{}, usersetFilterBlocks...)
		if usersetFilterSelfBlock != "" {
			parts = append(parts, usersetFilterSelfBlock)
		}
		usersetFilterQuery = joinUnionBlocks(parts)
	}

	regularQuery := joinUnionBlocks(regularBlocks)
	regularTypeGuard := ""
	if templateName != "list_subjects_userset.tpl.sql" {
		regularTypeGuard = fmt.Sprintf(`
        -- Guard: return empty if subject type is not allowed by the model
        IF p_subject_type NOT IN (%s) THEN
            RETURN;
        END IF;
`, formatSQLStringList(buildAllowedSubjectTypesList(a)))
	}

	var regularReturn string
	if templateName == "list_subjects_userset.tpl.sql" {
		regularReturn = fmt.Sprintf(`
        RETURN QUERY
        WITH base_results AS (
%s
        ),
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard
        )
%s`, indentLines(regularQuery, "        "), renderUsersetWildcardTail(a))
	} else {
		regularReturn = fmt.Sprintf(`
        -- Regular subject type (no userset filter)
        RETURN QUERY
%s;`, regularQuery)
	}

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
    -- Check if subject_type is a userset filter (e.g., "document#viewer")
    IF position('#' in p_subject_type) > 0 THEN
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);

        RETURN QUERY
%s;
    ELSE%s%s
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		usersetFilterQuery,
		regularTypeGuard,
		regularReturn,
	)
}

func renderUsersetWildcardTail(a RelationAnalysis) string {
	if a.Features.HasWildcard {
		return fmt.Sprintf(`
        -- Wildcard handling: when wildcard exists, filter non-wildcard subjects
        -- to only those with explicit (non-wildcard-derived) access
        SELECT br.subject_id
        FROM base_results br
        CROSS JOIN has_wildcard hw
        WHERE (NOT hw.has_wildcard)
           OR (br.subject_id = '*')
           OR (
               br.subject_id != '*'
               AND check_permission_no_wildcard(
                   p_subject_type,
                   br.subject_id,
                   '%s',
                   '%s',
                   p_object_id
               ) = 1
           );`, a.Relation, a.ObjectType)
	}

	return "        SELECT br.subject_id FROM base_results br;"
}

func filterComplexClosureRelations(a RelationAnalysis) []string {
	intersectionSet := make(map[string]bool)
	for _, rel := range a.IntersectionClosureRelations {
		intersectionSet[rel] = true
	}
	var complex []string
	for _, rel := range a.ComplexClosureRelations {
		if !intersectionSet[rel] {
			complex = append(complex, rel)
		}
	}
	return complex
}

func buildAllowedSubjectTypesList(a RelationAnalysis) []string {
	subjectTypes := a.AllowedSubjectTypes
	if len(subjectTypes) == 0 {
		subjectTypes = a.DirectSubjectTypes
	}
	if len(subjectTypes) == 0 {
		return []string{""}
	}
	return subjectTypes
}

func buildAllSatisfyingRelationsList(a RelationAnalysis) []string {
	relations := a.SatisfyingRelations
	if len(relations) == 0 {
		relations = []string{a.Relation}
	}
	return relations
}

func buildListUsersetPatternInputs(a RelationAnalysis) []listUsersetPatternInput {
	if len(a.UsersetPatterns) == 0 && len(a.ClosureUsersetPatterns) == 0 {
		return nil
	}
	directRelationList := buildTupleLookupRelations(a)
	var patterns []listUsersetPatternInput

	for _, p := range a.UsersetPatterns {
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}
		patterns = append(patterns, listUsersetPatternInput{
			SubjectType:         p.SubjectType,
			SubjectRelation:     p.SubjectRelation,
			SatisfyingRelations: satisfying,
			SourceRelations:     directRelationList,
			SourceRelation:      "",
			IsClosurePattern:    false,
			HasWildcard:         p.HasWildcard,
			IsComplex:           p.IsComplex,
		})
	}

	for _, p := range a.ClosureUsersetPatterns {
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}
		patterns = append(patterns, listUsersetPatternInput{
			SubjectType:         p.SubjectType,
			SubjectRelation:     p.SubjectRelation,
			SatisfyingRelations: satisfying,
			SourceRelations:     []string{p.SourceRelation},
			SourceRelation:      p.SourceRelation,
			IsClosurePattern:    true,
			HasWildcard:         p.HasWildcard,
			IsComplex:           p.IsComplex,
		})
	}

	return patterns
}

func buildExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr string) sqlgen.ExclusionInput {
	return sqlgen.ExclusionInput{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
		ExcludedParentRelations:  convertParentRelations(a.ExcludedParentRelations),
		ExcludedIntersection:     convertIntersectionGroups(a.ExcludedIntersectionGroups),
	}
}

func convertParentRelations(relations []ParentRelationInfo) []sqlgen.ExcludedParentRelation {
	if len(relations) == 0 {
		return nil
	}
	result := make([]sqlgen.ExcludedParentRelation, 0, len(relations))
	for _, rel := range relations {
		result = append(result, sqlgen.ExcludedParentRelation{
			Relation:            rel.Relation,
			LinkingRelation:     rel.LinkingRelation,
			AllowedLinkingTypes: rel.AllowedLinkingTypes,
		})
	}
	return result
}

func convertIntersectionGroups(groups []IntersectionGroupInfo) []sqlgen.ExcludedIntersectionGroup {
	if len(groups) == 0 {
		return nil
	}
	result := make([]sqlgen.ExcludedIntersectionGroup, 0, len(groups))
	for _, group := range groups {
		parts := make([]sqlgen.ExcludedIntersectionPart, 0, len(group.Parts))
		for _, part := range group.Parts {
			if part.ParentRelation != nil {
				parts = append(parts, sqlgen.ExcludedIntersectionPart{
					ParentRelation: &sqlgen.ExcludedParentRelation{
						Relation:            part.ParentRelation.Relation,
						LinkingRelation:     part.ParentRelation.LinkingRelation,
						AllowedLinkingTypes: part.ParentRelation.AllowedLinkingTypes,
					},
				})
				continue
			}
			parts = append(parts, sqlgen.ExcludedIntersectionPart{
				Relation:         part.Relation,
				ExcludedRelation: part.ExcludedRelation,
			})
		}
		result = append(result, sqlgen.ExcludedIntersectionGroup{Parts: parts})
	}
	return result
}

func exclusionPredicates(input sqlgen.ExclusionInput) ([]bob.Expression, error) {
	predicates, err := sqlgen.ExclusionPredicates(input)
	if err != nil {
		return nil, err
	}
	return predicates, nil
}

func formatQueryBlock(comments []string, sql string) string {
	var lines []string
	for _, comment := range comments {
		lines = append(lines, "    "+comment)
	}
	lines = append(lines, indentLines(sql, "    "))
	return strings.Join(lines, "\n")
}

func joinUnionBlocks(blocks []string) string {
	return strings.Join(blocks, "\n    UNION\n")
}

func indentLines(input, indent string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(input), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}
