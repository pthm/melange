// Package clientgen provides a registry of language-specific client code generators.
//
// The generator interface supports pluggable, language-specific code generators
// that produce type-safe client code from authorization schemas. Generators return
// a file map to support languages that need multiple output files (e.g., TypeScript
// with separate types.ts, client.ts, index.ts).
//
// This is an internal package used by the melange CLI. For programmatic code
// generation, use pkg/clientgen which provides a stable public API.
package clientgen

import (
	"fmt"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/schema"
)

// Generator produces language-specific client code from a schema.
//
// Implementations should be registered via Register() in their init() function.
// The CLI uses the registry to dispatch generation based on the --runtime flag.
type Generator interface {
	// Name returns the runtime identifier ("go", "typescript").
	// This is used as the value for --runtime in the CLI.
	Name() string

	// Generate returns a map of filename -> content for all generated files.
	// For single-file outputs (like Go), returns a single entry.
	// For multi-file outputs (like TypeScript), returns multiple entries.
	//
	// The filenames are relative paths (e.g., "schema_gen.go" or "types.ts").
	// The caller is responsible for writing these to the appropriate location.
	Generate(types []schema.TypeDefinition, cfg *Config) (map[string][]byte, error)

	// DefaultConfig returns the default configuration for this generator.
	// This is used when the user doesn't provide explicit configuration.
	DefaultConfig() *Config
}

// Config holds language-agnostic generation options.
//
// Each generator may interpret these options differently based on
// language conventions. Language-specific options can be passed via
// the Options map.
type Config struct {
	// Package is the package/module name for generated code.
	// For Go: package name (e.g., "authz")
	// For TypeScript: not typically used (uses exports)
	Package string

	// Exposure is the SQL API exposure the database is migrated with (the
	// sql section of melange.yaml). Accessible function names are emitted
	// for the functions it installs for its exposed relations, and unless
	// Relations is set its relations get constants. nil is the default
	// exposure: every relation and every function family.
	Exposure *compiler.Exposure

	// Relations selects the relations that get constants, with the pattern
	// syntax of Exposure.Relations. nil follows Exposure. Accessible function
	// names are emitted only for relations both select.
	Relations *compiler.RelationPatterns

	// RelationFilter is a relation name prefix applied after Exposure and
	// Relations, to relation constants and accessible function names alike.
	// If empty, it selects every relation.
	// Example: "can_" generates only permission relations, omitting roles.
	// It is kept for compatibility; prefer Relations with an include pattern
	// such as "can_*".
	RelationFilter string

	// IDType specifies the type to use for object IDs in constructors.
	// For Go: "string", "int64", "uuid.UUID", etc.
	// Other languages may ignore this or use their own type mappings.
	IDType string

	// Version is the melange version used for generation.
	// Included in the generated file header for traceability.
	// Example: "v0.4.3"
	Version string

	// SourcePath is the path to the source schema file.
	// Included in the generated file header for traceability.
	// Example: "schemas/schema.fga"
	SourcePath string

	// Options holds language-specific configuration.
	// Each generator documents its supported options.
	Options map[string]any
}

// registry maps runtime names to generators.
var registry = make(map[string]Generator)

// Register adds a generator to the global registry.
// Generators should call this from their init() function.
//
// Panics if a generator with the same name is already registered.
func Register(g Generator) {
	name := g.Name()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("clientgen: generator %q already registered", name))
	}
	registry[name] = g
}

// Get returns the generator for the given runtime name.
// Returns nil if no generator is registered for that name.
func Get(name string) Generator {
	return registry[name]
}

// List returns all registered generator names.
func List() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// Registered returns true if a generator is registered for the given name.
func Registered(name string) bool {
	_, ok := registry[name]
	return ok
}
