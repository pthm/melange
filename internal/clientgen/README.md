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
               └── internal/clientgen/typescript (TypeScript stub)
```

The public `pkg/clientgen` package wraps this internal registry, providing a stable API while keeping implementation details private.

## Key Components

- `Generator` interface - Contract for language-specific generators
- `Register()` - Called by generators in their `init()` functions
- `Get()` / `List()` - Registry lookup functions
- `Config` - Language-agnostic generation options (package name, ID type, etc.)

## Adding a New Generator

1. Create `internal/clientgen/<language>/generate.go`
2. Implement the `Generator` interface
3. Call `clientgen.Register()` in `init()`
4. Import the package in `pkg/clientgen/api.go` for registration

## Subpackages

- `go/` - Go code generator (implemented)
- `typescript/` - TypeScript generator (stub, not yet implemented)
