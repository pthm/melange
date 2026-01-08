package schema

import (
	"bytes"
	"fmt"
	"strings"
)

// ListGeneratedSQL contains all SQL generated for list functions.
// This is separate from check function generation to keep concerns isolated.
// Applied atomically during migration alongside check functions.
type ListGeneratedSQL struct {
	// ListObjectsFunctions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized list_objects function (list_{type}_{relation}_objects).
	ListObjectsFunctions []string

	// ListSubjectsFunctions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized list_subjects function (list_{type}_{relation}_subjects).
	ListSubjectsFunctions []string

	// ListObjectsDispatcher contains the list_accessible_objects dispatcher function
	// that routes to specialized functions or falls back to generic.
	ListObjectsDispatcher string

	// ListSubjectsDispatcher contains the list_accessible_subjects dispatcher function
	// that routes to specialized functions or falls back to generic.
	ListSubjectsDispatcher string
}

// GenerateListSQL generates specialized SQL functions for list operations.
// The generated SQL includes:
//   - Per-relation list_objects functions (list_{type}_{relation}_objects)
//   - Per-relation list_subjects functions (list_{type}_{relation}_subjects)
//   - Dispatchers that route to specialized functions or fall back to generic
//
// During the migration phase, relations that cannot be generated will use
// the generic list functions as fallback. As more patterns are supported,
// the CanGenerateList criteria will be relaxed.
func GenerateListSQL(analyses []RelationAnalysis) (ListGeneratedSQL, error) {
	var result ListGeneratedSQL

	// Generate specialized functions for each relation that can be generated
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}

		// Generate list_objects function
		objFn, err := generateListObjectsFunction(a)
		if err != nil {
			return ListGeneratedSQL{}, fmt.Errorf("generating list_objects function for %s.%s: %w",
				a.ObjectType, a.Relation, err)
		}
		result.ListObjectsFunctions = append(result.ListObjectsFunctions, objFn)

		// Generate list_subjects function
		subjFn, err := generateListSubjectsFunction(a)
		if err != nil {
			return ListGeneratedSQL{}, fmt.Errorf("generating list_subjects function for %s.%s: %w",
				a.ObjectType, a.Relation, err)
		}
		result.ListSubjectsFunctions = append(result.ListSubjectsFunctions, subjFn)
	}

	// Generate dispatchers (always generated, even if no specialized functions)
	var err error
	result.ListObjectsDispatcher, err = generateListObjectsDispatcher(analyses)
	if err != nil {
		return ListGeneratedSQL{}, fmt.Errorf("generating list_objects dispatcher: %w", err)
	}

	result.ListSubjectsDispatcher, err = generateListSubjectsDispatcher(analyses)
	if err != nil {
		return ListGeneratedSQL{}, fmt.Errorf("generating list_subjects dispatcher: %w", err)
	}

	return result, nil
}

// listObjectsFunctionName returns the name for a specialized list_objects function.
func listObjectsFunctionName(objectType, relation string) string {
	return fmt.Sprintf("list_%s_%s_objects", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// listSubjectsFunctionName returns the name for a specialized list_subjects function.
func listSubjectsFunctionName(objectType, relation string) string {
	return fmt.Sprintf("list_%s_%s_subjects", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// generateListObjectsFunction generates a specialized list_objects function for a relation.
func generateListObjectsFunction(a RelationAnalysis) (string, error) {
	data := ListObjectsFunctionData{
		ObjectType:        a.ObjectType,
		Relation:          a.Relation,
		FunctionName:      listObjectsFunctionName(a.ObjectType, a.Relation),
		FeaturesString:    a.Features.String(),
		MaxUsersetDepth:   a.MaxUsersetDepth,
		ExceedsDepthLimit: a.ExceedsDepthLimit,
	}

	// Build relation list from simple closure relations (tuple lookup)
	data.RelationList = buildRelationList(a)

	// Populate complex closure relations (need check_permission_internal)
	// Filter out intersection closure relations since they're handled separately via function composition
	intersectionSet := make(map[string]bool)
	for _, rel := range a.IntersectionClosureRelations {
		intersectionSet[rel] = true
	}
	for _, rel := range a.ComplexClosureRelations {
		if !intersectionSet[rel] {
			data.ComplexClosureRelations = append(data.ComplexClosureRelations, rel)
		}
	}

	// Populate intersection closure relations (compose with their list functions)
	data.IntersectionClosureRelations = a.IntersectionClosureRelations

	// Build subject_id check (with or without wildcard)
	data.SubjectIDCheck = buildSubjectIDCheck(a.Features.HasWildcard)

	// Build allowed subject types list for type restriction enforcement
	data.AllowedSubjectTypes = buildAllowedSubjectTypes(a)

	// Populate exclusion fields (Phase 3)
	data.HasExclusion = a.Features.HasExclusion
	data.SimpleExcludedRelations = a.SimpleExcludedRelations
	data.ComplexExcludedRelations = a.ComplexExcludedRelations
	data.ExcludedParentRelations = a.ExcludedParentRelations
	data.ExcludedIntersectionGroups = a.ExcludedIntersectionGroups

	// Populate userset fields (Phase 4)
	data.HasUserset = a.Features.HasUserset
	data.UsersetPatterns = buildListUsersetPatterns(a)

	// Populate TTU/recursive fields (Phase 5)
	data.ParentRelations = buildListParentRelations(a)
	data.SelfReferentialLinkingRelations = buildSelfReferentialLinkingRelations(data.ParentRelations)

	// Populate intersection fields (Phase 6)
	data.HasIntersection = a.Features.HasIntersection
	data.IntersectionGroups = a.IntersectionGroups
	data.HasStandaloneAccess = computeListHasStandaloneAccess(a)

	// Populate indirect anchor fields (Phase 8)
	data.IndirectAnchor = buildListIndirectAnchorData(a)
	data.HasIndirectAnchor = data.IndirectAnchor != nil

	// Populate self-referential userset fields (Phase 9B)
	data.HasSelfReferentialUserset = a.HasSelfReferentialUserset

	// Select appropriate template based on features
	templateName := selectListObjectsTemplate(a)

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("executing list_objects template %s: %w", templateName, err)
	}
	return buf.String(), nil
}

// selectListObjectsTemplate selects the appropriate list_objects template based on features.
func selectListObjectsTemplate(a RelationAnalysis) string {
	// Phase 9A: Use depth-exceeded template for relations with userset chains >= 25 levels.
	// These immediately raise M2002 without any computation.
	if a.ExceedsDepthLimit {
		return "list_objects_depth_exceeded.tpl.sql"
	}
	// Phase 9B: Use self-referential userset template for patterns like group.member: [group#member].
	// These require recursive CTEs to expand nested userset membership.
	if a.HasSelfReferentialUserset {
		return "list_objects_self_ref_userset.tpl.sql"
	}
	// Phase 8: Use composed template for indirect anchor patterns.
	// These are relations with no direct/implied access but reach subjects through
	// TTU or userset patterns to an anchor relation.
	if a.IndirectAnchor != nil {
		return "list_objects_composed.tpl.sql"
	}
	// Phase 6: Use intersection template if relation has intersection patterns.
	// The intersection template is the most comprehensive and handles all pattern
	// combinations (direct, userset, exclusion, TTU, intersection).
	if a.Features.HasIntersection {
		return "list_objects_intersection.tpl.sql"
	}
	// Phase 5: Use recursive template if relation has TTU patterns.
	// The recursive template is comprehensive and handles all pattern combinations
	// (direct, userset, exclusion, TTU) since TTU is the most complex pattern.
	// Also use recursive template if any closure relation has TTU (inherited TTU).
	if a.Features.HasRecursive || len(a.ClosureParentRelations) > 0 {
		return "list_objects_recursive.tpl.sql"
	}
	// Phase 4: Use userset template if relation has userset patterns.
	// This includes both direct patterns (UsersetPatterns) and patterns inherited
	// from closure relations (ClosureUsersetPatterns).
	// The userset template also handles exclusions if present.
	if a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0 {
		return "list_objects_userset.tpl.sql"
	}
	// Phase 3: Use exclusion template if relation has any exclusion patterns
	if a.Features.HasExclusion {
		return "list_objects_exclusion.tpl.sql"
	}
	// Phase 2: Direct/implied patterns use the direct template
	return "list_objects_direct.tpl.sql"
}

// generateListSubjectsFunction generates a specialized list_subjects function for a relation.
func generateListSubjectsFunction(a RelationAnalysis) (string, error) {
	data := ListSubjectsFunctionData{
		ObjectType:        a.ObjectType,
		Relation:          a.Relation,
		FunctionName:      listSubjectsFunctionName(a.ObjectType, a.Relation),
		FeaturesString:    a.Features.String(),
		HasWildcard:       a.Features.HasWildcard,
		MaxUsersetDepth:   a.MaxUsersetDepth,
		ExceedsDepthLimit: a.ExceedsDepthLimit,
	}

	// Build relation list from simple closure relations (tuple lookup)
	data.RelationList = buildRelationList(a)

	// Build all satisfying relations list (for userset filter case)
	data.AllSatisfyingRelations = buildAllSatisfyingRelations(a)

	// Populate complex closure relations (need check_permission_internal)
	// Filter out intersection closure relations since they're handled separately via function composition
	intersectionSet := make(map[string]bool)
	for _, rel := range a.IntersectionClosureRelations {
		intersectionSet[rel] = true
	}
	for _, rel := range a.ComplexClosureRelations {
		if !intersectionSet[rel] {
			data.ComplexClosureRelations = append(data.ComplexClosureRelations, rel)
		}
	}

	// Populate intersection closure relations (compose with their list functions)
	data.IntersectionClosureRelations = a.IntersectionClosureRelations

	// Build allowed subject types list for type restriction enforcement
	data.AllowedSubjectTypes = buildAllowedSubjectTypes(a)

	// Populate exclusion fields (Phase 3)
	data.HasExclusion = a.Features.HasExclusion
	data.SimpleExcludedRelations = a.SimpleExcludedRelations
	data.ComplexExcludedRelations = a.ComplexExcludedRelations
	data.ExcludedParentRelations = a.ExcludedParentRelations
	data.ExcludedIntersectionGroups = a.ExcludedIntersectionGroups

	// Populate userset fields (Phase 4)
	data.HasUserset = a.Features.HasUserset
	data.UsersetPatterns = buildListUsersetPatterns(a)

	// Populate TTU/recursive fields (Phase 5)
	data.ParentRelations = buildListParentRelations(a)

	// Populate intersection fields (Phase 6)
	data.HasIntersection = a.Features.HasIntersection
	data.IntersectionGroups = a.IntersectionGroups

	// Populate indirect anchor fields (Phase 8)
	data.IndirectAnchor = buildListIndirectAnchorData(a)
	data.HasIndirectAnchor = data.IndirectAnchor != nil

	// Populate self-referential userset fields (Phase 9B)
	data.HasSelfReferentialUserset = a.HasSelfReferentialUserset

	// Select appropriate template based on features
	templateName := selectListSubjectsTemplate(a)

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("executing list_subjects template %s: %w", templateName, err)
	}
	return buf.String(), nil
}

// selectListSubjectsTemplate selects the appropriate list_subjects template based on features.
func selectListSubjectsTemplate(a RelationAnalysis) string {
	// Phase 9A: Use depth-exceeded template for relations with userset chains >= 25 levels.
	// These immediately raise M2002 without any computation.
	if a.ExceedsDepthLimit {
		return "list_subjects_depth_exceeded.tpl.sql"
	}
	// Phase 9B: Use self-referential userset template for patterns like group.member: [group#member].
	// These require recursive CTEs to expand nested userset membership.
	if a.HasSelfReferentialUserset {
		return "list_subjects_self_ref_userset.tpl.sql"
	}
	// Phase 8: Use composed template for indirect anchor patterns.
	// These are relations with no direct/implied access but reach subjects through
	// TTU or userset patterns to an anchor relation.
	if a.IndirectAnchor != nil {
		return "list_subjects_composed.tpl.sql"
	}
	// Phase 6: Use intersection template if relation has intersection patterns.
	// The intersection template is the most comprehensive and handles all pattern
	// combinations (direct, userset, exclusion, TTU, intersection).
	if a.Features.HasIntersection {
		return "list_subjects_intersection.tpl.sql"
	}
	// Phase 5: Use recursive template if relation has TTU patterns.
	// The recursive template is comprehensive and handles all pattern combinations
	// (direct, userset, exclusion, TTU) since TTU is the most complex pattern.
	// Also use recursive template if any closure relation has TTU (inherited TTU).
	if a.Features.HasRecursive || len(a.ClosureParentRelations) > 0 {
		return "list_subjects_recursive.tpl.sql"
	}
	// Phase 4: Use userset template if relation has userset patterns.
	// This includes both direct patterns (UsersetPatterns) and patterns inherited
	// from closure relations (ClosureUsersetPatterns).
	// The userset template also handles exclusions if present.
	if a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0 {
		return "list_subjects_userset.tpl.sql"
	}
	// Phase 3: Use exclusion template if relation has any exclusion patterns
	if a.Features.HasExclusion {
		return "list_subjects_exclusion.tpl.sql"
	}
	// Phase 2: Direct/implied patterns use the direct template
	return "list_subjects_direct.tpl.sql"
}

// ListObjectsFunctionData contains data for rendering list_objects function templates.
type ListObjectsFunctionData struct {
	ObjectType     string
	Relation       string
	FunctionName   string
	FeaturesString string

	// MaxUsersetDepth is the maximum userset chain depth reachable from this relation.
	// Used by depth-exceeded template to report the actual depth in error messages.
	MaxUsersetDepth int

	// ExceedsDepthLimit is true if MaxUsersetDepth >= 25.
	// Routes to depth-exceeded template which immediately raises M2002.
	ExceedsDepthLimit bool

	// RelationList is a SQL-formatted list of simple closure relations to check.
	// e.g., "'viewer', 'editor', 'owner'" - only relations that can use tuple lookup
	RelationList string

	// ComplexClosureRelations are closure relations that need check_permission_internal.
	// These have exclusions or other complex features that can't be resolved via tuple lookup.
	ComplexClosureRelations []string

	// IntersectionClosureRelations are closure relations that have intersection patterns
	// and are list-generatable. These need to be composed with their list function.
	IntersectionClosureRelations []string

	// SubjectIDCheck is the SQL fragment for checking subject_id with wildcard support.
	// e.g., "(t.subject_id = p_subject_id OR t.subject_id = '*')"
	SubjectIDCheck string

	// AllowedSubjectTypes is a SQL-formatted list of allowed subject types.
	// e.g., "'user', 'employee'" - used to enforce model type restrictions.
	AllowedSubjectTypes string

	// Exclusion-related fields (Phase 3)
	HasExclusion bool // true if this relation has exclusion patterns

	// SimpleExcludedRelations are excluded relations that can use direct tuple lookup.
	// These are relations without userset, TTU, exclusion, intersection, or implied closure.
	SimpleExcludedRelations []string

	// ComplexExcludedRelations are excluded relations that need check_permission_internal.
	// These have userset, TTU, intersection, exclusion, or implied closure.
	ComplexExcludedRelations []string

	// ExcludedParentRelations are TTU exclusions like "but not viewer from parent".
	ExcludedParentRelations []ParentRelationInfo

	// ExcludedIntersectionGroups are intersection exclusions like "but not (editor and owner)".
	ExcludedIntersectionGroups []IntersectionGroupInfo

	// Userset-related fields (Phase 4)
	HasUserset      bool                     // true if this relation has userset patterns
	UsersetPatterns []ListUsersetPatternData // [group#member] patterns for UNION expansion

	// TTU/Recursive-related fields (Phase 5)
	ParentRelations []ListParentRelationData // TTU patterns like "viewer from parent"

	// SelfReferentialLinkingRelations is a SQL-formatted list of linking relations
	// from self-referential TTU patterns. Used for depth checking in recursive CTE.
	// e.g., "'parent', 'folder'" when there are TTU patterns viewer from parent, viewer from folder
	SelfReferentialLinkingRelations string

	// Intersection-related fields (Phase 6)
	HasIntersection     bool                   // true if this relation has intersection patterns
	IntersectionGroups  []IntersectionGroupInfo // Intersection groups for list functions
	HasStandaloneAccess bool                   // true if there are access paths outside intersections

	// Phase 8: Indirect anchor for composed access patterns
	HasIndirectAnchor bool                   // true if access is via indirect anchor
	IndirectAnchor    *ListIndirectAnchorData // Anchor info for composed templates

	// Phase 9B: Self-referential userset patterns
	HasSelfReferentialUserset bool // true if any userset pattern references same type/relation
}

// ListSubjectsFunctionData contains data for rendering list_subjects function templates.
type ListSubjectsFunctionData struct {
	ObjectType     string
	Relation       string
	FunctionName   string
	FeaturesString string

	// MaxUsersetDepth is the maximum userset chain depth reachable from this relation.
	// Used by depth-exceeded template to report the actual depth in error messages.
	MaxUsersetDepth int

	// ExceedsDepthLimit is true if MaxUsersetDepth >= 25.
	// Routes to depth-exceeded template which immediately raises M2002.
	ExceedsDepthLimit bool

	// RelationList is a SQL-formatted list of simple closure relations to check.
	RelationList string

	// AllSatisfyingRelations is a SQL-formatted list of ALL relations that satisfy this relation.
	// Includes both simple and complex closure relations. Used by userset filter case.
	// e.g., "'can_view', 'viewer'" when viewer implies can_view
	AllSatisfyingRelations string

	// ComplexClosureRelations are closure relations that need check_permission_internal.
	ComplexClosureRelations []string

	// IntersectionClosureRelations are closure relations that have intersection patterns
	// and are list-generatable. These need to be composed with their list function.
	IntersectionClosureRelations []string

	// AllowedSubjectTypes is a SQL-formatted list of allowed subject types.
	// e.g., "'user', 'employee'" - used to enforce model type restrictions.
	AllowedSubjectTypes string

	// HasWildcard is true if the model allows wildcard subjects.
	// When false, wildcard tuples (subject_id = '*') should be excluded from results.
	HasWildcard bool

	// Exclusion-related fields (Phase 3)
	HasExclusion bool // true if this relation has exclusion patterns

	// SimpleExcludedRelations are excluded relations that can use direct tuple lookup.
	SimpleExcludedRelations []string

	// ComplexExcludedRelations are excluded relations that need check_permission_internal.
	ComplexExcludedRelations []string

	// ExcludedParentRelations are TTU exclusions like "but not viewer from parent".
	ExcludedParentRelations []ParentRelationInfo

	// ExcludedIntersectionGroups are intersection exclusions like "but not (editor and owner)".
	ExcludedIntersectionGroups []IntersectionGroupInfo

	// Userset-related fields (Phase 4)
	HasUserset      bool                     // true if this relation has userset patterns
	UsersetPatterns []ListUsersetPatternData // [group#member] patterns for expansion

	// TTU/Recursive-related fields (Phase 5)
	ParentRelations []ListParentRelationData // TTU patterns like "viewer from parent"

	// Intersection-related fields (Phase 6)
	HasIntersection    bool                   // true if this relation has intersection patterns
	IntersectionGroups []IntersectionGroupInfo // Intersection groups for list functions

	// Phase 8: Indirect anchor for composed access patterns
	HasIndirectAnchor bool                   // true if access is via indirect anchor
	IndirectAnchor    *ListIndirectAnchorData // Anchor info for composed templates

	// Phase 9B: Self-referential userset patterns
	HasSelfReferentialUserset bool // true if any userset pattern references same type/relation
}

// ListParentRelationData contains data for rendering TTU pattern expansion in list templates.
// For a pattern like "viewer from parent", this represents the parent traversal.
type ListParentRelationData struct {
	Relation            string // Relation to check on parent (e.g., "viewer")
	LinkingRelation     string // Relation that links to parent (e.g., "parent")
	AllowedLinkingTypes string // SQL-formatted list of parent types (e.g., "'folder', 'org'")
	ParentType          string // First allowed linking type (for self-referential check)
	IsSelfReferential   bool   // True if any parent type equals the object type

	// CrossTypeLinkingTypes is a SQL-formatted list of linking types that are NOT self-referential.
	// When a parent relation allows both self-referential and cross-type links (e.g., [folder, document]
	// for document.parent), this contains only the cross-type entries (e.g., "'folder'").
	// Used to generate check_permission_internal calls for cross-type parents even when
	// IsSelfReferential is true for the same linking relation.
	CrossTypeLinkingTypes string
	HasCrossTypeLinks     bool // True if CrossTypeLinkingTypes is non-empty
}

// ListUsersetPatternData contains data for rendering userset pattern expansion in list templates.
// For a pattern like [group#member], this generates a UNION block that:
// - Finds grant tuples where subject is group#member
// - JOINs with membership tuples to find subjects who are members
type ListUsersetPatternData struct {
	SubjectType     string // e.g., "group"
	SubjectRelation string // e.g., "member"

	// SatisfyingRelationsList is a SQL-formatted list of relations that satisfy SubjectRelation.
	// e.g., "'member', 'admin'" when admin implies member.
	SatisfyingRelationsList string

	// SourceRelationList is a SQL-formatted list of relations to search for userset grant tuples.
	// For direct userset patterns, this is the same as the parent's RelationList.
	// For closure userset patterns (inherited from implied relations), this is the source relation.
	// e.g., "'viewer'" for a pattern inherited from viewer: [group#member]
	SourceRelationList string

	// SourceRelation is the relation where this userset pattern is defined (unquoted).
	// Used for closure patterns to verify permission via check_permission_internal.
	SourceRelation string

	// IsClosurePattern is true if this pattern is inherited from an implied relation.
	// When true, candidates need to be verified via check_permission_internal on the
	// source relation to apply any exclusions or complex features.
	IsClosurePattern bool

	// HasWildcard is true if any satisfying relation allows wildcards.
	// When true, membership check includes subject_id = '*'.
	HasWildcard bool

	// IsComplex is true if this pattern requires check_permission_internal for membership.
	// This happens when any relation in the closure has TTU, exclusion, or intersection.
	IsComplex bool

	// IsSelfReferential is true if SubjectType == ObjectType and SubjectRelation == Relation.
	// Self-referential usersets (e.g., group.member: [group#member]) require recursive CTEs.
	// Non-self-referential usersets use JOIN-based expansion.
	IsSelfReferential bool
}

// ListIndirectAnchorData contains data for rendering composed access patterns in list templates.
// This is used when a relation has no direct/implied access but can reach subjects through
// TTU or userset patterns to an anchor relation that has direct grants.
type ListIndirectAnchorData struct {
	// Path steps from this relation to the anchor
	Path []ListAnchorPathStepData

	// First step's target function (used for composition)
	// For multi-hop chains, we compose with the first step's target, not the anchor.
	// e.g., for job.can_read -> permission.assignee -> role.assignee, we call
	// list_permission_assignee_objects (first step's target), not list_role_assignee_objects (anchor).
	FirstStepTargetFunctionName string // e.g., "list_permission_assignee_objects"

	// Anchor relation info (end of the chain)
	AnchorType             string // Type of anchor relation (e.g., "folder")
	AnchorRelation         string // Anchor relation name (e.g., "viewer")
	AnchorFunctionName     string // Name of anchor's list function (e.g., "list_folder_viewer_objects")
	AnchorSubjectTypes     string // SQL-formatted allowed subject types from anchor
	AnchorHasWildcard      bool   // Whether anchor supports wildcards
	SatisfyingRelationsList string // SQL-formatted list of relations that satisfy the anchor
}

// ListAnchorPathStepData contains data for rendering one step in an indirect anchor path.
type ListAnchorPathStepData struct {
	Type string // "ttu" or "userset"

	// For TTU steps (e.g., "viewer from parent"):
	LinkingRelation string   // "parent"
	TargetType      string   // "folder" (first type with direct anchor)
	TargetRelation  string   // "viewer"
	AllTargetTypes  []string // All types with direct anchor (e.g., ["document", "folder"])
	RecursiveTypes  []string // Types needing check_permission_internal (same-type recursive TTU)

	// For userset steps (e.g., [group#member]):
	SubjectType             string // "group"
	SubjectRelation         string // "member"
	SatisfyingRelationsList string // SQL-formatted satisfying relations
	HasWildcard             bool   // Whether membership allows wildcards
}

// ListDispatcherData contains data for rendering list dispatcher templates.
type ListDispatcherData struct {
	// HasSpecializedFunctions is true if any specialized list functions were generated.
	HasSpecializedFunctions bool

	// Cases contains the routing cases for specialized functions.
	Cases []ListDispatcherCase
}

// ListDispatcherCase represents a single routing case in the list dispatcher.
type ListDispatcherCase struct {
	ObjectType   string
	Relation     string
	FunctionName string
}

// generateListObjectsDispatcher generates the list_accessible_objects dispatcher.
// For Phase 1, this always falls through to the generic implementation.
func generateListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	data := ListDispatcherData{
		HasSpecializedFunctions: false,
		Cases:                   nil,
	}

	// Build cases for relations that can be generated
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}
		data.Cases = append(data.Cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		})
	}
	data.HasSpecializedFunctions = len(data.Cases) > 0

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "list_objects_dispatcher.tpl.sql", data); err != nil {
		return "", fmt.Errorf("executing list_objects_dispatcher template: %w", err)
	}
	return buf.String(), nil
}

// generateListSubjectsDispatcher generates the list_accessible_subjects dispatcher.
// For Phase 1, this always falls through to the generic implementation.
func generateListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	data := ListDispatcherData{
		HasSpecializedFunctions: false,
		Cases:                   nil,
	}

	// Build cases for relations that can be generated
	for _, a := range analyses {
		if !a.CanGenerateList() {
			continue
		}
		data.Cases = append(data.Cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		})
	}
	data.HasSpecializedFunctions = len(data.Cases) > 0

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "list_subjects_dispatcher.tpl.sql", data); err != nil {
		return "", fmt.Errorf("executing list_subjects_dispatcher template: %w", err)
	}
	return buf.String(), nil
}

// buildRelationList builds a SQL-formatted list of simple relations from the closure.
// For example: "'viewer', 'editor', 'owner'"
// Only includes relations that can be resolved via tuple lookup (SimpleClosureRelations).
// Complex closure relations (with exclusions, etc.) are handled separately via check_permission_internal.
func buildRelationList(a RelationAnalysis) string {
	// Build relation list from self + simple closure relations
	// Complex closure relations are handled via function calls, not tuple lookup
	relations := []string{a.Relation}
	relations = append(relations, a.SimpleClosureRelations...)

	// Fallback to satisfying relations only if no partition was computed at all
	// (for backwards compatibility when closure relations not yet partitioned).
	// If ComplexClosureRelations is non-empty, the partition was computed and
	// we should use only the simple relations (even if that's just self).
	if len(a.SimpleClosureRelations) == 0 && len(a.ComplexClosureRelations) == 0 && len(a.SatisfyingRelations) > 0 {
		relations = a.SatisfyingRelations
	}

	quoted := make([]string, len(relations))
	for i, r := range relations {
		quoted[i] = fmt.Sprintf("'%s'", r)
	}
	return strings.Join(quoted, ", ")
}

// buildSubjectIDCheck builds the SQL fragment for checking subject_id.
// When hasWildcard is true, also matches wildcard tuples (subject_id = '*').
func buildSubjectIDCheck(hasWildcard bool) string {
	if hasWildcard {
		return "(t.subject_id = p_subject_id OR t.subject_id = '*')"
	}
	// Exclude wildcard tuples when model doesn't allow wildcards
	return "t.subject_id = p_subject_id AND t.subject_id != '*'"
}

// buildAllowedSubjectTypes builds a SQL-formatted list of allowed subject types.
// This enforces model type restrictions in list queries.
func buildAllowedSubjectTypes(a RelationAnalysis) string {
	// Use AllowedSubjectTypes if available (computed from closure)
	types := a.AllowedSubjectTypes
	if len(types) == 0 {
		// Fallback to DirectSubjectTypes
		types = a.DirectSubjectTypes
	}
	if len(types) == 0 {
		// No types - return empty which will cause no matches
		return "''"
	}

	quoted := make([]string, len(types))
	for i, t := range types {
		quoted[i] = fmt.Sprintf("'%s'", t)
	}
	return strings.Join(quoted, ", ")
}

// buildAllSatisfyingRelations builds a SQL-formatted list of ALL relations that satisfy this relation.
// This includes both simple closure relations (tuple lookup) and complex closure relations.
// Used by the userset filter case to find all tuples that grant access.
func buildAllSatisfyingRelations(a RelationAnalysis) string {
	relations := a.SatisfyingRelations
	if len(relations) == 0 {
		// Fallback to just self
		relations = []string{a.Relation}
	}

	quoted := make([]string, len(relations))
	for i, r := range relations {
		quoted[i] = fmt.Sprintf("'%s'", r)
	}
	return strings.Join(quoted, ", ")
}

// buildListUsersetPatterns builds template data for userset pattern expansion.
// For each [group#member] pattern, this creates data for a UNION block that:
// - Finds grant tuples where subject is group#member
// - JOINs with membership tuples to find subjects who are members
//
// This includes both:
// - UsersetPatterns: patterns from the relation itself (e.g., viewer: [group#member])
// - ClosureUsersetPatterns: patterns from implied closure relations (e.g., can_view: viewer where viewer has usersets)
func buildListUsersetPatterns(a RelationAnalysis) []ListUsersetPatternData {
	if len(a.UsersetPatterns) == 0 && len(a.ClosureUsersetPatterns) == 0 {
		return nil
	}

	// Build RelationList for direct patterns (same as main function's RelationList)
	directRelationList := buildRelationList(a)

	patterns := make([]ListUsersetPatternData, 0, len(a.UsersetPatterns)+len(a.ClosureUsersetPatterns))

	// Process direct userset patterns
	for _, p := range a.UsersetPatterns {
		pattern := ListUsersetPatternData{
			SubjectType:        p.SubjectType,
			SubjectRelation:    p.SubjectRelation,
			HasWildcard:        p.HasWildcard,
			IsComplex:          p.IsComplex,
			SourceRelationList: directRelationList, // Use main RelationList for direct patterns
			// Mark as self-referential if it references the same type and relation
			IsSelfReferential: p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
		}

		// Build satisfying relations list for the subject relation closure
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}

		quoted := make([]string, len(satisfying))
		for i, r := range satisfying {
			quoted[i] = fmt.Sprintf("'%s'", r)
		}
		pattern.SatisfyingRelationsList = strings.Join(quoted, ", ")

		patterns = append(patterns, pattern)
	}

	// Process closure userset patterns (inherited from implied relations)
	for _, p := range a.ClosureUsersetPatterns {
		pattern := ListUsersetPatternData{
			SubjectType:        p.SubjectType,
			SubjectRelation:    p.SubjectRelation,
			HasWildcard:        p.HasWildcard,
			IsComplex:          p.IsComplex,
			SourceRelationList: fmt.Sprintf("'%s'", p.SourceRelation), // Use source relation for closure patterns
			SourceRelation:     p.SourceRelation,
			IsClosurePattern:   true, // Closure patterns need source relation verification
			// Closure patterns are self-referential if they reference the same type and relation
			// (rare, but possible in complex models)
			IsSelfReferential: p.SubjectType == a.ObjectType && p.SubjectRelation == a.Relation,
		}

		// Build satisfying relations list for the subject relation closure
		satisfying := p.SatisfyingRelations
		if len(satisfying) == 0 {
			satisfying = []string{p.SubjectRelation}
		}

		quoted := make([]string, len(satisfying))
		for i, r := range satisfying {
			quoted[i] = fmt.Sprintf("'%s'", r)
		}
		pattern.SatisfyingRelationsList = strings.Join(quoted, ", ")

		patterns = append(patterns, pattern)
	}

	return patterns
}

// buildListParentRelations builds template data for TTU pattern expansion in list templates.
// For each "viewer from parent" pattern, this creates data for recursive CTE traversal or
// cross-type lookup using check_permission_internal.
//
// Includes both direct parent relations (a.ParentRelations) and inherited parent relations
// from closure (a.ClosureParentRelations). For implied relations like "can_view: viewer"
// where viewer has TTU patterns, the TTU info comes from ClosureParentRelations.
func buildListParentRelations(a RelationAnalysis) []ListParentRelationData {
	// Combine direct and closure parent relations
	allParentRelations := make([]ParentRelationInfo, 0, len(a.ParentRelations)+len(a.ClosureParentRelations))
	allParentRelations = append(allParentRelations, a.ParentRelations...)
	allParentRelations = append(allParentRelations, a.ClosureParentRelations...)

	if len(allParentRelations) == 0 {
		return nil
	}

	// Deduplicate by LinkingRelation + Relation combination
	seen := make(map[string]bool)
	result := make([]ListParentRelationData, 0, len(allParentRelations))

	for _, p := range allParentRelations {
		key := p.LinkingRelation + "->" + p.Relation
		if seen[key] {
			continue
		}
		seen[key] = true

		data := ListParentRelationData{
			Relation:        p.Relation,
			LinkingRelation: p.LinkingRelation,
		}

		// Build SQL-formatted list of allowed linking types
		// Also track cross-type (non-self-referential) types separately
		if len(p.AllowedLinkingTypes) > 0 {
			var allQuoted []string
			var crossTypeQuoted []string

			for _, t := range p.AllowedLinkingTypes {
				quoted := fmt.Sprintf("'%s'", t)
				allQuoted = append(allQuoted, quoted)

				if t == a.ObjectType {
					data.IsSelfReferential = true
				} else {
					crossTypeQuoted = append(crossTypeQuoted, quoted)
				}
			}

			data.AllowedLinkingTypes = strings.Join(allQuoted, ", ")
			data.ParentType = p.AllowedLinkingTypes[0]

			// Set cross-type fields for generating check_permission_internal calls
			// even when the relation has self-referential links
			if len(crossTypeQuoted) > 0 {
				data.CrossTypeLinkingTypes = strings.Join(crossTypeQuoted, ", ")
				data.HasCrossTypeLinks = true
			}
		}

		result = append(result, data)
	}

	return result
}

// buildSelfReferentialLinkingRelations extracts linking relations from self-referential
// parent relations and formats them as a SQL IN clause list.
// Returns empty string if no self-referential patterns exist.
func buildSelfReferentialLinkingRelations(parentRelations []ListParentRelationData) string {
	var linkingRelations []string
	seen := make(map[string]bool)

	for _, p := range parentRelations {
		if p.IsSelfReferential && !seen[p.LinkingRelation] {
			seen[p.LinkingRelation] = true
			linkingRelations = append(linkingRelations, fmt.Sprintf("'%s'", p.LinkingRelation))
		}
	}

	if len(linkingRelations) == 0 {
		return ""
	}

	return strings.Join(linkingRelations, ", ")
}

// buildListIndirectAnchorData builds template data for indirect anchor composed access.
// Returns nil if the relation has no indirect anchor.
func buildListIndirectAnchorData(a RelationAnalysis) *ListIndirectAnchorData {
	if a.IndirectAnchor == nil {
		return nil
	}

	anchor := a.IndirectAnchor
	data := &ListIndirectAnchorData{
		AnchorType:         anchor.AnchorType,
		AnchorRelation:     anchor.AnchorRelation,
		AnchorFunctionName: listObjectsFunctionName(anchor.AnchorType, anchor.AnchorRelation),
	}

	// Build path step data
	for _, step := range anchor.Path {
		stepData := ListAnchorPathStepData{
			Type:            step.Type,
			LinkingRelation: step.LinkingRelation,
			TargetType:      step.TargetType,
			TargetRelation:  step.TargetRelation,
			AllTargetTypes:  step.AllTargetTypes,
			RecursiveTypes:  step.RecursiveTypes,
			SubjectType:     step.SubjectType,
			SubjectRelation: step.SubjectRelation,
		}
		data.Path = append(data.Path, stepData)
	}

	// Set FirstStepTargetFunctionName - used for composition.
	// For multi-hop chains, we compose with the first step's target function,
	// not the anchor's. This allows each step to handle its own traversal.
	if len(anchor.Path) > 0 {
		firstStep := anchor.Path[0]
		if firstStep.Type == "ttu" {
			// For TTU, the first step's target is the relation we're looking up on the parent type
			data.FirstStepTargetFunctionName = listObjectsFunctionName(firstStep.TargetType, firstStep.TargetRelation)
		} else if firstStep.Type == "userset" {
			// For userset, the first step's target is the membership relation on the subject type
			data.FirstStepTargetFunctionName = listObjectsFunctionName(firstStep.SubjectType, firstStep.SubjectRelation)
		}
	}

	// Build AllowedSubjectTypes from the relation's propagated types
	if len(a.AllowedSubjectTypes) > 0 {
		quoted := make([]string, len(a.AllowedSubjectTypes))
		for i, t := range a.AllowedSubjectTypes {
			quoted[i] = fmt.Sprintf("'%s'", t)
		}
		data.AnchorSubjectTypes = strings.Join(quoted, ", ")
	} else {
		data.AnchorSubjectTypes = "''"
	}

	data.AnchorHasWildcard = a.Features.HasWildcard

	return data
}

// computeListHasStandaloneAccess determines if the relation has access paths outside of intersections.
// This is similar to computeHasStandaloneAccess in codegen.go but adapted for list functions.
// When false and HasIntersection is true, the only access is through intersection groups.
func computeListHasStandaloneAccess(a RelationAnalysis) bool {
	// If no intersection, all access paths are standalone
	if !a.Features.HasIntersection {
		return a.Features.HasDirect || a.Features.HasImplied || a.Features.HasUserset || a.Features.HasRecursive
	}

	// Check if any intersection group has a "This" part, meaning direct access is
	// constrained by the intersection rather than being standalone.
	hasIntersectionWithThis := false
	for _, group := range a.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				hasIntersectionWithThis = true
				break
			}
		}
		if hasIntersectionWithThis {
			break
		}
	}

	// If direct types are inside an intersection (This pattern), don't count them as standalone.
	// Userset patterns from subject type restrictions (e.g., [group#member]) are also part of
	// the "This" pattern, so they shouldn't be standalone either.
	// Check for other standalone access paths (implied, recursive).
	hasStandaloneDirect := a.Features.HasDirect && !hasIntersectionWithThis
	hasStandaloneImplied := a.Features.HasImplied
	hasStandaloneUserset := a.Features.HasUserset && !hasIntersectionWithThis
	hasStandaloneRecursive := a.Features.HasRecursive

	return hasStandaloneDirect || hasStandaloneImplied || hasStandaloneUserset || hasStandaloneRecursive
}
