package schema_test

import (
	"testing"

	"github.com/pthm/melange/tooling/schema"
)

func TestComputeRelationClosure_Simple(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "repo",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// owner should satisfy only itself
	if !hasClosureRow(rows, "repo", "owner", "owner") {
		t.Error("owner should satisfy itself")
	}

	if len(rows) != 1 {
		t.Errorf("expected 1 closure row, got %d", len(rows))
	}
}

func TestComputeRelationClosure_TwoLevel(t *testing.T) {
	// owner -> admin
	types := []schema.TypeDefinition{
		{
			Name: "repo",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", ImpliedBy: []string{"owner"}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// owner satisfies owner
	if !hasClosureRow(rows, "repo", "owner", "owner") {
		t.Error("owner should satisfy itself")
	}

	// admin satisfied by admin and owner
	if !hasClosureRow(rows, "repo", "admin", "admin") {
		t.Error("admin should satisfy itself")
	}
	if !hasClosureRow(rows, "repo", "admin", "owner") {
		t.Error("admin should be satisfied by owner")
	}

	// Verify via_path for owner -> admin
	for _, row := range rows {
		if row.ObjectType == "repo" && row.Relation == "admin" && row.SatisfyingRelation == "owner" {
			if len(row.ViaPath) != 2 || row.ViaPath[0] != "admin" || row.ViaPath[1] != "owner" {
				t.Errorf("via_path should be [admin, owner], got %v", row.ViaPath)
			}
		}
	}
}

func TestComputeRelationClosure_ThreeLevel(t *testing.T) {
	// owner -> admin -> member
	types := []schema.TypeDefinition{
		{
			Name: "repo",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "member", ImpliedBy: []string{"admin"}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// owner satisfies only itself
	if !hasClosureRow(rows, "repo", "owner", "owner") {
		t.Error("owner should satisfy itself")
	}

	// admin satisfied by admin and owner
	if !hasClosureRow(rows, "repo", "admin", "admin") {
		t.Error("admin should satisfy itself")
	}
	if !hasClosureRow(rows, "repo", "admin", "owner") {
		t.Error("admin should be satisfied by owner")
	}

	// member satisfied by member, admin, and owner (transitive!)
	if !hasClosureRow(rows, "repo", "member", "member") {
		t.Error("member should satisfy itself")
	}
	if !hasClosureRow(rows, "repo", "member", "admin") {
		t.Error("member should be satisfied by admin")
	}
	if !hasClosureRow(rows, "repo", "member", "owner") {
		t.Error("member should be satisfied by owner (transitive)")
	}
}

func TestComputeRelationClosure_Diamond(t *testing.T) {
	// viewer implied by both reader and writer
	// reader and writer implied by admin
	//
	//         admin
	//        /     \
	//    reader   writer
	//        \     /
	//        viewer
	types := []schema.TypeDefinition{
		{
			Name: "doc",
			Relations: []schema.RelationDefinition{
				{Name: "admin", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "reader", ImpliedBy: []string{"admin"}},
				{Name: "writer", ImpliedBy: []string{"admin"}},
				{Name: "viewer", ImpliedBy: []string{"reader", "writer"}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// viewer should be satisfied by viewer, reader, writer, and admin
	if !hasClosureRow(rows, "doc", "viewer", "viewer") {
		t.Error("viewer should satisfy itself")
	}
	if !hasClosureRow(rows, "doc", "viewer", "reader") {
		t.Error("viewer should be satisfied by reader")
	}
	if !hasClosureRow(rows, "doc", "viewer", "writer") {
		t.Error("viewer should be satisfied by writer")
	}
	if !hasClosureRow(rows, "doc", "viewer", "admin") {
		t.Error("viewer should be satisfied by admin (transitive)")
	}
}

func TestComputeRelationClosure_MultipleTypes(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
		{
			Name: "org",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "member", ImpliedBy: []string{"owner"}},
			},
		},
		{
			Name: "repo",
			Relations: []schema.RelationDefinition{
				{Name: "admin", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "viewer", ImpliedBy: []string{"admin"}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// Check org closure
	if !hasClosureRow(rows, "org", "member", "owner") {
		t.Error("org.member should be satisfied by owner")
	}

	// Check repo closure
	if !hasClosureRow(rows, "repo", "viewer", "admin") {
		t.Error("repo.viewer should be satisfied by admin")
	}

	// Make sure they don't mix
	if hasClosureRow(rows, "org", "member", "admin") {
		t.Error("org.member should not be satisfied by repo's admin")
	}
}

func TestComputeRelationClosure_NoImpliedBy(t *testing.T) {
	// Relations with only direct subject types, no implied_by
	types := []schema.TypeDefinition{
		{
			Name: "repo",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "viewer", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// Each relation should only satisfy itself
	if !hasClosureRow(rows, "repo", "owner", "owner") {
		t.Error("owner should satisfy itself")
	}
	if !hasClosureRow(rows, "repo", "admin", "admin") {
		t.Error("admin should satisfy itself")
	}
	if !hasClosureRow(rows, "repo", "viewer", "viewer") {
		t.Error("viewer should satisfy itself")
	}

	// No cross-satisfaction
	if hasClosureRow(rows, "repo", "admin", "owner") {
		t.Error("admin should not be satisfied by owner")
	}

	if len(rows) != 3 {
		t.Errorf("expected 3 closure rows, got %d", len(rows))
	}
}

func TestComputeRelationClosure_Empty(t *testing.T) {
	var types []schema.TypeDefinition

	rows := schema.ComputeRelationClosure(types)

	if len(rows) != 0 {
		t.Errorf("expected 0 closure rows for empty types, got %d", len(rows))
	}
}

func TestComputeRelationClosure_TypeWithNoRelations(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
	}

	rows := schema.ComputeRelationClosure(types)

	if len(rows) != 0 {
		t.Errorf("expected 0 closure rows for type with no relations, got %d", len(rows))
	}
}

func TestComputeRelationClosure_ViaPath(t *testing.T) {
	// owner -> admin -> member -> viewer
	types := []schema.TypeDefinition{
		{
			Name: "org",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "member", ImpliedBy: []string{"admin"}},
				{Name: "viewer", ImpliedBy: []string{"member"}},
			},
		},
	}

	rows := schema.ComputeRelationClosure(types)

	// Check via_path for viewer <- owner (should be [viewer, member, admin, owner])
	for _, row := range rows {
		if row.ObjectType == "org" && row.Relation == "viewer" && row.SatisfyingRelation == "owner" {
			expected := []string{"viewer", "member", "admin", "owner"}
			if len(row.ViaPath) != len(expected) {
				t.Errorf("via_path length mismatch: expected %d, got %d", len(expected), len(row.ViaPath))
				return
			}
			for i, v := range expected {
				if row.ViaPath[i] != v {
					t.Errorf("via_path[%d] mismatch: expected %s, got %s", i, v, row.ViaPath[i])
				}
			}
			return
		}
	}
	t.Error("did not find closure row for viewer <- owner")
}

func TestClosureRow_Fields(t *testing.T) {
	row := schema.ClosureRow{
		ObjectType:         "repository",
		Relation:           "can_read",
		SatisfyingRelation: "owner",
		ViaPath:            []string{"can_read", "owner"},
	}

	if row.ObjectType != "repository" {
		t.Errorf("ObjectType = %q, want %q", row.ObjectType, "repository")
	}
	if row.Relation != "can_read" {
		t.Errorf("Relation = %q, want %q", row.Relation, "can_read")
	}
	if row.SatisfyingRelation != "owner" {
		t.Errorf("SatisfyingRelation = %q, want %q", row.SatisfyingRelation, "owner")
	}
	if len(row.ViaPath) != 2 {
		t.Errorf("ViaPath length = %d, want 2", len(row.ViaPath))
	}
}

// hasClosureRow checks if a closure row exists with the given parameters
func hasClosureRow(rows []schema.ClosureRow, objectType, relation, satisfying string) bool {
	for _, row := range rows {
		if row.ObjectType == objectType && row.Relation == relation && row.SatisfyingRelation == satisfying {
			return true
		}
	}
	return false
}
