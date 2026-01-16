package typescript_test

import (
	"strings"
	"testing"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/clientgen/typescript"
	"github.com/pthm/melange/pkg/schema"
)

func TestGenerator_Interface(t *testing.T) {
	gen := &typescript.Generator{}

	t.Run("name returns typescript", func(t *testing.T) {
		if got := gen.Name(); got != "typescript" {
			t.Errorf("Name() = %q, want %q", got, "typescript")
		}
	})

	t.Run("default config has sensible values", func(t *testing.T) {
		cfg := gen.DefaultConfig()
		if cfg.Package != "" {
			t.Errorf("Package = %q, want empty (not used for TypeScript)", cfg.Package)
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

	gen := &typescript.Generator{}

	t.Run("returns multi-file map", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		expectedFiles := []string{"types.ts", "schema.ts", "index.ts"}
		if len(files) != len(expectedFiles) {
			t.Errorf("Generate returned %d files, want %d", len(files), len(expectedFiles))
		}

		for _, filename := range expectedFiles {
			if _, ok := files[filename]; !ok {
				t.Errorf("Generate should return %s file", filename)
			}
		}
	})

	t.Run("types.ts contains ObjectTypes constant", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "export const ObjectTypes = {") {
			t.Error("types.ts should export ObjectTypes constant")
		}

		if !strings.Contains(code, "User: \"user\"") {
			t.Error("types.ts should contain User type constant")
		}

		if !strings.Contains(code, "Repository: \"repository\"") {
			t.Error("types.ts should contain Repository type constant")
		}

		if !strings.Contains(code, "} as const;") {
			t.Error("types.ts should use 'as const' for type safety")
		}
	})

	t.Run("types.ts contains Relations constant", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "export const Relations = {") {
			t.Error("types.ts should export Relations constant")
		}

		if !strings.Contains(code, "Self: \"self\"") {
			t.Error("types.ts should contain Self relation constant")
		}

		if !strings.Contains(code, "Owner: \"owner\"") {
			t.Error("types.ts should contain Owner relation constant")
		}

		if !strings.Contains(code, "CanRead: \"can_read\"") {
			t.Error("types.ts should contain CanRead relation constant")
		}
	})

	t.Run("types.ts contains union types", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "export type ObjectType = (typeof ObjectTypes)[keyof typeof ObjectTypes];") {
			t.Error("types.ts should export ObjectType union type")
		}

		if !strings.Contains(code, "export type Relation = (typeof Relations)[keyof typeof Relations];") {
			t.Error("types.ts should export Relation union type")
		}
	})

	t.Run("types.ts imports MelangeObject", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "import type { MelangeObject } from '@pthm/melange';") {
			t.Error("types.ts should import MelangeObject from @pthm/melange")
		}
	})

	t.Run("schema.ts contains factory functions", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema.ts"])

		if !strings.Contains(code, "export function user(id: string): MelangeObject {") {
			t.Error("schema.ts should export user factory function")
		}

		if !strings.Contains(code, "export function repository(id: string): MelangeObject {") {
			t.Error("schema.ts should export repository factory function")
		}

		if !strings.Contains(code, "return { type: ObjectTypes.User, id };") {
			t.Error("schema.ts should use ObjectTypes.User in user function")
		}

		if !strings.Contains(code, "return { type: ObjectTypes.Repository, id };") {
			t.Error("schema.ts should use ObjectTypes.Repository in repository function")
		}
	})

	t.Run("schema.ts contains wildcard constructors", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema.ts"])

		if !strings.Contains(code, "export function anyUser(): MelangeObject {") {
			t.Error("schema.ts should export anyUser wildcard constructor")
		}

		if !strings.Contains(code, "export function anyRepository(): MelangeObject {") {
			t.Error("schema.ts should export anyRepository wildcard constructor")
		}

		if !strings.Contains(code, "id: '*'") {
			t.Error("schema.ts wildcard constructors should use '*' as id")
		}
	})

	t.Run("schema.ts imports from types.ts", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["schema.ts"])

		if !strings.Contains(code, "import type { MelangeObject } from '@pthm/melange';") {
			t.Error("schema.ts should import MelangeObject from @pthm/melange")
		}

		if !strings.Contains(code, "import { ObjectTypes } from './types.js';") {
			t.Error("schema.ts should import ObjectTypes from types.ts")
		}
	})

	t.Run("index.ts re-exports types and functions", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["index.ts"])

		if !strings.Contains(code, "export { ObjectTypes, Relations } from './types.js';") {
			t.Error("index.ts should re-export ObjectTypes and Relations")
		}

		if !strings.Contains(code, "export type { ObjectType, Relation } from './types.js';") {
			t.Error("index.ts should re-export ObjectType and Relation types")
		}

		if !strings.Contains(code, "export * from './schema.js';") {
			t.Error("index.ts should re-export all from schema.ts")
		}
	})

	t.Run("generates all relations by default", func(t *testing.T) {
		cfg := &clientgen.Config{
			RelationFilter: "", // No filter
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "Owner: \"owner\"") {
			t.Error("should generate Owner without prefix filter")
		}
		if !strings.Contains(code, "CanRead: \"can_read\"") {
			t.Error("should generate CanRead without prefix filter")
		}
		if !strings.Contains(code, "Self: \"self\"") {
			t.Error("should generate Self without prefix filter")
		}
	})

	t.Run("prefix filter limits relations", func(t *testing.T) {
		cfg := &clientgen.Config{
			RelationFilter: "can_",
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "CanRead: \"can_read\"") {
			t.Error("should generate CanRead with can_ prefix filter")
		}
		if strings.Contains(code, "Owner: \"owner\"") {
			t.Error("should NOT generate Owner with can_ prefix filter")
		}
		if strings.Contains(code, "Self: \"self\"") {
			t.Error("should NOT generate Self with can_ prefix filter")
		}
	})
}

func TestGenerator_VersionHeader(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
	}

	gen := &typescript.Generator{}

	t.Run("includes version and source in header", func(t *testing.T) {
		cfg := &clientgen.Config{
			Version:    "v0.5.1",
			SourcePath: "schemas/schema.fga",
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if !strings.Contains(code, "melange version: v0.5.1") {
			t.Error("types.ts should include melange version in header")
		}
		if !strings.Contains(code, "source: schemas/schema.fga") {
			t.Error("types.ts should include source path in header")
		}
	})

	t.Run("omits version/source when empty", func(t *testing.T) {
		cfg := &clientgen.Config{
			Version:    "",
			SourcePath: "",
		}

		files, err := gen.Generate(types, cfg)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		code := string(files["types.ts"])

		if strings.Contains(code, "melange version:") {
			t.Error("types.ts should not include melange version when empty")
		}
		if strings.Contains(code, "source:") {
			t.Error("types.ts should not include source when empty")
		}
	})
}

func TestGenerator_NamingConventions(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "pull_request",
			Relations: []schema.RelationDefinition{
				{Name: "can_review", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			},
		},
	}

	gen := &typescript.Generator{}

	t.Run("converts snake_case to PascalCase for constants", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		typesCode := string(files["types.ts"])

		if !strings.Contains(typesCode, "PullRequest: \"pull_request\"") {
			t.Error("should convert pull_request to PullRequest for ObjectTypes")
		}

		if !strings.Contains(typesCode, "CanReview: \"can_review\"") {
			t.Error("should convert can_review to CanReview for Relations")
		}
	})

	t.Run("converts snake_case to camelCase for functions", func(t *testing.T) {
		files, err := gen.Generate(types, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		schemaCode := string(files["schema.ts"])

		if !strings.Contains(schemaCode, "export function pullRequest(id: string)") {
			t.Error("should convert pull_request to pullRequest for factory function")
		}

		if !strings.Contains(schemaCode, "export function anyPullRequest()") {
			t.Error("should convert pull_request to anyPullRequest for wildcard function")
		}
	})
}

func TestGenerator_EmptySchema(t *testing.T) {
	gen := &typescript.Generator{}

	t.Run("handles empty schema", func(t *testing.T) {
		files, err := gen.Generate([]schema.TypeDefinition{}, nil)
		if err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		if len(files) != 3 {
			t.Errorf("should generate 3 files even for empty schema, got %d", len(files))
		}

		typesCode := string(files["types.ts"])
		if !strings.Contains(typesCode, "export const ObjectTypes = {") {
			t.Error("should generate empty ObjectTypes constant")
		}

		schemaCode := string(files["schema.ts"])
		if !strings.Contains(schemaCode, "import { ObjectTypes } from './types.js';") {
			t.Error("should generate valid schema.ts even with no types")
		}
	})
}

func TestRegistry_TypeScriptGeneratorRegistered(t *testing.T) {
	gen := clientgen.Get("typescript")
	if gen == nil {
		t.Fatal("TypeScript generator should be registered")
	}

	if gen.Name() != "typescript" {
		t.Errorf("Name() = %q, want %q", gen.Name(), "typescript")
	}
}
