# go

Go client code generator implementation.

## Responsibility

Generates type-safe Go code from OpenFGA schemas, producing:

- Object type constants (`TypeUser`, `TypeRepository`)
- Relation constants (`RelCanRead`, `RelOwner`)
- Constructor functions (`User(id)`, `Repository(id)`)
- Wildcard constructors (`AnyUser()` for `user:*` patterns)

## Architecture Role

```
internal/clientgen (registry)
       │
       └── internal/clientgen/go
               │
               └── Implements Generator interface
```

Registered via `init()`, discovered by the CLI through the registry.

## Output

Single file `schema_gen.go` containing all generated code. The file imports the melange runtime for type definitions.

## Design Decisions

- Uses `melange.Object` and `melange.Relation` types from runtime
- Pascal-cased type names, prefixed with `Type` and `Rel`
- Supports relation filtering via prefix (e.g., only `can_*` relations)
- Validates schema for cycles before generating
