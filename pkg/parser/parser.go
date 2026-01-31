// Package parser provides OpenFGA schema parsing for melange.
//
// This package wraps the official OpenFGA language parser to convert .fga
// schema files into melange's internal TypeDefinition format. It isolates
// the OpenFGA parser dependency from other packages.
//
// # Basic Usage
//
// Parse a schema file:
//
//	types, err := parser.ParseSchema("schemas/schema.fga")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// Parse schema from a string:
//
//	types, err := parser.ParseSchemaString(schemaContent)
//
// # Dependency Isolation
//
// The parser package is the only melange package that imports the OpenFGA
// language parser. This keeps the runtime (github.com/pthm/melange/melange)
// free of external dependencies.
//
// Consumers of parsed schemas should use pkg/schema types, which have no
// external dependencies.
package parser

import (
	"fmt"
	"os"
	"sort"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// ParseSchema reads an OpenFGA .fga file and returns type definitions.
// Uses the official OpenFGA language parser to ensure compatibility with
// the OpenFGA ecosystem and tooling.
//
// The parser extracts type definitions, relations, and metadata that are
// then converted to melange's internal representation for code generation
// and database migration.
func ParseSchema(path string) ([]schema.TypeDefinition, error) {
	content, err := os.ReadFile(path) //nolint:gosec // path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading schema file: %w", err)
	}

	return ParseSchemaString(string(content))
}

// ParseSchemaString parses OpenFGA DSL content and returns type definitions.
// This is the core parser used by both file-based and string-based parsing.
// Wraps the OpenFGA transformer to convert protobuf models to our format.
func ParseSchemaString(content string) ([]schema.TypeDefinition, error) {
	model, err := transformer.TransformDSLToProto(content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", melange.ErrInvalidSchema, err)
	}

	return convertModel(model), nil
}

// ConvertProtoModel converts an OpenFGA protobuf AuthorizationModel to schema
// TypeDefinitions. This is useful when you have a protobuf model directly
// (e.g., from the OpenFGA API) rather than DSL text.
//
// This function is used by the OpenFGA test suite adapter to convert test
// models without re-implementing the parsing logic.
func ConvertProtoModel(model *openfgav1.AuthorizationModel) []schema.TypeDefinition {
	return convertModel(model)
}

// convertModel converts the protobuf model to our TypeDefinition format.
// Extracts the essential authorization rules from OpenFGA's protobuf representation:
//   - Type definitions (user, repository, etc.)
//   - Relations on each type (owner, member, can_read, etc.)
//   - Directly related user types from metadata (who can have this relation)
//   - Userset rules (implied relations, parent inheritance, exclusions)
//
// The conversion preserves all information needed to generate Go code and
// populate the generated SQL entrypoints.
func convertModel(model *openfgav1.AuthorizationModel) []schema.TypeDefinition {
	typeDefs := model.GetTypeDefinitions()
	types := make([]schema.TypeDefinition, 0, len(typeDefs))

	for _, td := range typeDefs {
		typeDef := schema.TypeDefinition{
			Name: td.GetType(),
		}

		// Get directly related user types from metadata
		// This extracts both simple type references [user] and userset references [group#member]
		directTypeRefs := make(map[string][]schema.SubjectTypeRef)
		if meta := td.GetMetadata(); meta != nil {
			// Sort relation names for deterministic order
			relMetaMap := meta.GetRelations()
			relNames := make([]string, 0, len(relMetaMap))
			for relName := range relMetaMap {
				relNames = append(relNames, relName)
			}
			sort.Strings(relNames)

			for _, relName := range relNames {
				relMeta := relMetaMap[relName]
				for _, t := range relMeta.GetDirectlyRelatedUserTypes() {
					typeName := t.GetType()
					ref := schema.SubjectTypeRef{Type: typeName}

					switch v := t.GetRelationOrWildcard().(type) {
					case *openfgav1.RelationReference_Wildcard:
						ref.Wildcard = true
					case *openfgav1.RelationReference_Relation:
						// This is a userset reference like [group#member]
						ref.Relation = v.Relation
					}

					directTypeRefs[relName] = append(directTypeRefs[relName], ref)
				}
			}
		}

		// Convert relations - sort for deterministic order
		relMap := td.GetRelations()
		relNames := make([]string, 0, len(relMap))
		for relName := range relMap {
			relNames = append(relNames, relName)
		}
		sort.Strings(relNames)

		for _, relName := range relNames {
			rel := relMap[relName]
			relDef := convertRelation(relName, rel, directTypeRefs[relName])
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
func convertRelation(name string, rel *openfgav1.Userset, subjectTypeRefs []schema.SubjectTypeRef) schema.RelationDefinition {
	relDef := schema.RelationDefinition{
		Name:            name,
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
func extractUserset(us *openfgav1.Userset, rel *schema.RelationDefinition) {
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
		parentRel := v.TupleToUserset.GetComputedUserset().GetRelation()
		linkingRel := v.TupleToUserset.GetTupleset().GetRelation()
		rel.ParentRelations = append(rel.ParentRelations, schema.ParentRelationCheck{
			Relation:        parentRel,
			LinkingRelation: linkingRel,
		})

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
		// For nested exclusions like "(writer but not editor) but not owner",
		// we need to collect ALL exclusions, not just the outermost one.
		// The base may itself be a Difference, so we recurse first.
		extractUserset(v.Difference.GetBase(), rel)
		// Add exclusions from the subtract part
		// The subtract can be a simple relation (ComputedUserset) or a union (editor or owner)
		if subtract := v.Difference.GetSubtract(); subtract != nil {
			excludedRels, excludedParents := extractSubtractRelations(subtract)
			rel.ExcludedRelations = append(rel.ExcludedRelations, excludedRels...)
			rel.ExcludedParentRelations = append(rel.ExcludedParentRelations, excludedParents...)
			excludedIntersectionGroups := extractSubtractIntersectionGroups(subtract, rel.Name)
			rel.ExcludedIntersectionGroups = append(rel.ExcludedIntersectionGroups, excludedIntersectionGroups...)
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
func expandIntersection(intersection *openfgav1.Usersets, relationName string) []schema.IntersectionGroup {
	// Start with one empty group
	groups := []schema.IntersectionGroup{{}}

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
			// Add a parent-relation check to all groups
			rel := cv.TupleToUserset.GetComputedUserset().GetRelation()
			linkingRel := cv.TupleToUserset.GetTupleset().GetRelation()
			for i := range groups {
				groups[i].ParentRelations = append(groups[i].ParentRelations, schema.ParentRelationCheck{
					Relation:        rel,
					LinkingRelation: linkingRel,
				})
			}

		case *openfgav1.Userset_Union:
			// Union within intersection: apply distributive law
			// For "a and (b or c)", if groups = [[a]], expand to [[a,b], [a,c]]
			// For "a and (b from parent or c from parent)", expand with parent relations
			contents := extractUnionContents(cv.Union)
			if len(contents.Relations) > 0 || len(contents.ParentRelations) > 0 {
				groups = distributeUnionContents(groups, contents)
			}

		case *openfgav1.Userset_Intersection:
			// Nested intersection: flatten into existing groups
			nestedGroups := expandIntersection(cv.Intersection, relationName)
			// If nested has multiple groups (due to its own unions), we'd need
			// to cross-product. For now, just flatten the first group.
			if len(nestedGroups) == 0 {
				continue
			}
			first := nestedGroups[0]
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, first.Relations...)
				groups[i].ParentRelations = append(groups[i].ParentRelations, first.ParentRelations...)
				mergeExclusions(&groups[i], first.Exclusions)
			}

		case *openfgav1.Userset_Difference:
			// Difference within intersection: "a and (b but not c)"
			// Extract the base relation and the exclusion
			baseRel := extractBaseRelationFromDifference(cv.Difference)
			if baseRel == "" {
				continue
			}

			// Extract the excluded relation
			var excludedRel string
			if subtract := cv.Difference.GetSubtract(); subtract != nil {
				if computed := subtract.GetComputedUserset(); computed != nil {
					excludedRel = computed.GetRelation()
				}
			}

			// Add the base relation and its exclusion to all groups
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, baseRel)
				if excludedRel != "" {
					if groups[i].Exclusions == nil {
						groups[i].Exclusions = make(map[string][]string)
					}
					groups[i].Exclusions[baseRel] = append(groups[i].Exclusions[baseRel], excludedRel)
				}
			}
		}
	}

	return groups
}

// extractBaseRelationFromDifference extracts the base relation from a Difference node.
// For "b but not c", returns "b".
// Handles nested differences like "(a but not b) but not c" by recursing.
func extractBaseRelationFromDifference(diff *openfgav1.Difference) string {
	base := diff.GetBase()
	if base == nil {
		return ""
	}

	switch bv := base.Userset.(type) {
	case *openfgav1.Userset_ComputedUserset:
		return bv.ComputedUserset.GetRelation()
	case *openfgav1.Userset_Difference:
		// Nested difference: "(a but not b) but not c" - extract from inner
		return extractBaseRelationFromDifference(bv.Difference)
	case *openfgav1.Userset_This:
		// This case shouldn't happen in well-formed schemas, but handle it
		return ""
	default:
		return ""
	}
}

// unionContents holds the extracted contents from a union node.
//
// When applying the distributive law for intersections containing unions
// (A ∧ (B ∨ C) = (A ∧ B) ∨ (A ∧ C)), we need to handle both simple relations
// and tuple-to-userset (TTU) patterns. OpenFGA allows mixing these in unions:
//
//	"viewer and (editor or member from group)"
//
// This struct separates the two pattern types so distributeUnionContents can
// create proper IntersectionGroup instances for each distributed term.
type unionContents struct {
	Relations       []string                     // Simple computed usersets like "viewer"
	ParentRelations []schema.ParentRelationCheck // Tuple-to-userset patterns like "member from group"
}

// extractUnionContents extracts both relation names and parent relations from a union node.
//
// OpenFGA unions can contain:
//   - Simple relations: "a or b" → Relations: ["a", "b"]
//   - TTU patterns: "member from group or owner from group" → ParentRelations
//   - Mixed: "viewer or member from group" → both fields populated
//   - Nested unions: "(a or b) or c" → flattened recursively
//
// The separation into Relations vs ParentRelations preserves the semantic difference
// between direct relation checks and parent inheritance lookups. Nested unions are
// flattened because union is associative: (A ∨ B) ∨ C ≡ A ∨ B ∨ C.
func extractUnionContents(union *openfgav1.Usersets) unionContents {
	var contents unionContents
	for _, child := range union.GetChild() {
		switch cv := child.Userset.(type) {
		case *openfgav1.Userset_ComputedUserset:
			contents.Relations = append(contents.Relations, cv.ComputedUserset.GetRelation())
		case *openfgav1.Userset_TupleToUserset:
			// Extract tuple-to-userset pattern like "member from group"
			rel := cv.TupleToUserset.GetComputedUserset().GetRelation()
			linkingRel := cv.TupleToUserset.GetTupleset().GetRelation()
			contents.ParentRelations = append(contents.ParentRelations, schema.ParentRelationCheck{
				Relation:        rel,
				LinkingRelation: linkingRel,
			})
		case *openfgav1.Userset_Union:
			// Nested union: flatten
			nested := extractUnionContents(cv.Union)
			contents.Relations = append(contents.Relations, nested.Relations...)
			contents.ParentRelations = append(contents.ParentRelations, nested.ParentRelations...)
		}
	}
	return contents
}

// distributeUnionContents applies the distributive law for union contents that may
// contain both simple relations and parent relations (TTU patterns).
//
// Given existing groups and union contents, creates a cross-product where each
// existing group is combined with each union member:
//
//	groups=[[a]], contents={Relations:[b], ParentRelations:[{c,parent}]}
//	→ [[a,b], [a + parentRel(c,parent)]]
//
// This implements A ∧ (B ∨ C) = (A ∧ B) ∨ (A ∧ C). Each resulting group represents
// an AND (all conditions must match), while multiple groups represent OR (any group
// can match). Groups are deep-copied to avoid shared slice mutations.
func distributeUnionContents(groups []schema.IntersectionGroup, contents unionContents) []schema.IntersectionGroup {
	totalMembers := len(contents.Relations) + len(contents.ParentRelations)
	if totalMembers == 0 {
		return groups
	}

	expanded := make([]schema.IntersectionGroup, 0, len(groups)*totalMembers)

	for _, g := range groups {
		// Distribute simple relations
		for _, rel := range contents.Relations {
			ng := cloneIntersectionGroup(g)
			ng.Relations = append(ng.Relations, rel)
			expanded = append(expanded, ng)
		}
		// Distribute parent relations (TTU patterns)
		for _, parentRel := range contents.ParentRelations {
			ng := cloneIntersectionGroup(g)
			ng.ParentRelations = append(ng.ParentRelations, parentRel)
			expanded = append(expanded, ng)
		}
	}
	return expanded
}

// cloneIntersectionGroup creates a deep copy of an IntersectionGroup.
//
// Deep cloning is required during distributive expansion to prevent mutation
// conflicts. When distributing "a and (b or c)", we start with group [[a]]
// and create two outputs: [[a,b], [a,c]]. Without deep cloning, appending "b"
// to the first output would corrupt the second output via shared slice storage.
func cloneIntersectionGroup(g schema.IntersectionGroup) schema.IntersectionGroup {
	newGroup := schema.IntersectionGroup{
		Relations: append([]string{}, g.Relations...),
	}
	if len(g.ParentRelations) > 0 {
		newGroup.ParentRelations = append([]schema.ParentRelationCheck{}, g.ParentRelations...)
	}
	newGroup.Exclusions = copyExclusions(g.Exclusions)
	return newGroup
}

// copyExclusions creates a deep copy of an exclusion map.
//
// Copies both the map structure and the string slices within it. The nested
// cloning is necessary because distributeUnionContents appends to exclusion
// lists, and shared slice storage would cause mutations to leak across groups.
func copyExclusions(src map[string][]string) map[string][]string {
	if src == nil {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for k, v := range src {
		dst[k] = append([]string{}, v...)
	}
	return dst
}

// mergeExclusions merges source exclusions into a group's exclusion map.
//
// For each relation in src, appends its exclusions to the group's list for that
// relation. This is an append-merge, not a replacement:
//
//	existing: {"a": ["b"]}, src: {"a": ["c"]} → result: {"a": ["b", "c"]}
//
// Used when flattening nested intersections that already have exclusions.
// Creates the group's Exclusions map lazily on first merge.
func mergeExclusions(g *schema.IntersectionGroup, src map[string][]string) {
	if src == nil {
		return
	}
	if g.Exclusions == nil {
		g.Exclusions = make(map[string][]string, len(src))
	}
	for k, v := range src {
		g.Exclusions[k] = append(g.Exclusions[k], v...)
	}
}

// extractSubtractRelations extracts all relation names from a subtract userset,
// along with tuple-to-userset exclusions.
// Handles both simple relations (ComputedUserset) and unions (editor or owner).
// For "but not (editor or owner)", returns ["editor", "owner"].
// For "but not author", returns ["author"].
func extractSubtractRelations(us *openfgav1.Userset) ([]string, []schema.ParentRelationCheck) {
	if us == nil {
		return nil, nil
	}

	switch v := us.Userset.(type) {
	case *openfgav1.Userset_ComputedUserset:
		// Simple relation: "but not author"
		return []string{v.ComputedUserset.GetRelation()}, nil

	case *openfgav1.Userset_Union:
		// Union in subtract: "but not (editor or owner)"
		// All relations in the union should exclude
		var rels []string
		var parents []schema.ParentRelationCheck
		for _, child := range v.Union.GetChild() {
			childRels, childParents := extractSubtractRelations(child)
			rels = append(rels, childRels...)
			parents = append(parents, childParents...)
		}
		return rels, parents

	case *openfgav1.Userset_This:
		// Direct assignment in subtract - use the relation being defined
		// This is rare but could occur
		return nil, nil

	case *openfgav1.Userset_TupleToUserset:
		// TTU in subtract: "but not (viewer from parent)"
		// Preserve the tuple-to-userset linkage for exclusion evaluation.
		return nil, []schema.ParentRelationCheck{{
			Relation:        v.TupleToUserset.GetComputedUserset().GetRelation(),
			LinkingRelation: v.TupleToUserset.GetTupleset().GetRelation(),
		}}

	default:
		return nil, nil
	}
}

// extractSubtractIntersectionGroups extracts intersection groups from a subtract userset.
// For "but not (editor and owner)", returns one group: [[editor, owner]].
// For unions, returns the union of all child intersection groups.
func extractSubtractIntersectionGroups(us *openfgav1.Userset, relationName string) []schema.IntersectionGroup {
	if us == nil {
		return nil
	}

	switch v := us.Userset.(type) {
	case *openfgav1.Userset_Intersection:
		return expandIntersection(v.Intersection, relationName)
	case *openfgav1.Userset_Union:
		var groups []schema.IntersectionGroup
		for _, child := range v.Union.GetChild() {
			groups = append(groups, extractSubtractIntersectionGroups(child, relationName)...)
		}
		return groups
	case *openfgav1.Userset_Difference:
		baseGroups := extractBaseIntersectionGroups(v.Difference.GetBase(), relationName)
		if len(baseGroups) == 0 {
			return nil
		}

		excludedRels, _ := extractSubtractRelations(v.Difference.GetSubtract())
		if len(excludedRels) == 0 {
			return baseGroups
		}

		for i := range baseGroups {
			if len(baseGroups[i].Relations) == 0 {
				continue
			}
			if baseGroups[i].Exclusions == nil {
				baseGroups[i].Exclusions = make(map[string][]string)
			}
			for _, rel := range baseGroups[i].Relations {
				baseGroups[i].Exclusions[rel] = append(baseGroups[i].Exclusions[rel], excludedRels...)
			}
		}

		return baseGroups
	default:
		return nil
	}
}

// extractBaseIntersectionGroups extracts intersection groups from a base userset.
// For "editor", returns [[editor]]. For "a and b", returns [[a, b]].
// For unions, returns the concatenation of each child group's results.
func extractBaseIntersectionGroups(us *openfgav1.Userset, relationName string) []schema.IntersectionGroup {
	if us == nil {
		return nil
	}

	switch v := us.Userset.(type) {
	case *openfgav1.Userset_ComputedUserset:
		rel := v.ComputedUserset.GetRelation()
		return []schema.IntersectionGroup{{Relations: []string{rel}}}
	case *openfgav1.Userset_Intersection:
		return expandIntersection(v.Intersection, relationName)
	case *openfgav1.Userset_Union:
		var groups []schema.IntersectionGroup
		for _, child := range v.Union.GetChild() {
			groups = append(groups, extractBaseIntersectionGroups(child, relationName)...)
		}
		return groups
	case *openfgav1.Userset_Difference:
		return extractBaseIntersectionGroups(v.Difference.GetBase(), relationName)
	default:
		return nil
	}
}
