// Package tooling provides schema parsing, code generation, and migration
// utilities for melange authorization. This package depends on OpenFGA
// for schema parsing, so it's separated from the core melange package
// to keep runtime dependencies minimal.
//
// Users who only need runtime permission checking should import
// "github.com/pthm/melange" directly. Users who need programmatic
// schema parsing or migration should import this package.
package tooling

import (
	"fmt"
	"os"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"

	"github.com/pthm/melange"
)

// ParseSchema reads an OpenFGA .fga file and returns type definitions.
// Uses the official OpenFGA language parser to ensure compatibility with
// the OpenFGA ecosystem and tooling.
//
// The parser extracts type definitions, relations, and metadata that are
// then converted to melange's internal representation for code generation
// and database migration.
func ParseSchema(path string) ([]melange.TypeDefinition, error) {
	content, err := os.ReadFile(path) //nolint:gosec // path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading schema file: %w", err)
	}

	return ParseSchemaString(string(content))
}

// ParseSchemaString parses OpenFGA DSL content and returns type definitions.
// This is the core parser used by both file-based and string-based parsing.
// Wraps the OpenFGA transformer to convert protobuf models to our format.
func ParseSchemaString(content string) ([]melange.TypeDefinition, error) {
	model, err := transformer.TransformDSLToProto(content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", melange.ErrInvalidSchema, err)
	}

	return convertModel(model), nil
}

// convertModel converts the protobuf model to our TypeDefinition format.
// Extracts the essential authorization rules from OpenFGA's protobuf representation:
//   - Type definitions (user, repository, etc.)
//   - Relations on each type (owner, member, can_read, etc.)
//   - Directly related user types from metadata (who can have this relation)
//   - Userset rules (implied relations, parent inheritance, exclusions)
//
// The conversion preserves all information needed to generate Go code and
// populate the melange_model table.
func convertModel(model *openfgav1.AuthorizationModel) []melange.TypeDefinition {
	var types []melange.TypeDefinition

	for _, td := range model.GetTypeDefinitions() {
		typeDef := melange.TypeDefinition{
			Name: td.GetType(),
		}

		// Get directly related user types from metadata
		// This extracts both simple type references [user] and userset references [group#member]
		directTypes := make(map[string][]string)
		directTypeRefs := make(map[string][]melange.SubjectTypeRef)
		if meta := td.GetMetadata(); meta != nil {
			for relName, relMeta := range meta.GetRelations() {
				for _, t := range relMeta.GetDirectlyRelatedUserTypes() {
					typeName := t.GetType()
					ref := melange.SubjectTypeRef{Type: typeName}

					switch v := t.GetRelationOrWildcard().(type) {
					case *openfgav1.RelationReference_Wildcard:
						typeName += ":*"
						ref.Wildcard = true
					case *openfgav1.RelationReference_Relation:
						// This is a userset reference like [group#member]
						ref.Relation = v.Relation
					}

					directTypes[relName] = append(directTypes[relName], typeName)
					directTypeRefs[relName] = append(directTypeRefs[relName], ref)
				}
			}
		}

		// Convert relations
		for relName, rel := range td.GetRelations() {
			relDef := convertRelation(relName, rel, directTypes[relName], directTypeRefs[relName])
			typeDef.Relations = append(typeDef.Relations, relDef)
		}

		types = append(types, typeDef)
	}

	return types
}

// convertRelation converts a protobuf Userset to our RelationDefinition format.
// The Userset describes who has this relation and how it's computed:
//   - Direct assignment: explicitly granted via tuples
//   - Computed relations: implied by other relations (role hierarchy)
//   - Tuple-to-userset: inherited from related objects (parent permissions)
//   - Union/intersection/difference: combining multiple rules
//   - Userset references: access via group membership [type#relation]
func convertRelation(name string, rel *openfgav1.Userset, subjectTypes []string, subjectTypeRefs []melange.SubjectTypeRef) melange.RelationDefinition {
	relDef := melange.RelationDefinition{
		Name:            name,
		SubjectTypes:    subjectTypes,
		SubjectTypeRefs: subjectTypeRefs,
	}

	// Extract implied relations and parent relations from the userset
	extractUserset(rel, &relDef)

	return relDef
}

// extractUserset recursively extracts relation information from a Userset.
// OpenFGA Usersets are recursive tree structures describing permission rules.
//
// Supported patterns:
//   - This: direct tuple assignment (no recursion)
//   - ComputedUserset: implies another relation (owner → admin)
//   - TupleToUserset: inherit from parent object (can_read from org)
//   - Union: permission granted if ANY rule matches
//   - Intersection: permission granted if ALL rules match
//   - Difference: base permission with exclusions ("can_read but not author")
//
// The extraction flattens these rules into our RelationDefinition format,
// which the database functions can evaluate efficiently.
func extractUserset(us *openfgav1.Userset, rel *melange.RelationDefinition) {
	if us == nil {
		return
	}

	switch v := us.Userset.(type) {
	case *openfgav1.Userset_This:
		// Direct assignment - subject types are handled via metadata

	case *openfgav1.Userset_ComputedUserset:
		// Implied relation: this relation is implied by another
		rel.ImpliedBy = append(rel.ImpliedBy, v.ComputedUserset.GetRelation())

	case *openfgav1.Userset_TupleToUserset:
		// Parent relation: permission inherited from related object
		// e.g., "can_read from org" means check can_read on the org
		rel.ParentRelation = v.TupleToUserset.GetComputedUserset().GetRelation()
		rel.ParentType = v.TupleToUserset.GetTupleset().GetRelation()

	case *openfgav1.Userset_Union:
		// Union: permission granted if ANY child grants it
		for _, child := range v.Union.GetChild() {
			extractUserset(child, rel)
		}

	case *openfgav1.Userset_Intersection:
		// Intersection: permission granted if ALL children grant it
		// May produce multiple groups due to distributive expansion
		// E.g., "a and (b or c)" expands to [[a,b], [a,c]]
		groups := expandIntersection(v.Intersection, rel.Name)
		for _, group := range groups {
			if len(group.Relations) > 0 {
				rel.IntersectionGroups = append(rel.IntersectionGroups, group)
			}
		}

	case *openfgav1.Userset_Difference:
		// Difference: base minus subtract (e.g., "can_read but not author")
		// Extract from base
		extractUserset(v.Difference.GetBase(), rel)
		// Extract exclusion from subtract
		if subtract := v.Difference.GetSubtract(); subtract != nil {
			if computed := subtract.GetComputedUserset(); computed != nil {
				rel.ExcludedRelation = computed.GetRelation()
			}
		}
	}
}

// expandIntersection expands an intersection node into one or more groups.
// Returns multiple IntersectionGroups when union-in-intersection requires
// distributive expansion: A ∧ (B ∨ C) = (A ∧ B) ∨ (A ∧ C)
//
// The relationName parameter is needed to handle Userset_This within intersections.
// For "[user] and writer" on relation "viewer", This means "has direct viewer tuple".
//
// For simple intersections like "a and b", returns one group: [[a, b]]
// For "a and (b or c)", returns two groups: [[a, b], [a, c]]
func expandIntersection(intersection *openfgav1.Usersets, relationName string) []melange.IntersectionGroup {
	// Start with one empty group
	groups := []melange.IntersectionGroup{{}}

	for _, child := range intersection.GetChild() {
		switch cv := child.Userset.(type) {
		case *openfgav1.Userset_ComputedUserset:
			// Computed userset: add this relation to all existing groups
			rel := cv.ComputedUserset.GetRelation()
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, rel)
			}

		case *openfgav1.Userset_This:
			// Direct assignment within intersection: "[user] and writer"
			// This means "has a direct tuple for THIS relation"
			// Add the relation name itself to require direct tuple match
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, relationName)
			}

		case *openfgav1.Userset_TupleToUserset:
			// TTU within intersection, e.g., "writer and (can_write from org)"
			// Add the computed relation to all groups
			rel := cv.TupleToUserset.GetComputedUserset().GetRelation()
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, rel)
			}

		case *openfgav1.Userset_Union:
			// Union within intersection: apply distributive law
			// For "a and (b or c)", if groups = [[a]], expand to [[a,b], [a,c]]
			unionRels := extractUnionRelations(cv.Union)
			if len(unionRels) > 0 {
				groups = distributeUnion(groups, unionRels)
			}

		case *openfgav1.Userset_Intersection:
			// Nested intersection: flatten into existing groups
			nestedGroups := expandIntersection(cv.Intersection, relationName)
			// If nested has multiple groups (due to its own unions), we'd need
			// to cross-product. For now, just flatten the first group.
			if len(nestedGroups) > 0 {
				for i := range groups {
					groups[i].Relations = append(groups[i].Relations, nestedGroups[0].Relations...)
				}
			}
		}
	}

	return groups
}

// extractUnionRelations extracts relation names from a union node.
// For simple unions like "a or b", returns ["a", "b"].
// For nested structures, flattens computed usersets only.
func extractUnionRelations(union *openfgav1.Usersets) []string {
	var rels []string
	for _, child := range union.GetChild() {
		switch cv := child.Userset.(type) {
		case *openfgav1.Userset_ComputedUserset:
			rels = append(rels, cv.ComputedUserset.GetRelation())
		case *openfgav1.Userset_Union:
			// Nested union: flatten
			rels = append(rels, extractUnionRelations(cv.Union)...)
		}
	}
	return rels
}

// distributeUnion applies the distributive law: each existing group gets
// expanded for each union member.
// E.g., groups=[[a]], unionRels=[b,c] → [[a,b], [a,c]]
func distributeUnion(groups []melange.IntersectionGroup, unionRels []string) []melange.IntersectionGroup {
	var expanded []melange.IntersectionGroup
	for _, g := range groups {
		for _, rel := range unionRels {
			// Clone the group and add this union member
			newGroup := melange.IntersectionGroup{
				Relations: make([]string, len(g.Relations), len(g.Relations)+1),
			}
			copy(newGroup.Relations, g.Relations)
			newGroup.Relations = append(newGroup.Relations, rel)
			expanded = append(expanded, newGroup)
		}
	}
	return expanded
}
