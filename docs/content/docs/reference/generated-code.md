---
title: Generated Code
description: "What melange generate client produces for Go and TypeScript: type-safe constants and constructors from your schema."
weight: 6
---

`melange generate client` reads your `.fga` schema and produces type-safe constants and constructors. This page documents the output for each runtime.

## Go

Generates a single file: `schema_gen.go`.

### Object Type Constants

Each type in your schema produces a constant prefixed with `Type`:

```go
const (
    TypeUser         melange.ObjectType = "user"
    TypeOrganization melange.ObjectType = "organization"
    TypeRepository   melange.ObjectType = "repository"
)
```

Naming: snake_case type names are converted to PascalCase. `pull_request` becomes `TypePullRequest`.

### Relation Constants

Each relation produces a constant prefixed with `Rel`:

```go
const (
    RelOwner    melange.Relation = "owner"
    RelAdmin    melange.Relation = "admin"
    RelCanRead  melange.Relation = "can_read"
    RelCanWrite melange.Relation = "can_write"
)
```

When `--filter` is set (e.g., `--filter can_`), only relations matching the prefix are generated.

### Constructor Functions

Each type gets a constructor and a wildcard constructor:

```go
func User(id string) melange.Object {
    return melange.Object{Type: TypeUser, ID: id}
}

func AnyUser() melange.Object {
    return melange.Object{Type: TypeUser, ID: "*"}
}
```

The returned `melange.Object` implements both `ObjectLike` and `SubjectLike`, so constructors work on either side of a permission check.

### ID Type

The `--id-type` flag controls the constructor parameter type.

**`--id-type string`** (default):

```go
func Repository(id string) melange.Object {
    return melange.Object{Type: TypeRepository, ID: id}
}
```

**`--id-type int64`**:

```go
func Repository(id int64) melange.Object {
    return melange.Object{Type: TypeRepository, ID: fmt.Sprint(id)}
}
```

**`--id-type uuid.UUID`**:

```go
func Repository(id uuid.UUID) melange.Object {
    return melange.Object{Type: TypeRepository, ID: fmt.Sprint(id)}
}
```

When the ID type is not `string`, the package imports `fmt` for the conversion.

### Usage

```go
import "myapp/internal/authz"

allowed, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("42"),
)
```

## TypeScript

Generates three files: `types.ts`, `schema.ts`, `index.ts`.

### types.ts

Constants and union types for object types and relations:

```typescript
export const ObjectTypes = {
  User: "user",
  Organization: "organization",
  Repository: "repository",
} as const;

export type ObjectType = (typeof ObjectTypes)[keyof typeof ObjectTypes];

export const Relations = {
  Owner: "owner",
  Admin: "admin",
  CanRead: "can_read",
  CanWrite: "can_write",
} as const;

export type Relation = (typeof Relations)[keyof typeof Relations];
```

### schema.ts

Factory functions for each type:

```typescript
import type { MelangeObject } from '@pthm/melange';
import { ObjectTypes } from './types.js';

export function user(id: string): MelangeObject {
  return { type: ObjectTypes.User, id };
}

export function anyUser(): MelangeObject {
  return { type: ObjectTypes.User, id: '*' };
}

export function repository(id: string): MelangeObject {
  return { type: ObjectTypes.Repository, id };
}

export function anyRepository(): MelangeObject {
  return { type: ObjectTypes.Repository, id: '*' };
}
```

Naming: type names become camelCase for functions (`pull_request` becomes `pullRequest`), PascalCase for constants.

### index.ts

Re-exports for a clean import surface:

```typescript
export { ObjectTypes, Relations } from './types.js';
export type { ObjectType, Relation } from './types.js';
export * from './schema.js';
```

### Usage

```typescript
import { user, repository, Relations } from './authz';

const allowed = await checkPermission(
  user('alice'),
  Relations.CanRead,
  repository('42'),
);
```

### TypeScript Notes

- The `--id-type` flag is ignored. IDs are always `string`.
- The `--package` flag is ignored. TypeScript uses ES module exports.
- The `--filter` flag works the same as Go (prefix match on relation names).

## Regeneration

Run `melange generate client` after any schema change to keep the generated code in sync. The generated files include a header comment with the Melange version and source schema path for traceability.

## Configuration

All options can be set in `melange.yaml` under `generate.client`:

```yaml
generate:
  client:
    runtime: go
    output: internal/authz
    package: authz
    filter: can_
    id_type: string
```

See [Configuration](../configuration/) for the full reference.

## Next Steps

- [Go API](../go-api/): the runtime types that generated code builds on
- [Checking Permissions](../../guides/checking-permissions/): using generated code in permission checks
- [CLI Reference](../cli/): `generate client` command flags
