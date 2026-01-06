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
	// We can generate SQL if:
	// 1. The relation has Direct, Implied, Wildcard, Userset, Recursive, Exclusion,
	//    and/or Intersection features
	// Note: Exclusion is allowed here, but ComputeCanGenerate will verify that
	// all excluded relations are simply resolvable before enabling codegen.
	// Note: Userset is supported via JOIN-based expansion in userset_check.tpl.sql
	// Note: Recursive (TTU) is supported for same-type recursion. ComputeCanGenerate
	// verifies that all parent relations link to the same type.
	// Note: Intersection is supported by calling check functions for each part.
	// ComputeCanGenerate verifies all intersection parts are generatable.

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
}

// ParentRelationInfo represents a "X from Y" pattern (tuple-to-userset).
// For "viewer from parent" on a folder type, this would have:
//   - Relation: "viewer" (the relation to check on the parent)
//   - LinkingRelation: "parent" (the relation that links to the parent)
//   - ParentType: "folder" (the type of the parent object)
type ParentRelationInfo struct {
	Relation        string // Relation to check on parent (e.g., "viewer")
	LinkingRelation string // Relation that links to parent (e.g., "parent")
	ParentType      string // Type of parent object (e.g., "folder")
}

// IntersectionPart represents one part of an intersection check.
// For "writer and (editor but not owner)", we'd have:
//   - {Relation: "writer"}
//   - {Relation: "editor", IsExcluded: false, ExcludedRelation: "owner"}
type IntersectionPart struct {
	IsThis           bool                // [user] - direct assignment check on the same relation
	HasWildcard      bool                // For IsThis parts: whether direct assignment allows wildcards
	Relation         string              // Relation to check
	IsExcluded       bool                // "but not" - negate the check
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

	// CanGenerate is true if this relation can use generated SQL.
	// This is computed by checking both the relation's own features AND
	// ensuring all relations in the satisfying closure are simply resolvable.
	// Set by ComputeCanGenerate after all relations are analyzed.
	CanGenerate bool

	// CannotGenerateReason explains why CanGenerate is false.
	// Empty when CanGenerate is true.
	CannotGenerateReason string

	// For Direct/Implied patterns - from closure table
	SatisfyingRelations []string // Relations that satisfy this one (e.g., ["viewer", "editor", "owner"])

	// For Exclusion patterns
	ExcludedRelations []string // Relations to exclude (for simple "but not X" patterns)

	// HasComplexExclusion is true if the relation has exclusions that can't be
	// handled by simple tuple lookup (TTU exclusions or intersection exclusions).
	// When true, the relation must fall back to generic for exclusion handling.
	HasComplexExclusion bool

	// For Userset patterns
	UsersetPatterns []UsersetPattern // [group#member] patterns

	// For Recursive patterns (tuple-to-userset)
	ParentRelations []ParentRelationInfo

	// For Intersection patterns
	IntersectionGroups []IntersectionGroupInfo

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

	// Check for complex exclusions (TTU or intersection exclusions)
	// These require generic handling, simple codegen can't handle them
	analysis.HasComplexExclusion = len(r.ExcludedParentRelations) > 0 || len(r.ExcludedIntersectionGroups) > 0

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

	// Legacy path: SubjectTypes
	for _, st := range r.SubjectTypes {
		// Strip wildcard suffix
		typeName := st
		if len(typeName) > 2 && typeName[len(typeName)-2:] == ":*" {
			typeName = typeName[:len(typeName)-2]
		}
		if !seen[typeName] {
			types = append(types, typeName)
			seen[typeName] = true
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
	// Legacy path: check SubjectTypes for :* suffix
	for _, st := range r.SubjectTypes {
		if len(st) > 2 && st[len(st)-2:] == ":*" {
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

	// New field: ParentRelations slice
	for _, pr := range r.ParentRelations {
		parents = append(parents, ParentRelationInfo{
			Relation:        pr.Relation,
			LinkingRelation: pr.ParentType,
			ParentType:      pr.ParentType, // Note: In current model, ParentType is the linking relation
		})
	}

	// Legacy fields: single ParentRelation/ParentType
	if r.ParentRelation != "" && r.ParentType != "" {
		parents = append(parents, ParentRelationInfo{
			Relation:        r.ParentRelation,
			LinkingRelation: r.ParentType,
			ParentType:      r.ParentType,
		})
	}

	return parents
}

// collectExcludedRelations extracts all "but not X" patterns from a relation.
func collectExcludedRelations(r RelationDefinition) []string {
	var excluded []string

	// New field: ExcludedRelations slice
	excluded = append(excluded, r.ExcludedRelations...)

	// Legacy field: single ExcludedRelation
	if r.ExcludedRelation != "" {
		// Avoid duplicates
		found := false
		for _, e := range excluded {
			if e == r.ExcludedRelation {
				found = true
				break
			}
		}
		if !found {
			excluded = append(excluded, r.ExcludedRelation)
		}
	}

	return excluded
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
			// Check for nested exclusions
			if excls, ok := ig.Exclusions[rel]; ok && len(excls) > 0 {
				part.ExcludedRelation = excls[0] // Take first exclusion
			}
			group.Parts = append(group.Parts, part)
		}

		// Add parent relation checks
		for _, pr := range ig.ParentRelations {
			part := IntersectionPart{
				Relation: pr.Relation,
				ParentRelation: &ParentRelationInfo{
					Relation:        pr.Relation,
					LinkingRelation: pr.ParentType,
					ParentType:      pr.ParentType,
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

// sortByDependency performs topological sort on analyses based on closure dependencies.
// Returns sorted analyses where each relation is processed after its dependencies.
// This ensures that when we check if a closure relation CanGenerate, it has already been evaluated.
func sortByDependency(analyses []RelationAnalysis) []RelationAnalysis {
	// Build dependency graph: relation -> relations it depends on (from SatisfyingRelations)
	deps := make(map[string][]string) // key: "type.relation"
	for _, a := range analyses {
		key := a.ObjectType + "." + a.Relation
		for _, rel := range a.SatisfyingRelations {
			if rel != a.Relation { // Skip self
				deps[key] = append(deps[key], a.ObjectType+"."+rel)
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

// ComputeCanGenerate walks the dependency graph and sets CanGenerate on each analysis.
// A relation can be generated if:
// 1. Its own features allow generation (CanGenerate() on features returns true)
// 2. ALL relations in its satisfying closure are either:
//   - Simply resolvable (can use tuple lookup), OR
//   - Complex but generatable (have exclusions but can generate their own function)
// 3. If the relation has exclusions, ALL excluded relations must be:
//   - Simply resolvable (no usersets, TTU, etc.)
//   - Have no implied relations (closure must be just the relation itself)
//
// For relations in the closure that need function calls (have exclusions but are generatable),
// the generated SQL will call their specialized check function rather than using tuple lookup.
//
// This function also:
// - Propagates HasWildcard: if ANY relation in the closure supports wildcards
// - Collects AllowedSubjectTypes: union of all subject types from satisfying relations
// - Partitions closure relations into SimpleClosureRelations and ComplexClosureRelations
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
			} else if relAnalysis.CanGenerate && relAnalysis.Features.HasExclusion {
				// Complex but generatable: delegate to its check function
				complexRels = append(complexRels, rel)
			} else {
				// Truly incompatible: fall back to generic
				canGenerate = false
				cannotGenerateReason = "closure relation not generatable: " + rel
				break
			}
		}

		// Third check: must have at least one allowed subject type
		// (relations with no direct types in closure fall back to generic)
		if canGenerate && len(a.AllowedSubjectTypes) == 0 {
			canGenerate = false
			cannotGenerateReason = "no allowed subject types in closure"
		}

		// Fourth check: if relation has complex exclusions (TTU or intersection),
		// we can't generate - these require the generic implementation.
		if canGenerate && a.HasComplexExclusion {
			canGenerate = false
			cannotGenerateReason = "has complex exclusion (TTU or intersection-based)"
		}

		// Fifth check: if relation has simple exclusions, verify all excluded relations
		// are suitable for simple codegen (simply resolvable and no implied relations).
		// This ensures the exclusion check can be a direct tuple lookup.
		if canGenerate && a.Features.HasExclusion {
			for _, excludedRel := range a.ExcludedRelations {
				excludedAnalysis, ok := lookup[a.ObjectType][excludedRel]
				if !ok {
					// Unknown excluded relation - fall back to generic
					canGenerate = false
					cannotGenerateReason = "unknown excluded relation: " + excludedRel
					break
				}
				// Excluded relation must be simply resolvable
				if !excludedAnalysis.Features.IsSimplyResolvable() {
					canGenerate = false
					cannotGenerateReason = "excluded relation not simply resolvable: " + excludedRel
					break
				}
				// Excluded relation must not have implied relations (closure must be just itself)
				// because our exclusion template does a direct lookup: relation = 'blocked'
				// If blocked had implied relations, we'd need: relation IN ('blocked', 'editor', ...)
				if len(excludedAnalysis.SatisfyingRelations) > 1 {
					canGenerate = false
					cannotGenerateReason = "excluded relation has implied closure: " + excludedRel
					break
				}
			}
		}

		// Sixth check: if relation has userset patterns, verify ALL relations in each
		// pattern's closure are suitable for simple codegen (can use tuple lookup).
		// This ensures the userset JOIN can find membership tuples directly.
		// For example, [group#member_c4] requires ALL satisfying relations (member_c4,
		// member_c3, ... member) to be simply resolvable tuple lookups.
		if canGenerate && a.Features.HasUserset {
			for _, pattern := range a.UsersetPatterns {
				for _, rel := range pattern.SatisfyingRelations {
					relAnalysis, ok := lookup[pattern.SubjectType][rel]
					if !ok {
						// Unknown relation - fall back to generic
						canGenerate = false
						cannotGenerateReason = "unknown relation in userset closure: " + pattern.SubjectType + "#" + rel
						break
					}
					// Each satisfying relation must be resolvable via tuple lookup
					// (no TTU, userset, intersection, or exclusion)
					if !relAnalysis.Features.IsClosureCompatible() {
						canGenerate = false
						cannotGenerateReason = "userset closure relation not closure-compatible: " + pattern.SubjectType + "#" + rel
						break
					}
				}
				if !canGenerate {
					break
				}
			}
		}

		// Seventh check: if relation has recursive/TTU patterns, verify parent relations
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

		// Eighth check: if relation has intersection groups, verify all parts can be
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
		a.CanGenerate = canGenerate
	}

	return sorted
}
