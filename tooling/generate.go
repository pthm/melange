package tooling

import (
	"io"

	"github.com/pthm/melange"
)

// GenerateConfig is an alias for melange.GenerateConfig.
// This allows users of the tooling package to configure code generation
// without importing the core melange package separately.
type GenerateConfig = melange.GenerateConfig

// DefaultGenerateConfig returns sensible defaults for code generation.
// Package: "authz", no relation filter (all relations), string IDs.
func DefaultGenerateConfig() *GenerateConfig {
	return melange.DefaultGenerateConfig()
}

// GenerateGo writes Go code for the types and relations from a parsed schema.
// This is a convenience re-export of melange.GenerateGo.
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
func GenerateGo(w io.Writer, types []melange.TypeDefinition, cfg *GenerateConfig) error {
	return melange.GenerateGo(w, types, cfg)
}
