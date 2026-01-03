package melange_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pthm/melange"
)

func TestDetectCycles_ImpliedBy(t *testing.T) {
	types := []melange.TypeDefinition{
		{
			Name: "resource",
			Relations: []melange.RelationDefinition{
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "owner", ImpliedBy: []string{"admin"}}, // cycle!
			},
		},
	}

	err := melange.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for implied-by cycle")
	}
	if !melange.IsCyclicSchemaErr(err) {
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
	types := []melange.TypeDefinition{
		{
			Name: "resource",
			Relations: []melange.RelationDefinition{
				{Name: "a", ImpliedBy: []string{"c"}},
				{Name: "b", ImpliedBy: []string{"a"}},
				{Name: "c", ImpliedBy: []string{"b"}}, // completes cycle
			},
		},
	}

	err := melange.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for three-way implied-by cycle")
	}
	if !melange.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true")
	}
}

func TestDetectCycles_Parent(t *testing.T) {
	types := []melange.TypeDefinition{
		{
			Name: "organization",
			Relations: []melange.RelationDefinition{
				{Name: "repo", SubjectTypes: []string{"repository"}},
				{Name: "can_read", ParentRelation: "can_read", ParentType: "repo"},
			},
		},
		{
			Name: "repository",
			Relations: []melange.RelationDefinition{
				{Name: "org", SubjectTypes: []string{"organization"}},
				{Name: "can_read", ParentRelation: "can_read", ParentType: "org"},
			},
		},
	}

	err := melange.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for parent relation cycle")
	}
	if !melange.IsCyclicSchemaErr(err) {
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
	types := []melange.TypeDefinition{
		{
			Name: "resource",
			Relations: []melange.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "member", ImpliedBy: []string{"admin"}},
				{Name: "viewer", ImpliedBy: []string{"member"}},
			},
		},
	}

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for valid DAG, got: %v", err)
	}
}

func TestDetectCycles_DisconnectedGraphs(t *testing.T) {
	// Multiple types with no cycles
	types := []melange.TypeDefinition{
		{Name: "user"},
		{
			Name: "org",
			Relations: []melange.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "member", ImpliedBy: []string{"owner"}},
			},
		},
		{
			Name: "repo",
			Relations: []melange.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "viewer", ImpliedBy: []string{"owner"}},
			},
		},
	}

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for disconnected graphs, got: %v", err)
	}
}

func TestDetectCycles_ValidParentChain(t *testing.T) {
	// Valid: org → repo → issue (no cycle)
	types := []melange.TypeDefinition{
		{Name: "user"},
		{
			Name: "organization",
			Relations: []melange.RelationDefinition{
				{Name: "member", SubjectTypes: []string{"user"}},
				{Name: "can_read", ImpliedBy: []string{"member"}},
			},
		},
		{
			Name: "repository",
			Relations: []melange.RelationDefinition{
				{Name: "org", SubjectTypes: []string{"organization"}},
				{Name: "can_read", ParentRelation: "can_read", ParentType: "org"},
			},
		},
		{
			Name: "issue",
			Relations: []melange.RelationDefinition{
				{Name: "repo", SubjectTypes: []string{"repository"}},
				{Name: "can_read", ParentRelation: "can_read", ParentType: "repo"},
			},
		},
	}

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for valid parent chain, got: %v", err)
	}
}

func TestDetectCycles_EmptySchema(t *testing.T) {
	var types []melange.TypeDefinition

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for empty schema, got: %v", err)
	}
}

func TestDetectCycles_TypeWithNoRelations(t *testing.T) {
	types := []melange.TypeDefinition{
		{Name: "user"},
		{Name: "team"},
	}

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for types with no relations, got: %v", err)
	}
}

func TestDetectCycles_SelfLoop(t *testing.T) {
	// admin implies admin (self-loop)
	types := []melange.TypeDefinition{
		{
			Name: "resource",
			Relations: []melange.RelationDefinition{
				{Name: "admin", SubjectTypes: []string{"user"}, ImpliedBy: []string{"admin"}},
			},
		},
	}

	err := melange.DetectCycles(types)
	if err == nil {
		t.Fatal("expected error for self-loop")
	}
	if !melange.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true")
	}
}

func TestDetectCycles_MultipleImpliers(t *testing.T) {
	// viewer implied by multiple relations, no cycle
	types := []melange.TypeDefinition{
		{
			Name: "resource",
			Relations: []melange.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "admin", SubjectTypes: []string{"user"}},
				{Name: "viewer", ImpliedBy: []string{"owner", "admin"}}, // diamond, not a cycle
			},
		},
	}

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for diamond pattern, got: %v", err)
	}
}

func TestGenerateGo_RejectsCyclicSchema(t *testing.T) {
	types := []melange.TypeDefinition{
		{
			Name: "resource",
			Relations: []melange.RelationDefinition{
				{Name: "a", ImpliedBy: []string{"b"}},
				{Name: "b", ImpliedBy: []string{"a"}},
			},
		},
	}

	var buf bytes.Buffer
	err := melange.GenerateGo(&buf, types, nil)
	if err == nil {
		t.Fatal("expected error for cyclic schema")
	}
	if !melange.IsCyclicSchemaErr(err) {
		t.Errorf("expected IsCyclicSchemaErr to return true, got: %v", err)
	}
}

func TestIsCyclicSchemaErr(t *testing.T) {
	t.Run("wrapped error", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", melange.ErrCyclicSchema)
		if !melange.IsCyclicSchemaErr(err) {
			t.Error("IsCyclicSchemaErr should return true for wrapped ErrCyclicSchema")
		}
	})

	t.Run("other error", func(t *testing.T) {
		if melange.IsCyclicSchemaErr(errors.New("other error")) {
			t.Error("IsCyclicSchemaErr should return false for other errors")
		}
	})

	t.Run("nil error", func(t *testing.T) {
		if melange.IsCyclicSchemaErr(nil) {
			t.Error("IsCyclicSchemaErr should return false for nil")
		}
	})
}

func TestDetectCycles_ComplexValidSchema(t *testing.T) {
	// A realistic schema with multiple types and inheritance
	types := []melange.TypeDefinition{
		{Name: "user"},
		{
			Name: "organization",
			Relations: []melange.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "admin", SubjectTypes: []string{"user"}, ImpliedBy: []string{"owner"}},
				{Name: "member", SubjectTypes: []string{"user"}, ImpliedBy: []string{"admin"}},
				{Name: "can_read", ImpliedBy: []string{"member"}},
				{Name: "can_write", ImpliedBy: []string{"admin"}},
				{Name: "can_delete", ImpliedBy: []string{"owner"}},
			},
		},
		{
			Name: "repository",
			Relations: []melange.RelationDefinition{
				{Name: "org", SubjectTypes: []string{"organization"}},
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "collaborator", SubjectTypes: []string{"user"}},
				{Name: "can_read", ParentRelation: "can_read", ParentType: "org", ImpliedBy: []string{"collaborator", "owner"}},
				{Name: "can_write", ParentRelation: "can_write", ParentType: "org", ImpliedBy: []string{"owner"}},
			},
		},
		{
			Name: "issue",
			Relations: []melange.RelationDefinition{
				{Name: "repo", SubjectTypes: []string{"repository"}},
				{Name: "author", SubjectTypes: []string{"user"}},
				{Name: "can_read", ParentRelation: "can_read", ParentType: "repo"},
				{Name: "can_write", ParentRelation: "can_write", ParentType: "repo", ImpliedBy: []string{"author"}},
			},
		},
	}

	err := melange.DetectCycles(types)
	if err != nil {
		t.Errorf("expected no error for complex valid schema, got: %v", err)
	}
}
