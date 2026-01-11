// Package clientgen provides a public API for generating type-safe client code
// from authorization schemas.
//
// This package wraps the internal generator registry, providing a stable public
// interface for programmatic code generation. For CLI usage, see the melange
// command: `melange generate client --runtime go`.
//
// # Supported Runtimes
//
// Currently supported:
//   - "go" - Type-safe Go code with constants and constructors
//
// Registered but not yet implemented:
//   - "typescript" - TypeScript types and factory functions (stub)
//
// # Example Usage
//
//	types, _ := parser.ParseSchema("schema.fga")
//	files, err := clientgen.Generate("go", types, nil)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for filename, content := range files {
//	    os.WriteFile(filename, content, 0644)
//	}
package clientgen

import (
	"fmt"
	"io"

	"github.com/pthm/melange/internal/clientgen"
	_ "github.com/pthm/melange/internal/clientgen/go"         // Register Go generator
	_ "github.com/pthm/melange/internal/clientgen/typescript" // Register TypeScript generator (stub)
	"github.com/pthm/melange/pkg/schema"
)

// Config is an alias for the internal Config type.
// This allows users to configure code generation without importing internal packages.
type Config = clientgen.Config

// TypeDefinition is an alias for schema.TypeDefinition.
type TypeDefinition = schema.TypeDefinition

// DefaultConfig returns sensible defaults for code generation.
// Package: "authz", no relation filter (all relations), string IDs.
func DefaultConfig() *Config {
	return &Config{
		Package:        "authz",
		RelationFilter: "",
		IDType:         "string",
		Options:        make(map[string]any),
	}
}

// Generate produces client code for the specified runtime.
//
// Supported runtimes: "go"
//
// Returns a map of filename -> content. For single-file outputs (like Go),
// the map contains one entry. Multi-file outputs (like TypeScript) will
// contain multiple entries.
//
// Returns an error if the runtime is not registered or generation fails.
func Generate(runtime string, types []TypeDefinition, cfg *Config) (map[string][]byte, error) {
	gen := clientgen.Get(runtime)
	if gen == nil {
		return nil, fmt.Errorf("unknown runtime: %q (available: %v)", runtime, clientgen.List())
	}
	return gen.Generate(types, cfg)
}

// ListRuntimes returns all registered runtime names.
func ListRuntimes() []string {
	return clientgen.List()
}

// Registered returns true if a generator exists for the given runtime name.
func Registered(name string) bool {
	return clientgen.Registered(name)
}

// GenerateConfig is provided for backwards compatibility.
// New code should use Config instead.
//
// Deprecated: Use Config instead.
type GenerateConfig struct {
	// Package name for generated code. Default: "authz"
	Package string

	// RelationPrefixFilter is a prefix filter for relation names.
	// Only relations with this prefix will have constants generated.
	// If empty, all relations will be generated.
	RelationPrefixFilter string

	// IDType specifies the type to use for object IDs.
	// Default: "string"
	IDType string
}

// DefaultGenerateConfig returns sensible defaults for code generation.
//
// Deprecated: Use DefaultConfig instead.
func DefaultGenerateConfig() *GenerateConfig {
	return &GenerateConfig{
		Package:              "authz",
		RelationPrefixFilter: "",
		IDType:               "string",
	}
}

// GenerateGo writes type-safe Go code from a parsed OpenFGA schema.
//
// This function is provided for backwards compatibility. New code should
// use Generate("go", types, cfg) instead.
//
// Deprecated: Use Generate("go", types, cfg) instead.
func GenerateGo(w io.Writer, types []TypeDefinition, cfg *GenerateConfig) error {
	// Convert old config to new config
	var newCfg *Config
	if cfg != nil {
		newCfg = &Config{
			Package:        cfg.Package,
			RelationFilter: cfg.RelationPrefixFilter,
			IDType:         cfg.IDType,
		}
	}

	files, err := Generate("go", types, newCfg)
	if err != nil {
		return err
	}

	// Write the first (and only) file to the writer
	for _, content := range files {
		_, err := w.Write(content)
		return err
	}

	return nil
}
