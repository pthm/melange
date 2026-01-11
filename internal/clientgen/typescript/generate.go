// Package typescript implements a stub TypeScript client code generator.
//
// This generator is not yet implemented. It registers with the generator
// registry to provide helpful error messages when users attempt to generate
// TypeScript code.
package typescript

import (
	"errors"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/pkg/schema"
)

func init() {
	clientgen.Register(&Generator{})
}

// Generator implements clientgen.Generator for TypeScript.
// Currently a stub that returns "not implemented" errors.
type Generator struct{}

// Name returns "typescript" as the runtime identifier.
func (g *Generator) Name() string { return "typescript" }

// DefaultConfig returns default configuration for TypeScript code generation.
func (g *Generator) DefaultConfig() *clientgen.Config {
	return &clientgen.Config{
		Package:        "",
		RelationFilter: "",
		IDType:         "string",
		Options:        make(map[string]any),
	}
}

// ErrNotImplemented is returned when attempting to generate TypeScript code.
var ErrNotImplemented = errors.New("typescript generator not yet implemented")

// Generate returns an error as TypeScript generation is not yet implemented.
//
// Future implementation will produce:
//   - types.ts: Type constants and TypeScript types
//   - schema.ts: Factory functions for creating objects
//   - index.ts: Re-exports for clean imports
func (g *Generator) Generate(_ []schema.TypeDefinition, _ *clientgen.Config) (map[string][]byte, error) {
	return nil, ErrNotImplemented
}
