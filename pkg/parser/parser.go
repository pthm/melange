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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// IsModularSchema reports whether path points to an fga.mod manifest.
func IsModularSchema(path string) bool {
	return filepath.Base(path) == "fga.mod"
}

// ParseSchema reads an OpenFGA schema and returns type definitions.
// Accepts either a single .fga file or an fga.mod manifest for modular schemas.
//
// For single .fga files, uses the existing single-file parser.
// For fga.mod manifests, reads all referenced module files and merges them
// into a unified model using the upstream OpenFGA library.
func ParseSchema(path string) ([]schema.TypeDefinition, error) {
	if IsModularSchema(path) {
		return ParseModularSchema(path)
	}

	content, err := os.ReadFile(path) //nolint:gosec // path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading schema file: %w", err)
	}

	return ParseSchemaString(string(content))
}

// ReadSchemaContent reads the full content of a schema for hashing purposes.
// For single .fga files, returns the file bytes directly.
// For fga.mod manifests, returns the manifest plus all referenced module files
// concatenated in manifest order (deterministic).
func ReadSchemaContent(path string) ([]byte, error) {
	if IsModularSchema(path) {
		return ReadManifestContents(path)
	}
	content, err := os.ReadFile(path) //nolint:gosec // path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading schema file: %w", err)
	}
	return content, nil
}

// ParseModularSchema parses an fga.mod manifest and all referenced module
// files, merging them into a unified set of type definitions.
//
// Uses the upstream OpenFGA library for manifest parsing (TransformModFile),
// module merging (TransformModuleFilesToModel), and all validation including
// duplicate relation detection, undefined type extensions, and schema version
// checks.
func ParseModularSchema(manifestPath string) ([]schema.TypeDefinition, error) {
	data, err := readManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	model, err := transformer.TransformModuleFilesToModel(data.Modules, data.SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("compiling modules: %w", err)
	}

	if err := checkUnsupported(model); err != nil {
		return nil, err
	}

	return convertModel(model)
}

// ParseSchemaString parses OpenFGA DSL content and returns type definitions.
// This is the core parser used by both file-based and string-based parsing.
// Wraps the OpenFGA transformer to convert protobuf models to our format.
func ParseSchemaString(content string) ([]schema.TypeDefinition, error) {
	model, err := transformer.TransformDSLToProto(content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", melange.ErrInvalidSchema, err)
	}

	if err := checkUnsupported(model); err != nil {
		return nil, err
	}

	return convertModel(model)
}

// compatibilityDocsURL documents which OpenFGA features melange supports.
const compatibilityDocsURL = "https://melange.sh/docs/reference/openfga-compatibility/"

// checkUnsupported rejects models using OpenFGA features melange cannot
// compile. Conditions are the only one: convertModel drops them, so a schema
// relying on a condition to narrow access would compile to a broader grant.
// Failing closed is the only safe option until conditions are supported.
func checkUnsupported(model *openfgav1.AuthorizationModel) error {
	var found []string

	for _, td := range model.GetTypeDefinitions() {
		relMeta := td.GetMetadata().GetRelations()
		for _, relName := range slices.Sorted(maps.Keys(relMeta)) {
			for _, t := range relMeta[relName].GetDirectlyRelatedUserTypes() {
				if cond := t.GetCondition(); cond != "" {
					found = append(found, fmt.Sprintf("%s#%s allows [%s with %s]",
						td.GetType(), relName, subjectRefString(t), cond))
				}
			}
		}
	}

	// A condition can be declared but never applied to a type restriction.
	// Still unsupported, and reporting it beats a confusing silent pass.
	if len(found) == 0 {
		for _, name := range slices.Sorted(maps.Keys(model.GetConditions())) {
			found = append(found, fmt.Sprintf("condition %q is declared", name))
		}
	}

	if len(found) == 0 {
		return nil
	}

	return fmt.Errorf("%w: conditions are not supported and would be silently dropped: %s. Remove them or see %s",
		melange.ErrInvalidSchema, strings.Join(found, "; "), compatibilityDocsURL)
}

// subjectRefString renders a type restriction the way it appears in the DSL:
// "user", "user:*", or "group#member".
func subjectRefString(t *openfgav1.RelationReference) string {
	switch v := t.GetRelationOrWildcard().(type) {
	case *openfgav1.RelationReference_Wildcard:
		return t.GetType() + ":*"
	case *openfgav1.RelationReference_Relation:
		return t.GetType() + "#" + v.Relation
	default:
		return t.GetType()
	}
}

// ConvertProtoModel converts an OpenFGA protobuf AuthorizationModel to schema
// TypeDefinitions. This is useful when you have a protobuf model directly
// (e.g., from the OpenFGA API) rather than DSL text.
//
// Like the DSL entrypoints it rejects models melange cannot compile exactly
// (conditions, or expressions outside what the flat schema can represent)
// with an error wrapping melange.ErrInvalidSchema.
func ConvertProtoModel(model *openfgav1.AuthorizationModel) ([]schema.TypeDefinition, error) {
	if err := checkUnsupported(model); err != nil {
		return nil, err
	}
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
func convertModel(model *openfgav1.AuthorizationModel) ([]schema.TypeDefinition, error) {
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
			relDef, err := convertRelation(relName, rel, directTypeRefs[relName])
			if err != nil {
				return nil, err
			}
			typeDef.Relations = append(typeDef.Relations, relDef)
		}

		types = append(types, typeDef)
	}

	return types, nil
}

// convertRelation converts a protobuf Userset to our RelationDefinition format.
// The Userset describes who has this relation and how it's computed:
//   - Direct assignment: explicitly granted via tuples
//   - Computed relations: implied by other relations (role hierarchy)
//   - Tuple-to-userset: inherited from related objects (parent permissions)
//   - Union/intersection/difference: combining multiple rules
//   - Userset references: access via group membership [type#relation]
func convertRelation(name string, rel *openfgav1.Userset, subjectTypeRefs []schema.SubjectTypeRef) (schema.RelationDefinition, error) {
	relDef := schema.RelationDefinition{
		Name:            name,
		SubjectTypeRefs: subjectTypeRefs,
	}
	err := extractUserset(rel, &relDef)
	return relDef, err
}

// extractUserset flattens a relation's userset into RelationDefinition fields.
//
// The relation's own top level keeps the cheap flat encodings that codegen
// optimizes: direct, implied (closure-inlined), TTU, and relation-level
// exclusions for a `but not` chain at the root. Anything nested (an
// intersection anywhere, or a difference under a union) goes through
// normalize, which handles every node or rejects the schema; nothing is
// silently dropped.
func extractUserset(us *openfgav1.Userset, rel *schema.RelationDefinition) error {
	switch v := us.GetUserset().(type) {
	case *openfgav1.Userset_This:
		// Direct assignment - subject types are handled via metadata.
		return nil

	case *openfgav1.Userset_ComputedUserset:
		rel.ImpliedBy = append(rel.ImpliedBy, v.ComputedUserset.GetRelation())
		return nil

	case *openfgav1.Userset_TupleToUserset:
		rel.ParentRelations = append(rel.ParentRelations, schema.ParentRelationCheck{
			Relation:        v.TupleToUserset.GetComputedUserset().GetRelation(),
			LinkingRelation: v.TupleToUserset.GetTupleset().GetRelation(),
		})
		return nil

	case *openfgav1.Userset_Union:
		for _, child := range v.Union.GetChild() {
			// A difference under a union only narrows its own branch, so it
			// must become a group; relation-level exclusions would narrow the
			// sibling branches too.
			if _, ok := child.GetUserset().(*openfgav1.Userset_Difference); ok {
				if err := appendGroups(child, rel); err != nil {
					return err
				}
				continue
			}
			if err := extractUserset(child, rel); err != nil {
				return err
			}
		}
		return nil

	case *openfgav1.Userset_Intersection:
		return appendGroups(us, rel)

	case *openfgav1.Userset_Difference:
		// Root `but not` chain: the subtract narrows the whole relation.
		if err := extractUserset(v.Difference.GetBase(), rel); err != nil {
			return err
		}
		return appendExclusions(v.Difference.GetSubtract(), rel)

	default:
		return fmt.Errorf("%w: relation %q has an unsupported userset node %T", melange.ErrInvalidSchema, rel.Name, v)
	}
}

// appendGroups normalizes a userset and adds each term as an intersection group.
func appendGroups(us *openfgav1.Userset, rel *schema.RelationDefinition) error {
	terms, err := normalize(us)
	if err != nil {
		return err
	}
	for _, t := range terms {
		g, err := toGroup(t, rel.Name, true)
		if err != nil {
			return err
		}
		rel.IntersectionGroups = append(rel.IntersectionGroups, g)
	}
	return nil
}

// appendExclusions records a relation-level subtract. Each normal-form term
// is excluded independently (not (A or B) = not A and not B), using the
// cheapest encoding that expresses it exactly.
func appendExclusions(subtract *openfgav1.Userset, rel *schema.RelationDefinition) error {
	terms, err := normalize(subtract)
	if err != nil {
		return err
	}
	for _, t := range terms {
		if len(t) == 1 && !t[0].negated {
			switch t[0].kind {
			case litRelation:
				rel.ExcludedRelations = append(rel.ExcludedRelations, t[0].rel)
				continue
			case litTTU:
				rel.ExcludedParentRelations = append(rel.ExcludedParentRelations, schema.ParentRelationCheck{
					Relation: t[0].rel, LinkingRelation: t[0].link,
				})
				continue
			}
		}
		g, err := toGroup(t, rel.Name, false)
		if err != nil {
			return err
		}
		rel.ExcludedIntersectionGroups = append(rel.ExcludedIntersectionGroups, g)
	}
	return nil
}
