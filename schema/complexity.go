package schema

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
	// 1. The relation only has Direct, Implied, and/or Wildcard features
	// 2. No complex features that require JOINs, recursion, etc.
	if f.HasUserset || f.HasRecursive || f.HasExclusion || f.HasIntersection {
		return false
	}
	// Must have at least one access path (direct or implied)
	return f.HasDirect || f.HasImplied
}

// IsSimplyResolvable returns true if this relation can be resolved
// with a simple tuple lookup (no userset JOINs, recursion, etc.).
// This is used to check if all relations in a closure are compatible
// with simple closure-based SQL generation.
func (f RelationFeatures) IsSimplyResolvable() bool {
	// Can use simple tuple lookup only if no complex features
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

	// For Direct/Implied patterns - from closure table
	SatisfyingRelations []string // Relations that satisfy this one (e.g., ["viewer", "editor", "owner"])

	// For Exclusion patterns
	ExcludedRelations []string // Relations to exclude (for "but not" patterns)

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
	return RelationFeatures{
		HasDirect:       len(analysis.DirectSubjectTypes) > 0,
		HasImplied:      len(r.ImpliedBy) > 0,
		HasWildcard:     hasWildcardRefs(r),
		HasUserset:      len(analysis.UsersetPatterns) > 0,
		HasRecursive:    len(analysis.ParentRelations) > 0,
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

// ComputeCanGenerate walks the dependency graph and sets CanGenerate on each analysis.
// A relation can be generated if:
// 1. Its own features allow generation (CanGenerate() on features returns true)
// 2. ALL relations in its satisfying closure are simply resolvable
//
// This ensures that closure-based SQL like `relation IN ('viewer', 'editor', 'owner')`
// will work correctly - every relation in the list must be resolvable via direct tuple lookup.
//
// This function also:
// - Propagates HasWildcard: if ANY relation in the closure supports wildcards
// - Collects AllowedSubjectTypes: union of all subject types from satisfying relations
func ComputeCanGenerate(analyses []RelationAnalysis) []RelationAnalysis {
	// Build lookup map: (objectType, relation) -> *RelationAnalysis
	lookup := make(map[string]map[string]*RelationAnalysis)
	for i := range analyses {
		a := &analyses[i]
		if lookup[a.ObjectType] == nil {
			lookup[a.ObjectType] = make(map[string]*RelationAnalysis)
		}
		lookup[a.ObjectType][a.Relation] = a
	}

	// For each analysis, check if it can be generated and propagate properties
	for i := range analyses {
		a := &analyses[i]

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

		// First check: does this relation's features allow generation?
		if !a.Features.CanGenerate() {
			a.CanGenerate = false
			continue
		}

		// Second check: are ALL satisfying relations simply resolvable?
		canGenerate := true
		for _, rel := range a.SatisfyingRelations {
			relAnalysis, ok := lookup[a.ObjectType][rel]
			if !ok {
				// Unknown relation - shouldn't happen with valid closure, but be safe
				canGenerate = false
				break
			}
			if !relAnalysis.Features.IsSimplyResolvable() {
				canGenerate = false
				break
			}
		}

		// Third check: must have at least one allowed subject type
		// (relations with no direct types in closure fall back to generic)
		if len(a.AllowedSubjectTypes) == 0 {
			canGenerate = false
		}

		a.CanGenerate = canGenerate
	}

	return analyses
}
