package schema

import "strings"

// RelationFeatures tracks which features a relation uses.
// Multiple features can be present and will be composed in generated SQL.
// For example, a relation with HasDirect, HasUserset, and HasRecursive
// will generate SQL that ORs together all three access paths.
type RelationFeatures struct {
	HasDirect       bool // [user] - direct tuple lookup
	HasImplied      bool // viewer: editor - uses closure for satisfying relations
	HasWildcard     bool // [user:*] - checks subject_id = '*'
	HasUserset      bool // [group#member] - requires JOIN for membership
	HasRecursive    bool // viewer from parent - requires cycle detection
	HasExclusion    bool // but not blocked - adds AND NOT check
	HasIntersection bool // writer and editor - requires AND of all parts
}

// CanGenerate returns true if we can generate specialized SQL for this feature set.
// This checks the features themselves - for full generation eligibility, also check
// that all dependency relations are generatable via RelationAnalysis.CanGenerate.
func (f RelationFeatures) CanGenerate() bool {
	// We can generate SQL if the relation has at least one access path:
	// - Direct: tuple lookup for [user] patterns
	// - Implied: closure-based lookup for "viewer: editor" patterns
	// - Userset: JOIN-based expansion for [group#member] patterns
	// - Recursive: TTU patterns like "viewer from parent" (uses check_permission_internal)
	// - Intersection: AND patterns like "writer and editor"
	//
	// Note: All patterns are now supported. Complex cases (cross-type TTU, deep usersets)
	// use check_permission_internal to delegate to the appropriate handler.

	// Must have at least one access path
	return f.HasDirect || f.HasImplied || f.HasUserset || f.HasRecursive || f.HasIntersection
}

// IsSimplyResolvable returns true if this relation can be fully resolved
// with a simple tuple lookup (no userset JOINs, recursion, exclusions, etc.).
// This is used to check if excluded relations can be handled with a simple
// EXISTS check, since exclusions on excluded relations would require full
// permission resolution.
func (f RelationFeatures) IsSimplyResolvable() bool {
	// Can use simple tuple lookup only if no complex features
	return !f.HasUserset && !f.HasRecursive && !f.HasExclusion && !f.HasIntersection
}

// IsClosureCompatible returns true if this relation can participate in a
// closure-based tuple lookup (relation IN ('a', 'b', 'c')) WITHOUT additional
// permission logic.
//
// This returns false for relations with exclusions because when a relation A
// implies relation B (e.g., can_view: viewer), and B has an exclusion
// (e.g., viewer: [user] but not blocked), checking A requires applying B's
// exclusion. A simple closure lookup won't do this.
//
// Note: A relation can still generate code for ITSELF even if it has an
// exclusion - its generated function handles the exclusion. But it can't
// be part of another relation's closure lookup.
func (f RelationFeatures) IsClosureCompatible() bool {
	// Usersets require JOINs, recursive requires function calls
	// Exclusions require the exclusion check to be applied
	// Intersections require AND logic
	return !f.HasUserset && !f.HasRecursive && !f.HasExclusion && !f.HasIntersection
}

// NeedsCycleDetection returns true if the generated function needs cycle detection.
func (f RelationFeatures) NeedsCycleDetection() bool {
	return f.HasRecursive
}

// NeedsPLpgSQL returns true if the generated function needs PL/pgSQL (vs pure SQL).
// Required for cycle detection (variables, IF statements).
func (f RelationFeatures) NeedsPLpgSQL() bool {
	return f.HasRecursive
}

// String returns a human-readable description of the features.
func (f RelationFeatures) String() string {
	var parts []string
	if f.HasDirect {
		parts = append(parts, "Direct")
	}
	if f.HasImplied {
		parts = append(parts, "Implied")
	}
	if f.HasWildcard {
		parts = append(parts, "Wildcard")
	}
	if f.HasUserset {
		parts = append(parts, "Userset")
	}
	if f.HasRecursive {
		parts = append(parts, "Recursive")
	}
	if f.HasExclusion {
		parts = append(parts, "Exclusion")
	}
	if f.HasIntersection {
		parts = append(parts, "Intersection")
	}
	if len(parts) == 0 {
		return "None"
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += "+" + parts[i]
	}
	return result
}

// UsersetPattern represents a [type#relation] pattern in a relation definition.
// For example, [group#member] would have SubjectType="group" and SubjectRelation="member".
type UsersetPattern struct {
	SubjectType     string // e.g., "group"
	SubjectRelation string // e.g., "member"

	// SatisfyingRelations contains all relations in the closure of SubjectRelation.
	// For example, if SubjectRelation="member_c4" and member_c4 is implied by member,
	// this would contain ["member_c4", "member_c3", "member_c2", "member_c1", "member"].
	// This is populated by ComputeCanGenerate from the closure data.
	SatisfyingRelations []string

	// SourceRelation is the relation where this userset pattern is defined.
	// For direct patterns, this is the same as the relation being analyzed.
	// For closure patterns (inherited from implied relations), this is the source relation.
	// e.g., for can_view: viewer where viewer: [group#member], SourceRelation="viewer"
	// This is used by list functions to search for grant tuples in the correct relation.
	SourceRelation string

	// HasWildcard is true if any relation in the closure supports wildcards.
	// When true, the userset check should match membership tuples with subject_id = '*'.
	// This is populated by ComputeCanGenerate from the subject relation's features.
	HasWildcard bool

	// IsComplex is true if any relation in the closure is not closure-compatible
	// (has userset, TTU, exclusion, or intersection). When true, the userset check
	// must call check_permission_internal to verify membership instead of using
	// a simple tuple JOIN.
	// This is populated by ComputeCanGenerate.
	IsComplex bool
}

// ParentRelationInfo represents a "X from Y" pattern (tuple-to-userset).
// For "viewer from parent" on a folder type, this would have:
//   - Relation: "viewer" (the relation to check on the parent)
//   - LinkingRelation: "parent" (the relation that links to the parent)
//   - AllowedLinkingTypes: ["folder"] (types the linking relation can point to)
//
// The actual parent type is determined at runtime from the linking relation's
// subject types. AllowedLinkingTypes captures these for code generation.
type ParentRelationInfo struct {
	Relation            string   // Relation to check on parent (e.g., "viewer")
	LinkingRelation     string   // Relation that links to parent (e.g., "parent")
	AllowedLinkingTypes []string // Types allowed for linking relation (e.g., ["folder", "org"])
}

// IntersectionPart represents one part of an intersection check.
// For "writer and (editor but not owner)", we'd have:
//   - {Relation: "writer"}
//   - {Relation: "editor", ExcludedRelation: "owner"}
type IntersectionPart struct {
	IsThis           bool                // [user] - direct assignment check on the same relation
	HasWildcard      bool                // For IsThis parts: whether direct assignment allows wildcards
	Relation         string              // Relation to check
	ExcludedRelation string              // For nested exclusions like "editor but not owner"
	ParentRelation   *ParentRelationInfo // For tuple-to-userset in intersection
}

// IntersectionGroupInfo represents a group of parts that must ALL be satisfied (AND).
// Multiple groups are OR'd together.
type IntersectionGroupInfo struct {
	Parts []IntersectionPart
}

// IndirectAnchorInfo describes how to reach a relation with direct grants
// when the relation itself has no direct/implied access paths.
// This enables list generation for "pure" patterns like pure TTU or pure userset
// by tracing through to find an anchor relation that has [user] or similar direct grants.
//
// For example, for "document.viewer: viewer from folder" where folder.viewer: [user],
// the IndirectAnchor would point to folder.viewer with a TTU path step.
type IndirectAnchorInfo struct {
	// Path describes the traversal from this relation to the anchor.
	// For "document.viewer: viewer from folder" where folder.viewer: [user],
	// Path would contain one TTU step pointing to folder.viewer.
	// For multi-hop chains, Path contains multiple steps.
	Path []AnchorPathStep

	// AnchorType and AnchorRelation identify the relation with direct grants.
	// This is the final destination of the path - a relation that has HasDirect.
	AnchorType     string
	AnchorRelation string
}

// AnchorPathStep represents one step in the path from a relation to its anchor.
// Steps can be either TTU (tuple-to-userset) or userset patterns.
type AnchorPathStep struct {
	// Type is either "ttu" or "userset"
	Type string

	// For TTU patterns (e.g., "viewer from parent"):
	// LinkingRelation is "parent" (the relation that links to the parent object)
	// TargetType is the first parent object type found with an anchor (e.g., "folder")
	// TargetRelation is the relation to check on the parent (e.g., "viewer")
	// AllTargetTypes contains ALL object types that the linking relation can point to
	// when each type has the target relation with direct grants. This is used for
	// generating UNION queries when parent can be multiple types (e.g., [document, folder]).
	// RecursiveTypes contains object types where the target relation is recursive
	// (same type as the object type being checked). These require check_permission_internal
	// instead of list function composition to handle the recursion correctly.
	LinkingRelation string
	TargetType      string
	TargetRelation  string
	AllTargetTypes  []string
	RecursiveTypes  []string

	// For userset patterns (e.g., [group#member]):
	// SubjectType is "group"
	// SubjectRelation is "member"
	SubjectType     string
	SubjectRelation string
}

// RelationAnalysis contains all data needed to generate SQL for a relation.
// This struct is populated by AnalyzeRelations and consumed by the SQL generator.
type RelationAnalysis struct {
	ObjectType string           // The object type (e.g., "document")
	Relation   string           // The relation name (e.g., "viewer")
	Features   RelationFeatures // Feature flags determining what SQL to generate

	// CanGenerate is true if this relation can use generated SQL for check.
	// This is computed by checking both the relation's own features AND
	// ensuring all relations in the satisfying closure are simply resolvable.
	// Set by ComputeCanGenerate after all relations are analyzed.
	CanGenerate bool

	// CannotGenerateReason explains why CanGenerate is false.
	// Empty when CanGenerate is true.
	CannotGenerateReason string

	// CanGenerateListValue is true if this relation can use generated SQL for list functions.
	// List functions have stricter requirements than check functions because they use
	// set operations (UNION, EXCEPT) rather than boolean composition.
	// Set by ComputeCanGenerate after all relations are analyzed.
	CanGenerateListValue bool

	// CannotGenerateListReason explains why CanGenerateListValue is false.
	// Empty when CanGenerateListValue is true.
	CannotGenerateListReason string

	// For Direct/Implied patterns - from closure table
	SatisfyingRelations []string // Relations that satisfy this one (e.g., ["viewer", "editor", "owner"])

	// For Exclusion patterns
	ExcludedRelations []string // Relations to exclude (for simple "but not X" patterns)

	// SimpleExcludedRelations are excluded relations that can be checked with
	// a direct tuple lookup (no userset, TTU, exclusion, or intersection).
	SimpleExcludedRelations []string

	// ComplexExcludedRelations are excluded relations that need function calls
	// (have userset, TTU, exclusion, intersection, or implied closure).
	// The generated code will call check_permission_internal for these.
	ComplexExcludedRelations []string

	// ExcludedParentRelations captures "but not X from Y" patterns (TTU exclusions).
	// These are resolved by looking up the linking relation Y and calling
	// check_permission_internal for relation X on each linked object.
	ExcludedParentRelations []ParentRelationInfo

	// ExcludedIntersectionGroups captures "but not (A and B)" patterns.
	// These are resolved by ANDing together check_permission_internal calls
	// for each relation in the group.
	ExcludedIntersectionGroups []IntersectionGroupInfo

	// For Userset patterns
	UsersetPatterns []UsersetPattern // [group#member] patterns

	// For Recursive patterns (tuple-to-userset)
	ParentRelations []ParentRelationInfo

	// For Intersection patterns
	IntersectionGroups []IntersectionGroupInfo

	// HasComplexUsersetPatterns is true if any userset pattern is complex
	// (requires check_permission_internal call). When true, the generated
	// function needs PL/pgSQL with cycle detection.
	HasComplexUsersetPatterns bool

	// Direct subject types (for generating direct tuple checks)
	DirectSubjectTypes []string // e.g., ["user", "org"]

	// AllowedSubjectTypes is the union of all subject types from satisfying relations.
	// This is used to enforce type restrictions in generated SQL.
	// Computed by ComputeCanGenerate.
	AllowedSubjectTypes []string

	// SimpleClosureRelations contains relations in the closure that can use tuple lookup.
	// These are relations without exclusions, usersets, recursion, or intersections.
	// Computed by ComputeCanGenerate.
	SimpleClosureRelations []string

	// ComplexClosureRelations contains relations in the closure that need function calls.
	// These are relations with exclusions that are themselves generatable.
	// Computed by ComputeCanGenerate.
	ComplexClosureRelations []string

	// ClosureUsersetPatterns contains userset patterns from closure relations.
	// For example, if `can_view: viewer` and `viewer: [group#member]`, then
	// can_view's ClosureUsersetPatterns includes group#member.
	// This is used by list functions to expand usersets from implied relations.
	// Computed by ComputeCanGenerate.
	ClosureUsersetPatterns []UsersetPattern

	// ClosureParentRelations contains TTU patterns from closure relations.
	// For example, if `can_view: viewer` and `viewer: viewer from parent`, then
	// can_view's ClosureParentRelations includes the parent relation info.
	// This is used by list functions to traverse TTU paths from implied relations.
	// Computed by ComputeCanGenerate.
	ClosureParentRelations []ParentRelationInfo

	// ClosureExcludedRelations contains excluded relations from closure relations.
	// For example, if `can_read: reader` and `reader: [user] but not restricted`,
	// then can_read's ClosureExcludedRelations includes "restricted".
	// This ensures exclusions are applied when accessing through implied relations.
	// Computed by ComputeCanGenerate.
	ClosureExcludedRelations []string

	// IndirectAnchor describes how to reach a relation with direct grants when this
	// relation has no direct/implied access paths. For pure TTU or pure userset patterns,
	// this traces through the pattern to find an anchor relation with [user] or similar.
	// Nil if the relation has direct/implied access or if no anchor can be found.
	// Computed by ComputeCanGenerate via findIndirectAnchor.
	IndirectAnchor *IndirectAnchorInfo

	// MaxUsersetDepth is the maximum userset chain depth reachable from this relation.
	// -1 means infinite (self-referential userset cycle), 0 means no userset patterns.
	// Values >= 25 indicate the relation will always exceed the depth limit.
	// Computed by ComputeCanGenerate via computeMaxUsersetDepth.
	MaxUsersetDepth int

	// ExceedsDepthLimit is true if MaxUsersetDepth >= 25.
	// These relations generate functions that immediately raise M2002.
	ExceedsDepthLimit bool

	// SelfReferentialUsersets lists userset patterns that reference the same type and relation.
	// e.g., for group.member: [user, group#member], this would contain
	// UsersetPattern{SubjectType: "group", SubjectRelation: "member"}
	// These patterns require recursive CTEs to expand nested group membership.
	// Computed by ComputeCanGenerate.
	SelfReferentialUsersets []UsersetPattern

	// HasSelfReferentialUserset is true if len(SelfReferentialUsersets) > 0.
	// When true, the list templates use recursive CTEs to expand the userset chain.
	HasSelfReferentialUserset bool
}

// AnalyzeRelations classifies all relations and gathers data needed for SQL generation.
// It uses the precomputed closure to determine satisfying relations for each relation.
func AnalyzeRelations(types []TypeDefinition, closure []ClosureRow) []RelationAnalysis {
	// Build closure lookup: (object_type, relation) -> satisfying relations
	closureLookup := buildClosureLookup(closure)

	var results []RelationAnalysis
	for _, t := range types {
		for _, r := range t.Relations {
			analysis := analyzeRelation(t, r, closureLookup)
			results = append(results, analysis)
		}
	}
	return results
}

// buildClosureLookup creates a nested map for efficient closure lookups.
// Returns map[objectType][relation] -> []satisfyingRelations
func buildClosureLookup(closure []ClosureRow) map[string]map[string][]string {
	lookup := make(map[string]map[string][]string)
	for _, row := range closure {
		if lookup[row.ObjectType] == nil {
			lookup[row.ObjectType] = make(map[string][]string)
		}
		lookup[row.ObjectType][row.Relation] = append(
			lookup[row.ObjectType][row.Relation],
			row.SatisfyingRelation,
		)
	}
	return lookup
}

// analyzeRelation performs detailed analysis of a single relation.
func analyzeRelation(
	t TypeDefinition,
	r RelationDefinition,
	closureLookup map[string]map[string][]string,
) RelationAnalysis {
	analysis := RelationAnalysis{
		ObjectType: t.Name,
		Relation:   r.Name,
	}

	// Gather satisfying relations from closure
	if typeClosures, ok := closureLookup[t.Name]; ok {
		if rels, ok := typeClosures[r.Name]; ok {
			analysis.SatisfyingRelations = rels
		}
	}

	// Collect userset patterns
	analysis.UsersetPatterns = collectUsersetPatterns(r)

	// Collect parent relations (tuple-to-userset)
	analysis.ParentRelations = collectParentRelations(r)

	// Collect excluded relations
	analysis.ExcludedRelations = collectExcludedRelations(r)

	// Collect excluded parent relations (TTU exclusions like "but not viewer from parent")
	analysis.ExcludedParentRelations = collectExcludedParentRelations(r)

	// Collect excluded intersection groups (like "but not (editor and owner)")
	analysis.ExcludedIntersectionGroups = collectExcludedIntersectionGroups(r)

	// Collect intersection groups
	analysis.IntersectionGroups = collectIntersectionGroups(r)

	// Collect direct subject types
	analysis.DirectSubjectTypes = collectDirectSubjectTypes(r)

	// Build feature flags
	analysis.Features = detectFeatures(r, analysis)

	return analysis
}

// detectFeatures identifies which features a relation uses.
// Multiple features can be present - they will be composed in generated SQL.
func detectFeatures(r RelationDefinition, analysis RelationAnalysis) RelationFeatures {
	// Check for TTU in top-level parent relations
	hasRecursive := len(analysis.ParentRelations) > 0

	// Also check for TTU inside intersection groups - these also need cycle detection
	if !hasRecursive {
		for _, ig := range analysis.IntersectionGroups {
			for _, part := range ig.Parts {
				if part.ParentRelation != nil {
					hasRecursive = true
					break
				}
			}
			if hasRecursive {
				break
			}
		}
	}

	return RelationFeatures{
		HasDirect:       len(analysis.DirectSubjectTypes) > 0,
		HasImplied:      len(r.ImpliedBy) > 0,
		HasWildcard:     hasWildcardRefs(r),
		HasUserset:      len(analysis.UsersetPatterns) > 0,
		HasRecursive:    hasRecursive,
		HasExclusion:    len(analysis.ExcludedRelations) > 0 || len(r.ExcludedParentRelations) > 0 || len(r.ExcludedIntersectionGroups) > 0,
		HasIntersection: len(r.IntersectionGroups) > 0,
	}
}

// collectDirectSubjectTypes extracts direct subject types (not usersets).
func collectDirectSubjectTypes(r RelationDefinition) []string {
	var types []string
	seen := make(map[string]bool)

	// From SubjectTypeRefs (preferred)
	for _, ref := range r.SubjectTypeRefs {
		if ref.Relation == "" && !seen[ref.Type] {
			// Direct reference (not a userset like group#member)
			types = append(types, ref.Type)
			seen[ref.Type] = true
		}
	}

	return types
}

// hasWildcardRefs checks if any subject type reference allows wildcards.
func hasWildcardRefs(r RelationDefinition) bool {
	for _, ref := range r.SubjectTypeRefs {
		if ref.Wildcard {
			return true
		}
	}
	return false
}

// collectUsersetPatterns extracts [type#relation] patterns from a relation.
func collectUsersetPatterns(r RelationDefinition) []UsersetPattern {
	var patterns []UsersetPattern
	for _, ref := range r.SubjectTypeRefs {
		if ref.Relation != "" {
			patterns = append(patterns, UsersetPattern{
				SubjectType:     ref.Type,
				SubjectRelation: ref.Relation,
			})
		}
	}
	return patterns
}

// collectParentRelations extracts "X from Y" patterns from a relation.
func collectParentRelations(r RelationDefinition) []ParentRelationInfo {
	var parents []ParentRelationInfo
	seen := make(map[string]bool) // Deduplicate by "relation:linkingRelation" key

	// New field: ParentRelations slice
	for _, pr := range r.ParentRelations {
		key := pr.Relation + ":" + pr.LinkingRelation
		if seen[key] {
			continue
		}
		seen[key] = true
		parents = append(parents, ParentRelationInfo{
			Relation:        pr.Relation,
			LinkingRelation: pr.LinkingRelation,
		})
	}

	return parents
}

// collectExcludedRelations extracts all "but not X" patterns from a relation.
func collectExcludedRelations(r RelationDefinition) []string {
	var excluded []string

	// New field: ExcludedRelations slice
	excluded = append(excluded, r.ExcludedRelations...)

	return excluded
}

// collectExcludedParentRelations extracts TTU exclusions like "but not viewer from parent".
func collectExcludedParentRelations(r RelationDefinition) []ParentRelationInfo {
	var excluded []ParentRelationInfo
	for _, pr := range r.ExcludedParentRelations {
		excluded = append(excluded, ParentRelationInfo{
			Relation:        pr.Relation,
			LinkingRelation: pr.LinkingRelation,
		})
	}
	return excluded
}

// collectExcludedIntersectionGroups extracts intersection exclusions like "but not (editor and owner)"
// and nested exclusions like "but not (editor but not owner)".
func collectExcludedIntersectionGroups(r RelationDefinition) []IntersectionGroupInfo {
	var groups []IntersectionGroupInfo
	for _, ig := range r.ExcludedIntersectionGroups {
		group := IntersectionGroupInfo{}
		// Add relation checks
		for _, rel := range ig.Relations {
			part := IntersectionPart{
				Relation: rel,
			}
			// Check for nested exclusions within the intersection part
			// For "but not (editor but not owner)", ig.Exclusions["editor"] = ["owner"]
			// NOTE: Currently only the first exclusion per relation is supported.
			// Multiple exclusions on the same relation (e.g., "editor but not a but not b")
			// are not common in FGA schemas and would require IntersectionPart.ExcludedRelations
			// to be a slice. For now, we take the first exclusion.
			if excls, ok := ig.Exclusions[rel]; ok && len(excls) > 0 {
				part.ExcludedRelation = excls[0]
			}
			group.Parts = append(group.Parts, part)
		}
		// Add parent relation checks (rare but possible)
		for _, pr := range ig.ParentRelations {
			part := IntersectionPart{
				Relation: pr.Relation,
				ParentRelation: &ParentRelationInfo{
					Relation:        pr.Relation,
					LinkingRelation: pr.LinkingRelation,
				},
			}
			group.Parts = append(group.Parts, part)
		}
		if len(group.Parts) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

// collectIntersectionGroups converts IntersectionGroup to IntersectionGroupInfo.
func collectIntersectionGroups(r RelationDefinition) []IntersectionGroupInfo {
	var groups []IntersectionGroupInfo

	for _, ig := range r.IntersectionGroups {
		group := IntersectionGroupInfo{}

		// Add relation checks
		for _, rel := range ig.Relations {
			part := IntersectionPart{
				Relation: rel,
			}
			// Check if this is a self-reference (same as the relation being defined)
			if rel == r.Name {
				part.IsThis = true
				// For "This" parts, check if the relation's own direct assignments allow wildcards.
				// This is distinct from the relation's overall HasWildcard, which may include
				// wildcards inherited from closure relations.
				part.HasWildcard = hasWildcardRefs(r)
			}
			// Check for nested exclusions (see note in collectExcludedIntersectionGroups)
			if excls, ok := ig.Exclusions[rel]; ok && len(excls) > 0 {
				part.ExcludedRelation = excls[0]
			}
			group.Parts = append(group.Parts, part)
		}

		// Add parent relation checks
		for _, pr := range ig.ParentRelations {
			part := IntersectionPart{
				Relation: pr.Relation,
				ParentRelation: &ParentRelationInfo{
					Relation:        pr.Relation,
					LinkingRelation: pr.LinkingRelation,
				},
			}
			group.Parts = append(group.Parts, part)
		}

		if len(group.Parts) > 0 {
			groups = append(groups, group)
		}
	}

	return groups
}

// buildAnalysisLookup creates a nested map for efficient analysis lookups.
// Returns map[objectType][relation] -> *RelationAnalysis
func buildAnalysisLookup(analyses []RelationAnalysis) map[string]map[string]*RelationAnalysis {
	lookup := make(map[string]map[string]*RelationAnalysis)
	for i := range analyses {
		a := &analyses[i]
		if lookup[a.ObjectType] == nil {
			lookup[a.ObjectType] = make(map[string]*RelationAnalysis)
		}
		lookup[a.ObjectType][a.Relation] = a
	}
	return lookup
}

// sortByDependency performs topological sort on analyses based on all dependencies.
// Returns sorted analyses where each relation is processed after its dependencies.
// This ensures that when we check if a relation CanGenerate, all relations it depends on
// have already been evaluated.
//
// Dependencies include:
// - SatisfyingRelations (closure): implied-by relationships
// - IntersectionGroups: relations referenced in AND groups
// - ExcludedRelations: relations in "but not" clauses
func sortByDependency(analyses []RelationAnalysis) []RelationAnalysis {
	// Build lookup map for finding analyses during dependency tracking
	lookup := buildAnalysisLookup(analyses)

	// Build dependency graph: relation -> relations it depends on
	deps := make(map[string][]string) // key: "type.relation"
	seen := make(map[string]map[string]bool) // Deduplicate dependencies

	addDep := func(key, dep string) {
		if seen[key] == nil {
			seen[key] = make(map[string]bool)
		}
		if !seen[key][dep] {
			seen[key][dep] = true
			deps[key] = append(deps[key], dep)
		}
	}

	for _, a := range analyses {
		key := a.ObjectType + "." + a.Relation

		// Dependencies from closure (SatisfyingRelations)
		for _, rel := range a.SatisfyingRelations {
			if rel != a.Relation { // Skip self
				addDep(key, a.ObjectType+"."+rel)
			}
		}

		// Dependencies from TTU patterns (ParentRelations) - cross-type dependencies
		// This ensures that when we process a TTU pattern like "viewer from parent",
		// the target relation on the parent type has already been processed.
		// Note: AllowedLinkingTypes isn't populated yet, so we look up the linking
		// relation's DirectSubjectTypes directly.
		for _, parent := range a.ParentRelations {
			// Look up the linking relation to find what types it allows
			linkingRel := parent.LinkingRelation
			linkingAnalysis := lookup[a.ObjectType][linkingRel]
			if linkingAnalysis == nil {
				continue
			}
			for _, parentType := range linkingAnalysis.DirectSubjectTypes {
				if parentType != a.ObjectType { // Cross-type dependency
					addDep(key, parentType+"."+parent.Relation)
				}
			}
		}

		// Dependencies from intersection groups
		for _, group := range a.IntersectionGroups {
			for _, part := range group.Parts {
				// Skip "This" patterns and TTU patterns (handled inline)
				if part.IsThis || part.ParentRelation != nil {
					continue
				}
				if part.Relation != "" && part.Relation != a.Relation {
					addDep(key, a.ObjectType+"."+part.Relation)
				}
			}
		}

		// Dependencies from excluded relations
		for _, rel := range a.ExcludedRelations {
			if rel != a.Relation {
				addDep(key, a.ObjectType+"."+rel)
			}
		}

		// Dependencies from SAME-TYPE userset patterns only.
		// This ensures subject relations are processed first so AllowedSubjectTypes
		// can be propagated correctly through userset chains.
		// For example, in "a2: [resource#a1]" on type resource, a2 depends on a1.
		// Cross-type usersets (e.g., folder.viewer: [group#member]) are NOT added
		// as dependencies because they don't need type propagation in the same way.
		for _, pattern := range a.UsersetPatterns {
			// Only add dependency for same-type usersets
			if pattern.SubjectType == a.ObjectType {
				depKey := pattern.SubjectType + "." + pattern.SubjectRelation
				if depKey != key { // Skip self-reference
					addDep(key, depKey)
				}
			}
		}
	}

	// Build reverse mapping: for each relation, which relations depend on it
	dependents := make(map[string][]string)
	for key, depList := range deps {
		for _, dep := range depList {
			dependents[dep] = append(dependents[dep], key)
		}
	}

	// Start with relations that have no dependencies
	var queue []string
	for _, a := range analyses {
		key := a.ObjectType + "." + a.Relation
		if len(deps[key]) == 0 {
			queue = append(queue, key)
		}
	}

	// Process in order using Kahn's algorithm
	var sorted []RelationAnalysis
	processed := make(map[string]bool)
	// Note: lookup already built at start of function

	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]

		if processed[key] {
			continue
		}
		processed[key] = true

		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			continue
		}
		a := lookup[parts[0]][parts[1]]
		if a == nil {
			continue
		}
		sorted = append(sorted, *a)

		// Add dependents whose dependencies are now satisfied
		for _, depKey := range dependents[key] {
			allDepsProcessed := true
			for _, dep := range deps[depKey] {
				if !processed[dep] {
					allDepsProcessed = false
					break
				}
			}
			if allDepsProcessed && !processed[depKey] {
				queue = append(queue, depKey)
			}
		}
	}

	// Handle cycles or unprocessed (add remaining)
	for _, a := range analyses {
		key := a.ObjectType + "." + a.Relation
		if !processed[key] {
			sorted = append(sorted, a)
		}
	}

	return sorted
}

// CanGenerateList returns true if we can generate specialized list SQL for this relation.
// This returns the CanGenerateListValue field which is computed by ComputeCanGenerate.
//
// List functions have different constraints than check functions because they use set
// operations (UNION, EXCEPT, INTERSECT) rather than boolean composition.
//
// This is progressively relaxed as more list codegen patterns are implemented:
// - Phase 2: Direct/Implied patterns (simple tuple lookup with closure)
// - Phase 3+: Will add Exclusion support
// - Phase 4+: Will add Userset support
// - Phase 5+: Will add Recursive/TTU support
//
// When CanGenerateList returns false, the list dispatcher falls through to the
// generic list functions (list_accessible_objects, list_accessible_subjects).
func (a *RelationAnalysis) CanGenerateList() bool {
	return a.CanGenerateListValue
}

// canGenerateListFeatures checks if the given features allow list generation.
// This checks a single relation's features - the full check in ComputeCanGenerate
// also validates that ALL relations in the closure have compatible features.
//
// The hasIndirectAnchor parameter indicates whether an indirect anchor was found
// via findIndirectAnchor(). When true, list generation is allowed even without
// direct/implied access paths, as the access comes through the indirect anchor.
func canGenerateListFeatures(f RelationFeatures, hasIndirectAnchor bool) (bool, string) {
	// Phase 8: If we have an indirect anchor, we can generate even without direct/implied.
	// The composed template will trace through TTU paths to the anchor.
	if hasIndirectAnchor {
		// Indirect anchor provides access path - allow generation.
		// The composed template will compose with the anchor relation's list function.
		return true, ""
	}

	// Phase 2 & 4: Support Direct, Implied, Userset, and Wildcard patterns.
	// These can all be handled with tuple lookup or JOIN patterns.
	//
	// Must have at least one access path:
	// - Direct: relation has [user] or similar type restriction
	// - Implied: relation references another relation (can_view: viewer)
	// - Userset: relation has [group#member] patterns (Phase 4 handles these)
	// - Recursive (TTU): relation has "viewer from parent" patterns (Phase 5 handles these)
	//
	// Any of these is sufficient as long as the closure validation passes.
	// Phase 6: HasIntersection is also a valid access path - intersection groups
	// combine direct relations and/or TTU patterns into a single access check.
	if !f.HasDirect && !f.HasImplied && !f.HasUserset && !f.HasRecursive && !f.HasIntersection {
		return false, "no access path (neither direct nor implied)"
	}

	// HasWildcard is allowed: SubjectIDCheck handles wildcard matching,
	// and model changes regenerate functions with correct behavior.
	// Type guards (AllowedSubjectTypes) ensure type restrictions are enforced.

	// HasImplied is allowed: the relation closure (SatisfyingRelations) is inlined
	// into the RelationList. The closure validation in computeCanGenerateList
	// ensures all relations in the closure are themselves simple.

	// Phase 3: HasExclusion is now supported via NOT EXISTS anti-join
	// or check_permission_internal for complex excluded relations.

	// Phase 4: HasUserset is now supported via UNION with JOIN patterns.
	// Simple userset patterns use tuple JOINs; complex patterns (where the
	// subject relation has TTU/exclusion/etc.) use check_permission_internal.

	// Phase 5: HasRecursive is now supported via recursive CTEs.
	// Self-referential TTU uses true recursive CTEs with depth limit.
	// Cross-type TTU uses check_permission_internal on parent objects.

	// Phase 6: HasIntersection is now supported via INTERSECT set operations.
	// Each intersection group uses INTERSECT on its parts, groups are UNION'd.
	// For list_subjects, candidates are gathered and filtered with check_permission.

	return true, ""
}

// ComputeCanGenerate walks the dependency graph and sets CanGenerate on each analysis.
// A relation can be generated if:
// 1. Its own features allow generation (CanGenerate() on features returns true)
// 2. ALL relations in its satisfying closure are either:
//   - Simply resolvable (can use tuple lookup), OR
//   - Complex but generatable (have exclusions but can generate their own function)
// 3. If the relation has exclusions, excluded relations are classified as:
//   - Simple: can use direct tuple lookup (simply resolvable AND no implied closure)
//   - Complex: use check_permission_internal call (has TTU, userset, intersection, etc.)
//
// For relations in the closure that need function calls (have exclusions but are generatable),
// the generated SQL will call their specialized check function rather than using tuple lookup.
//
// This function also:
// - Propagates HasWildcard: if ANY relation in the closure supports wildcards
// - Collects AllowedSubjectTypes: union of all subject types from satisfying relations
// - Partitions closure relations into SimpleClosureRelations and ComplexClosureRelations
// - Partitions excluded relations into SimpleExcludedRelations and ComplexExcludedRelations
func ComputeCanGenerate(analyses []RelationAnalysis) []RelationAnalysis {
	// Sort by dependency order first - ensures relations are processed after their dependencies
	sorted := sortByDependency(analyses)

	// Build lookup map on sorted analyses: (objectType, relation) -> *RelationAnalysis
	lookup := buildAnalysisLookup(sorted)

	// For each analysis, check if it can be generated and propagate properties
	for i := range sorted {
		a := &sorted[i]

		// Collect allowed subject types from all satisfying relations
		// This ensures type restrictions are enforced correctly
		seenTypes := make(map[string]bool)
		for _, rel := range a.SatisfyingRelations {
			relAnalysis, ok := lookup[a.ObjectType][rel]
			if !ok {
				continue
			}
			for _, t := range relAnalysis.DirectSubjectTypes {
				if !seenTypes[t] {
					seenTypes[t] = true
					a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
				}
			}
			// Also propagate AllowedSubjectTypes from satisfying relations.
			// This is critical for purely implied relations (like "can_view: viewer")
			// where the satisfying relation (viewer) may only have AllowedSubjectTypes
			// from TTU chains, not DirectSubjectTypes.
			for _, t := range relAnalysis.AllowedSubjectTypes {
				if !seenTypes[t] {
					seenTypes[t] = true
					a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
				}
			}
			// Also propagate wildcard flag
			if relAnalysis.Features.HasWildcard {
				a.Features.HasWildcard = true
			}
			// For userset patterns in closure relations, also include the subject types
			// from the userset's subject relation. E.g., for viewer: [group#member],
			// include subject types from group.member (user).
			for _, pattern := range relAnalysis.UsersetPatterns {
				subjectAnalysis, ok := lookup[pattern.SubjectType][pattern.SubjectRelation]
				if !ok {
					continue
				}
				// Add types from the userset's subject relation
				for _, t := range subjectAnalysis.DirectSubjectTypes {
					if !seenTypes[t] {
						seenTypes[t] = true
						a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
					}
				}
				for _, t := range subjectAnalysis.AllowedSubjectTypes {
					if !seenTypes[t] {
						seenTypes[t] = true
						a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
					}
				}
			}
		}

		// Also propagate from this relation's own userset patterns
		// This handles cases like viewer: [group#member] where we need to
		// include subject types from group.member (user).
		for _, pattern := range a.UsersetPatterns {
			subjectAnalysis, ok := lookup[pattern.SubjectType][pattern.SubjectRelation]
			if !ok {
				continue
			}
			for _, t := range subjectAnalysis.DirectSubjectTypes {
				if !seenTypes[t] {
					seenTypes[t] = true
					a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
				}
			}
			for _, t := range subjectAnalysis.AllowedSubjectTypes {
				if !seenTypes[t] {
					seenTypes[t] = true
					a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
				}
			}
			// Propagate wildcard from userset subject relation
			if subjectAnalysis.Features.HasWildcard {
				a.Features.HasWildcard = true
			}
		}

		// Populate SatisfyingRelations and HasWildcard for userset patterns from the closure.
		// For [group#member_c4], we need to look up the closure for group.member_c4
		// to get all relations that satisfy member_c4 (e.g., member, member_c1, etc.)
		for i := range a.UsersetPatterns {
			pattern := &a.UsersetPatterns[i]
			subjectAnalysis, ok := lookup[pattern.SubjectType][pattern.SubjectRelation]
			if !ok {
				// Subject relation not found - use just the relation itself
				pattern.SatisfyingRelations = []string{pattern.SubjectRelation}
				continue
			}
			// Use the satisfying relations from the subject relation's closure
			if len(subjectAnalysis.SatisfyingRelations) > 0 {
				pattern.SatisfyingRelations = subjectAnalysis.SatisfyingRelations
			} else {
				// No closure computed - use just the relation itself
				pattern.SatisfyingRelations = []string{pattern.SubjectRelation}
			}
			// Propagate wildcard flag from subject relation
			// If any relation in the closure supports wildcards, the userset check
			// needs to match membership tuples with subject_id = '*'
			for _, rel := range pattern.SatisfyingRelations {
				relAnalysis, ok := lookup[pattern.SubjectType][rel]
				if ok && relAnalysis.Features.HasWildcard {
					pattern.HasWildcard = true
					break
				}
			}
		}

		// Populate AllowedLinkingTypes for parent relations from the linking relation's analysis.
		// This ensures TTU lookups only match parent types allowed by the current model.
		for i := range a.ParentRelations {
			parent := &a.ParentRelations[i]
			linkingAnalysis, ok := lookup[a.ObjectType][parent.LinkingRelation]
			if ok && len(linkingAnalysis.AllowedSubjectTypes) > 0 {
				parent.AllowedLinkingTypes = linkingAnalysis.AllowedSubjectTypes
			} else if ok && len(linkingAnalysis.DirectSubjectTypes) > 0 {
				parent.AllowedLinkingTypes = linkingAnalysis.DirectSubjectTypes
			}
		}

		// Also populate AllowedLinkingTypes for excluded parent relations (TTU exclusions).
		// This ensures TTU exclusion checks only match allowed parent types.
		for i := range a.ExcludedParentRelations {
			excludedParent := &a.ExcludedParentRelations[i]
			linkingAnalysis, ok := lookup[a.ObjectType][excludedParent.LinkingRelation]
			if ok && len(linkingAnalysis.AllowedSubjectTypes) > 0 {
				excludedParent.AllowedLinkingTypes = linkingAnalysis.AllowedSubjectTypes
			} else if ok && len(linkingAnalysis.DirectSubjectTypes) > 0 {
				excludedParent.AllowedLinkingTypes = linkingAnalysis.DirectSubjectTypes
			}
		}

		// Propagate AllowedSubjectTypes from intersection group parts.
		// For intersection patterns like "writer AND viewer from parent", we need to
		// collect subject types from both parts.
		for _, group := range a.IntersectionGroups {
			for _, part := range group.Parts {
				// Skip "This" patterns - already handled by DirectSubjectTypes
				if part.IsThis {
					continue
				}
				// For regular relation parts (like "writer"), get types from that relation
				if part.Relation != "" && part.ParentRelation == nil {
					partAnalysis, ok := lookup[a.ObjectType][part.Relation]
					if ok {
						for _, t := range partAnalysis.DirectSubjectTypes {
							if !seenTypes[t] {
								seenTypes[t] = true
								a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
							}
						}
						for _, t := range partAnalysis.AllowedSubjectTypes {
							if !seenTypes[t] {
								seenTypes[t] = true
								a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
							}
						}
					}
				}
				// For TTU parts (like "viewer from parent"), get types from the target relation
				if part.ParentRelation != nil {
					parent := part.ParentRelation
					// Get the linking relation to find target types
					linkingAnalysis, ok := lookup[a.ObjectType][parent.LinkingRelation]
					if ok {
						// For each possible target type, get the target relation's types
						targetTypes := linkingAnalysis.AllowedSubjectTypes
						if len(targetTypes) == 0 {
							targetTypes = linkingAnalysis.DirectSubjectTypes
						}
						for _, targetType := range targetTypes {
							targetAnalysis, ok := lookup[targetType][parent.Relation]
							if ok {
								for _, t := range targetAnalysis.DirectSubjectTypes {
									if !seenTypes[t] {
										seenTypes[t] = true
										a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
									}
								}
								for _, t := range targetAnalysis.AllowedSubjectTypes {
									if !seenTypes[t] {
										seenTypes[t] = true
										a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
									}
								}
							}
						}
					}
				}
			}
		}

		// Propagate AllowedSubjectTypes from TTU target relations (ParentRelations).
		// For patterns like "viewer from parent" where parent links to folders,
		// we need the subject types from folder.viewer.
		for _, parent := range a.ParentRelations {
			// Get the linking relation to find target types
			linkingAnalysis, ok := lookup[a.ObjectType][parent.LinkingRelation]
			if !ok {
				continue
			}
			// For each possible target type, get the target relation's types
			targetTypes := linkingAnalysis.AllowedSubjectTypes
			if len(targetTypes) == 0 {
				targetTypes = linkingAnalysis.DirectSubjectTypes
			}
			for _, targetType := range targetTypes {
				targetAnalysis, ok := lookup[targetType][parent.Relation]
				if !ok {
					continue
				}
				for _, t := range targetAnalysis.DirectSubjectTypes {
					if !seenTypes[t] {
						seenTypes[t] = true
						a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
					}
				}
				for _, t := range targetAnalysis.AllowedSubjectTypes {
					if !seenTypes[t] {
						seenTypes[t] = true
						a.AllowedSubjectTypes = append(a.AllowedSubjectTypes, t)
					}
				}
			}
		}

		// First check: does this relation's features allow generation?
		if !a.Features.CanGenerate() {
			a.CanGenerate = false
			a.CannotGenerateReason = "features do not allow generation (no access paths)"
			continue
		}

		// Second check: partition satisfying relations into simple vs complex
		// Skip the relation itself - its features are already checked by CanGenerate().
		// - Simple: can use tuple lookup (closure-compatible)
		// - Complex: has exclusion but is itself generatable (delegate to function)
		// Also collect closure exclusions while iterating through satisfying relations.
		canGenerate := true
		cannotGenerateReason := ""
		var simpleRels, complexRels []string
		seenClosureExcl := make(map[string]bool)
		for _, rel := range a.SatisfyingRelations {
			// Skip self - the relation's own features are handled by its generated code
			if rel == a.Relation {
				continue
			}
			relAnalysis, ok := lookup[a.ObjectType][rel]
			if !ok {
				// Unknown relation - shouldn't happen with valid closure, but be safe
				canGenerate = false
				cannotGenerateReason = "unknown relation in closure: " + rel
				break
			}
			if relAnalysis.Features.IsClosureCompatible() {
				// Simple: can use tuple lookup
				simpleRels = append(simpleRels, rel)
			} else if relAnalysis.CanGenerate {
				// Complex but generatable: delegate to check_permission_internal
				complexRels = append(complexRels, rel)
			} else {
				// Truly incompatible: fall back to generic
				canGenerate = false
				cannotGenerateReason = "closure relation not generatable: " + rel
				break
			}

			// Collect closure exclusions from this satisfying relation.
			// This allows implied relations like `can_read: reader` to inherit exclusions
			// from reader (e.g., "but not restricted").
			for _, excl := range relAnalysis.ExcludedRelations {
				if !seenClosureExcl[excl] {
					seenClosureExcl[excl] = true
					a.ClosureExcludedRelations = append(a.ClosureExcludedRelations, excl)
				}
			}
		}

		// Third check: classify excluded relations as simple or complex.
		// Simple exclusions use direct tuple lookup; complex exclusions use function calls.
		// Unknown excluded relations still prevent generation.
		// This includes both the relation's own exclusions AND closure-inherited exclusions.
		var simpleExcluded, complexExcluded []string
		seenExcluded := make(map[string]bool) // Avoid duplicates
		classifyExcluded := func(excludedRel string) bool {
			if seenExcluded[excludedRel] {
				return true // Already processed
			}
			seenExcluded[excludedRel] = true

			excludedAnalysis, ok := lookup[a.ObjectType][excludedRel]
			if !ok {
				// Unknown excluded relation - fall back to generic
				canGenerate = false
				cannotGenerateReason = "unknown excluded relation: " + excludedRel
				return false
			}
			// Classify excluded relation as simple or complex:
			// Simple: can use direct tuple lookup (simply resolvable AND no implied closure)
			// Complex: needs check_permission_internal call
			isSimple := excludedAnalysis.Features.IsSimplyResolvable() &&
				len(excludedAnalysis.SatisfyingRelations) <= 1
			if isSimple {
				simpleExcluded = append(simpleExcluded, excludedRel)
			} else {
				complexExcluded = append(complexExcluded, excludedRel)
			}
			return true
		}

		// Classify the relation's own exclusions
		if canGenerate && a.Features.HasExclusion {
			for _, excludedRel := range a.ExcludedRelations {
				if !classifyExcluded(excludedRel) {
					break
				}
			}
		}

		// Also classify closure-inherited exclusions (for list functions).
		// These are exclusions from implied relations like `can_read: reader`
		// where reader has "but not restricted".
		if canGenerate {
			for _, excludedRel := range a.ClosureExcludedRelations {
				if !classifyExcluded(excludedRel) {
					break
				}
			}
		}

		if canGenerate {
			a.SimpleExcludedRelations = simpleExcluded
			a.ComplexExcludedRelations = complexExcluded
		}

		// Fourth check: if relation has userset patterns, classify each pattern as
		// simple or complex. Simple patterns use tuple JOINs; complex patterns call
		// check_permission_internal for membership verification.
		//
		// A pattern is complex if ANY relation in its closure is not closure-compatible
		// (has userset, TTU, exclusion, or intersection).
		//
		// For complex patterns, we call check_permission_internal which always works
		// because it falls back to check_permission_generic_internal for non-generatable
		// relations. So we don't need to check if the subject relation is generatable -
		// we just mark the pattern as complex and use the function call approach.
		//
		// We only reject if a relation in the closure is truly unknown (not in the schema).
		if canGenerate && a.Features.HasUserset {
			for i := range a.UsersetPatterns {
				pattern := &a.UsersetPatterns[i]
				patternIsComplex := false

				for _, rel := range pattern.SatisfyingRelations {
					relAnalysis, ok := lookup[pattern.SubjectType][rel]
					if !ok {
						// Unknown relation - fall back to generic
						canGenerate = false
						cannotGenerateReason = "unknown relation in userset closure: " + pattern.SubjectType + "#" + rel
						break
					}

					// If any relation in the closure is not closure-compatible, the pattern is complex.
					// We'll use check_permission_internal which handles this via generic fallback.
					if !relAnalysis.Features.IsClosureCompatible() {
						patternIsComplex = true
						// No need to verify the subject relation is generatable -
						// check_permission_internal falls back to generic for non-generatable relations
					}
				}
				if !canGenerate {
					break
				}
				pattern.IsComplex = patternIsComplex
				if patternIsComplex {
					a.HasComplexUsersetPatterns = true
				}
			}
		}

		// Fifth check: if relation has recursive/TTU patterns, verify parent relations
		// have valid data. The parent type may vary at runtime (e.g., parent: [folder, org]),
		// so we use check_permission_internal to dispatch to the correct type's function.
		// This check just ensures the parent relation definitions are present.
		if canGenerate && a.Features.HasRecursive {
			for _, parent := range a.ParentRelations {
				if parent.LinkingRelation == "" || parent.Relation == "" {
					// Invalid parent relation data - fall back to generic
					canGenerate = false
					cannotGenerateReason = "invalid parent relation data (empty linking or relation)"
					break
				}
			}
		}

		// Sixth check: if relation has intersection groups, verify all parts can be
		// generated or are special patterns we can handle (This, TTU).
		// For regular relation parts, we call their check function, so they must be generatable.
		if canGenerate && a.Features.HasIntersection {
			for _, group := range a.IntersectionGroups {
				for _, part := range group.Parts {
					// This pattern and TTU patterns are handled inline, no dependency check needed
					if part.IsThis || part.ParentRelation != nil {
						continue
					}
					// Regular relation part - must be generatable
					partAnalysis, ok := lookup[a.ObjectType][part.Relation]
					if !ok {
						// Unknown relation - fall back to generic
						canGenerate = false
						cannotGenerateReason = "unknown relation in intersection: " + part.Relation
						break
					}
					// The part's relation must be generatable since we call its function
					// Note: We check CanGenerate on the analysis, which is computed in dependency order
					if !partAnalysis.CanGenerate {
						canGenerate = false
						cannotGenerateReason = "intersection part not generatable: " + part.Relation
						break
					}
				}
				if !canGenerate {
					break
				}
			}
		}

		if canGenerate {
			a.SimpleClosureRelations = simpleRels
			a.ComplexClosureRelations = complexRels

			// Collect userset patterns from all closure relations (for list functions).
			// This allows implied relations like `can_view: viewer` to expand usersets
			// from viewer when generating list functions for can_view.
			var closureUsersetPatterns []UsersetPattern
			seen := make(map[string]bool)
			for _, rel := range a.SatisfyingRelations {
				if rel == a.Relation {
					continue
				}
				relAnalysis, ok := lookup[a.ObjectType][rel]
				if !ok {
					continue
				}
				for _, pattern := range relAnalysis.UsersetPatterns {
					key := pattern.SubjectType + "#" + pattern.SubjectRelation
					if !seen[key] {
						seen[key] = true
						// Copy the pattern and set SourceRelation to the closure relation
						// so list functions know which relation to search for grant tuples
						patternCopy := pattern
						patternCopy.SourceRelation = rel
						closureUsersetPatterns = append(closureUsersetPatterns, patternCopy)
					}
				}
			}
			a.ClosureUsersetPatterns = closureUsersetPatterns

			// Collect parent relations from all closure relations (for list functions).
			// This allows implied relations like `can_view: viewer` to traverse TTU paths
			// from viewer when generating list functions for can_view.
			var closureParentRelations []ParentRelationInfo
			seenParent := make(map[string]bool)
			for _, rel := range a.SatisfyingRelations {
				if rel == a.Relation {
					continue
				}
				relAnalysis, ok := lookup[a.ObjectType][rel]
				if !ok {
					continue
				}
				for _, parent := range relAnalysis.ParentRelations {
					// Create a unique key for this parent relation pattern
					key := parent.LinkingRelation + "->" + parent.Relation
					if !seenParent[key] {
						seenParent[key] = true
						closureParentRelations = append(closureParentRelations, parent)
					}
				}
			}
			a.ClosureParentRelations = closureParentRelations

			// Note: ClosureExcludedRelations is now collected earlier in the "Second check"
			// loop, before exclusion classification. This ensures exclusions from closure
			// relations are properly classified as simple or complex.
		} else {
			a.CannotGenerateReason = cannotGenerateReason
		}

		// Design decision: Always generate specialized functions for all relations.
		//
		// Previously, complex patterns (deep TTU, complex usersets, etc.) would fall back
		// to check_permission_generic. Now we generate specialized SQL for every relation,
		// using check_permission_internal to delegate when needed. This achieves:
		//   1. Consistent code path for all permission checks
		//   2. Better debugging (all relations have named functions)
		//   3. Potential for future optimizations in generated code
		//
		// The checks above still run to populate SimpleClosureRelations, ComplexClosureRelations,
		// and other classification data used by the code generator. CannotGenerateReason is
		// preserved for diagnostics but CanGenerate is always true.
		a.CanGenerate = true

		// Compute maximum userset depth for this relation.
		// This enables generating functions that immediately raise M2002 for
		// relations with userset chains exceeding the 25-level depth limit.
		a.MaxUsersetDepth = computeMaxUsersetDepth(a, lookup)
		a.ExceedsDepthLimit = a.MaxUsersetDepth >= 25

		// Detect self-referential userset patterns for list function generation.
		// These are patterns like [group#member] on group.member where the userset
		// references the same type and relation, requiring recursive CTEs.
		a.SelfReferentialUsersets = detectSelfReferentialUsersets(a)
		a.HasSelfReferentialUserset = len(a.SelfReferentialUsersets) > 0

		// Compute CanGenerateListValue for list function generation.
		// List functions have stricter requirements - ALL relations in the closure
		// must have features compatible with simple tuple lookup.
		canGenerateList, cannotGenerateListReason := computeCanGenerateList(a, lookup)
		a.CanGenerateListValue = canGenerateList
		a.CannotGenerateListReason = cannotGenerateListReason
	}

	return sorted
}

// computeCanGenerateList determines if a relation can use specialized list functions.
// This checks:
// 1. The relation itself has features that allow list generation (no TTU or intersection)
// 2. All relations in the closure are known
// 3. Closure relations don't have recursive patterns (TTU) - these need Phase 5
// 4. Self-referential userset patterns are not present (need recursive expansion)
//
// Note: Closure relations with userset or exclusion features are handled via
// ComplexClosureRelations and check_permission_internal. The templates handle these
// correctly.
//
// However, closure relations with recursive patterns (TTU) cannot be handled via
// check_permission_internal for list operations because check_permission_internal
// only verifies a specific object - it doesn't help discover which objects are
// accessible via recursive traversal. TTU in closure requires recursive CTEs.
//
// Phase 8: For relations without direct/implied access paths (pure TTU, pure userset),
// this function tries to find an indirect anchor by tracing through the patterns.
// If found, the IndirectAnchor field is set and list generation proceeds.
func computeCanGenerateList(a *RelationAnalysis, lookup map[string]map[string]*RelationAnalysis) (bool, string) {
	// Phase 8: Find indirect anchors for relations without direct/implied access.
	// This enables list generation for pure TTU patterns (not userset - those use Phase 4 templates).
	//
	// IMPORTANT: Only look for indirect anchors when the relation has NO userset patterns.
	// The Phase 4 userset template already correctly handles userset patterns by generating
	// UNION blocks for all patterns. The composed template is only for pure TTU access
	// where we trace through parent relations to reach an anchor.
	hasIndirectAnchor := false
	if !a.Features.HasDirect && !a.Features.HasImplied && !a.Features.HasUserset {
		// Try to find an indirect anchor via TTU patterns only
		// (userset patterns are handled by Phase 4 template)
		visited := make(map[string]bool)
		anchor := findIndirectAnchor(a, lookup, visited)
		if anchor != nil {
			a.IndirectAnchor = anchor
			hasIndirectAnchor = true

			// Phase 8: Propagate AllowedSubjectTypes from anchor relation.
			// The anchor relation has the direct subject types that ultimately grant access.
			anchorAnalysis := lookup[anchor.AnchorType][anchor.AnchorRelation]
			if anchorAnalysis != nil && len(a.AllowedSubjectTypes) == 0 {
				// Copy AllowedSubjectTypes from anchor
				if len(anchorAnalysis.AllowedSubjectTypes) > 0 {
					a.AllowedSubjectTypes = anchorAnalysis.AllowedSubjectTypes
				} else if len(anchorAnalysis.DirectSubjectTypes) > 0 {
					a.AllowedSubjectTypes = anchorAnalysis.DirectSubjectTypes
				}

				// Also propagate wildcard flag from anchor
				if anchorAnalysis.Features.HasWildcard {
					a.Features.HasWildcard = true
				}
			}
		}
	}

	// First check: does this relation's features allow list generation?
	canGenerate, reason := canGenerateListFeatures(a.Features, hasIndirectAnchor)
	if !canGenerate {
		return false, reason
	}

	// Phase 9B: Self-referential userset patterns (e.g., group#member on group.member)
	// are now supported via recursive CTEs. The self_ref_userset templates handle these.
	// We already detected and populated HasSelfReferentialUserset in ComputeCanGenerate.
	// No need to reject here - the template selection will route to the correct template.

	// Phase 9A: If the relation exceeds the depth limit, we can still generate a specialized
	// function that immediately raises M2002. This is more efficient than falling back to
	// the generic handler and provides clearer error semantics.
	if a.ExceedsDepthLimit {
		return true, "" // Will use depth-exceeded template
	}

	// Check for deeply nested userset chains: if we have userset patterns (direct or via closure)
	// but no AllowedSubjectTypes, the userset chain is too deep to propagate types.
	// Fall back to generic function which has proper depth limit checking.
	hasUsersetAccess := len(a.UsersetPatterns) > 0 || len(a.ClosureUsersetPatterns) > 0
	hasDirectSubjects := len(a.DirectSubjectTypes) > 0
	hasAllowedSubjects := len(a.AllowedSubjectTypes) > 0

	if hasUsersetAccess && !hasDirectSubjects && !hasAllowedSubjects {
		return false, "userset chain too deep - no reachable subject types (depth limit protection)"
	}

	// Second check: ensure closure relations are valid.
	for _, rel := range a.SatisfyingRelations {
		// Skip self
		if rel == a.Relation {
			continue
		}

		relAnalysis, ok := lookup[a.ObjectType][rel]
		if !ok {
			return false, "unknown relation in closure: " + rel
		}

		// Phase 5: Closure relations with recursive patterns (TTU) are now supported.
		// They are handled via check_permission_internal for cross-type TTU or
		// recursive CTEs for self-referential TTU.

		// If a closure relation is not list-generatable, we can't generate list for this relation either.
		// This handles cases like "can_view: viewer" where viewer is pure TTU (no direct/implied access).
		// The closure relation was already computed (dependency order), so check its result.
		if !relAnalysis.CanGenerateListValue {
			return false, "closure relation " + rel + " is not list-generatable: " + relAnalysis.CannotGenerateListReason
		}

		// Closure relations with intersection still need special handling
		if relAnalysis.Features.HasIntersection {
			return false, "closure relation " + rel + " has intersection patterns (requires Phase 6+)"
		}

		// Phase 9B: Self-referential usersets in closure relations are now supported.
		// The closure relation itself will generate a self_ref_userset template.
		// If the closure relation is generatable (checked above), we can generate for this relation too.

		// Userset, exclusion, and recursive in closure are OK - handled via check_permission_internal
	}

	return true, ""
}

// findIndirectAnchor traces through TTU and userset paths to find a relation
// with direct grants. Returns nil if the relation itself has HasDirect or HasImplied,
// or if no anchor can be found (unsatisfiable pattern or cycle).
//
// This enables list generation for "pure" patterns:
//   - Pure TTU: document.viewer: viewer from folder → folder.viewer: [user]
//   - Pure userset: document.viewer: [group#member] → group.member: [user]
//   - Userset chains: job.can_read: [permission#assignee] → permission.assignee: [role#assignee] → role.assignee: [user]
//
// The function performs depth-first search with cycle detection.
func findIndirectAnchor(
	a *RelationAnalysis,
	lookup map[string]map[string]*RelationAnalysis,
	visited map[string]bool,
) *IndirectAnchorInfo {
	key := a.ObjectType + "." + a.Relation
	if visited[key] {
		return nil // Cycle detected
	}
	visited[key] = true

	// Check TTU paths first - these are "viewer from parent" patterns
	for _, parent := range a.ParentRelations {
		// Collect ALL target types that have the target relation with direct/implied grants
		// This is needed because parent relations can point to multiple types (e.g., [document, folder])
		// and we need to check the target relation on ALL of them
		var directAnchorTypes []string
		var recursiveTypes []string // Types where target is same as object type with recursive TTU
		var firstDirectType string
		var firstDeeperResult *IndirectAnchorInfo

		for _, parentType := range parent.AllowedLinkingTypes {
			// Look up the target relation on the parent type
			typeAnalyses := lookup[parentType]
			if typeAnalyses == nil {
				continue
			}
			targetAnalysis := typeAnalyses[parent.Relation]
			if targetAnalysis == nil {
				continue
			}

			// Found direct anchor?
			if targetAnalysis.Features.HasDirect || targetAnalysis.Features.HasImplied {
				directAnchorTypes = append(directAnchorTypes, parentType)
				if firstDirectType == "" {
					firstDirectType = parentType
				}
				continue
			}

			// Check if this is a recursive same-type TTU pattern
			// e.g., document.can_view: can_view from parent where parent: [document, folder]
			// When parentType == a.ObjectType and target has recursive TTU, we need special handling
			if parentType == a.ObjectType && targetAnalysis.Features.HasRecursive {
				recursiveTypes = append(recursiveTypes, parentType)
				continue
			}

			// Recurse to find deeper anchor (only if we haven't found a direct one)
			if firstDirectType == "" && firstDeeperResult == nil {
				deeper := findIndirectAnchor(targetAnalysis, lookup, visited)
				if deeper != nil {
					// Prepend our step to the path
					step := AnchorPathStep{
						Type:            "ttu",
						LinkingRelation: parent.LinkingRelation,
						TargetType:      parentType,
						TargetRelation:  parent.Relation,
						AllTargetTypes:  []string{parentType},
					}
					deeper.Path = append([]AnchorPathStep{step}, deeper.Path...)
					firstDeeperResult = deeper
				}
			}
		}

		// If we found direct anchors, return that (prefer direct over deeper)
		if len(directAnchorTypes) > 0 {
			return &IndirectAnchorInfo{
				Path: []AnchorPathStep{{
					Type:            "ttu",
					LinkingRelation: parent.LinkingRelation,
					TargetType:      firstDirectType,
					TargetRelation:  parent.Relation,
					AllTargetTypes:  directAnchorTypes,
					RecursiveTypes:  recursiveTypes,
				}},
				AnchorType:     firstDirectType,
				AnchorRelation: parent.Relation,
			}
		}

		// Return deeper result if found
		if firstDeeperResult != nil {
			return firstDeeperResult
		}
	}

	// Check userset paths - these are [group#member] patterns
	for _, pattern := range a.UsersetPatterns {
		// Look up the subject relation on the subject type
		typeAnalyses := lookup[pattern.SubjectType]
		if typeAnalyses == nil {
			continue
		}
		targetAnalysis := typeAnalyses[pattern.SubjectRelation]
		if targetAnalysis == nil {
			continue
		}

		// Found direct anchor?
		if targetAnalysis.Features.HasDirect || targetAnalysis.Features.HasImplied {
			return &IndirectAnchorInfo{
				Path: []AnchorPathStep{{
					Type:            "userset",
					SubjectType:     pattern.SubjectType,
					SubjectRelation: pattern.SubjectRelation,
				}},
				AnchorType:     pattern.SubjectType,
				AnchorRelation: pattern.SubjectRelation,
			}
		}

		// Recurse to find deeper anchor
		deeper := findIndirectAnchor(targetAnalysis, lookup, visited)
		if deeper != nil {
			// Prepend our step to the path
			step := AnchorPathStep{
				Type:            "userset",
				SubjectType:     pattern.SubjectType,
				SubjectRelation: pattern.SubjectRelation,
			}
			deeper.Path = append([]AnchorPathStep{step}, deeper.Path...)
			return deeper
		}
	}

	return nil // No anchor found
}

// computeMaxUsersetDepth calculates the maximum userset chain depth reachable from a relation.
// Returns:
//   - 0 if the relation has no userset patterns
//   - -1 if the relation has a self-referential userset cycle
//   - positive value for the maximum depth of userset chains
//
// The depth limit of 25 is enforced at runtime, so values >= 25 indicate the relation
// will always fail with M2002 (resolution too complex).
//
// This uses BFS with memoization to efficiently compute depths even for complex chains.
func computeMaxUsersetDepth(a *RelationAnalysis, lookup map[string]map[string]*RelationAnalysis) int {
	// Use memoization to avoid redundant computation
	memo := make(map[string]int) // key: "type.relation", value: computed depth or -1 for cycle
	return computeDepthRecursive(a.ObjectType, a.Relation, lookup, memo, make(map[string]bool))
}

// computeDepthRecursive recursively computes the maximum userset depth from a relation.
// visited tracks the current path for cycle detection.
// memo caches computed results to avoid redundant work.
func computeDepthRecursive(
	objectType string,
	relation string,
	lookup map[string]map[string]*RelationAnalysis,
	memo map[string]int,
	visited map[string]bool,
) int {
	key := objectType + "." + relation

	// Check if we're in a cycle
	if visited[key] {
		return -1 // Cycle detected
	}

	// Check memo
	if depth, ok := memo[key]; ok {
		return depth
	}

	// Get the analysis for this relation
	typeAnalyses := lookup[objectType]
	if typeAnalyses == nil {
		memo[key] = 0
		return 0
	}
	a := typeAnalyses[relation]
	if a == nil {
		memo[key] = 0
		return 0
	}

	// Mark as visited for cycle detection
	visited[key] = true
	defer func() { delete(visited, key) }()

	maxDepth := 0

	// Check direct userset patterns
	for _, pattern := range a.UsersetPatterns {
		// Check for self-referential userset (same type and relation)
		if pattern.SubjectType == objectType && pattern.SubjectRelation == relation {
			memo[key] = -1
			return -1 // Self-referential cycle
		}

		// Each userset pattern adds 1 to the depth, then we recursively check
		// the subject relation for its depth
		subDepth := computeDepthRecursive(
			pattern.SubjectType,
			pattern.SubjectRelation,
			lookup,
			memo,
			visited,
		)

		if subDepth == -1 {
			// Cycle found in sub-chain
			memo[key] = -1
			return -1
		}

		// This pattern's depth is 1 (for this hop) + the sub-relation's depth
		patternDepth := 1 + subDepth
		if patternDepth > maxDepth {
			maxDepth = patternDepth
		}
	}

	// Also check implied relations for userset chains.
	// If this relation implies another that has userset patterns, we need to count those too.
	// This handles cases like "can_view: a27" where a27 has the userset patterns.
	for _, impliedRel := range a.SatisfyingRelations {
		if impliedRel == relation {
			continue // Skip self
		}

		// Compute depth for implied relation (it may have userset patterns)
		impliedDepth := computeDepthRecursive(objectType, impliedRel, lookup, memo, visited)
		if impliedDepth == -1 {
			memo[key] = -1
			return -1
		}

		if impliedDepth > maxDepth {
			maxDepth = impliedDepth
		}
	}

	memo[key] = maxDepth
	return maxDepth
}

// detectSelfReferentialUsersets identifies userset patterns that reference the same type and relation.
// For example, group.member: [user, group#member] has a self-referential userset because
// SubjectType="group" matches ObjectType and SubjectRelation="member" matches Relation.
// These patterns require recursive CTEs to expand nested membership.
func detectSelfReferentialUsersets(a *RelationAnalysis) []UsersetPattern {
	var selfRef []UsersetPattern
	for _, pattern := range a.UsersetPatterns {
		if pattern.SubjectType == a.ObjectType && pattern.SubjectRelation == a.Relation {
			selfRef = append(selfRef, pattern)
		}
	}
	return selfRef
}
