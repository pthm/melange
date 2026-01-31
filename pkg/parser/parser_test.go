package parser

import (
	"testing"
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

	// Find folder type
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

	// Find can_view relation
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
