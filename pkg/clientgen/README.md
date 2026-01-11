# clientgen

Package `clientgen` provides a public API for generating type-safe client code from authorization schemas.

This package wraps the internal generator registry, providing a stable public interface for programmatic code generation. For CLI usage, see the melange command: `melange generate client --runtime go`.

## Package Responsibilities

- Generate type-safe constants for object types and relations
- Create constructor functions for `melange.Object` values
- Support multiple language runtimes (Go, TypeScript)
- Provide configurable code generation options

## Public API

### Functions

```go
// Generate produces client code for the specified runtime.
// Returns a map of filename -> content.
func Generate(runtime string, types []TypeDefinition, cfg *Config) (map[string][]byte, error)

// ListRuntimes returns all registered runtime names.
func ListRuntimes() []string

// Registered returns true if a generator exists for the given runtime name.
func Registered(name string) bool

// DefaultConfig returns sensible defaults for code generation.
func DefaultConfig() *Config
```

### Types

```go
// Config controls code generation behavior.
type Config struct {
    Package        string         // Package name for generated code
    RelationFilter string         // Prefix filter for relations
    IDType         string         // Type for object IDs (default: "string")
    Options        map[string]any // Runtime-specific options
}
```

## Supported Runtimes

| Runtime | Status | Output |
|---------|--------|--------|
| `go` | Supported | Type-safe Go code with constants and constructors |
| `typescript` | Stub | TypeScript types and factory functions (planned) |

## Usage Examples

### Generate Go Client

```go
import (
    "github.com/pthm/melange/pkg/parser"
    "github.com/pthm/melange/pkg/clientgen"
)

// Parse schema
types, err := parser.ParseSchema("schema.fga")
if err != nil {
    log.Fatal(err)
}

// Generate with defaults
files, err := clientgen.Generate("go", types, nil)
if err != nil {
    log.Fatal(err)
}

// Write generated files
for filename, content := range files {
    if err := os.WriteFile(filename, content, 0644); err != nil {
        log.Fatal(err)
    }
}
```

### Custom Configuration

```go
cfg := &clientgen.Config{
    Package:        "authz",      // Package name
    RelationFilter: "can_",       // Only generate can_* relations
    IDType:         "string",     // ID type
}

files, err := clientgen.Generate("go", types, cfg)
```

### Check Available Runtimes

```go
fmt.Println("Available runtimes:", clientgen.ListRuntimes())
// Output: Available runtimes: [go typescript]

if clientgen.Registered("go") {
    fmt.Println("Go generator is available")
}
```

## Generated Code Example

For this schema:

```fga
model
  schema 1.1

type user

type repository
  relations
    define owner: [user]
    define viewer: [user] or owner
```

The Go generator produces:

```go
package authz

import "github.com/pthm/melange/melange"

// Object types
const (
    TypeUser       = "user"
    TypeRepository = "repository"
)

// Relations
const (
    RelOwner  melange.Relation = "owner"
    RelViewer melange.Relation = "viewer"
)

// Constructors
func User(id string) melange.Object {
    return melange.Object{Type: TypeUser, ID: id}
}

func Repository(id string) melange.Object {
    return melange.Object{Type: TypeRepository, ID: id}
}
```

### Using Generated Code

```go
import (
    "myapp/internal/authz"
    "github.com/pthm/melange/melange"
)

checker := melange.NewChecker(db)

// Type-safe permission check
allowed, err := checker.Check(ctx,
    authz.User("123"),
    authz.RelViewer,
    authz.Repository("456"),
)
```

## CLI Usage

Generate code via the melange CLI:

```bash
# Generate Go client code
melange generate client --runtime go --output internal/authz/authz.go

# Generate with custom package
melange generate client --runtime go --package authz --output authz.go

# Generate TypeScript (when implemented)
melange generate client --runtime typescript --output src/authz.ts
```

## Integration with Build

Add to your `go:generate` directives:

```go
//go:generate melange generate client --runtime go --output authz_gen.go
```

Or in a Makefile:

```makefile
generate:
	melange generate client --runtime go --output internal/authz/authz.go
```

## Deprecated API

The following are provided for backwards compatibility:

```go
// Deprecated: Use Generate("go", types, cfg) instead.
func GenerateGo(w io.Writer, types []TypeDefinition, cfg *GenerateConfig) error

// Deprecated: Use Config instead.
type GenerateConfig struct { ... }

// Deprecated: Use DefaultConfig instead.
func DefaultGenerateConfig() *GenerateConfig
```

## Dependency Information

This package imports:

- `github.com/pthm/melange/internal/clientgen` - Generator registry
- `github.com/pthm/melange/internal/clientgen/go` - Go generator (via init)
- `github.com/pthm/melange/internal/clientgen/typescript` - TypeScript generator (via init)
- `github.com/pthm/melange/pkg/schema` - Schema types
