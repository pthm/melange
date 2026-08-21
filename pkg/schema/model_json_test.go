package schema

import (
	"reflect"
	"testing"
)

// fullModel exercises every rule variant so the round-trip test proves the JSON
// encoding preserves the whole schema surface: direct, wildcard, implied,
// parent (TTU), simple exclusion, excluded-parent, userset references,
// intersection groups (with per-relation exclusions and parent checks), and
// excluded intersection groups.
func fullModel() []TypeDefinition {
	return []TypeDefinition{
		{Name: "user"},
		{Name: "group", Relations: []RelationDefinition{
			{Name: "member", SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}}},
		}},
		{
			Name: "folder",
			Relations: []RelationDefinition{
				{Name: "parent", SubjectTypeRefs: []SubjectTypeRef{{Type: "folder"}}},
				{Name: "viewer", SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}}},
			},
		},
		{
			Name: "document",
			Relations: []RelationDefinition{
				// Direct + wildcard + userset reference.
				{Name: "owner", SubjectTypeRefs: []SubjectTypeRef{
					{Type: "user"},
					{Type: "user", Wildcard: true},
					{Type: "group", Relation: "member"},
				}},
				// Implied (computed userset) + simple exclusion.
				{
					Name:              "editor",
					ImpliedBy:         []string{"owner"},
					ExcludedRelations: []string{"banned"},
				},
				// Parent (TTU) + excluded parent (TTU exclusion).
				{
					Name:            "viewer",
					ImpliedBy:       []string{"editor"},
					ParentRelations: []ParentRelationCheck{{Relation: "viewer", LinkingRelation: "parent"}},
					ExcludedParentRelations: []ParentRelationCheck{
						{Relation: "banned", LinkingRelation: "parent"},
					},
				},
				// Intersection group with a per-relation exclusion and a parent check.
				{
					Name: "can_share",
					IntersectionGroups: []IntersectionGroup{
						{
							Relations:       []string{"editor", "viewer"},
							ParentRelations: []ParentRelationCheck{{Relation: "can_share", LinkingRelation: "parent"}},
							Exclusions:      map[string][]string{"editor": {"banned"}},
						},
					},
				},
				// Excluded intersection group: "but not (editor and owner)".
				{
					Name: "restricted",
					ExcludedIntersectionGroups: []IntersectionGroup{
						{Relations: []string{"editor", "owner"}},
					},
				},
				{Name: "banned", SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}}},
			},
		},
	}
}

func TestModelJSONRoundTrip(t *testing.T) {
	original := fullModel()

	data, err := MarshalModel(original)
	if err != nil {
		t.Fatalf("MarshalModel: %v", err)
	}

	got, err := UnmarshalModel(data)
	if err != nil {
		t.Fatalf("UnmarshalModel: %v", err)
	}

	if !reflect.DeepEqual(original, got) {
		t.Fatalf("round-trip mismatch:\n original = %#v\n got      = %#v", original, got)
	}
}

func TestUnmarshalModel_Empty(t *testing.T) {
	for _, in := range [][]byte{nil, {}} {
		got, err := UnmarshalModel(in)
		if err != nil {
			t.Fatalf("UnmarshalModel(%v): %v", in, err)
		}
		if got != nil {
			t.Errorf("UnmarshalModel(%v) = %#v, want nil", in, got)
		}
	}
}

func TestUnmarshalModel_Invalid(t *testing.T) {
	if _, err := UnmarshalModel([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
