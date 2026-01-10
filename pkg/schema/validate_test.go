package schema_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	gogen "github.com/pthm/melange/pkg/clientgen/go"
	"github.com/pthm/melange/pkg/schema"
)

func TestDetectCycles_ImpliedBy(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "resource",
			Relations: []schema.RelationDefinition{
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "owner", ImpliedBy: []string{"admin"}}, // cycle!
			},
		},
	}

	err := schema.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for implied-by cycle")
	}
	if !schema.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true, got false")
	}
	if !strings.Contains(err.Error(), "implied-by cycle") {
		t.Errorf("error should mention 'implied-by cycle', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "resource") {
		t.Errorf("error should mention type name 'resource', got: %s", err.Error())
	}
}

func TestDetectCycles_ImpliedByThreeWay(t *testing.T) {
	// A → B → C → A
	types := []schema.TypeDefinition{
		{
			Name: "resource",
			Relations: []schema.RelationDefinition{
				{Name: "a", ImpliedBy: []string{"c"}},
				{Name: "b", ImpliedBy: []string{"a"}},
				{Name: "c", ImpliedBy: []string{"b"}}, // completes cycle
			},
		},
	}

	err := schema.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for three-way implied-by cycle")
	}
	if !schema.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true")
	}
}

func TestDetectCycles_Parent(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "organization",
			Relations: []schema.RelationDefinition{
				{Name: "repo", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "repository"}}},
				{Name: "can_read", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "repo"}}},
			},
		},
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "org", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "organization"}}},
				{Name: "can_read", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "org"}}},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Fatalf("expected no error for same-relation parent recursion, got: %v", err)
	}
}

func TestDetectCycles_ParentDifferentRelations(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "organization",
			Relations: []schema.RelationDefinition{
				{Name: "repo", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "repository"}}},
				{Name: "can_read", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_write", LinkingRelation: "repo"}}},
			},
		},
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "org", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "organization"}}},
				{Name: "can_write", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "org"}}},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for parent relation cycle with differing relations")
	}
	if !schema.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true")
	}
	if !strings.Contains(err.Error(), "parent") {
		t.Errorf("error should mention 'parent', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention 'cycle', got: %s", err.Error())
	}
}

func TestDetectCycles_ValidDAG(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "resource",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "member", ImpliedBy: []string{"admin"}},
				{Name: "viewer", ImpliedBy: []string{"member"}},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for valid DAG, got: %v", err)
	}
}

func TestDetectCycles_DisconnectedGraphs(t *testing.T) {
	// Multiple types with no cycles
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
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "viewer", ImpliedBy: []string{"owner"}},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for disconnected graphs, got: %v", err)
	}
}

func TestDetectCycles_ValidParentChain(t *testing.T) {
	// Valid: org → repo → issue (no cycle)
	types := []schema.TypeDefinition{
		{Name: "user"},
		{
			Name: "organization",
			Relations: []schema.RelationDefinition{
				{Name: "member", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "can_read", ImpliedBy: []string{"member"}},
			},
		},
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "org", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "organization"}}},
				{Name: "can_read", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "org"}}},
			},
		},
		{
			Name: "issue",
			Relations: []schema.RelationDefinition{
				{Name: "repo", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "repository"}}},
				{Name: "can_read", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "repo"}}},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for valid parent chain, got: %v", err)
	}
}

func TestDetectCycles_EmptySchema(t *testing.T) {
	var types []schema.TypeDefinition

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for empty schema, got: %v", err)
	}
}

func TestDetectCycles_TypeWithNoRelations(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
		{Name: "team"},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for types with no relations, got: %v", err)
	}
}

func TestDetectCycles_SelfLoop(t *testing.T) {
	// admin implies admin (self-loop)
	types := []schema.TypeDefinition{
		{
			Name: "resource",
			Relations: []schema.RelationDefinition{
				{Name: "admin", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}, ImpliedBy: []string{"admin"}},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for self-loop")
	}
	if !schema.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true")
	}
}

func TestDetectCycles_MultipleImpliers(t *testing.T) {
	// viewer implied by multiple relations, no cycle
	types := []schema.TypeDefinition{
		{
			Name: "resource",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "viewer", ImpliedBy: []string{"owner", "admin"}}, // diamond, not a cycle
			},
		},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for diamond pattern, got: %v", err)
	}
}

func TestGenerateGo_RejectsCyclicSchema(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "resource",
			Relations: []schema.RelationDefinition{
				{Name: "a", ImpliedBy: []string{"b"}},
				{Name: "b", ImpliedBy: []string{"a"}},
			},
		},
	}

	var buf bytes.Buffer
	err := gogen.GenerateGo(&buf, types, nil)
	if err == nil {
		t.Fatal("expected error for cyclic schema")
	}
	if !schema.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true, got: %v", err)
	}
}

func TestIsCyclicSchemaErr(t *testing.T) {
	t.Run("wrapped error", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", schema.ErrCyclicSchema)
		if !schema.IsCyclicSchemaErr(err) {
			t.Error("IsCyclicSchemaErr should return true for wrapped ErrCyclicSchema")
		}
	})

	t.Run("other error", func(t *testing.T) {
		if schema.IsCyclicSchemaErr(errors.New("other error")) {
			t.Error("IsCyclicSchemaErr should return false for other errors")
		}
	})

	t.Run("nil error", func(t *testing.T) {
		if schema.IsCyclicSchemaErr(nil) {
			t.Error("IsCyclicSchemaErr should return false for nil")
		}
	})
}

func TestDetectCycles_ComplexValidSchema(t *testing.T) {
	// A realistic schema with multiple types and inheritance
	types := []schema.TypeDefinition{
		{Name: "user"},
		{
			Name: "organization",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "admin", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}, ImpliedBy: []string{"owner"}},
				{Name: "member", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}, ImpliedBy: []string{"admin"}},
				{Name: "can_read", ImpliedBy: []string{"member"}},
				{Name: "can_write", ImpliedBy: []string{"admin"}},
				{Name: "can_delete", ImpliedBy: []string{"owner"}},
			},
		},
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "org", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "organization"}}},
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "collaborator", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{
					Name:            "can_read",
					ImpliedBy:       []string{"collaborator", "owner"},
					ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "org"}},
				},
				{
					Name:            "can_write",
					ImpliedBy:       []string{"owner"},
					ParentRelations: []schema.ParentRelationCheck{{Relation: "can_write", LinkingRelation: "org"}},
				},
			},
		},
		{
			Name: "issue",
			Relations: []schema.RelationDefinition{
				{Name: "repo", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "repository"}}},
				{Name: "author", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "can_read", ParentRelations: []schema.ParentRelationCheck{{Relation: "can_read", LinkingRelation: "repo"}}},
				{
					Name:            "can_write",
					ImpliedBy:       []string{"author"},
					ParentRelations: []schema.ParentRelationCheck{{Relation: "can_write", LinkingRelation: "repo"}},
				},
			},
		},
	}

	err := schema.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for complex valid schema, got: %v", err)
	}
}
