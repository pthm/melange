package tooling

import (
	"io"

	"github.com/pthm/melange/schema"
)

// GenerateConfig is an alias for schema.GenerateConfig.
// This allows users of the tooling package to configure code generation
// without importing the schema package separately.
type GenerateConfig = schema.GenerateConfig

// DefaultGenerateConfig returns sensible defaults for code generation.
// Package: "authz", no relation filter (all relations), string IDs.
func DefaultGenerateConfig() *GenerateConfig {
	return schema.DefaultGenerateConfig()
}

// GenerateGo writes Go code for the types and relations from a parsed schema.
// This is a convenience re-export of schema.GenerateGo.
//
// The generated code includes:
//   - ObjectType constants (TypeUser, TypeRepository, etc.)
//   - Relation constants (RelCanRead, RelOwner, etc.)
//   - Constructor functions (User(id), Repository(id), etc.)
//   - Wildcard constructors (AnyUser(), AnyRepository(), etc.)
//
// Example:
//
//	types, _ := tooling.ParseSchema("schemas/schema.fga")
//	f, _ := os.Create("internal/authz/schema_gen.go")
//	tooling.GenerateGo(f, types, &tooling.GenerateConfig{
//	    Package: "authz",
//	    IDType:  "int64",
//	})
func GenerateGo(w io.Writer, types []schema.TypeDefinition, cfg *GenerateConfig) error {
	return schema.GenerateGo(w, types, cfg)
}
