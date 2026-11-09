# clientgen

Package `clientgen` provides a public API for generating type-safe client code from authorization schemas.

This package wraps the internal generator registry, providing a stable public interface for programmatic code generation. For CLI usage, see the melange command: `melange generate client --runtime go`.

## Package Responsibilities

- Generate type-safe constants for object types and relations
- Create constructor functions for `melange.Object` values
- Emit the exact names of the accessible functions a migration installs, so application code never builds them (long names are truncated and hashed)
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
    Package        string                     // Package name for generated code
    Exposure       *compiler.Exposure         // SQL API exposure the database is migrated with (nil: default)
    Relations      *compiler.RelationPatterns // Relations that get constants (nil: follow Exposure)
    RelationFilter string                     // Legacy prefix filter applied after them
    IDType         string                     // Type for object IDs (default: "string")
    Version        string                     // melange version for the header
    SourcePath     string                     // Schema path for the header
    Options        map[string]any             // Runtime-specific options
}
```

`Exposure` is the `sql:` section of `melange.yaml` (or `migrator.MigrateOptions.Exposure`). The accessible names are those a migration with it installs for the selected relations, so pass the exposure the database is migrated with. A `Config` that sets neither field generates every relation and every accessible function.

## Supported Runtimes

| Runtime      | Output                                                                                   |
| ------------ | ---------------------------------------------------------------------------------------- |
| `go`         | `schema_gen.go`: constants, constructors and accessible function name constants          |
| `typescript` | `types.ts`, `schema.ts`, `sql.ts`, `index.ts`: constants, factories and the name maps    |

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
    Package:  "authz",  // Package name
    IDType:   "string", // ID type
    // The exposure migrate applies: only can_* relations
    Exposure: &compiler.Exposure{Relations: compiler.RelationPatterns{Include: []string{"can_*"}}},
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

// ObjectType constants from schema.
const (
    TypeRepository melange.ObjectType = "repository"
    TypeUser       melange.ObjectType = "user"
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

// Wildcard constructors for public access patterns.
func AnyRepository() melange.Object { return melange.Object{Type: TypeRepository, ID: "*"} }
func AnyUser() melange.Object       { return melange.Object{Type: TypeUser, ID: "*"} }

// Accessible functions, as installed
const (
    SQLAccessibleRepositoryOwner  = "accessible_repository_owner"
    SQLAccessibleRepositoryViewer = "accessible_repository_viewer"
)

func SQLAccessibleFunction(objectType melange.ObjectType, relation melange.Relation) (string, bool) { ... }
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
# Generate Go client code (writes internal/authz/schema_gen.go)
melange generate client --runtime go --output internal/authz/

# Generate with custom package
melange generate client --runtime go --package authz --output .

# Generate TypeScript (a directory: types.ts, schema.ts, sql.ts, index.ts)
melange generate client --runtime typescript --output src/authz/
```

## Integration with Build

Add to your `go:generate` directives:

```go
//go:generate melange generate client --runtime go --output .
```

Or in a Makefile:

```makefile
generate:
	melange generate client --runtime go --output internal/authz/
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
