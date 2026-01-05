package melange

// TypeDefinition represents a parsed type from an .fga file.
// Each type definition describes an object type (user, repository, etc.)
// and the relations that can exist on objects of that type.
type TypeDefinition struct {
	Name      string
	Relations []RelationDefinition
}

// SubjectTypeRef represents a subject type reference in a relation definition.
// For userset references like [group#member], Type is "group" and Relation is "member".
// For direct references like [user], Type is "user" and Relation is empty.
type SubjectTypeRef struct {
	Type     string // Subject type: "user", "group", etc.
	Relation string // For userset refs: the relation (e.g., "member" in [group#member])
	Wildcard bool   // True if this is a wildcard reference (user:*)
}

// IntersectionGroup represents a group of relations that must ALL be satisfied.
// For "viewer: writer and editor", the group would be ["writer", "editor"].
// For "viewer: writer and (editor but not owner)", the group would be
// ["writer", "editor"] with Exclusions["editor"] = ["owner"].
type IntersectionGroup struct {
	Relations       []string              // Relations that must all be satisfied (AND)
	ParentRelations []ParentRelationCheck // Parent inheritance checks (tuple-to-userset)
	Exclusions      map[string][]string   // Per-relation exclusions: relation -> list of excluded relations
}

// ParentRelationCheck represents a tuple-to-userset check in an intersection group.
// For "viewer: writer and (viewer from parent)", this captures the parent relation.
type ParentRelationCheck struct {
	Relation   string // Relation to check on the parent object (e.g., "viewer")
	ParentType string // Linking relation on the current object (e.g., "parent")
}

// RelationDefinition represents a parsed relation.
// Relations describe who can have what relationship with an object.
//
// A relation can be:
//   - Direct: explicitly granted via tuples (SubjectTypes)
//   - Implied: granted by having another relation (ImpliedBy)
//   - Inherited: derived from a parent object (ParentRelation, ParentType)
//   - Exclusive: granted except for excluded subjects (ExcludedRelation)
//   - Userset: granted via group membership (SubjectTypeRefs with Relation set)
//   - Intersection: granted if ALL relations in a group are satisfied
type RelationDefinition struct {
	Name              string   // Relation name: "owner", "can_read", etc.
	SubjectTypes      []string // Direct subject types: ["user"], ["organization"] (legacy)
	ImpliedBy         []string // Relations that imply this one: ["owner", "admin"]
	ParentRelation    string   // For inheritance: "can_read from org" → "can_read"
	ParentType        string   // The relation linking to parent: "org", "repo"
	ExcludedRelation  string   // For exclusions: "can_read but not author" -> "author" (deprecated, use ExcludedRelations)
	ExcludedRelations []string // For nested exclusions: "(a but not b) but not c" -> ["b", "c"]
	// ExcludedParentRelations captures tuple-to-userset exclusions like "but not viewer from parent".
	ExcludedParentRelations []ParentRelationCheck
	// SubjectTypeRefs provides detailed subject type info including userset relations.
	// For [user, group#member], this would contain:
	//   - {Type: "user", Relation: ""}
	//   - {Type: "group", Relation: "member"}
	SubjectTypeRefs []SubjectTypeRef
	// IntersectionGroups contains groups of relations that must ALL be satisfied.
	// Each group is an AND (intersection), multiple groups are OR'd together.
	// For "viewer: writer and editor", IntersectionGroups = [["writer", "editor"]]
	// For "viewer: (a and b) or (c and d)", IntersectionGroups = [["a","b"], ["c","d"]]
	IntersectionGroups []IntersectionGroup
}

// AuthzModel represents an entry in the melange_model table.
// Each row defines one authorization rule that check_permission evaluates.
//
// The table stores the flattened authorization model, precomputing transitive
// closures and normalizing rules for efficient query execution.
//
// Rule types:
//   - Direct: SubjectType is set, others NULL (user can have relation)
//   - Implied: ImpliedBy is set (having one relation grants another)
//   - Parent: ParentRelation and SubjectType set (inherit from parent object)
//   - Exclusive: ExcludedRelation set (permission denied if exclusion holds)
//   - Userset: SubjectType and SubjectRelation set (e.g., [group#member])
//   - Intersection: RuleGroupID and RuleGroupMode set (AND semantics)
type AuthzModel struct {
	ID               int64
	ObjectType       string  // Object type this rule applies to
	Relation         string  // Relation this rule defines
	SubjectType      *string // Allowed subject type (for direct rules)
	ImpliedBy        *string // Implying relation (for role hierarchy)
	ParentRelation   *string // Parent relation to check (for inheritance)
	ExcludedRelation *string // Relation to exclude (for "but not" rules)
	SubjectWildcard  *bool   // Whether wildcard subjects are allowed for SubjectType
	// Excluded parent relation for tuple-to-userset exclusions.
	ExcludedParentRelation *string // Parent relation to exclude (for "but not rel from parent")
	ExcludedParentType     *string // Linking relation for the excluded parent relation
	// New fields for userset references and intersection support
	SubjectRelation       *string // For userset refs [type#relation]: the relation part
	RuleGroupID           *int64  // Groups rules that form an intersection
	RuleGroupMode         *string // 'intersection' for AND, 'union' or NULL for OR
	CheckRelation         *string // For intersection: which relation to check
	CheckExcludedRelation *string // For intersection: exclusion on check_relation (e.g., "editor but not owner")
	CheckParentRelation   *string // For intersection: parent relation to check (tuple-to-userset)
	CheckParentType       *string // For intersection: linking relation on current object
}

// SubjectTypes returns all types that can be subjects in authorization checks.
// A type is a subject type if it appears in any relation's SubjectTypes list.
// This is useful for understanding which types can be the "who" in permission checks.
//
// Example:
//
//	types, _ := melange.ParseSchema("schema.fga")
//	subjects := melange.SubjectTypes(types)
//	// Returns: ["user", "organization", "team"]
func SubjectTypes(types []TypeDefinition) []string {
	seen := make(map[string]bool)
	var result []string

	for _, t := range types {
		for _, r := range t.Relations {
			// Use SubjectTypeRefs if available
			if len(r.SubjectTypeRefs) > 0 {
				for _, ref := range r.SubjectTypeRefs {
					if !seen[ref.Type] {
						seen[ref.Type] = true
						result = append(result, ref.Type)
					}
				}
			} else {
				// Fall back to SubjectTypes
				for _, st := range r.SubjectTypes {
					// Strip wildcard suffix if present (e.g., "user:*" → "user")
					typeName := st
					if len(typeName) > 2 && typeName[len(typeName)-2:] == ":*" {
						typeName = typeName[:len(typeName)-2]
					}
					if !seen[typeName] {
						seen[typeName] = true
						result = append(result, typeName)
					}
				}
			}
		}
	}

	return result
}

// RelationSubjects returns the subject types that can have a specific relation
// on objects of the given type. This is useful for understanding who can be
// granted a particular permission.
//
// Example:
//
//	types, _ := melange.ParseSchema("schema.fga")
//	subjects := melange.RelationSubjects(types, "repository", "owner")
//	// Returns: ["user"] - only users can be repository owners
//
//	readers := melange.RelationSubjects(types, "repository", "can_read")
//	// Returns: ["user", "organization"] - users and orgs can read repositories
func RelationSubjects(types []TypeDefinition, objectType string, relation string) []string {
	for _, t := range types {
		if t.Name != objectType {
			continue
		}

		for _, r := range t.Relations {
			if r.Name != relation {
				continue
			}

			// Use SubjectTypeRefs if available
			if len(r.SubjectTypeRefs) > 0 {
				var result []string
				for _, ref := range r.SubjectTypeRefs {
					result = append(result, ref.Type)
				}
				return result
			}

			// Fall back to SubjectTypes, stripping wildcard suffix
			var result []string
			for _, st := range r.SubjectTypes {
				typeName := st
				if len(typeName) > 2 && typeName[len(typeName)-2:] == ":*" {
					typeName = typeName[:len(typeName)-2]
				}
				result = append(result, typeName)
			}
			return result
		}
	}

	return nil
}

// ToAuthzModels converts parsed type definitions to database models.
// This is the critical transformation that enables permission checking.
//
// The conversion performs transitive closure of implied_by relationships to
// support role hierarchies. For example, if owner → admin and admin → member,
// the closure ensures owner also implies member without explicit declaration.
//
// Each AuthzModel row represents one authorization rule:
//   - Direct subject types: "repository.can_read allows user"
//   - Implied relations: "repository.can_read implied by can_write"
//   - Parent inheritance: "change.can_read from repository.can_read"
//   - Exclusions: "change.can_read but not is_author"
//   - Intersection groups: "viewer: writer and editor" (all must be satisfied)
//
// The check_permission function queries these rows to evaluate permissions
// recursively, following the graph of implications and parent relationships.
func ToAuthzModels(types []TypeDefinition) []AuthzModel {
	var models []AuthzModel
	var ruleGroupIDCounter int64 = 1 // Counter for intersection group IDs

	for _, t := range types {
		// Build implied_by graph for this type
		// impliedByGraph[relation] = list of relations that directly imply it
		impliedByGraph := make(map[string][]string)

		for _, r := range t.Relations {
			impliedByGraph[r.Name] = append(impliedByGraph[r.Name], r.ImpliedBy...)
		}

		// Compute transitive closure: what relations transitively imply each relation?
		transitiveImpliers := computeTransitiveClosure(impliedByGraph)

		for _, r := range t.Relations {

			// Collect all exclusions for this relation.
			// For nested exclusions like "(A but not B) but not C", this will be ["B", "C"].
			// For simple exclusions like "A but not B", this will be ["B"].
			// For backward compatibility, also check the deprecated ExcludedRelation field.
			exclusions := r.ExcludedRelations
			if len(exclusions) == 0 && r.ExcludedRelation != "" {
				exclusions = []string{r.ExcludedRelation}
			}

			// Add entries for direct subject types
			// Use SubjectTypeRefs if available (includes userset relation info),
			// otherwise fall back to SubjectTypes for backward compatibility.
			if len(r.SubjectTypeRefs) > 0 {
				for _, ref := range r.SubjectTypeRefs {
					st := ref.Type
					wildcard := ref.Wildcard
					model := AuthzModel{
						ObjectType:      t.Name,
						Relation:        r.Name,
						SubjectType:     &st,
						SubjectWildcard: &wildcard,
					}
					// Set subject_relation for userset references
					if ref.Relation != "" {
						sr := ref.Relation
						model.SubjectRelation = &sr
					}
					models = append(models, model)
				}
			} else {
				// Legacy path: use SubjectTypes (no userset info)
				for _, subjectType := range r.SubjectTypes {
					// Strip wildcard suffix for storage
					st := subjectType
					wildcard := false
					if len(st) > 2 && st[len(st)-2:] == ":*" {
						wildcard = true
						st = st[:len(st)-2]
					}
					model := AuthzModel{
						ObjectType:      t.Name,
						Relation:        r.Name,
						SubjectType:     &st,
						SubjectWildcard: &wildcard,
					}
					models = append(models, model)
				}
			}

			// Add separate rows for each exclusion.
			// This allows the SQL to find ALL exclusions via:
			//   SELECT excluded_relation FROM melange_model WHERE ... AND excluded_relation IS NOT NULL
			// Each exclusion is stored in its own row with no other rule fields set.
			for _, excl := range exclusions {
				er := excl
				model := AuthzModel{
					ObjectType:       t.Name,
					Relation:         r.Name,
					ExcludedRelation: &er,
				}
				models = append(models, model)
			}
			for _, excl := range r.ExcludedParentRelations {
				er := excl.Relation
				et := excl.ParentType
				model := AuthzModel{
					ObjectType:             t.Name,
					Relation:               r.Name,
					ExcludedParentRelation: &er,
					ExcludedParentType:     &et,
				}
				models = append(models, model)
			}

			// Add entries for ALL implied relations (including transitive)
			// Note: Exclusions are stored in separate rows, not embedded here.
			for _, impliedBy := range transitiveImpliers[r.Name] {
				ib := impliedBy
				model := AuthzModel{
					ObjectType: t.Name,
					Relation:   r.Name,
					ImpliedBy:  &ib,
				}
				models = append(models, model)
			}

			// Add entry for parent relation (inheritance from related object)
			// For "can_read from org":
			//   - subject_type = "org" (the LINKING RELATION, not resolved type)
			//   - parent_relation = "can_read" (the relation to check on parent)
			// The SQL uses t.relation = am.subject_type to find tuples with the right
			// linking relation, then gets the actual parent type from t.subject_type.
			// Note: Exclusions are stored in separate rows, not embedded here.
			if r.ParentRelation != "" {
				pr := r.ParentRelation
				pt := r.ParentType // Keep as linking relation, don't resolve to type
				model := AuthzModel{
					ObjectType:     t.Name,
					Relation:       r.Name,
					ParentRelation: &pr,
					SubjectType:    &pt,
					// ImpliedBy left nil - parent relations use parent_relation field
				}
				models = append(models, model)
			}

			// Add entries for intersection groups
			// For "viewer: writer and editor", we create:
			//   - {relation: viewer, check_relation: writer, rule_group_id: 1, rule_group_mode: intersection}
			//   - {relation: viewer, check_relation: editor, rule_group_id: 1, rule_group_mode: intersection}
			// For "viewer: writer and (editor but not owner)", we create:
			//   - {relation: viewer, check_relation: writer, rule_group_id: 1, rule_group_mode: intersection}
			//   - {relation: viewer, check_relation: editor, check_excluded_relation: owner, rule_group_id: 1, rule_group_mode: intersection}
			for _, group := range r.IntersectionGroups {
				if len(group.Relations) == 0 {
					if len(group.ParentRelations) == 0 {
						continue
					}
				}
				groupID := ruleGroupIDCounter
				ruleGroupIDCounter++
				mode := "intersection"

				for _, checkRel := range group.Relations {
					cr := checkRel
					model := AuthzModel{
						ObjectType:    t.Name,
						Relation:      r.Name,
						CheckRelation: &cr,
						RuleGroupID:   &groupID,
						RuleGroupMode: &mode,
					}
					// Check for exclusions on this check_relation
					// For "writer and (editor but not owner)", group.Exclusions["editor"] = ["owner"]
					if excls, ok := group.Exclusions[checkRel]; ok && len(excls) > 0 {
						// Take first exclusion (could extend for multiple in future)
						cer := excls[0]
						model.CheckExcludedRelation = &cer
					}
					models = append(models, model)
				}

				for _, checkParent := range group.ParentRelations {
					cr := checkParent.Relation
					cp := checkParent.ParentType
					model := AuthzModel{
						ObjectType:          t.Name,
						Relation:            r.Name,
						CheckParentRelation: &cr,
						CheckParentType:     &cp,
						RuleGroupID:         &groupID,
						RuleGroupMode:       &mode,
					}
					models = append(models, model)
				}
			}
		}
	}

	return models
}

// computeTransitiveClosure computes the transitive closure of an implied_by graph.
// Given impliedByGraph[A] = [B] means B implies A (having B grants A).
//
// Returns transitiveImpliers[A] = all relations that transitively imply A.
// For example, if owner → admin and admin → member, then:
//   - transitiveImpliers[member] = [admin, owner]
//   - transitiveImpliers[admin] = [owner]
//   - transitiveImpliers[owner] = []
//
// This closure is precomputed during schema loading to avoid recursive
// graph traversal in the database during permission checks.
func computeTransitiveClosure(impliedByGraph map[string][]string) map[string][]string {
	result := make(map[string][]string)

	// For each relation, find all relations that transitively imply it
	for relation := range impliedByGraph {
		visited := make(map[string]bool)
		var allImpliers []string
		collectTransitiveImpliers(relation, impliedByGraph, visited, &allImpliers)
		result[relation] = allImpliers
	}

	return result
}

// collectTransitiveImpliers does a DFS to find all relations that imply the target.
// Uses visited set to handle cycles in the relation graph (though well-formed
// schemas should not have cycles).
func collectTransitiveImpliers(target string, graph map[string][]string, visited map[string]bool, result *[]string) {
	for _, implier := range graph[target] {
		if visited[implier] {
			continue
		}
		visited[implier] = true
		*result = append(*result, implier)
		// Recursively find what implies the implier
		collectTransitiveImpliers(implier, graph, visited, result)
	}
}
