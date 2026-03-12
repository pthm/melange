package parser_test

import (
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/stretchr/testify/require"
)

func TestSubtractDifferenceExclusionParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer but not (editor but not owner)
`

	types, err := parser.ParseSchemaString(dsl)
	require.NoError(t, err)

	var viewerRel *schema.RelationDefinition
	for i := range types {
		if types[i].Name != "document" {
			continue
		}
		for j := range types[i].Relations {
			if types[i].Relations[j].Name == "viewer" {
				viewerRel = &types[i].Relations[j]
				break
			}
		}
	}
	require.NotNil(t, viewerRel)
	require.Len(t, viewerRel.ExcludedIntersectionGroups, 1)
	require.ElementsMatch(t, viewerRel.ExcludedIntersectionGroups[0].Relations, []string{"editor"})
	require.Contains(t, viewerRel.ExcludedIntersectionGroups[0].Exclusions, "editor")
	require.ElementsMatch(t, viewerRel.ExcludedIntersectionGroups[0].Exclusions["editor"], []string{"owner"})
}

func TestIntersectionParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define viewer: writer and editor
`

	types, err := parser.ParseSchemaString(dsl)
	require.NoError(t, err)
	require.Len(t, types, 2) // user, document

	// Find document type
	var docType *schema.TypeDefinition
	for i := range types {
		if types[i].Name == "document" {
			docType = &types[i]
			break
		}
	}
	require.NotNil(t, docType)

	// Find viewer relation
	var viewerRel *schema.RelationDefinition
	for i := range docType.Relations {
		if docType.Relations[i].Name == "viewer" {
			viewerRel = &docType.Relations[i]
			break
		}
	}
	require.NotNil(t, viewerRel)

	// Check intersection groups
	t.Logf("viewer.IntersectionGroups: %+v", viewerRel.IntersectionGroups)
	t.Logf("viewer.ImpliedBy: %+v", viewerRel.ImpliedBy)
	t.Logf("viewer.SubjectTypeRefs: %+v", viewerRel.SubjectTypeRefs)

	require.Len(t, viewerRel.IntersectionGroups, 1, "should have one intersection group")
	require.ElementsMatch(t, viewerRel.IntersectionGroups[0].Relations, []string{"writer", "editor"})
	require.Empty(t, viewerRel.ImpliedBy, "ImpliedBy should be empty for intersection")

	// Check model generation
	models := schema.ToAuthzModels(types)
	t.Logf("Generated %d models", len(models))

	// Count intersection rules for viewer
	var intersectionRules []schema.AuthzModel
	for _, m := range models {
		if m.ObjectType == "document" && m.Relation == "viewer" {
			t.Logf("viewer model: %+v", m)
			if m.RuleGroupMode != nil && *m.RuleGroupMode == "intersection" {
				intersectionRules = append(intersectionRules, m)
			}
		}
	}

	require.Len(t, intersectionRules, 2, "should have 2 intersection rules for viewer")

	// Check the rules have correct check_relations
	checkRelations := make([]string, 0, len(intersectionRules))
	for _, r := range intersectionRules {
		require.NotNil(t, r.CheckRelation)
		checkRelations = append(checkRelations, *r.CheckRelation)
	}
	require.ElementsMatch(t, checkRelations, []string{"writer", "editor"})
}

func TestIntersectionExclusionInSubtractParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer but not (editor and owner)
`

	types, err := parser.ParseSchemaString(dsl)
	require.NoError(t, err)

	var viewerRel *schema.RelationDefinition
	for i := range types {
		if types[i].Name != "document" {
			continue
		}
		for j := range types[i].Relations {
			if types[i].Relations[j].Name == "viewer" {
				viewerRel = &types[i].Relations[j]
				break
			}
		}
	}
	require.NotNil(t, viewerRel)
	require.Len(t, viewerRel.ExcludedIntersectionGroups, 1, "should have one excluded intersection group")
	require.ElementsMatch(t, viewerRel.ExcludedIntersectionGroups[0].Relations, []string{"editor", "owner"})
}

// TestIntersectionWithTTUUnionParsing verifies that intersection rules containing
// unions of tuple-to-userset patterns are correctly parsed.
//
// Schema: can_view: viewer and (member from group or owner from group)
// This should produce 2 intersection groups (via distributive law):
//   - Group 1: viewer AND (member from group)
//   - Group 2: viewer AND (owner from group)
func TestIntersectionWithTTUUnionParsing(t *testing.T) {
	dsl := `
model
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
    define can_view: viewer and (member from group or owner from group)
`

	types, err := parser.ParseSchemaString(dsl)
	require.NoError(t, err)

	// Find the can_view relation on folder
	var canViewRel *schema.RelationDefinition
	for i := range types {
		if types[i].Name != "folder" {
			continue
		}
		for j := range types[i].Relations {
			if types[i].Relations[j].Name == "can_view" {
				canViewRel = &types[i].Relations[j]
				break
			}
		}
	}
	require.NotNil(t, canViewRel, "can_view relation not found")

	// Should have 2 intersection groups due to distributive expansion
	require.Len(t, canViewRel.IntersectionGroups, 2,
		"expected 2 intersection groups from distributive law: (viewer AND member from group) OR (viewer AND owner from group)")

	// Each group should have the viewer relation
	for i, g := range canViewRel.IntersectionGroups {
		require.Contains(t, g.Relations, "viewer",
			"group %d should contain 'viewer' relation", i)
		require.Len(t, g.ParentRelations, 1,
			"group %d should have exactly 1 parent relation", i)
		require.Equal(t, "group", g.ParentRelations[0].LinkingRelation,
			"group %d parent relation should link via 'group'", i)
	}

	// Verify we have both member and owner parent relations across groups
	parentRels := make(map[string]bool)
	for _, g := range canViewRel.IntersectionGroups {
		for _, pr := range g.ParentRelations {
			parentRels[pr.Relation] = true
		}
	}
	require.True(t, parentRels["member"], "should have 'member from group' parent relation")
	require.True(t, parentRels["owner"], "should have 'owner from group' parent relation")
}
