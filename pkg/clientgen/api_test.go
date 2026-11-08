package clientgen

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/schema"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Package != "authz" {
		t.Errorf("Package = %q, want %q", cfg.Package, "authz")
	}
	if cfg.RelationFilter != "" {
		t.Errorf("RelationFilter = %q, want empty", cfg.RelationFilter)
	}
	if cfg.IDType != "string" {
		t.Errorf("IDType = %q, want %q", cfg.IDType, "string")
	}
	if cfg.Options == nil {
		t.Error("Options should be initialized (non-nil map)")
	}
	if cfg.Exposure != nil || cfg.Relations != nil {
		t.Error("Exposure and Relations should be nil (the default exposure)")
	}
}

func TestListRuntimes(t *testing.T) {
	runtimes := ListRuntimes()

	if !slices.Contains(runtimes, "go") {
		t.Error("ListRuntimes should include 'go'")
	}
	if !slices.Contains(runtimes, "typescript") {
		t.Error("ListRuntimes should include 'typescript'")
	}
}

func TestRegistered(t *testing.T) {
	if !Registered("go") {
		t.Error("'go' should be registered")
	}
	if !Registered("typescript") {
		t.Error("'typescript' should be registered")
	}
	if Registered("python") {
		t.Error("'python' should not be registered")
	}
	if Registered("") {
		t.Error("empty string should not be registered")
	}
}

func TestGenerate(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
		{
			Name: "document",
			Relations: []schema.RelationDefinition{
				{Name: "viewer", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			},
		},
	}

	t.Run("go runtime produces output", func(t *testing.T) {
		files, err := Generate("go", types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}
		if len(files) == 0 {
			t.Error("Generate should return at least one file")
		}
		content, ok := files["schema_gen.go"]
		if !ok {
			t.Fatal("should produce schema_gen.go")
		}
		code := string(content)
		if !strings.Contains(code, "TypeUser") {
			t.Error("generated Go should contain TypeUser constant")
		}
		if !strings.Contains(code, "TypeDocument") {
			t.Error("generated Go should contain TypeDocument constant")
		}
		if !strings.Contains(code, "RelViewer") {
			t.Error("generated Go should contain RelViewer constant")
		}
	})

	t.Run("typescript runtime produces output", func(t *testing.T) {
		files, err := Generate("typescript", types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}
		if len(files) < 3 {
			t.Errorf("TypeScript should produce at least 3 files, got %d", len(files))
		}
		if _, ok := files["types.ts"]; !ok {
			t.Error("should produce types.ts")
		}
		if _, ok := files["schema.ts"]; !ok {
			t.Error("should produce schema.ts")
		}
		if _, ok := files["index.ts"]; !ok {
			t.Error("should produce index.ts")
		}
	})

	t.Run("unknown runtime returns error", func(t *testing.T) {
		_, err := Generate("python", types, nil)
		if err == nil {
			t.Fatal("should return error for unknown runtime")
		}
		if !strings.Contains(err.Error(), "unknown runtime") {
			t.Errorf("error should mention 'unknown runtime', got: %v", err)
		}
		if !strings.Contains(err.Error(), "python") {
			t.Errorf("error should mention the requested runtime name, got: %v", err)
		}
	})

	t.Run("custom config is forwarded", func(t *testing.T) {
		cfg := &Config{
			Package:        "mypkg",
			RelationFilter: "can_",
			IDType:         "int64",
		}
		files, err := Generate("go", types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}
		code := string(files["schema_gen.go"])
		if !strings.Contains(code, "package mypkg") {
			t.Error("should use custom package name")
		}
		// With filter "can_", "viewer" should be excluded, and so should its
		// accessible function name
		if strings.Contains(code, "RelViewer") {
			t.Error("should filter out relations not matching prefix")
		}
		if strings.Contains(code, "SQLAccessibleDocumentViewer") {
			t.Error("should filter out accessible function names of relations not matching prefix")
		}
	})
}

func TestDefaultGenerateConfig(t *testing.T) {
	cfg := DefaultGenerateConfig()

	if cfg.Package != "authz" {
		t.Errorf("Package = %q, want %q", cfg.Package, "authz")
	}
	if cfg.RelationPrefixFilter != "" {
		t.Errorf("RelationPrefixFilter = %q, want empty", cfg.RelationPrefixFilter)
	}
	if cfg.IDType != "string" {
		t.Errorf("IDType = %q, want %q", cfg.IDType, "string")
	}
}

func TestGenerateGo(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
		{
			Name: "document",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			},
		},
	}

	t.Run("writes to writer", func(t *testing.T) {
		var buf bytes.Buffer
		err := GenerateGo(&buf, types, nil)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}
		code := buf.String()
		if code == "" {
			t.Error("GenerateGo should write output")
		}
		if !strings.Contains(code, "TypeUser") {
			t.Error("should contain TypeUser")
		}
		// The deprecated config sets no exposure: the default, with every
		// accessible function.
		if !strings.Contains(code, `SQLAccessibleDocumentOwner = "accessible_document_owner"`) {
			t.Errorf("should contain the accessible function name:\n%s", code)
		}
	})

	t.Run("forwards config fields", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &GenerateConfig{
			Package:              "custom",
			RelationPrefixFilter: "",
			IDType:               "string",
		}
		err := GenerateGo(&buf, types, cfg)
		if err != nil {
			t.Fatalf("GenerateGo error: %v", err)
		}
		code := buf.String()
		if !strings.Contains(code, "package custom") {
			t.Error("should use custom package name")
		}
	})
}
