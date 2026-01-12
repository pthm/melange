package sqlgen

import "fmt"

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
func GenerateListSQL(analyses []RelationAnalysis, inline InlineSQLData) (ListGeneratedSQL, error) {
	var result ListGeneratedSQL

	// Generate specialized functions for each relation that can be generated
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}

		// Generate list_objects function
		objFn, err := generateListObjectsFunction(a, inline)
		if err != nil {
			return ListGeneratedSQL{}, fmt.Errorf("generating list_objects function for %s.%s: %w",
				a.ObjectType, a.Relation, err)
		}
		result.ListObjectsFunctions = append(result.ListObjectsFunctions, objFn)

		// Generate list_subjects function
		subjFn, err := generateListSubjectsFunction(a, inline)
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
func generateListObjectsFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
	// Route to appropriate generator based on ListStrategy
	switch a.ListStrategy {
	case ListStrategyDirect, ListStrategyUserset, ListStrategyIntersection:
		// Use unified Plan → Blocks → Render architecture
		plan := BuildListObjectsPlan(a, inline)
		blocks, err := BuildListObjectsBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListObjectsFunction(plan, blocks)
	case ListStrategyRecursive:
		// Use Plan → Blocks → Render for TTU patterns
		plan := BuildListObjectsPlan(a, inline)
		blocks, err := BuildListObjectsRecursiveBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListObjectsRecursiveFunction(plan, blocks)
	case ListStrategyDepthExceeded:
		// Use Plan → Render (no blocks needed - just raises error)
		plan := BuildListObjectsPlan(a, inline)
		return RenderListObjectsDepthExceededFunction(plan), nil
	case ListStrategySelfRefUserset:
		// Use Plan → Blocks → Render for self-referential userset patterns
		plan := BuildListObjectsPlan(a, inline)
		blocks, err := BuildListObjectsSelfRefUsersetBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListObjectsSelfRefUsersetFunction(plan, blocks)
	case ListStrategyComposed:
		// Use Plan → Blocks → Render for indirect anchor composition
		plan := BuildListObjectsPlan(a, inline)
		blocks, err := BuildListObjectsComposedBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListObjectsComposedFunction(plan, blocks)
	default:
		return "", fmt.Errorf("unknown list strategy %v for %s.%s", a.ListStrategy, a.ObjectType, a.Relation)
	}
}

// generateListSubjectsFunction generates a specialized list_subjects function for a relation.
func generateListSubjectsFunction(a RelationAnalysis, inline InlineSQLData) (string, error) {
	// Route to appropriate generator based on ListStrategy
	switch a.ListStrategy {
	case ListStrategyDirect, ListStrategyUserset:
		// Wire to Plan → Blocks → Render
		plan := BuildListSubjectsPlan(a, inline)
		blocks, err := BuildListSubjectsBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListSubjectsFunction(plan, blocks)
	case ListStrategyRecursive:
		// Wire to Plan → Blocks → Render for TTU patterns
		plan := BuildListSubjectsPlan(a, inline)
		blocks, err := BuildListSubjectsRecursiveBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListSubjectsRecursiveFunction(plan, blocks)
	case ListStrategyIntersection:
		// Wire to Plan → Blocks → Render for intersection patterns
		plan := BuildListSubjectsPlan(a, inline)
		blocks, err := BuildListSubjectsIntersectionBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListSubjectsIntersectionFunction(plan, blocks)
	case ListStrategyDepthExceeded:
		// Use Plan → Render (no blocks needed - just raises error)
		plan := BuildListSubjectsPlan(a, inline)
		return RenderListSubjectsDepthExceededFunction(plan), nil
	case ListStrategySelfRefUserset:
		// Use Plan → Blocks → Render for self-referential userset patterns
		plan := BuildListSubjectsPlan(a, inline)
		blocks, err := BuildListSubjectsSelfRefUsersetBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListSubjectsSelfRefUsersetFunction(plan, blocks)
	case ListStrategyComposed:
		// Use Plan → Blocks → Render for indirect anchor composition
		plan := BuildListSubjectsPlan(a, inline)
		blocks, err := BuildListSubjectsComposedBlocks(plan)
		if err != nil {
			return "", err
		}
		return RenderListSubjectsComposedFunction(plan, blocks)
	default:
		return "", fmt.Errorf("unknown list strategy %v for %s.%s", a.ListStrategy, a.ObjectType, a.Relation)
	}
}

// ListParentRelationData contains data for rendering TTU pattern expansion in list templates.
// For a pattern like "viewer from parent", this represents the parent traversal.
type ListParentRelationData struct {
	Relation            string // Relation to check on parent (e.g., "viewer")
	LinkingRelation     string // Relation that links to parent (e.g., "parent")
	AllowedLinkingTypes string // SQL-formatted list of parent types (e.g., "'folder', 'org'")
	ParentType          string // First allowed linking type (for self-referential check)
	IsSelfReferential   bool   // True if any parent type equals the object type

	// AllowedLinkingTypesSlice contains the same types as AllowedLinkingTypes but as a slice.
	// Used for typed DSL expressions (In{Expr: ..., Values: AllowedLinkingTypesSlice}).
	AllowedLinkingTypesSlice []string

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
	AnchorType              string // Type of anchor relation (e.g., "folder")
	AnchorRelation          string // Anchor relation name (e.g., "viewer")
	AnchorFunctionName      string // Name of anchor's list function (e.g., "list_folder_viewer_objects")
	AnchorSubjectTypes      string // SQL-formatted allowed subject types from anchor
	AnchorHasWildcard       bool   // Whether anchor supports wildcards
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
			var allTypes []string
			var crossTypes []string

			for _, t := range p.AllowedLinkingTypes {
				allTypes = append(allTypes, t)

				if t == a.ObjectType {
					data.IsSelfReferential = true
				} else {
					crossTypes = append(crossTypes, t)
				}
			}

			data.AllowedLinkingTypes = formatSQLStringList(allTypes)
			data.AllowedLinkingTypesSlice = allTypes
			data.ParentType = p.AllowedLinkingTypes[0]

			// Set cross-type fields for generating check_permission_internal calls
			// even when the relation has self-referential links
			if len(crossTypes) > 0 {
				data.CrossTypeLinkingTypes = formatSQLStringList(crossTypes)
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
			linkingRelations = append(linkingRelations, p.LinkingRelation)
		}
	}

	if len(linkingRelations) == 0 {
		return ""
	}

	return formatSQLStringList(linkingRelations)
}
