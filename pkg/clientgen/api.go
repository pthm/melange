package clientgen

import (
	"io"

	gogen "github.com/pthm/melange/pkg/clientgen/go"
	"github.com/pthm/melange/pkg/schema"
)

// GenerateConfig is an alias for gogen.GenerateConfig.
// This allows users of the tooling package to configure code generation
// without importing the gogen package separately.
type GenerateConfig = gogen.GenerateConfig

// TypeDefinition is an alias for schema.TypeDefinition.
type TypeDefinition = schema.TypeDefinition

// DefaultGenerateConfig returns sensible defaults for code generation.
// Package: "authz", no relation filter (all relations), string IDs.
func DefaultGenerateConfig() *GenerateConfig {
	return gogen.DefaultGenerateConfig()
}

// GenerateGo writes type-safe Go code from a parsed OpenFGA schema.
// This is a convenience re-export of schema.GenerateGo for use in build tooling.
//
// Code generation enables compile-time checking of authorization logic by
// generating constants for object types and relations. Instead of error-prone
// string literals, use generated types:
//
//	// Before: fragile, typos caught at runtime
//	checker.Check(ctx, user, "can_raed", repo) // typo!
//
//	// After: type-safe, typos caught at compile time
//	checker.Check(ctx, user, authz.RelCanRead, repo)
//
// Generated code includes:
//   - ObjectType constants (TypeUser, TypeRepository, etc.)
//   - Relation constants (RelCanRead, RelOwner, etc.)
//   - Constructor functions (User(id), Repository(id), etc.)
//   - Wildcard constructors (AnyUser(), AnyRepository(), etc.)
//
// Typical workflow (run via go:generate or build script):
//
//	//go:generate go run scripts/generate-authz.go
//
//	types, _ := tooling.ParseSchema("schemas/schema.fga")
//	f, _ := os.Create("internal/authz/schema_gen.go")
//	defer f.Close()
//
//	tooling.GenerateGo(f, types, &tooling.GenerateConfig{
//	    Package: "authz",
//	    IDType:  "string", // or "int64" if using integer IDs
//	})
//
// The generated file should be committed to version control to enable
// compile-time validation across the team without requiring schema access.
func GenerateGo(w io.Writer, types []TypeDefinition, cfg *GenerateConfig) error {
	return gogen.GenerateGo(w, types, cfg)
}
