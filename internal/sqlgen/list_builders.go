package sqlgen

import (
	"fmt"
	"strings"
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
	IsSelfReferential   bool
}

// =============================================================================
// ListObjectsBuilder - Feature-driven list_objects function generation
// =============================================================================

// ListObjectsBuilder generates list_objects functions using a feature-driven approach
// rather than template-based switch statements.
type ListObjectsBuilder struct {
	analysis RelationAnalysis
	inline   InlineSQLData

	// Computed data
	functionName        string
	relationList        []string
	allowedSubjectTypes []string
	complexClosure      []string

	// Configuration based on features
	exclusions    ExclusionConfig
	allowWildcard bool

	// Built blocks
	blocks []QueryBlock
}

// NewListObjectsBuilder creates a builder for generating list_objects functions.
func NewListObjectsBuilder(a RelationAnalysis, inline InlineSQLData) *ListObjectsBuilder {
	return &ListObjectsBuilder{
		analysis:            a,
		inline:              inline,
		functionName:        listObjectsFunctionName(a.ObjectType, a.Relation),
		relationList:        buildTupleLookupRelations(a),
		allowedSubjectTypes: buildAllowedSubjectTypesList(a),
		complexClosure:      filterComplexClosureRelations(a),
		allowWildcard:       a.Features.HasWildcard,
	}
}

// Build generates the complete list_objects function SQL.
func (b *ListObjectsBuilder) Build() (string, error) {
	// Check for special delegated cases first
	// Order matters! Must match selectListObjectsTemplate priority:
	// 1. Intersection (most comprehensive, handles all patterns)
	// 2. Recursive/TTU (handles direct, userset, exclusion, TTU)
	if b.hasComplexIntersection() {
		return generateListObjectsIntersectionFunction(b.analysis, b.inline)
	}
	if b.hasRecursiveParent() {
		return generateListObjectsRecursiveFunction(b.analysis, b.inline)
	}

	// Configure exclusions if the relation has exclusion features
	if b.analysis.Features.HasExclusion {
		b.exclusions = buildExclusionInput(b.analysis, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)
	}

	// Feature-driven block building
	if err := b.addDirectBlock(); err != nil {
		return "", err
	}
	if b.hasUsersetSubject() {
		if err := b.addUsersetSubjectBlock(); err != nil {
			return "", err
		}
	}
	if err := b.addComplexClosureBlocks(); err != nil {
		return "", err
	}
	if err := b.addIntersectionClosureBlocks(); err != nil {
		return "", err
	}
	if b.hasUsersetPatterns() {
		if err := b.addUsersetPatternBlocks(); err != nil {
			return "", err
		}
	}
	if err := b.addSelfCandidateBlock(); err != nil {
		return "", err
	}

	return b.renderFunction()
}

// =============================================================================
// Feature Detection Methods
// =============================================================================

// hasRecursiveParent returns true if the relation has recursive TTU patterns
// that require special handling with CTEs.
func (b *ListObjectsBuilder) hasRecursiveParent() bool {
	return len(b.analysis.ClosureParentRelations) > 0 || b.analysis.Features.HasRecursive
}

// hasComplexIntersection returns true if the relation has intersection patterns
// that require special handling (INTERSECT queries).
// Note: matches selectListObjectsTemplate which only checks HasIntersection.
func (b *ListObjectsBuilder) hasComplexIntersection() bool {
	return b.analysis.Features.HasIntersection
}

// hasUsersetSubject returns true if the relation supports userset subject matching.
// This is when a subject like "group:1#member" can match tuples via closure.
func (b *ListObjectsBuilder) hasUsersetSubject() bool {
	return b.analysis.Features.HasUserset || len(b.analysis.ClosureUsersetPatterns) > 0
}

// hasUsersetPatterns returns true if there are userset patterns to expand.
func (b *ListObjectsBuilder) hasUsersetPatterns() bool {
	return len(buildListUsersetPatternInputs(b.analysis)) > 0
}

// =============================================================================
// Block Building Methods
// =============================================================================

// addDirectBlock adds the direct tuple lookup block.
func (b *ListObjectsBuilder) addDirectBlock() error {
	baseSQL, err := ListObjectsDirectQuery(ListObjectsDirectInput{
		ObjectType:          b.analysis.ObjectType,
		Relations:           b.relationList,
		AllowedSubjectTypes: b.allowedSubjectTypes,
		AllowWildcard:       b.allowWildcard,
		Exclusions:          b.exclusions,
	})
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, QueryBlock{
		Comments: []string{
			"-- Direct tuple lookup with simple closure relations",
			"-- Type guard: only return results if subject type is in allowed subject types",
		},
		SQL: baseSQL,
	})
	return nil
}

// addUsersetSubjectBlock adds the userset subject matching block.
// This handles cases like querying with subject "group:1#member".
func (b *ListObjectsBuilder) addUsersetSubjectBlock() error {
	usersetSubjectSQL, err := ListObjectsUsersetSubjectQuery(ListObjectsUsersetSubjectInput{
		ObjectType:    b.analysis.ObjectType,
		Relations:     b.relationList,
		ClosureValues: b.inline.ClosureValues,
		Exclusions:    b.exclusions,
	})
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, QueryBlock{
		Comments: []string{
			"-- Direct userset subject matching: when the subject IS a userset (e.g., group:fga#member)",
			"-- and there's a tuple with that userset (or a satisfying relation) as the subject",
			"-- This handles cases like: tuple(document:1, viewer, group:fga#member_c4) queried by group:fga#member",
			"-- where member satisfies member_c4 via the closure (member → member_c1 → ... → member_c4)",
			"-- No type guard - we're matching userset subjects via closure",
		},
		SQL: usersetSubjectSQL,
	})
	return nil
}

// addComplexClosureBlocks adds blocks for complex closure relations.
// These require check_permission_internal for validation.
func (b *ListObjectsBuilder) addComplexClosureBlocks() error {
	// For non-exclusion cases, don't apply exclusions to complex closure blocks
	exclusions := b.exclusions
	if !b.analysis.Features.HasExclusion {
		exclusions = ExclusionConfig{}
	}

	blocks, err := buildListObjectsComplexClosureBlocks(
		b.analysis,
		b.complexClosure,
		b.allowedSubjectTypes,
		b.allowWildcard,
		exclusions,
	)
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, blocks...)
	return nil
}

// addIntersectionClosureBlocks adds blocks for intersection closure relations.
func (b *ListObjectsBuilder) addIntersectionClosureBlocks() error {
	// Validate intersection blocks if we have exclusions
	validate := b.analysis.Features.HasExclusion

	blocks, err := buildListObjectsIntersectionBlocks(b.analysis, validate)
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, blocks...)
	return nil
}

// addUsersetPatternBlocks adds blocks for userset pattern expansion.
func (b *ListObjectsBuilder) addUsersetPatternBlocks() error {
	for _, pattern := range buildListUsersetPatternInputs(b.analysis) {
		if pattern.IsComplex {
			patternSQL, err := ListObjectsUsersetPatternComplexQuery(ListObjectsUsersetPatternComplexInput{
				ObjectType:       b.analysis.ObjectType,
				SubjectType:      pattern.SubjectType,
				SubjectRelation:  pattern.SubjectRelation,
				SourceRelations:  pattern.SourceRelations,
				IsClosurePattern: pattern.IsClosurePattern,
				SourceRelation:   pattern.SourceRelation,
				Exclusions:       b.exclusions,
			})
			if err != nil {
				return err
			}
			b.blocks = append(b.blocks, QueryBlock{
				Comments: []string{
					fmt.Sprintf("-- Path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
					"-- Complex userset: use check_permission_internal for membership verification",
					"-- Note: No type guard needed here because check_permission_internal handles all validation",
					"-- including userset self-referential checks (e.g., group:1#member checking member on group:1)",
				},
				SQL: patternSQL,
			})
			continue
		}

		patternSQL, err := ListObjectsUsersetPatternSimpleQuery(ListObjectsUsersetPatternSimpleInput{
			ObjectType:          b.analysis.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			AllowedSubjectTypes: b.allowedSubjectTypes,
			AllowWildcard:       pattern.HasWildcard,
			IsClosurePattern:    pattern.IsClosurePattern,
			SourceRelation:      pattern.SourceRelation,
			Exclusions:          b.exclusions,
		})
		if err != nil {
			return err
		}
		b.blocks = append(b.blocks, QueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
				"-- Simple userset: JOIN with membership tuples",
			},
			SQL: patternSQL,
		})
	}
	return nil
}

// addSelfCandidateBlock adds the self-candidate block for userset self-referencing.
func (b *ListObjectsBuilder) addSelfCandidateBlock() error {
	selfSQL, err := ListObjectsSelfCandidateQuery(ListObjectsSelfCandidateInput{
		ObjectType:    b.analysis.ObjectType,
		Relation:      b.analysis.Relation,
		ClosureValues: b.inline.ClosureValues,
	})
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, QueryBlock{
		Comments: []string{
			"-- Self-candidate: when subject is a userset on the same object type",
			"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
			"-- The object 'document:1' should be considered as a candidate",
			"-- No type guard here - validity comes from the closure check below",
			"-- No exclusion checks for self-candidate - this is a structural validity check",
		},
		SQL: selfSQL,
	})
	return nil
}

// =============================================================================
// Rendering
// =============================================================================

// renderFunction renders the final function SQL from the built blocks.
func (b *ListObjectsBuilder) renderFunction() (string, error) {
	query := RenderUnionBlocks(b.blocks)
	return buildListObjectsFunctionSQL(b.functionName, b.analysis, query), nil
}

// =============================================================================
// ListSubjectsBuilder - Feature-driven list_subjects function generation
// =============================================================================

// ListSubjectsBuilder generates list_subjects functions using a feature-driven approach.
// Unlike list_objects, list_subjects has TWO code paths:
// 1. Userset filter path - when p_subject_type contains '#' (e.g., "group#member")
// 2. Regular path - when p_subject_type is a simple type (e.g., "user")
type ListSubjectsBuilder struct {
	analysis RelationAnalysis
	inline   InlineSQLData

	// Computed data
	functionName           string
	relationList           []string
	allSatisfyingRelations []string
	allowedSubjectTypes    []string
	complexClosure         []string
	excludeWildcard        bool

	// Built blocks - separate for userset filter and regular paths
	usersetFilterBlocks    []QueryBlock
	usersetFilterSelfBlock *QueryBlock
	regularBlocks          []QueryBlock
}

// NewListSubjectsBuilder creates a builder for generating list_subjects functions.
func NewListSubjectsBuilder(a RelationAnalysis, inline InlineSQLData) *ListSubjectsBuilder {
	return &ListSubjectsBuilder{
		analysis:               a,
		inline:                 inline,
		functionName:           listSubjectsFunctionName(a.ObjectType, a.Relation),
		relationList:           buildTupleLookupRelations(a),
		allSatisfyingRelations: buildAllSatisfyingRelationsList(a),
		allowedSubjectTypes:    buildAllowedSubjectTypesList(a),
		complexClosure:         filterComplexClosureRelations(a),
		excludeWildcard:        !a.Features.HasWildcard,
	}
}

// Build generates the complete list_subjects function SQL.
func (b *ListSubjectsBuilder) Build() (string, error) {
	// Check for special delegated cases first
	// Order matters! Must match selectListSubjectsTemplate priority:
	// 1. Intersection (most comprehensive, handles all patterns)
	// 2. Recursive/TTU (handles direct, userset, exclusion, TTU)
	if b.hasComplexIntersection() {
		return generateListSubjectsIntersectionFunction(b.analysis, b.inline)
	}
	if b.hasRecursiveParent() {
		return generateListSubjectsRecursiveFunction(b.analysis, b.inline)
	}

	// Feature-driven block building for userset filter path
	if err := b.buildUsersetFilterPath(); err != nil {
		return "", err
	}

	// Feature-driven block building for regular path
	if err := b.buildRegularPath(); err != nil {
		return "", err
	}

	return b.renderFunction()
}

// =============================================================================
// Feature Detection Methods (ListSubjectsBuilder)
// =============================================================================

// hasRecursiveParent returns true if the relation has recursive TTU patterns.
func (b *ListSubjectsBuilder) hasRecursiveParent() bool {
	return len(b.analysis.ClosureParentRelations) > 0 || b.analysis.Features.HasRecursive
}

// hasComplexIntersection returns true if the relation has intersection patterns.
func (b *ListSubjectsBuilder) hasComplexIntersection() bool {
	return b.analysis.Features.HasIntersection
}

// hasUsersetPatterns returns true if there are userset patterns to expand.
func (b *ListSubjectsBuilder) hasUsersetPatterns() bool {
	return b.analysis.Features.HasUserset || len(b.analysis.ClosureUsersetPatterns) > 0
}

// =============================================================================
// Userset Filter Path Building (when p_subject_type contains '#')
// =============================================================================

// buildUsersetFilterPath builds all blocks for the userset filter code path.
func (b *ListSubjectsBuilder) buildUsersetFilterPath() error {
	if b.hasUsersetPatterns() {
		// Userset template: use check_permission for validation
		return b.buildUsersetFilterPathUserset()
	}
	if b.analysis.Features.HasExclusion {
		return b.buildUsersetFilterPathExclusion()
	}
	return b.buildUsersetFilterPathDirect()
}

// buildUsersetFilterPathDirect builds userset filter blocks for direct template.
func (b *ListSubjectsBuilder) buildUsersetFilterPathDirect() error {
	// Direct tuple lookup
	usersetBaseSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
		ObjectType:          b.analysis.ObjectType,
		RelationList:        b.relationList,
		AllowedSubjectTypes: b.allowedSubjectTypes,
		ObjectIDExpr:        ObjectID,
		FilterTypeExpr:      ParamRef("v_filter_type"),
		FilterRelationExpr:  ParamRef("v_filter_relation"),
		ClosureValues:       b.inline.ClosureValues,
		UseTypeGuard:        true,
	})
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, QueryBlock{
		Comments: []string{
			"-- Direct tuple lookup with simple closure relations",
			"-- Normalize results to use the filter relation (e.g., group:1#admin -> group:1#member if admin implies member)",
			"-- Type guard: only return results if filter type is in allowed subject types",
		},
		SQL: usersetBaseSQL,
	})

	// Complex closure blocks
	filterBlocks, err := buildListSubjectsComplexClosureFilterBlocks(
		b.analysis,
		b.complexClosure,
		b.allowedSubjectTypes,
		b.inline.ClosureValues,
		false,
	)
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, filterBlocks...)

	// Intersection closure blocks
	filterUsersetExpr := Concat{Parts: []Expr{ParamRef("v_filter_type"), Lit("#"), ParamRef("v_filter_relation")}}
	intersectionBlocks, err := buildListSubjectsIntersectionBlocks(
		b.analysis,
		false,
		filterUsersetExpr,
		nil,
	)
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, intersectionBlocks...)

	// Self-candidate block
	selfBlock, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         b.analysis.ObjectType,
		Relation:           b.analysis.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
		ClosureValues:      b.inline.ClosureValues,
	})
	if err != nil {
		return err
	}
	b.usersetFilterSelfBlock = &QueryBlock{
		Comments: []string{
			"-- Self-candidate: when filter type matches object type",
			"-- e.g., querying document:1.viewer with filter document#writer",
			"-- should return document:1#writer if writer satisfies the relation",
			"-- No type guard here - validity comes from the closure check below",
		},
		SQL: selfBlock,
	}

	return nil
}

// buildUsersetFilterPathExclusion builds userset filter blocks for exclusion template.
func (b *ListSubjectsBuilder) buildUsersetFilterPathExclusion() error {
	usersetNormalized := UsersetNormalized{Source: Col{Table: "t", Column: "subject_id"}, Relation: ParamRef("v_filter_relation")}
	usersetExclusions := buildExclusionInput(b.analysis, ObjectID, ParamRef("v_filter_type"), usersetNormalized)

	usersetPreds := usersetExclusions.BuildPredicates()
	usersetPredsSQL := RenderDSLExprs(usersetPreds)
	usersetBaseSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
		ObjectType:          b.analysis.ObjectType,
		RelationList:        b.relationList,
		AllowedSubjectTypes: b.allowedSubjectTypes,
		ObjectIDExpr:        ObjectID,
		FilterTypeExpr:      ParamRef("v_filter_type"),
		FilterRelationExpr:  ParamRef("v_filter_relation"),
		ClosureValues:       b.inline.ClosureValues,
		UseTypeGuard:        true,
		ExtraPredicatesSQL:  usersetPredsSQL,
	})
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, QueryBlock{
		Comments: []string{
			"-- Direct tuple lookup with closure-inlined relations",
			"-- Normalize results to use the filter relation (e.g., group:1#admin -> group:1#member if admin implies member)",
			"-- Type guard: only return results if filter type is in allowed subject types",
		},
		SQL: usersetBaseSQL,
	})

	// Complex closure blocks with exclusions
	filterBlocks, err := buildListSubjectsComplexClosureFilterBlocks(
		b.analysis,
		b.complexClosure,
		b.allowedSubjectTypes,
		b.inline.ClosureValues,
		true,
	)
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, filterBlocks...)

	// Intersection closure blocks with validation
	filterUsersetExpr := Concat{Parts: []Expr{ParamRef("v_filter_type"), Lit("#"), ParamRef("v_filter_relation")}}
	intersectionBlocks, err := buildListSubjectsIntersectionBlocks(
		b.analysis,
		true,
		filterUsersetExpr,
		ParamRef("v_filter_type"),
	)
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, intersectionBlocks...)

	// Self-candidate block with exclusions
	selfExclusions := buildExclusionInput(b.analysis, ObjectID, Lit(b.analysis.ObjectType), Concat{Parts: []Expr{ObjectID, Lit("#"), ParamRef("v_filter_relation")}})
	selfPreds := selfExclusions.BuildPredicates()
	selfPredsSQL := RenderDSLExprs(selfPreds)
	selfBlock, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         b.analysis.ObjectType,
		Relation:           b.analysis.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
		ClosureValues:      b.inline.ClosureValues,
		ExtraPredicatesSQL: selfPredsSQL,
	})
	if err != nil {
		return err
	}
	b.usersetFilterSelfBlock = &QueryBlock{
		Comments: []string{
			"-- Self-candidate: when filter type matches object type",
			"-- e.g., querying document:1.viewer with filter document#writer",
			"-- should return document:1#writer if writer satisfies the relation",
			"-- No type guard here - validity comes from the closure check below",
		},
		SQL: selfBlock,
	}

	return nil
}

// buildUsersetFilterPathUserset builds userset filter blocks for userset template.
func (b *ListSubjectsBuilder) buildUsersetFilterPathUserset() error {
	checkExprSQL := CheckPermissionExprDSL("check_permission", "v_filter_type", "t.subject_id", b.analysis.Relation, fmt.Sprintf("'%s'", b.analysis.ObjectType), "p_object_id", true).SQL()
	usersetBaseSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
		ObjectType:         b.analysis.ObjectType,
		RelationList:       b.allSatisfyingRelations,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
		ClosureValues:      b.inline.ClosureValues,
		UseTypeGuard:       false,
		ExtraPredicatesSQL: []string{checkExprSQL},
	})
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, QueryBlock{
		Comments: []string{
			"-- Userset filter: find userset tuples that match and return normalized references",
		},
		SQL: usersetBaseSQL,
	})

	// Intersection closure blocks
	filterUsersetExpr := Concat{Parts: []Expr{ParamRef("v_filter_type"), Lit("#"), ParamRef("v_filter_relation")}}
	intersectionBlocks, err := buildListSubjectsIntersectionBlocks(
		b.analysis,
		false,
		filterUsersetExpr,
		nil,
	)
	if err != nil {
		return err
	}
	b.usersetFilterBlocks = append(b.usersetFilterBlocks, intersectionBlocks...)

	// Self-candidate block
	selfBlock, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         b.analysis.ObjectType,
		Relation:           b.analysis.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
		ClosureValues:      b.inline.ClosureValues,
	})
	if err != nil {
		return err
	}
	b.usersetFilterSelfBlock = &QueryBlock{
		Comments: []string{
			"-- Self-referential userset: when object_type matches filter_type and filter_relation",
			"-- satisfies the requested relation, the userset reference object_id#filter_relation has access",
			"-- e.g., for group:1.member with filter group#member, return 1#member (= group:1#member)",
			"-- NOTE: Exclusions don't apply to self-referential userset checks (structural validity)",
		},
		SQL: selfBlock,
	}

	return nil
}

// =============================================================================
// Regular Path Building (when p_subject_type is a simple type)
// =============================================================================

// buildRegularPath builds all blocks for the regular code path.
func (b *ListSubjectsBuilder) buildRegularPath() error {
	if b.hasUsersetPatterns() {
		return b.buildRegularPathUserset()
	}
	if b.analysis.Features.HasExclusion {
		return b.buildRegularPathExclusion()
	}
	return b.buildRegularPathDirect()
}

// buildRegularPathDirect builds regular blocks for direct template.
func (b *ListSubjectsBuilder) buildRegularPathDirect() error {
	regularBaseSQL, err := ListSubjectsDirectQuery(ListSubjectsDirectInput{
		ObjectType:      b.analysis.ObjectType,
		RelationList:    b.relationList,
		ObjectIDExpr:    ObjectID,
		SubjectTypeExpr: SubjectType,
		ExcludeWildcard: b.excludeWildcard,
		Exclusions:      ExclusionConfig{},
	})
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, QueryBlock{SQL: regularBaseSQL})

	// Complex closure blocks
	complexBlocks, err := buildListSubjectsComplexClosureBlocks(
		b.analysis,
		b.complexClosure,
		SubjectType,
		b.excludeWildcard,
		ExclusionConfig{},
	)
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, complexBlocks...)

	// Intersection closure blocks
	intersectionBlocks, err := buildListSubjectsIntersectionBlocks(
		b.analysis,
		false,
		SubjectType,
		nil,
	)
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, intersectionBlocks...)

	return nil
}

// buildRegularPathExclusion builds regular blocks for exclusion template.
func (b *ListSubjectsBuilder) buildRegularPathExclusion() error {
	regularExclusions := buildExclusionInput(b.analysis, ObjectID, SubjectType, Col{Table: "t", Column: "subject_id"})
	regularBaseSQL, err := ListSubjectsDirectQuery(ListSubjectsDirectInput{
		ObjectType:      b.analysis.ObjectType,
		RelationList:    b.relationList,
		ObjectIDExpr:    ObjectID,
		SubjectTypeExpr: SubjectType,
		ExcludeWildcard: b.excludeWildcard,
		Exclusions:      regularExclusions,
	})
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, QueryBlock{SQL: regularBaseSQL})

	// Complex closure blocks with exclusions
	complexBlocks, err := buildListSubjectsComplexClosureBlocks(
		b.analysis,
		b.complexClosure,
		SubjectType,
		b.excludeWildcard,
		regularExclusions,
	)
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, complexBlocks...)

	// Intersection closure blocks with validation
	intersectionBlocks, err := buildListSubjectsIntersectionBlocks(
		b.analysis,
		true,
		SubjectType,
		SubjectType,
	)
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, intersectionBlocks...)

	return nil
}

// buildRegularPathUserset builds regular blocks for userset template.
func (b *ListSubjectsBuilder) buildRegularPathUserset() error {
	baseExclusions := buildExclusionInput(b.analysis, ObjectID, SubjectType, Col{Table: "t", Column: "subject_id"})

	// Direct tuple lookup
	regularBaseSQL, err := ListSubjectsDirectQuery(ListSubjectsDirectInput{
		ObjectType:      b.analysis.ObjectType,
		RelationList:    b.relationList,
		ObjectIDExpr:    ObjectID,
		SubjectTypeExpr: SubjectType,
		ExcludeWildcard: b.excludeWildcard,
		Exclusions:      baseExclusions,
	})
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, QueryBlock{
		Comments: []string{
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		SQL: regularBaseSQL,
	})

	// Complex closure blocks
	complexBlocks, err := buildListSubjectsComplexClosureBlocks(
		b.analysis,
		b.complexClosure,
		SubjectType,
		b.excludeWildcard,
		baseExclusions,
	)
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, complexBlocks...)

	// Intersection closure blocks
	intersectionBlocks, err := buildListSubjectsIntersectionBlocks(
		b.analysis,
		false,
		SubjectType,
		nil,
	)
	if err != nil {
		return err
	}
	b.regularBlocks = append(b.regularBlocks, intersectionBlocks...)

	// Userset pattern expansion blocks
	for _, pattern := range buildListUsersetPatternInputs(b.analysis) {
		if pattern.IsComplex {
			patternSQL, err := ListSubjectsUsersetPatternComplexQuery(ListSubjectsUsersetPatternComplexInput{
				ObjectType:       b.analysis.ObjectType,
				SubjectType:      pattern.SubjectType,
				SubjectRelation:  pattern.SubjectRelation,
				SourceRelations:  pattern.SourceRelations,
				ObjectIDExpr:     ObjectID,
				SubjectTypeExpr:  SubjectType,
				IsClosurePattern: pattern.IsClosurePattern,
				SourceRelation:   pattern.SourceRelation,
				Exclusions:       baseExclusions,
			})
			if err != nil {
				return err
			}
			b.regularBlocks = append(b.regularBlocks, QueryBlock{
				Comments: []string{
					fmt.Sprintf("-- Path: Via %s#%s - expand group membership to return individual subjects", pattern.SubjectType, pattern.SubjectRelation),
					"-- Complex userset: use LATERAL join with userset's list_subjects function",
					"-- This handles userset-to-userset chains where there are no direct subject tuples",
				},
				SQL: patternSQL,
			})
			continue
		}

		patternSQL, err := ListSubjectsUsersetPatternSimpleQuery(ListSubjectsUsersetPatternSimpleInput{
			ObjectType:          b.analysis.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			ObjectIDExpr:        ObjectID,
			SubjectTypeExpr:     SubjectType,
			AllowedSubjectTypes: b.allowedSubjectTypes,
			ExcludeWildcard:     b.excludeWildcard,
			IsClosurePattern:    pattern.IsClosurePattern,
			SourceRelation:      pattern.SourceRelation,
			Exclusions:          baseExclusions,
		})
		if err != nil {
			return err
		}
		b.regularBlocks = append(b.regularBlocks, QueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Path: Via %s#%s - expand group membership to return individual subjects", pattern.SubjectType, pattern.SubjectRelation),
				"-- Simple userset: JOIN with membership tuples",
			},
			SQL: patternSQL,
		})
	}

	return nil
}

// =============================================================================
// Rendering (ListSubjectsBuilder)
// =============================================================================

// renderFunction renders the final function SQL from the built blocks.
func (b *ListSubjectsBuilder) renderFunction() (string, error) {
	templateName := b.determineTemplateName()
	return buildListSubjectsFunctionSQL(b.functionName, b.analysis, b.usersetFilterBlocks, b.usersetFilterSelfBlock, b.regularBlocks, templateName), nil
}

// determineTemplateName returns the template name based on features.
// This is needed for renderUsersetWildcardTail to know how to render the wildcard handling.
func (b *ListSubjectsBuilder) determineTemplateName() string {
	if b.hasUsersetPatterns() {
		return "list_subjects_userset.tpl.sql"
	}
	if b.analysis.Features.HasExclusion {
		return "list_subjects_exclusion.tpl.sql"
	}
	return "list_subjects_direct.tpl.sql"
}

func buildListObjectsComplexClosureBlocks(a RelationAnalysis, relations, allowedSubjectTypes []string, allowWildcard bool, exclusions ExclusionConfig) ([]QueryBlock, error) {
	var blocks []QueryBlock
	for _, rel := range relations {
		blockSQL, err := ListObjectsComplexClosureQuery(ListObjectsComplexClosureInput{
			ObjectType:          a.ObjectType,
			Relation:            rel,
			AllowedSubjectTypes: allowedSubjectTypes,
			AllowWildcard:       allowWildcard,
			Exclusions:          exclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, QueryBlock{
			Comments: []string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
				"-- These relations have exclusions or other complex features that require full permission check",
			},
			SQL: blockSQL,
		})
	}
	return blocks, nil
}

func buildListObjectsIntersectionBlocks(a RelationAnalysis, validate bool) ([]QueryBlock, error) {
	var blocks []QueryBlock
	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_objects", a.ObjectType, rel)
		var blockSQL string
		var err error
		if validate {
			blockSQL, err = ListObjectsIntersectionClosureValidatedQuery(a.ObjectType, a.Relation, functionName)
		} else {
			blockSQL, err = ListObjectsIntersectionClosureQuery(functionName)
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, QueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			SQL: blockSQL,
		})
	}
	return blocks, nil
}

func buildListSubjectsComplexClosureFilterBlocks(a RelationAnalysis, relations, allowedSubjectTypes []string, closureValues string, applyExclusions bool) ([]QueryBlock, error) {
	var blocks []QueryBlock
	normalizedExpr := UsersetNormalized{Source: Col{Table: "t", Column: "subject_id"}, Relation: ParamRef("v_filter_relation")}
	for _, rel := range relations {
		exclusions := ExclusionConfig{}
		if applyExclusions {
			exclusions = buildExclusionInput(a, ObjectID, Col{Table: "t", Column: "subject_type"}, normalizedExpr)
		}
		exclusionPreds := exclusions.BuildPredicates()
		checkPred := CheckPermissionInternalExprDSL("t.subject_type", "t.subject_id", rel, fmt.Sprintf("'%s'", a.ObjectType), "p_object_id", true)
		allPreds := append(exclusionPreds, checkPred) //nolint:gocritic // intentionally creating new slice
		extraPredsSQL := RenderDSLExprs(allPreds)
		blockSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
			ObjectType:          a.ObjectType,
			RelationList:        []string{rel},
			AllowedSubjectTypes: allowedSubjectTypes,
			ObjectIDExpr:        ObjectID,
			FilterTypeExpr:      ParamRef("v_filter_type"),
			FilterRelationExpr:  ParamRef("v_filter_relation"),
			ClosureValues:       closureValues,
			UseTypeGuard:        true,
			ExtraPredicatesSQL:  extraPredsSQL,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, QueryBlock{
			Comments: []string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
			},
			SQL: blockSQL,
		})
	}
	return blocks, nil
}

func buildListSubjectsComplexClosureBlocks(a RelationAnalysis, relations []string, subjectTypeExpr Expr, excludeWildcard bool, exclusions ExclusionConfig) ([]QueryBlock, error) {
	var blocks []QueryBlock
	for _, rel := range relations {
		blockSQL, err := ListSubjectsComplexClosureQuery(ListSubjectsComplexClosureInput{
			ObjectType:      a.ObjectType,
			Relation:        rel,
			ObjectIDExpr:    ObjectID,
			SubjectTypeExpr: subjectTypeExpr,
			ExcludeWildcard: excludeWildcard,
			Exclusions:      exclusions,
		})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, QueryBlock{
			Comments: []string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
			},
			SQL: blockSQL,
		})
	}
	return blocks, nil
}

func buildListSubjectsIntersectionBlocks(a RelationAnalysis, validate bool, functionSubjectTypeExpr, checkSubjectTypeExpr Expr) ([]QueryBlock, error) {
	var blocks []QueryBlock
	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		var blockSQL string
		var err error
		if validate {
			blockSQL, err = ListSubjectsIntersectionClosureValidatedQuery(a.ObjectType, a.Relation, functionName, functionSubjectTypeExpr, checkSubjectTypeExpr, ObjectID)
		} else {
			blockSQL, err = ListSubjectsIntersectionClosureQuery(functionName, functionSubjectTypeExpr)
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, QueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			SQL: blockSQL,
		})
	}
	return blocks, nil
}

func buildListObjectsFunctionSQL(functionName string, a RelationAnalysis, query string) string {
	paginatedQuery := wrapWithPagination(query, "object_id")
	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT) AS $$
BEGIN
    RETURN QUERY
    %s;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		paginatedQuery,
	)
}

func buildListSubjectsFunctionSQL(functionName string, a RelationAnalysis, usersetFilterBlocks []QueryBlock, usersetFilterSelfBlock *QueryBlock, regularBlocks []QueryBlock, templateName string) string {
	// Build userset filter path query (when p_subject_type contains '#')
	var usersetFilterPaginatedQuery string
	if len(usersetFilterBlocks) > 0 {
		parts := append([]QueryBlock{}, usersetFilterBlocks...)
		if usersetFilterSelfBlock != nil {
			parts = append(parts, *usersetFilterSelfBlock)
		}
		usersetFilterQuery := RenderUnionBlocks(parts)
		usersetFilterPaginatedQuery = wrapWithPaginationWildcardFirst(usersetFilterQuery)
	}

	regularQuery := RenderUnionBlocks(regularBlocks)
	regularTypeGuard := ""
	if templateName != "list_subjects_userset.tpl.sql" {
		regularTypeGuard = fmt.Sprintf(`
        -- Guard: return empty if subject type is not allowed by the model
        IF p_subject_type NOT IN (%s) THEN
            RETURN;
        END IF;
`, formatSQLStringList(buildAllowedSubjectTypesList(a)))
	}

	// Build regular path query with pagination
	var regularReturn string
	if templateName == "list_subjects_userset.tpl.sql" {
		// For userset template, wrap the entire CTE construct as a subquery
		// to avoid CTE name collision with pagination wrapper
		innerQuery := fmt.Sprintf(`SELECT iq.subject_id FROM (
            WITH inner_base AS (
%s
            ),
            has_wildcard AS (
                SELECT EXISTS (SELECT 1 FROM inner_base ib WHERE ib.subject_id = '*') AS has_wildcard
            )
%s
        ) AS iq`, indentLines(regularQuery, "            "), renderUsersetWildcardTailRenamed(a))
		paginatedQuery := wrapWithPaginationWildcardFirst(innerQuery)
		regularReturn = fmt.Sprintf(`
        RETURN QUERY
        %s;`, paginatedQuery)
	} else {
		paginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)
		regularReturn = fmt.Sprintf(`
        -- Regular subject type (no userset filter)
        RETURN QUERY
        %s;`, paginatedQuery)
	}

	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT) AS $$
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
		usersetFilterPaginatedQuery,
		regularTypeGuard,
		regularReturn,
	)
}

// renderUsersetWildcardTailRenamed is like renderUsersetWildcardTail but uses inner_base instead of base_results
// to avoid CTE name collision when wrapping with pagination.
func renderUsersetWildcardTailRenamed(a RelationAnalysis) string {
	if a.Features.HasWildcard {
		return fmt.Sprintf(`
            -- Wildcard handling: when wildcard exists, filter non-wildcard subjects
            -- to only those with explicit (non-wildcard-derived) access
            SELECT ib.subject_id
            FROM inner_base ib
            CROSS JOIN has_wildcard hw
            WHERE (NOT hw.has_wildcard)
               OR (ib.subject_id = '*')
               OR (
                   ib.subject_id != '*'
                   AND check_permission_no_wildcard(
                       p_subject_type,
                       ib.subject_id,
                       '%s',
                       '%s',
                       p_object_id
                   ) = 1
               )`, a.Relation, a.ObjectType)
	}

	return "            SELECT ib.subject_id FROM inner_base ib"
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
	var complexRels []string
	for _, rel := range a.ComplexClosureRelations {
		if !intersectionSet[rel] {
			complexRels = append(complexRels, rel)
		}
	}
	return complexRels
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
			IsSelfReferential:   p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
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
			IsSelfReferential:   p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
		})
	}

	return patterns
}

func buildExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
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

func convertParentRelations(relations []ParentRelationInfo) []ExcludedParentRelation {
	if len(relations) == 0 {
		return nil
	}
	result := make([]ExcludedParentRelation, 0, len(relations))
	for _, rel := range relations {
		result = append(result, ExcludedParentRelation(rel))
	}
	return result
}

func convertIntersectionGroups(groups []IntersectionGroupInfo) []ExcludedIntersectionGroup {
	if len(groups) == 0 {
		return nil
	}
	result := make([]ExcludedIntersectionGroup, 0, len(groups))
	for _, group := range groups {
		parts := make([]ExcludedIntersectionPart, 0, len(group.Parts))
		for _, part := range group.Parts {
			if part.ParentRelation != nil {
				parts = append(parts, ExcludedIntersectionPart{
					ParentRelation: &ExcludedParentRelation{
						Relation:            part.ParentRelation.Relation,
						LinkingRelation:     part.ParentRelation.LinkingRelation,
						AllowedLinkingTypes: part.ParentRelation.AllowedLinkingTypes,
					},
				})
				continue
			}
			parts = append(parts, ExcludedIntersectionPart{
				Relation:         part.Relation,
				ExcludedRelation: part.ExcludedRelation,
			})
		}
		result = append(result, ExcludedIntersectionGroup{Parts: parts})
	}
	return result
}

// formatQueryBlock formats a query block with comments and indentation.
// Deprecated: Use QueryBlock and RenderBlocks/RenderUnionBlocks in sql.go instead.
// This function is retained for the delegated recursive/intersection generators.
func formatQueryBlock(comments []string, sql string) string {
	lines := make([]string, 0, len(comments)+1)
	for _, comment := range comments {
		lines = append(lines, "    "+comment)
	}
	lines = append(lines, indentLines(sql, "    "))
	return strings.Join(lines, "\n")
}

// joinUnionBlocks joins formatted query blocks with UNION.
// Deprecated: Use RenderUnionBlocks in sql.go instead.
// This function is retained for the delegated recursive/intersection generators.
func joinUnionBlocks(blocks []string) string {
	return strings.Join(blocks, "\n    UNION\n")
}

// =============================================================================
// Pagination Helpers
// =============================================================================

// wrapWithPagination wraps a query in pagination CTEs for list_objects functions.
// Returns a complete SQL query that implements cursor-based pagination with:
// - p_limit: maximum number of results to return (NULL = no limit)
// - p_after: cursor from previous page (NULL = start from beginning)
// - next_cursor: returned with each row, NULL when no more pages
func wrapWithPagination(query, idColumn string) string {
	return fmt.Sprintf(`WITH base_results AS (
%s
    ),
    paged AS (
        SELECT br.%s
        FROM base_results br
        WHERE (p_after IS NULL OR br.%s > p_after)
        ORDER BY br.%s
        LIMIT CASE WHEN p_limit IS NULL THEN NULL ELSE p_limit + 1 END
    ),
    returned AS (
        SELECT p.%s FROM paged p ORDER BY p.%s LIMIT p_limit
    ),
    next AS (
        SELECT CASE
            WHEN p_limit IS NOT NULL AND (SELECT count(*) FROM paged) > p_limit
            THEN (SELECT max(r.%s) FROM returned r)
        END AS next_cursor
    )
    SELECT r.%s, n.next_cursor
    FROM returned r
    CROSS JOIN next n`,
		indentLines(query, "        "), idColumn, idColumn, idColumn,
		idColumn, idColumn, idColumn, idColumn)
}

// wrapWithPaginationWildcardFirst wraps a query for list_subjects with wildcard-first ordering.
// Wildcards ('*') are sorted before all other subject IDs to ensure consistent pagination.
// Uses a compound sort key: (is_not_wildcard, subject_id) where is_not_wildcard is 0 for '*', 1 otherwise.
func wrapWithPaginationWildcardFirst(query string) string {
	return fmt.Sprintf(`WITH base_results AS (
%s
    ),
    paged AS (
        SELECT br.subject_id
        FROM base_results br
        WHERE p_after IS NULL OR (
            -- Compound comparison for wildcard-first ordering:
            -- (is_not_wildcard, subject_id) > (cursor_is_not_wildcard, cursor)
            (CASE WHEN br.subject_id = '*' THEN 0 ELSE 1 END, br.subject_id) >
            (CASE WHEN p_after = '*' THEN 0 ELSE 1 END, p_after)
        )
        ORDER BY (CASE WHEN br.subject_id = '*' THEN 0 ELSE 1 END), br.subject_id
        LIMIT CASE WHEN p_limit IS NULL THEN NULL ELSE p_limit + 1 END
    ),
    returned AS (
        SELECT p.subject_id FROM paged p
        ORDER BY (CASE WHEN p.subject_id = '*' THEN 0 ELSE 1 END), p.subject_id
        LIMIT p_limit
    ),
    next AS (
        SELECT CASE
            WHEN p_limit IS NOT NULL AND (SELECT count(*) FROM paged) > p_limit
            THEN (SELECT r.subject_id FROM returned r
                  ORDER BY (CASE WHEN r.subject_id = '*' THEN 0 ELSE 1 END) DESC, r.subject_id DESC
                  LIMIT 1)
        END AS next_cursor
    )
    SELECT r.subject_id, n.next_cursor
    FROM returned r
    CROSS JOIN next n`,
		indentLines(query, "        "))
}

func generateListObjectsRecursiveFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
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
		recursiveExclusions := buildExclusionInput(a, Col{Table: "child", Column: "object_id"}, SubjectType, SubjectID)
		recursiveSQL, err := ListObjectsRecursiveTTUQuery(ListObjectsRecursiveTTUInput{
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

	selfCandidateSQL, err := ListObjectsSelfCandidateQuery(ListObjectsSelfCandidateInput{
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
	paginatedQuery := wrapWithPagination(query, "object_id")
	functionSQL := fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT) AS $$
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
		paginatedQuery,
	)

	return functionSQL, nil
}

func buildListObjectsRecursiveBaseBlocks(a RelationAnalysis, inline InlineSQLData, relationList, allowedSubjectTypes []string, allowWildcard bool, complexClosure []string) ([]string, error) {
	var blocks []string
	baseExclusions := buildExclusionInput(a, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)

	directSQL, err := ListObjectsDirectQuery(ListObjectsDirectInput{
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
		complexSQL, err := ListObjectsComplexClosureQuery(ListObjectsComplexClosureInput{
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
		closureSQL, err := ListObjectsIntersectionClosureQuery(functionName)
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
			patternSQL, err := ListObjectsUsersetPatternComplexQuery(ListObjectsUsersetPatternComplexInput{
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

		patternSQL, err := ListObjectsUsersetPatternSimpleQuery(ListObjectsUsersetPatternSimpleInput{
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
		crossExclusions := buildExclusionInput(a, Col{Table: "child", Column: "object_id"}, SubjectType, SubjectID)
		crossSQL, err := ListObjectsCrossTypeTTUQuery(ListObjectsCrossTypeTTUInput{
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

	finalExclusions := buildExclusionInput(a, Col{Table: "acc", Column: "object_id"}, SubjectType, SubjectID)
	exclusionPreds := finalExclusions.BuildPredicates()

	var whereExpr Expr
	if len(exclusionPreds) > 0 {
		// Prepend TRUE to ensure valid AND expression when there are exclusions
		allPreds := append([]Expr{Bool(true)}, exclusionPreds...)
		whereExpr = And(allPreds...)
	}

	finalStmt := SelectStmt{
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

func wrapQueryWithDepth(sql, depthExpr, alias string) string {
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

func generateListObjectsIntersectionFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
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
		standaloneExclusions := buildSimpleComplexExclusionInput(a, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)
		directSQL, err := ListObjectsDirectQuery(ListObjectsDirectInput{
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
			complexSQL, err := ListObjectsComplexClosureQuery(ListObjectsComplexClosureInput{
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
			closureSQL, err := ListObjectsIntersectionClosureQuery(functionName)
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
				patternSQL, err := ListObjectsUsersetPatternComplexQuery(ListObjectsUsersetPatternComplexInput{
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

			patternSQL, err := ListObjectsUsersetPatternSimpleQuery(ListObjectsUsersetPatternSimpleInput{
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
			crossExclusions := buildSimpleComplexExclusionInput(a, Col{Table: "child", Column: "object_id"}, SubjectType, SubjectID)
			crossSQL, err := ListObjectsCrossTypeTTUQuery(ListObjectsCrossTypeTTUInput{
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

	selfCandidateSQL, err := ListObjectsSelfCandidateQuery(ListObjectsSelfCandidateInput{
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
	paginatedQuery := wrapWithPagination(query, "object_id")
	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT) AS $$
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
		paginatedQuery,
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

	exclusions := buildSimpleComplexExclusionInput(a, Raw(fmt.Sprintf("ig_%d.object_id", idx)), SubjectType, SubjectID)
	exclusionPreds := exclusions.BuildPredicates()
	if len(exclusionPreds) == 0 {
		return groupSQL, nil
	}

	groupSQL = groupSQL + "\n    WHERE " + And(exclusionPreds...).SQL()
	return groupSQL, nil
}

func buildObjectsIntersectionPartSQL(a RelationAnalysis, partIdx int, part IntersectionPart) (string, error) {
	switch {
	case part.IsThis:
		q := Tuples("t").
			ObjectType(a.ObjectType).
			Relations(a.Relation).
			Select("t.object_id").
			WhereSubjectType(SubjectType).
			WhereSubjectID(SubjectID, part.HasWildcard).
			Distinct()

		if part.ExcludedRelation != "" {
			q = q.Where(CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ExcludedRelation, fmt.Sprintf("'%s'", a.ObjectType), "t.object_id", false))
		}
		return q.SQL(), nil
	case part.ParentRelation != nil:
		q := Tuples("child").
			ObjectType(a.ObjectType).
			Relations(part.ParentRelation.LinkingRelation).
			Select("child.object_id").
			Where(CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ParentRelation.Relation, "child.subject_type", "child.subject_id", true)).
			Distinct()

		if part.ExcludedRelation != "" {
			q = q.Where(CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ExcludedRelation, fmt.Sprintf("'%s'", a.ObjectType), "child.object_id", false))
		}
		return q.SQL(), nil
	default:
		q := Tuples("t").
			ObjectType(a.ObjectType).
			Select("t.object_id").
			Where(CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.Relation, fmt.Sprintf("'%s'", a.ObjectType), "t.object_id", true)).
			Distinct()

		if part.ExcludedRelation != "" {
			q = q.Where(CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", part.ExcludedRelation, fmt.Sprintf("'%s'", a.ObjectType), "t.object_id", false))
		}
		return q.SQL(), nil
	}
}

func buildObjectsIntersectionRecursiveSQL(a RelationAnalysis, relationList, allowedSubjectTypes, selfRefRelations []string, hasStandalone bool) (string, error) {
	var seedBlocks []string
	if hasStandalone && len(relationList) > 0 {
		q := Tuples("t").
			ObjectType(a.ObjectType).
			Relations(relationList...).
			Select("t.object_id").
			WhereSubjectType(SubjectType).
			Where(In{Expr: SubjectType, Values: allowedSubjectTypes}).
			WhereSubjectID(SubjectID, a.Features.HasWildcard)
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

func buildSimpleComplexExclusionInput(a RelationAnalysis, objectIDExpr, subjectTypeExpr, subjectIDExpr Expr) ExclusionConfig {
	return ExclusionConfig{
		ObjectType:               a.ObjectType,
		ObjectIDExpr:             objectIDExpr,
		SubjectTypeExpr:          subjectTypeExpr,
		SubjectIDExpr:            subjectIDExpr,
		SimpleExcludedRelations:  a.SimpleExcludedRelations,
		ComplexExcludedRelations: a.ComplexExcludedRelations,
	}
}

func generateListSubjectsRecursiveFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
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
	usersetFilterQuery := joinUnionBlocks(append(usersetFilterBlocks, usersetSelfBlock))
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)
	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT) AS $$
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
		usersetFilterPaginatedQuery,
		regularPaginatedQuery,
	), nil
}

func buildListSubjectsRecursiveUsersetFilterBlocks(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string) (filterBlocks []string, selfBlock string, err error) {
	var blocks []string
	checkExprSQL := CheckPermissionExprDSL("check_permission", "v_filter_type", "t.subject_id", a.Relation, fmt.Sprintf("'%s'", a.ObjectType), "p_object_id", true).SQL()
	baseSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
		ObjectType:         a.ObjectType,
		RelationList:       allSatisfyingRelations,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
		ClosureValues:      inline.ClosureValues,
		UseTypeGuard:       false,
		ExtraPredicatesSQL: []string{checkExprSQL},
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

	filterUsersetExpr := Concat{Parts: []Expr{ParamRef("v_filter_type"), Lit("#"), ParamRef("v_filter_relation")}}
	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		closureSQL, err := ListSubjectsIntersectionClosureQuery(functionName, filterUsersetExpr)
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

	selfSQL, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         a.ObjectType,
		Relation:           a.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
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
	baseExclusions := buildExclusionInput(a, ObjectID, SubjectType, Col{Table: "t", Column: "subject_id"})

	subjectPoolSQL, err := buildSubjectPoolSQL(allowedSubjectTypes, excludeWildcard)
	if err != nil {
		return "", err
	}

	var baseBlocks []string
	directSQL, err := ListSubjectsDirectQuery(ListSubjectsDirectInput{
		ObjectType:      a.ObjectType,
		RelationList:    relationList,
		ObjectIDExpr:    ObjectID,
		SubjectTypeExpr: SubjectType,
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
		complexSQL, err := ListSubjectsComplexClosureQuery(ListSubjectsComplexClosureInput{
			ObjectType:      a.ObjectType,
			Relation:        rel,
			ObjectIDExpr:    ObjectID,
			SubjectTypeExpr: SubjectType,
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
		closureSQL, err := ListSubjectsIntersectionClosureQuery(functionName, SubjectType)
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
		usersetExclusions := buildExclusionInput(a, ObjectID, Col{Table: "m", Column: "subject_type"}, Col{Table: "m", Column: "subject_id"})
		simpleUsersetExclusions := buildExclusionInput(a, ObjectID, Col{Table: "s", Column: "subject_type"}, Col{Table: "s", Column: "subject_id"})
		if pattern.IsComplex {
			patternSQL, err := ListSubjectsUsersetPatternRecursiveComplexQuery(ListSubjectsUsersetPatternRecursiveComplexInput{
				ObjectType:          a.ObjectType,
				SubjectType:         pattern.SubjectType,
				SubjectRelation:     pattern.SubjectRelation,
				SourceRelations:     pattern.SourceRelations,
				ObjectIDExpr:        ObjectID,
				SubjectTypeExpr:     SubjectType,
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

		patternSQL, err := ListSubjectsUsersetPatternSimpleQuery(ListSubjectsUsersetPatternSimpleInput{
			ObjectType:          a.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			ObjectIDExpr:        ObjectID,
			SubjectTypeExpr:     SubjectType,
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
	q := Tuples("t").
		Select("t.subject_id").
		WhereSubjectType(SubjectType).
		Where(In{Expr: SubjectType, Values: allowedSubjectTypes}).
		Distinct()

	if excludeWildcard {
		q = q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	return q.SQL(), nil
}

func buildSubjectsTTUPathQuery(a RelationAnalysis, parent ListParentRelationData) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		CheckPermissionInternalExprDSL("p_subject_type", "sp.subject_id", parent.Relation, "link.subject_type", "link.subject_id", true),
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	exclusions := buildExclusionInput(a, ObjectID, SubjectType, Col{Table: "sp", Column: "subject_id"})
	exclusionPreds := exclusions.BuildPredicates()
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"sp.subject_id"},
		From:     "subject_pool",
		Alias:    "sp",
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "melange_tuples",
			Alias: "link",
			On:    Bool(true), // CROSS JOIN has ON TRUE (always matches)
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterTTUQuery(a RelationAnalysis, inline InlineSQLData, parent ListParentRelationData) (string, error) {
	closureRelStmt := SelectStmt{
		Columns: []string{"c.satisfying_relation"},
		From:    fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", inline.ClosureValues),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Raw("link.subject_type")},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
		),
	}
	closureRelSQL := closureRelStmt.SQL()

	closureExistsStmt := SelectStmt{
		Columns: []string{"1"},
		From:    fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", inline.ClosureValues),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Raw("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)")},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Raw("v_filter_relation")},
		),
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Raw("v_filter_type")},
		Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
		Or(
			Eq{Left: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)"), Right: Raw("v_filter_relation")},
			Exists{Query: closureExistsStmt},
		),
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Raw("link.subject_type")},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Raw("link.subject_id")},
				Raw("pt.relation IN ("+closureRelSQL+")"),
			),
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterTTUIntermediateQuery(a RelationAnalysis, inline InlineSQLData, parent ListParentRelationData) (string, error) {
	closureExistsStmt := SelectStmt{
		Columns: []string{"1"},
		From:    fmt.Sprintf("(VALUES %s) AS c(object_type, relation, satisfying_relation)", inline.ClosureValues),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Raw("link.subject_type")},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Raw("v_filter_relation")},
		),
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Raw("link.subject_type"), Right: Raw("v_filter_type")},
		Exists{Query: closureExistsStmt},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"link.subject_id || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Where:    And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterTTUNestedQuery(objectType string, parent ListParentRelationData) (string, error) {
	lateralCall := fmt.Sprintf("LATERAL list_accessible_subjects(link.subject_type, link.subject_id, '%s', p_subject_type)", parent.Relation)

	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(objectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := SelectStmt{
		Columns: []string{"nested.subject_id"},
		From:    "melange_tuples",
		Alias:   "link",
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: lateralCall,
			Alias: "nested",
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func generateListSubjectsIntersectionFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	allSatisfyingRelations := buildAllSatisfyingRelationsList(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	excludeWildcard := !a.Features.HasWildcard

	usersetCandidatesSQL, err := buildUsersetIntersectionCandidates(a, inline, allSatisfyingRelations)
	if err != nil {
		return "", err
	}
	usersetSelfSQL, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         a.ObjectType,
		Relation:           a.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
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

	// Build userset filter query
	usersetFilterQuery := fmt.Sprintf(`WITH userset_candidates AS (
%s
        )
        SELECT DISTINCT c.subject_id
        FROM userset_candidates c
        WHERE check_permission(v_filter_type, c.subject_id, '%s', '%s', p_object_id) = 1

        UNION

%s`,
		usersetCandidatesSQL,
		a.Relation,
		a.ObjectType,
		formatQueryBlock(
			[]string{"-- Self-referential userset"},
			usersetSelfSQL,
		),
	)

	regularQuery = trimTrailingSemicolon(regularQuery)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)
	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT) AS $$
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
		usersetFilterPaginatedQuery,
		formatSQLStringList(allowedSubjectTypes),
		regularPaginatedQuery,
	), nil
}

func buildUsersetIntersectionCandidates(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string) (string, error) {
	var blocks []string
	baseSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
		ObjectType:         a.ObjectType,
		RelationList:       allSatisfyingRelations,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
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
		conditions := []Expr{
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(part.ParentRelation.LinkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Raw("v_filter_type")},
			Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
			relationMatch,
		}
		stmt := SelectStmt{
			Distinct: true,
			Columns:  []string{"substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
			From:     "melange_tuples",
			Alias:    "link",
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "pt",
				On: And(
					Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Raw("link.subject_type")},
					Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Raw("link.subject_id")},
				),
			}},
			Where: And(conditions...),
		}
		return stmt.SQL(), nil
	}

	relationMatch := buildUsersetFilterRelationMatchExprDSL("t.subject_id", inline.ClosureValues)
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Raw("v_filter_type")},
		Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
		relationMatch,
	}
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetIntersectionTTUCandidates(a RelationAnalysis, inline InlineSQLData, parent ListParentRelationData) (string, error) {
	relationMatch := buildUsersetFilterRelationMatchExprDSL("pt.subject_id", inline.ClosureValues)
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Raw("v_filter_type")},
		Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
		relationMatch,
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Raw("link.subject_type")},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Raw("link.subject_id")},
			),
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildUsersetFilterRelationMatchExprDSL(subjectIDExpr, closureValues string) Expr {
	closureExistsStmt := SelectStmt{
		Columns: []string{"1"},
		From:    fmt.Sprintf("(VALUES %s) AS subj_c(object_type, relation, satisfying_relation)", closureValues),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Raw("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw("substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)")},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Raw("v_filter_relation")},
		),
	}
	return Or(
		Eq{Left: Raw("substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)"), Right: Raw("v_filter_relation")},
		Exists{Query: closureExistsStmt},
	)
}

func buildRegularIntersectionCandidates(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string, excludeWildcard bool) (string, error) {
	var blocks []string

	// Base query
	baseConditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: allSatisfyingRelations},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		baseConditions = append(baseConditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	baseStmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    And(baseConditions...),
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
		patternSQL, err := ListSubjectsUsersetPatternSimpleQuery(ListSubjectsUsersetPatternSimpleInput{
			ObjectType:          a.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			ObjectIDExpr:        ObjectID,
			SubjectTypeExpr:     SubjectType,
			AllowedSubjectTypes: buildAllowedSubjectTypesList(a),
			ExcludeWildcard:     excludeWildcard,
			IsClosurePattern:    pattern.IsClosurePattern,
			SourceRelation:      pattern.SourceRelation,
			Exclusions:          ExclusionConfig{},
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
	poolConditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		poolConditions = append(poolConditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	poolStmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    And(poolConditions...),
	}
	blocks = append(blocks, poolStmt.SQL())

	return strings.Join(blocks, "\n            UNION\n"), nil
}

func buildRegularIntersectionPartCandidates(a RelationAnalysis, part IntersectionPart, excludeWildcard bool) (string, error) {
	if part.ParentRelation != nil {
		conditions := []Expr{
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(part.ParentRelation.LinkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
		}
		if excludeWildcard {
			conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
		}
		stmt := SelectStmt{
			Distinct: true,
			Columns:  []string{"pt.subject_id"},
			From:     "melange_tuples",
			Alias:    "link",
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "pt",
				On: And(
					Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Raw("link.subject_type")},
					Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Raw("link.subject_id")},
				),
			}},
			Where: And(conditions...),
		}
		return stmt.SQL(), nil
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Where:    And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildRegularIntersectionTTUCandidates(a RelationAnalysis, parent ListParentRelationData, excludeWildcard bool) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
	}
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"pt.subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Raw("link.subject_type")},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Raw("link.subject_id")},
				Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
			),
		}},
		Where: And(conditions...),
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
	return strings.TrimSuffix(trimmed, ";")
}

func generateListObjectsDepthExceededFunction(a RelationAnalysis) string {
	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
-- DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT) AS $$
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

func generateListSubjectsDepthExceededFunction(a RelationAnalysis) string {
	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
-- DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT) AS $$
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

func generateListObjectsSelfRefUsersetFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
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

	selfCandidateSQL, err := ListObjectsSelfCandidateQuery(ListObjectsSelfCandidateInput{
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

	paginatedQuery := wrapWithPagination(query, "object_id")

	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s (self-referential userset)
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT) AS $$
BEGIN
    RETURN QUERY
    %s;
END;
$$ LANGUAGE plpgsql STABLE;`,
		a.ObjectType,
		a.Relation,
		a.Features.String(),
		functionName,
		paginatedQuery,
	), nil
}

func buildListObjectsSelfRefBaseBlocks(a RelationAnalysis, relationList, allowedSubjectTypes []string, allowWildcard bool, complexClosure []string) ([]string, error) {
	var blocks []string
	baseExclusions := buildExclusionInput(a, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)

	directSQL, err := ListObjectsDirectQuery(ListObjectsDirectInput{
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
		complexSQL, err := ListObjectsComplexClosureQuery(ListObjectsComplexClosureInput{
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
		closureSQL, err := ListObjectsIntersectionClosureQuery(functionName)
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
			patternSQL, err := ListObjectsUsersetPatternComplexQuery(ListObjectsUsersetPatternComplexInput{
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

		patternSQL, err := ListObjectsUsersetPatternSimpleQuery(ListObjectsUsersetPatternSimpleInput{
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
	baseExclusions := buildExclusionInput(a, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)
	exclusionPreds := baseExclusions.BuildPredicates()

	conditions := make([]Expr, 0, 6+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: relationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(a.ObjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(a.Relation)},
		Raw("me.depth < 25"),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"t.object_id", "me.depth + 1 AS depth"},
		From:     "member_expansion",
		Alias:    "me",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, Right: Col{Table: "me", Column: "object_id"}},
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildListObjectsSelfRefFinalQuery(a RelationAnalysis) (string, error) {
	finalExclusions := buildExclusionInput(a, Col{Table: "me", Column: "object_id"}, SubjectType, SubjectID)
	exclusionPreds := finalExclusions.BuildPredicates()

	var whereExpr Expr
	if len(exclusionPreds) > 0 {
		whereExpr = And(exclusionPreds...)
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"me.object_id"},
		From:     "member_expansion",
		Alias:    "me",
		Where:    whereExpr,
	}
	return stmt.SQL(), nil
}

func generateListSubjectsSelfRefUsersetFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
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
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s (self-referential userset)
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT) AS $$
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
		usersetFilterPaginatedQuery,
		regularPaginatedQuery,
	), nil
}

func buildListSubjectsSelfRefUsersetFilterQuery(a RelationAnalysis, inline InlineSQLData, allSatisfyingRelations []string) (string, error) {
	checkExprSQL := CheckPermissionExprDSL("check_permission", "v_filter_type", "t.subject_id", a.Relation, fmt.Sprintf("'%s'", a.ObjectType), "p_object_id", true).SQL()
	baseSQL, err := ListSubjectsUsersetFilterQuery(ListSubjectsUsersetFilterInput{
		ObjectType:         a.ObjectType,
		RelationList:       allSatisfyingRelations,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
		ClosureValues:      inline.ClosureValues,
		UseTypeGuard:       false,
		ExtraPredicatesSQL: []string{checkExprSQL},
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

	filterUsersetExpr := Concat{Parts: []Expr{ParamRef("v_filter_type"), Lit("#"), ParamRef("v_filter_relation")}}
	for _, rel := range a.IntersectionClosureRelations {
		functionName := fmt.Sprintf("list_%s_%s_subjects", a.ObjectType, rel)
		closureSQL, err := ListSubjectsIntersectionClosureQuery(functionName, filterUsersetExpr)
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

	selfSQL, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         a.ObjectType,
		Relation:           a.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
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
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Raw("v_filter_type")},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "ue", Column: "userset_object_id"}},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Raw("v_filter_relation")},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Raw("v_filter_type")},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Raw("v_filter_relation")},
		Raw("ue.depth < 25"),
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"split_part(t.subject_id, '#', 1) AS userset_object_id", "ue.depth + 1 AS depth"},
		From:     "userset_expansion",
		Alias:    "ue",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "ue", Column: "userset_object_id"}},
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildListSubjectsSelfRefRegularQuery(a RelationAnalysis, inline InlineSQLData, relationList, complexClosure, allowedSubjectTypes []string, excludeWildcard bool) (string, error) {
	baseExclusions := buildSimpleComplexExclusionInput(a, ObjectID, SubjectType, Col{Table: "t", Column: "subject_id"})

	usersetObjectsBaseSQL, err := buildListSubjectsSelfRefUsersetObjectsBaseQuery(a, relationList)
	if err != nil {
		return "", err
	}

	usersetObjectsRecursiveSQL, err := buildListSubjectsSelfRefUsersetObjectsRecursiveQuery(a)
	if err != nil {
		return "", err
	}

	var baseBlocks []string
	directSQL, err := ListSubjectsDirectQuery(ListSubjectsDirectInput{
		ObjectType:      a.ObjectType,
		RelationList:    relationList,
		ObjectIDExpr:    ObjectID,
		SubjectTypeExpr: SubjectType,
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
		complexSQL, err := ListSubjectsComplexClosureQuery(ListSubjectsComplexClosureInput{
			ObjectType:      a.ObjectType,
			Relation:        rel,
			ObjectIDExpr:    ObjectID,
			SubjectTypeExpr: SubjectType,
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
		closureSQL, err := ListSubjectsIntersectionClosureQuery(functionName, SubjectType)
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
		patternExclusions := buildSimpleComplexExclusionInput(a, ObjectID, SubjectType, Col{Table: "s", Column: "subject_id"})
		if pattern.IsComplex {
			patternSQL, err := ListSubjectsUsersetPatternComplexQuery(ListSubjectsUsersetPatternComplexInput{
				ObjectType:       a.ObjectType,
				SubjectType:      pattern.SubjectType,
				SubjectRelation:  pattern.SubjectRelation,
				SourceRelations:  pattern.SourceRelations,
				ObjectIDExpr:     ObjectID,
				SubjectTypeExpr:  SubjectType,
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

		patternSQL, err := ListSubjectsUsersetPatternSimpleQuery(ListSubjectsUsersetPatternSimpleInput{
			ObjectType:          a.ObjectType,
			SubjectType:         pattern.SubjectType,
			SubjectRelation:     pattern.SubjectRelation,
			SourceRelations:     pattern.SourceRelations,
			SatisfyingRelations: pattern.SatisfyingRelations,
			ObjectIDExpr:        ObjectID,
			SubjectTypeExpr:     SubjectType,
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
	q := Tuples("t").
		ObjectType(a.ObjectType).
		Relations(relationList...).
		Select("split_part(t.subject_id, '#', 1) AS userset_object_id", "0 AS depth").
		WhereObjectID(ObjectID).
		Where(Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(a.ObjectType)}).
		WhereHasUserset().
		WhereUsersetRelation(a.Relation).
		Distinct()
	return q.SQL(), nil
}

func buildListSubjectsSelfRefUsersetObjectsRecursiveQuery(a RelationAnalysis) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(a.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(a.ObjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(a.Relation)},
		Raw("uo.depth < 25"),
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"split_part(t.subject_id, '#', 1) AS userset_object_id", "uo.depth + 1 AS depth"},
		From:     "userset_objects",
		Alias:    "uo",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildListSubjectsSelfRefUsersetObjectsExpansionQuery(a RelationAnalysis, relationList, allowedSubjectTypes []string, excludeWildcard bool, exclusions ExclusionConfig) (string, error) {
	exclusionPreds := exclusions.BuildPredicates()

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: relationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: allowedSubjectTypes},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"t.subject_id"},
		From:     "userset_objects",
		Alias:    "uo",
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func generateListObjectsComposedFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listObjectsFunctionName(a.ObjectType, a.Relation)
	relationList := buildTupleLookupRelations(a)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	anchor := buildListIndirectAnchorData(a)
	if anchor == nil || len(anchor.Path) == 0 {
		return "", fmt.Errorf("missing indirect anchor data for %s.%s", a.ObjectType, a.Relation)
	}

	selfSQL, err := ListObjectsSelfCandidateQuery(ListObjectsSelfCandidateInput{
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

	selfPaginatedSQL := wrapWithPagination(selfSQL, "object_id")
	queryPaginatedSQL := wrapWithPagination(querySQL, "object_id")

	return fmt.Sprintf(`-- Generated list_objects function for %s.%s
-- Features: %s
-- Indirect anchor: %s.%s via %s
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT) AS $$
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
		selfPaginatedSQL,
		formatSQLStringList(allowedSubjectTypes),
		queryPaginatedSQL,
	), nil
}

func buildListObjectsComposedQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, relationList []string) (string, error) {
	if anchor == nil || len(anchor.Path) == 0 {
		return "SELECT NULL::TEXT WHERE FALSE", nil
	}

	firstStep := anchor.Path[0]
	exclusions := buildSimpleComplexExclusionInput(a, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)

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

func buildComposedTTUObjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, targetType string, exclusions ExclusionConfig) (string, error) {
	exclusionPreds := exclusions.BuildPredicates()

	// Pass NULL for pagination params - inner function should return all results,
	// outer pagination wrapper handles limiting
	targetFunction := fmt.Sprintf("list_%s_%s_objects", targetType, anchor.Path[0].TargetRelation)
	subquery := fmt.Sprintf("SELECT obj.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) obj", targetFunction)

	conditions := make([]Expr, 0, 4+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(targetType)},
		Raw("t.subject_id IN ("+subquery+")"),
	)
	conditions = append(conditions, exclusionPreds...)

	q := Tuples("t").
		Select("t.object_id").
		Where(conditions...).
		Distinct()
	return q.SQL(), nil
}

func buildComposedRecursiveTTUObjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, recursiveType string, exclusions ExclusionConfig) (string, error) {
	exclusionPreds := exclusions.BuildPredicates()

	conditions := make([]Expr, 0, 4+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(recursiveType)},
		CheckPermissionInternalExprDSL("p_subject_type", "p_subject_id", anchor.Path[0].TargetRelation, fmt.Sprintf("'%s'", recursiveType), "t.subject_id", true),
	)
	conditions = append(conditions, exclusionPreds...)

	q := Tuples("t").
		Select("t.object_id").
		Where(conditions...).
		Distinct()
	return q.SQL(), nil
}

func buildComposedUsersetObjectsQuery(a RelationAnalysis, anchor *ListIndirectAnchorData, firstStep ListAnchorPathStepData, relationList []string, exclusions ExclusionConfig) (string, error) {
	exclusionPreds := exclusions.BuildPredicates()

	// Pass NULL for pagination params - inner function should return all results,
	// outer pagination wrapper handles limiting
	targetFunction := anchor.FirstStepTargetFunctionName
	subquery := fmt.Sprintf("SELECT obj.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) obj", targetFunction)

	conditions := make([]Expr, 0, 6+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: relationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(firstStep.SubjectRelation)},
		Or(
			Raw("split_part(t.subject_id, '#', 1) IN ("+subquery+")"),
			CheckPermissionInternalExprDSL(
				"p_subject_type",
				"p_subject_id",
				firstStep.SubjectRelation,
				fmt.Sprintf("'%s'", firstStep.SubjectType),
				"split_part(t.subject_id, '#', 1)",
				true,
			),
		),
	)
	conditions = append(conditions, exclusionPreds...)

	q := Tuples("t").
		Select("t.object_id").
		Where(conditions...).
		Distinct()
	return q.SQL(), nil
}

func generateListSubjectsComposedFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
	functionName := listSubjectsFunctionName(a.ObjectType, a.Relation)
	allowedSubjectTypes := buildAllowedSubjectTypesList(a)
	anchor := buildListIndirectAnchorData(a)
	if anchor == nil || len(anchor.Path) == 0 {
		return "", fmt.Errorf("missing indirect anchor data for %s.%s", a.ObjectType, a.Relation)
	}

	selfSQL, err := ListSubjectsSelfCandidateQuery(ListSubjectsSelfCandidateInput{
		ObjectType:         a.ObjectType,
		Relation:           a.Relation,
		ObjectIDExpr:       ObjectID,
		FilterTypeExpr:     ParamRef("v_filter_type"),
		FilterRelationExpr: ParamRef("v_filter_relation"),
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

	selfPaginatedSQL := wrapWithPaginationWildcardFirst(selfSQL)
	usersetFilterPaginatedSQL := wrapWithPaginationWildcardFirst(usersetFilterSQL)
	regularPaginatedSQL := wrapWithPaginationWildcardFirst(regularSQL)

	return fmt.Sprintf(`-- Generated list_subjects function for %s.%s
-- Features: %s
-- Indirect anchor: %s.%s via %s
CREATE OR REPLACE FUNCTION %s(
    p_object_id TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT) AS $$
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
		selfPaginatedSQL,
		usersetFilterPaginatedSQL,
		formatSQLStringList(allowedSubjectTypes),
		regularPaginatedSQL,
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

	exclusions := buildSimpleComplexExclusionInput(a, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})
	exclusionPreds := exclusions.BuildPredicates()

	whereClause := ""
	if len(exclusionPreds) > 0 {
		whereClause = "\nWHERE " + And(exclusionPreds...).SQL()
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

	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Lit(targetType)},
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"s.subject_id"},
		From:     "melange_tuples",
		Alias:    "link",
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "LATERAL " + listFunction,
			Alias: "s",
			On:    nil, // CROSS JOIN has no ON clause
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func buildComposedUsersetSubjectsQuery(a RelationAnalysis, firstStep ListAnchorPathStepData, subjectTypeExpr string) (string, error) {
	listFunction := fmt.Sprintf("list_%s_%s_subjects(split_part(t.subject_id, '#', 1), %s)", firstStep.SubjectType, firstStep.SubjectRelation, subjectTypeExpr)
	relationList := buildTupleLookupRelations(a)

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(a.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: relationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(firstStep.SubjectRelation)},
	}

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"s.subject_id"},
		From:     "melange_tuples",
		Alias:    "t",
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "LATERAL " + listFunction,
			Alias: "s",
			On:    nil, // CROSS JOIN has no ON clause
		}},
		Where: And(conditions...),
	}
	return stmt.SQL(), nil
}

func generateListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
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
	buf.WriteString("    p_object_type TEXT,\n")
	buf.WriteString("    p_limit INT DEFAULT NULL,\n")
	buf.WriteString("    p_after TEXT DEFAULT NULL\n")
	buf.WriteString(") RETURNS TABLE (object_id TEXT, next_cursor TEXT) AS $$\n")
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
			buf.WriteString("(p_subject_type, p_subject_id, p_limit, p_after);\n")
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

func generateListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
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
	buf.WriteString("    p_subject_type TEXT,\n")
	buf.WriteString("    p_limit INT DEFAULT NULL,\n")
	buf.WriteString("    p_after TEXT DEFAULT NULL\n")
	buf.WriteString(") RETURNS TABLE (subject_id TEXT, next_cursor TEXT) AS $$\n")
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
			buf.WriteString("(p_object_id, p_subject_type, p_limit, p_after);\n")
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
