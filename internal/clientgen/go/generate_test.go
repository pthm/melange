package gogen_test

import (
	"strings"
	"testing"

	"github.com/pthm/melange/internal/clientgen"
	gogen "github.com/pthm/melange/internal/clientgen/go"
	"github.com/pthm/melange/pkg/schema"
)

func TestGenerator_Interface(t *testing.T) {
	gen := &gogen.Generator{}

	t.Run("name returns go", func(t *testing.T) {
		if got := gen.Name(); got != "go" {
			t.Errorf("Name() = %q, want %q", got, "go")
		}
	})

	t.Run("default config has sensible values", func(t *testing.T) {
		cfg := gen.DefaultConfig()
		if cfg.Package != "authz" {
			t.Errorf("Package = %q, want %q", cfg.Package, "authz")
		}
		if cfg.IDType != "string" {
			t.Errorf("IDType = %q, want %q", cfg.IDType, "string")
		}
		if cfg.RelationFilter != "" {
			t.Errorf("RelationFilter = %q, want empty", cfg.RelationFilter)
		}
	})
}

func TestGenerator_Generate(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "user",
			Relations: []schema.RelationDefinition{
				{Name: "self", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			},
		},
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
				{Name: "can_read", ImpliedBy: []string{"owner"}},
			},
		},
	}

	gen := &gogen.Generator{}

	t.Run("returns single file map", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		if len(files) != 1 {
			t.Errorf("Generate returned %d files, want 1", len(files))
		}

		if _, ok := files["schema_gen.go"]; !ok {
			t.Error("Generate should return schema_gen.go file")
		}
	})

	t.Run("default config uses string ID", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])

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
		cfg := &clientgen.Config{
			Package: "authz",
			IDType:  "", // Empty should default to string
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])
		if !strings.Contains(code, "func User(id string)") {
			t.Error("empty IDType should default to string")
		}
	})

	t.Run("int64 IDType uses fmt.Sprint", func(t *testing.T) {
		cfg := &clientgen.Config{
			Package: "authz",
			IDType:  "int64",
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])

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
		cfg := &clientgen.Config{
			Package:        "authz",
			RelationFilter: "", // No filter
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])

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
		cfg := &clientgen.Config{
			Package:        "authz",
			RelationFilter: "can_",
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])

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
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])

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

	t.Run("uses correct melange import path", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema_gen.go"])
		if !strings.Contains(code, "github.com/pthm/melange/melange") {
			t.Error("should import github.com/pthm/melange/melange")
		}
	})
}

func TestRegistry_GoGeneratorRegistered(t *testing.T) {
	gen := clientgen.Get("go")
	if gen == nil {
		t.Fatal("Go generator should be registered")
	}

	if gen.Name() != "go" {
		t.Errorf("Name() = %q, want %q", gen.Name(), "go")
	}
}
