# parser

Package `parser` provides OpenFGA schema parsing for Melange.

This package wraps the official OpenFGA language parser to convert `.fga` schema files into Melange's internal `TypeDefinition` format. It isolates the OpenFGA parser dependency from other packages.

## Package Responsibilities

- Parse OpenFGA DSL files (`.fga`) into structured type definitions
- Convert OpenFGA protobuf models to Melange schema types
- Isolate external dependency on `github.com/openfga/language` parser

## Public API

### File Parsing

```go
// ParseSchema reads an OpenFGA .fga file and returns type definitions.
func ParseSchema(path string) ([]schema.TypeDefinition, error)
```

### String Parsing

```go
// ParseSchemaString parses OpenFGA DSL content and returns type definitions.
func ParseSchemaString(content string) ([]schema.TypeDefinition, error)
```

### Protobuf Conversion

```go
// ConvertProtoModel converts an OpenFGA protobuf AuthorizationModel
// to schema TypeDefinitions. Useful for OpenFGA API integration.
func ConvertProtoModel(model *openfgav1.AuthorizationModel) []schema.TypeDefinition
```

## Usage Examples

### Parse Schema File

```go
import "github.com/pthm/melange/pkg/parser"

types, err := parser.ParseSchema("schemas/schema.fga")
if err != nil {
    log.Fatalf("Failed to parse schema: %v", err)
}

for _, t := range types {
    fmt.Printf("Type: %s\n", t.Name)
    for _, r := range t.Relations {
        fmt.Printf("  Relation: %s\n", r.Name)
    }
}
```

### Parse Embedded Schema

```go
import "github.com/pthm/melange/pkg/parser"

const schemaContent = `
model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define viewer: [user] or owner
`

types, err := parser.ParseSchemaString(schemaContent)
if err != nil {
    log.Fatalf("Failed to parse schema: %v", err)
}
```

### Parse with Go Embed

```go
import (
    _ "embed"
    "github.com/pthm/melange/pkg/parser"
)

//go:embed schema.fga
var embeddedSchema string

func init() {
    types, err := parser.ParseSchemaString(embeddedSchema)
    if err != nil {
        panic(err)
    }
    // Use types...
}
```

### Integration with Migration

```go
import (
    "github.com/pthm/melange/pkg/parser"
    "github.com/pthm/melange/pkg/migrator"
)

// Parse schema
types, err := parser.ParseSchema("schemas/schema.fga")
if err != nil {
    log.Fatal(err)
}

// Migrate to database
m := migrator.NewMigrator(db, "schemas/schema.fga")
if err := m.MigrateWithTypes(ctx, types); err != nil {
    log.Fatal(err)
}
```

### Integration with Code Generation

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

// Generate type-safe client code
files, err := clientgen.Generate("go", types, nil)
if err != nil {
    log.Fatal(err)
}

for filename, content := range files {
    os.WriteFile(filename, content, 0644)
}
```

## Error Handling

Parse errors wrap `melange.ErrInvalidSchema`:

```go
import "github.com/pthm/melange/melange"

types, err := parser.ParseSchema("invalid.fga")
if err != nil {
    if errors.Is(err, melange.ErrInvalidSchema) {
        // Schema syntax error
        log.Printf("Invalid schema: %v", err)
    } else {
        // File I/O error
        log.Printf("Could not read schema: %v", err)
    }
}
```

## Supported OpenFGA Features

The parser supports OpenFGA Schema 1.1 including:

- Type definitions (`type user`, `type document`)
- Direct relations (`[user]`, `[user, group]`)
- Wildcard subjects (`[user:*]`)
- Userset references (`[group#member]`)
- Computed usersets / implied relations (`viewer: owner`)
- Tuple-to-userset (`viewer from parent`)
- Union (`[user] or owner`)
- Intersection (`writer and editor`)
- Exclusion (`viewer but not blocked`)
- Nested expressions (`(a and b) or (c but not d)`)

**Not supported:** Conditions (OpenFGA 1.2 feature)

## Dependency Information

This package imports:

- `github.com/openfga/api/proto/openfga/v1` - OpenFGA protobuf types
- `github.com/openfga/language/pkg/go/transformer` - OpenFGA DSL parser
- `github.com/pthm/melange/melange` - Error types
- `github.com/pthm/melange/pkg/schema` - Output types

The parser package is the **only** Melange package that imports the OpenFGA language parser, keeping other packages dependency-free.
