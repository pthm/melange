// Package python implements a stub Python client code generator.
//
// This generator is not yet implemented. It registers with the generator
// registry to provide helpful error messages when users attempt to generate
// Python code.
package python

import (
	"errors"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/pkg/schema"
)

func init() {
	clientgen.Register(&Generator{})
}

// Generator implements clientgen.Generator for Python.
// Currently a stub that returns "not implemented" errors.
type Generator struct{}

// Name returns "python" as the runtime identifier.
func (g *Generator) Name() string { return "python" }

// DefaultConfig returns default configuration for Python code generation.
func (g *Generator) DefaultConfig() *clientgen.Config {
	return &clientgen.Config{
		Package:        "authz",
		RelationFilter: "",
		IDType:         "str",
		Options:        make(map[string]any),
	}
}

// ErrNotImplemented is returned when attempting to generate Python code.
var ErrNotImplemented = errors.New("python generator not yet implemented")

// Generate returns an error as Python generation is not yet implemented.
//
// Future implementation will produce:
//   - schema.py: Type constants, relation constants, and factory functions
//   - __init__.py: Re-exports for clean imports
func (g *Generator) Generate(_ []schema.TypeDefinition, _ *clientgen.Config) (map[string][]byte, error) {
	return nil, ErrNotImplemented
}
