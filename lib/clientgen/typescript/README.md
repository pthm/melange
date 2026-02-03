# typescript

TypeScript client code generator for Melange.

## Responsibility

Generates type-safe TypeScript code from OpenFGA schemas, producing object type constants, relation constants, and factory functions.

## Architecture Role

Registered in the generator registry as "typescript". Invoked by the CLI via `melange generate client --runtime typescript`.

## Generated Output

The generator produces three TypeScript files:

### types.ts

Contains type constants and union types:

- `ObjectTypes` - Constant object with PascalCase keys mapping to object type strings
- `Relations` - Constant object with PascalCase keys mapping to relation strings
- `ObjectType` - Union type of all valid object types
- `Relation` - Union type of all valid relations

Uses TypeScript's `as const` for type safety.

### schema.ts

Contains factory functions and wildcard constructors:

- Factory functions (camelCase) - e.g., `user(id)`, `repository(id)`
- Wildcard constructors (any + PascalCase) - e.g., `anyUser()`, `anyRepository()`

All functions return `MelangeObject` from the `@pthm/melange` runtime package.

### index.ts

Re-exports all types and functions for clean imports.

## Example Output

Given a schema with `user` and `repository` types:

```typescript
// types.ts
export const ObjectTypes = {
  User: "user",
  Repository: "repository",
} as const;

export type ObjectType = (typeof ObjectTypes)[keyof typeof ObjectTypes];

// schema.ts
export function user(id: string): MelangeObject {
  return { type: ObjectTypes.User, id };
}

export function anyUser(): MelangeObject {
  return { type: ObjectTypes.User, id: '*' };
}

// index.ts
export { ObjectTypes, Relations } from './types.js';
export type { ObjectType, Relation } from './types.js';
export * from './schema.js';
```

## Configuration

Supports standard `clientgen.Config` options:

- `RelationFilter` - Prefix filter for relations (e.g., "can_" to generate only permissions)
- `Version` - Melange version for header comment
- `SourcePath` - Schema file path for header comment

The `Package` and `IDType` config fields are not used (TypeScript doesn't have packages, IDs are always strings).

## Naming Conventions

- Object type constants: PascalCase (`User`, `PullRequest`)
- Relation constants: PascalCase (`CanRead`, `Owner`)
- Factory functions: camelCase (`user`, `pullRequest`)
- Wildcard functions: `any` + PascalCase (`anyUser`, `anyPullRequest`)

## Usage

```bash
# Generate to a directory
melange generate client --runtime typescript --schema schema.fga --output src/authz/

# Generate only permission relations
melange generate client --runtime typescript --schema schema.fga --output src/authz/ --filter can_
```
