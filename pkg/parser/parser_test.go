package parser

import (
	"testing"

	"github.com/pthm/melange/pkg/schema"
)

func TestExpandIntersection_TTUUnion(t *testing.T) {
	// Test case: viewer and (member from group or owner from group)
	// This should produce TWO intersection groups via distribution:
	// - Group 1: Relations: ["viewer"], ParentRelations: [{member, group}]
	// - Group 2: Relations: ["viewer"], ParentRelations: [{owner, group}]

	schemaStr := `model
  schema 1.1

type user

type group
  relations
    define owner: [user]
    define member: [user]

type folder
  relations
    define group: [group]
    define viewer: [user]
    define can_view: viewer and (member from group or owner from group)`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	canViewRel := findRelation(t, types, "folder", "can_view")

	// Verify intersection groups exist
	if len(canViewRel.IntersectionGroups) == 0 {
		t.Fatal("expected intersection groups to be populated")
	}

	// The distributive law should produce 2 groups:
	// Group 1: viewer AND (member from group)
	// Group 2: viewer AND (owner from group)
	if len(canViewRel.IntersectionGroups) != 2 {
		t.Errorf("expected 2 intersection groups (distributed), got %d", len(canViewRel.IntersectionGroups))
		for i, g := range canViewRel.IntersectionGroups {
			t.Logf("Group %d: Relations=%v, ParentRelations=%v", i, g.Relations, g.ParentRelations)
		}
	}

	// Verify each group has viewer relation
	for i, g := range canViewRel.IntersectionGroups {
		hasViewer := false
		for _, rel := range g.Relations {
			if rel == "viewer" {
				hasViewer = true
				break
			}
		}
		if !hasViewer {
			t.Errorf("group %d missing 'viewer' relation: %v", i, g.Relations)
		}

		// Each group should have exactly one parent relation
		if len(g.ParentRelations) != 1 {
			t.Errorf("group %d: expected 1 parent relation, got %d: %v", i, len(g.ParentRelations), g.ParentRelations)
		}
	}

	// Verify we have both member and owner parent relations across groups
	foundMember := false
	foundOwner := false
	for _, g := range canViewRel.IntersectionGroups {
		for _, pr := range g.ParentRelations {
			if pr.Relation == "member" && pr.LinkingRelation == "group" {
				foundMember = true
			}
			if pr.Relation == "owner" && pr.LinkingRelation == "group" {
				foundOwner = true
			}
		}
	}
	if !foundMember {
		t.Error("missing parent relation for 'member from group'")
	}
	if !foundOwner {
		t.Error("missing parent relation for 'owner from group'")
	}
}

func TestExpandIntersection_MixedUnion(t *testing.T) {
	// Test case: viewer and (editor or member from group)
	// This is a mixed union with both simple relation and TTU

	schemaStr := `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type folder
  relations
    define group: [group]
    define viewer: [user]
    define editor: [user]
    define can_view: viewer and (editor or member from group)`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	folderTypeIdx := -1
	for i := range types {
		if types[i].Name == "folder" {
			folderTypeIdx = i
			break
		}
	}
	if folderTypeIdx < 0 {
		t.Fatal("folder type not found")
	}
	folderType := types[folderTypeIdx]

	canViewRelIdx := -1
	for i := range folderType.Relations {
		if folderType.Relations[i].Name == "can_view" {
			canViewRelIdx = i
			break
		}
	}
	if canViewRelIdx < 0 {
		t.Fatal("can_view relation not found")
	}
	canViewRel := folderType.Relations[canViewRelIdx]

	// Should produce 2 groups:
	// Group 1: viewer AND editor
	// Group 2: viewer AND (member from group)
	if len(canViewRel.IntersectionGroups) != 2 {
		t.Errorf("expected 2 intersection groups, got %d", len(canViewRel.IntersectionGroups))
		for i, g := range canViewRel.IntersectionGroups {
			t.Logf("Group %d: Relations=%v, ParentRelations=%v", i, g.Relations, g.ParentRelations)
		}
	}

	// Find the groups
	groupWithEditorIdx, groupWithParentIdx := -1, -1
	for i := range canViewRel.IntersectionGroups {
		g := canViewRel.IntersectionGroups[i]
		for _, rel := range g.Relations {
			if rel == "editor" {
				groupWithEditorIdx = i
			}
		}
		if len(g.ParentRelations) > 0 {
			groupWithParentIdx = i
		}
	}

	if groupWithEditorIdx < 0 {
		t.Error("missing group with 'editor' relation")
	} else {
		groupWithEditor := canViewRel.IntersectionGroups[groupWithEditorIdx]
		// This group should have viewer + editor, no parent relations
		if len(groupWithEditor.ParentRelations) != 0 {
			t.Errorf("editor group should have no parent relations, got %v", groupWithEditor.ParentRelations)
		}
	}

	if groupWithParentIdx < 0 {
		t.Error("missing group with parent relation")
	} else {
		groupWithParent := canViewRel.IntersectionGroups[groupWithParentIdx]
		// This group should have viewer + parent relation for member from group
		if len(groupWithParent.ParentRelations) != 1 {
			t.Errorf("expected 1 parent relation, got %d", len(groupWithParent.ParentRelations))
		} else {
			pr := groupWithParent.ParentRelations[0]
			if pr.Relation != "member" || pr.LinkingRelation != "group" {
				t.Errorf("expected member from group, got %s from %s", pr.Relation, pr.LinkingRelation)
			}
		}
	}
}

func TestExpandIntersection_SimpleIntersection(t *testing.T) {
	// Test that simple intersections still work
	schemaStr := `model
  schema 1.1

type user

type doc
  relations
    define writer: [user]
    define editor: [user]
    define can_edit: writer and editor`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	docTypeIdx := -1
	for i := range types {
		if types[i].Name == "doc" {
			docTypeIdx = i
			break
		}
	}
	if docTypeIdx < 0 {
		t.Fatal("doc type not found")
	}
	docType := types[docTypeIdx]

	canEditRelIdx := -1
	for i := range docType.Relations {
		if docType.Relations[i].Name == "can_edit" {
			canEditRelIdx = i
			break
		}
	}
	if canEditRelIdx < 0 {
		t.Fatal("can_edit relation not found")
	}
	canEditRel := docType.Relations[canEditRelIdx]

	// Should have 1 group with writer AND editor
	if len(canEditRel.IntersectionGroups) != 1 {
		t.Errorf("expected 1 intersection group, got %d", len(canEditRel.IntersectionGroups))
	}

	g := canEditRel.IntersectionGroups[0]
	if len(g.Relations) != 2 {
		t.Errorf("expected 2 relations, got %d: %v", len(g.Relations), g.Relations)
	}

	hasWriter := false
	hasEditor := false
	for _, rel := range g.Relations {
		if rel == "writer" {
			hasWriter = true
		}
		if rel == "editor" {
			hasEditor = true
		}
	}
	if !hasWriter || !hasEditor {
		t.Errorf("missing expected relations: writer=%v, editor=%v, got %v", hasWriter, hasEditor, g.Relations)
	}
}

func TestExpandIntersection_DifferenceInIntersection(t *testing.T) {
	// Pattern: viewer: writer and (editor but not owner)
	// This should create an intersection group where one part has an exclusion.
	schemaStr := `model
  schema 1.1

type user

type doc
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer and (editor but not owner)`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	rel := findRelation(t, types, "doc", "viewer")

	if len(rel.IntersectionGroups) != 1 {
		t.Fatalf("expected 1 intersection group, got %d", len(rel.IntersectionGroups))
	}

	g := rel.IntersectionGroups[0]
	if len(g.Relations) != 2 {
		t.Errorf("expected 2 relations in group, got %d: %v", len(g.Relations), g.Relations)
	}

	// The "editor but not owner" part should create an exclusion entry
	if g.Exclusions == nil || len(g.Exclusions["editor"]) == 0 {
		t.Errorf("expected exclusion on editor, got Exclusions=%v", g.Exclusions)
	}
	if g.Exclusions != nil && len(g.Exclusions["editor"]) > 0 && g.Exclusions["editor"][0] != "owner" {
		t.Errorf("expected editor excluded by owner, got %v", g.Exclusions["editor"])
	}
}

func TestExpandIntersection_TTUDirect(t *testing.T) {
	// Pattern: viewer: writer and (member from group)
	// Intersection with a direct TTU (not in a union).
	schemaStr := `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type doc
  relations
    define group: [group]
    define writer: [user]
    define viewer: writer and member from group`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	rel := findRelation(t, types, "doc", "viewer")

	if len(rel.IntersectionGroups) != 1 {
		t.Fatalf("expected 1 intersection group, got %d", len(rel.IntersectionGroups))
	}

	g := rel.IntersectionGroups[0]
	// Should have writer as a relation and member from group as a parent relation
	if len(g.Relations) != 1 || g.Relations[0] != "writer" {
		t.Errorf("expected [writer] relation, got %v", g.Relations)
	}
	if len(g.ParentRelations) != 1 {
		t.Fatalf("expected 1 parent relation, got %d", len(g.ParentRelations))
	}
	if g.ParentRelations[0].Relation != "member" || g.ParentRelations[0].LinkingRelation != "group" {
		t.Errorf("expected member from group, got %s from %s",
			g.ParentRelations[0].Relation, g.ParentRelations[0].LinkingRelation)
	}
}

func TestParseSchemaString_SubtractWithTTU(t *testing.T) {
	// Pattern: viewer: reader but not (admin from parent)
	// Exercises TTU in subtract (extractSubtractRelations).
	schemaStr := `model
  schema 1.1

type user

type folder
  relations
    define admin: [user]

type doc
  relations
    define parent: [folder]
    define reader: [user]
    define viewer: reader but not admin from parent`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	rel := findRelation(t, types, "doc", "viewer")

	// Should have excluded parent relation
	if len(rel.ExcludedParentRelations) == 0 {
		t.Error("expected excluded parent relation for 'admin from parent'")
	}
	if len(rel.ExcludedParentRelations) > 0 {
		ep := rel.ExcludedParentRelations[0]
		if ep.Relation != "admin" || ep.LinkingRelation != "parent" {
			t.Errorf("expected admin from parent, got %s from %s", ep.Relation, ep.LinkingRelation)
		}
	}
}

func TestParseSchemaString_UnionFlattening(t *testing.T) {
	// Pattern: viewer: [user] or editor or owner
	// Multiple OR branches should all resolve.
	schemaStr := `model
  schema 1.1

type user

type doc
  relations
    define editor: [user]
    define owner: [user]
    define viewer: [user] or editor or owner`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	rel := findRelation(t, types, "doc", "viewer")

	// viewer should have direct subject type refs and implied relations
	if len(rel.SubjectTypeRefs) == 0 {
		t.Error("expected direct subject type refs for viewer")
	}
	if len(rel.ImpliedBy) < 2 {
		t.Errorf("expected at least 2 implied relations, got %d", len(rel.ImpliedBy))
	}
}

func TestParseSchemaString_ErrorOnInvalid(t *testing.T) {
	_, err := ParseSchemaString("not a valid schema")
	if err == nil {
		t.Error("expected error for invalid schema")
	}
}

func TestParseSchemaString_EmptySchema(t *testing.T) {
	schemaStr := `model
  schema 1.1

type user`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(types) != 1 || types[0].Name != "user" {
		t.Errorf("expected single 'user' type, got %v", types)
	}
}

func TestParseSchemaString_WildcardSubject(t *testing.T) {
	schemaStr := `model
  schema 1.1

type user

type doc
  relations
    define public: [user:*]`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rel := findRelation(t, types, "doc", "public")
	hasWildcard := false
	for _, ref := range rel.SubjectTypeRefs {
		if ref.Wildcard {
			hasWildcard = true
		}
	}
	if !hasWildcard {
		t.Error("expected wildcard subject type ref for [user:*]")
	}
}

func TestParseSchemaString_UsersetSubjectType(t *testing.T) {
	schemaStr := `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type doc
  relations
    define viewer: [group#member]`

	types, err := ParseSchemaString(schemaStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rel := findRelation(t, types, "doc", "viewer")
	found := false
	for _, ref := range rel.SubjectTypeRefs {
		if ref.Type == "group" && ref.Relation == "member" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected group#member in subject type refs, got %v", rel.SubjectTypeRefs)
	}
}

// findRelation is a test helper that locates a relation by type and name.
func findRelation(t *testing.T, types []schema.TypeDefinition, typeName, relName string) schema.RelationDefinition {
	t.Helper()
	for _, td := range types {
		if td.Name == typeName {
			for _, rel := range td.Relations {
				if rel.Name == relName {
					return rel
				}
			}
			t.Fatalf("relation %s not found in type %s", relName, typeName)
		}
	}
	t.Fatalf("type %s not found", typeName)
	return schema.RelationDefinition{}
}
