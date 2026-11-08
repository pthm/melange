# clientgen

Generator registry for language-specific client code generation.

## Responsibility

This package defines the `Generator` interface and maintains a registry of language-specific implementations. It enables pluggable code generation for different target languages.

## Architecture Role

```
pkg/clientgen (public API)
       │
       └── internal/clientgen (registry + interface)
               │
               ├── internal/clientgen/go (Go implementation)
               └── internal/clientgen/typescript (TypeScript implementation)
```

The public `pkg/clientgen` package wraps this internal registry, providing a stable API while keeping implementation details private.

## Key Components

- `Generator` interface - Contract for language-specific generators
- `Register()` - Called by generators in their `init()` functions
- `Get()` / `List()` - Registry lookup functions
- `Config` - Language-agnostic generation options (package name, ID type, the SQL API exposure and relation selector, etc.)
- `Resolve()` - Validates and compiles the schema as migrate does, and selects what both generators emit: every type, the selected relations, and the installed accessible functions of those relations (from `compiler.AccessibleFunctions`, so the names are the ones migrate creates)
- `Identifiers` - Detects two schema names that map to one generated identifier

## Adding a New Generator

1. Create `internal/clientgen/<language>/generate.go`
2. Implement the `Generator` interface
3. Call `clientgen.Register()` in `init()`
4. Import the package in `pkg/clientgen/api.go` for registration

## Subpackages

- `go/` - Go code generator
- `typescript/` - TypeScript code generator
