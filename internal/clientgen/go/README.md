# go

Go client code generator implementation.

## Responsibility

Generates type-safe Go code from OpenFGA schemas, producing:

- Object type constants (`TypeUser`, `TypeRepository`)
- Relation constants (`RelCanRead`, `RelOwner`)
- Constructor functions (`User(id)`, `Repository(id)`)
- Wildcard constructors (`AnyUser()` for `user:*` patterns)
- Accessible function names (`SQLAccessibleRepositoryCanRead`, parent-key `SQLAccessibleIssueCanReadByRepo`) and the `SQLAccessibleFunction(type, relation)` lookup

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

Single file `schema_gen.go` containing all generated code, formatted with `go/format`. The file imports the melange runtime for type definitions.

## Design Decisions

- Uses `melange.Object` and `melange.Relation` types from runtime
- Pascal-cased type names, prefixed with `Type` and `Rel`
- Relation constants and accessible names follow `Config.Exposure` and `Config.Relations` (`clientgen.Resolve`); the legacy prefix filter applies after them
- Accessible names are untyped string constants holding the exact installed names, truncated ones included
- Fails with both sources named when two schema names map to one Go identifier, or a name is not a valid identifier
- Validates and compiles the schema as migrate does before generating
