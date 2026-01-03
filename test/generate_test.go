package melange_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pthm/melange"
)

func TestGenerateGo_Defaults(t *testing.T) {
	types := []melange.TypeDefinition{
		{
			Name: "user",
			Relations: []melange.RelationDefinition{
				{Name: "self", SubjectTypes: []string{"user"}},
			},
		},
		{
			Name: "repository",
			Relations: []melange.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "can_read", ImpliedBy: []string{"owner"}},
			},
		},
	}

	t.Run("default config uses string ID", func(t *testing.T) {
		var buf bytes.Buffer
		err := melange.GenerateGo(&buf, types, nil)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}

		code := buf.String()

		// Should have string ID type in constructor
		if !strings.Contains(code, "func User(id string)") {
			t.Error("default config should use string ID type")
		}

		// Should NOT import fmt (not needed for string IDs)
		if strings.Contains(code, "\"fmt\"") {
			t.Error("default config should not import fmt")
		}
	})

	t.Run("empty IDType defaults to string", func(t *testing.T) {
		cfg := &melange.GenerateConfig{
			Package: "authz",
			IDType:  "", // Empty should default to string
		}

		var buf bytes.Buffer
		err := melange.GenerateGo(&buf, types, cfg)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}

		code := buf.String()
		if !strings.Contains(code, "func User(id string)") {
			t.Error("empty IDType should default to string")
		}
	})

	t.Run("int64 IDType uses fmt.Sprint", func(t *testing.T) {
		cfg := &melange.GenerateConfig{
			Package: "authz",
			IDType:  "int64",
		}

		var buf bytes.Buffer
		err := melange.GenerateGo(&buf, types, cfg)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}

		code := buf.String()

		// Should have int64 ID type
		if !strings.Contains(code, "func User(id int64)") {
			t.Error("should use int64 ID type")
		}

		// Should import fmt for conversion
		if !strings.Contains(code, "\"fmt\"") {
			t.Error("int64 IDType should import fmt")
		}

		// Should use fmt.Sprint
		if !strings.Contains(code, "fmt.Sprint(id)") {
			t.Error("int64 IDType should use fmt.Sprint")
		}
	})

	t.Run("generates all relations by default", func(t *testing.T) {
		cfg := &melange.GenerateConfig{
			Package:              "authz",
			RelationPrefixFilter: "", // No filter
		}

		var buf bytes.Buffer
		err := melange.GenerateGo(&buf, types, cfg)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}

		code := buf.String()

		// Should have both relations
		if !strings.Contains(code, "RelOwner") {
			t.Error("should generate RelOwner without prefix filter")
		}
		if !strings.Contains(code, "RelCanRead") {
			t.Error("should generate RelCanRead without prefix filter")
		}
		if !strings.Contains(code, "RelSelf") {
			t.Error("should generate RelSelf without prefix filter")
		}
	})

	t.Run("prefix filter limits relations", func(t *testing.T) {
		cfg := &melange.GenerateConfig{
			Package:              "authz",
			RelationPrefixFilter: "can_",
		}

		var buf bytes.Buffer
		err := melange.GenerateGo(&buf, types, cfg)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}

		code := buf.String()

		// Should only have can_* relations
		if !strings.Contains(code, "RelCanRead") {
			t.Error("should generate RelCanRead with can_ prefix filter")
		}
		if strings.Contains(code, "RelOwner") {
			t.Error("should NOT generate RelOwner with can_ prefix filter")
		}
		if strings.Contains(code, "RelSelf") {
			t.Error("should NOT generate RelSelf with can_ prefix filter")
		}
	})

	t.Run("generates wildcard constructors", func(t *testing.T) {
		var buf bytes.Buffer
		err := melange.GenerateGo(&buf, types, nil)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}

		code := buf.String()

		if !strings.Contains(code, "func AnyUser()") {
			t.Error("should generate AnyUser wildcard constructor")
		}
		if !strings.Contains(code, "func AnyRepository()") {
			t.Error("should generate AnyRepository wildcard constructor")
		}
		if !strings.Contains(code, "ID: \"*\"") {
			t.Error("wildcard constructors should use * ID")
		}
	})
}
