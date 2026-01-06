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
	lookup := buildAnalysisLookup(analyses)

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
func canGenerateListFeatures(f RelationFeatures) (bool, string) {
	// Phase 2: Support Direct, Implied, and Wildcard patterns.
	// These can all be handled with simple tuple lookup using relation closure.
	//
	// Must have at least one access path (direct or implied).
	// - Direct: relation has [user] or similar type restriction
	// - Implied: relation references another relation (can_view: viewer)
	// Either is sufficient as long as the closure is simple.
	if !f.HasDirect && !f.HasImplied {
		return false, "no access path (neither direct nor implied)"
	}

	// HasWildcard is allowed: SubjectIDCheck handles wildcard matching,
	// and model changes regenerate functions with correct behavior.
	// Type guards (AllowedSubjectTypes) ensure type restrictions are enforced.

	// HasImplied is allowed: the relation closure (SatisfyingRelations) is inlined
	// into the RelationList. The closure validation in computeCanGenerateList
	// ensures all relations in the closure are themselves simple.

	// Complex features still require later phases:
	if f.HasUserset {
		return false, "has userset patterns (requires Phase 4)"
	}
	if f.HasRecursive {
		return false, "has recursive/TTU patterns (requires Phase 5)"
	}
	if f.HasExclusion {
		return false, "has exclusion patterns (requires Phase 3)"
	}
	if f.HasIntersection {
		return false, "has intersection patterns (requires Phase 3+)"
	}

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
			// Also propagate wildcard flag
			if relAnalysis.Features.HasWildcard {
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
		canGenerate := true
		cannotGenerateReason := ""
		var simpleRels, complexRels []string
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
		}

		// Third check: classify excluded relations as simple or complex.
		// Simple exclusions use direct tuple lookup; complex exclusions use function calls.
		// Unknown excluded relations still prevent generation.
		var simpleExcluded, complexExcluded []string
		if canGenerate && a.Features.HasExclusion {
			for _, excludedRel := range a.ExcludedRelations {
				excludedAnalysis, ok := lookup[a.ObjectType][excludedRel]
				if !ok {
					// Unknown excluded relation - fall back to generic
					canGenerate = false
					cannotGenerateReason = "unknown excluded relation: " + excludedRel
					break
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
// For Phase 2, this requires:
// 1. The relation itself has only Direct/Implied features (no userset, TTU, exclusion, intersection)
// 2. ALL relations in the satisfying closure also have only Direct/Implied features
//
// This ensures that a simple tuple lookup with IN (relation1, relation2, ...) will produce
// correct results without needing recursive CTEs or complex JOINs.
func computeCanGenerateList(a *RelationAnalysis, lookup map[string]map[string]*RelationAnalysis) (bool, string) {
	// First check: does this relation's features allow list generation?
	canGenerate, reason := canGenerateListFeatures(a.Features)
	if !canGenerate {
		return false, reason
	}

	// Second check: ALL relations in the closure must also be list-compatible.
	// This is critical because `can_view: viewer` where `viewer: viewer from parent`
	// cannot use simple tuple lookup even though `can_view` itself only has HasImplied.
	for _, rel := range a.SatisfyingRelations {
		// Skip self
		if rel == a.Relation {
			continue
		}

		relAnalysis, ok := lookup[a.ObjectType][rel]
		if !ok {
			return false, "unknown relation in closure: " + rel
		}

		// Check if this closure relation has features that prevent simple tuple lookup
		closureCanGenerate, closureReason := canGenerateListFeatures(relAnalysis.Features)
		if !closureCanGenerate {
			return false, "closure relation " + rel + " " + closureReason
		}
	}

	return true, ""
}
