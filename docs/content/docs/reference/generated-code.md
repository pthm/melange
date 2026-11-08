---
title: Generated Code
description: "What melange generate client produces for Go and TypeScript: type-safe constants, constructors and the exact names of the installed accessible functions."
weight: 6
---

`melange generate client` reads your `.fga` schema and produces type-safe constants and constructors, and the exact names of the [accessible functions](../sql-api/#accessible_type_relation) a migration installs. This page documents the output for each runtime.

## Which Relations

The client follows the [SQL API exposure](../sql-api/#exposure) in `melange.yaml`, so its constants match what `melange migrate` installs:

- **Relation constants** are generated for the relations `sql.relations` exposes on at least one type. Set `generate.client.relations` to choose them separately, with the same `include` / `exclude` patterns. Without its own `include`, the client inherits `sql.relations` and its `exclude` patterns narrow that further; with an `include`, its patterns replace `sql.relations`. `include: ["*"]` selects every relation.
- **Accessible function names** are generated for the accessible functions a migration with that exposure installs for the selected relations: `accessible_{type}_{relation}` when `sql.functions.accessible` is on, and the parent-key `accessible_{type}_{relation}_by_{link}` forms when `sql.functions.parent_key` is on. A relation the client selects but `sql.relations` does not expose gets a relation constant and no function name. The functions installed only because an exposed relation resolves through them are not named either.
- **Types**, their constants and constructors, are generated for every type.

```yaml
sql:
  relations:
    include: ["can_*"]          # what migrate exposes
generate:
  client:
    relations:                  # leave out to inherit sql.relations
      include: ["can_*", "document#owner"]
```

The function families come from `sql.functions` only. The `--no-<family>` flags of `melange migrate` apply to one migrate run and are not seen by `generate client`, so keep that choice in `melange.yaml` and both commands agree.

The `--filter` flag and `generate.client.filter` key (a relation name prefix such as `can_`) still work, applied after the patterns to relation constants and function names alike. They are deprecated: use `--include-relation 'can_*'` or `generate.client.relations.include` instead.

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

Only the [selected relations](#which-relations) get constants.

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

### Accessible Function Names

Each installed accessible function of a selected relation gets a string constant holding its name, prefixed with `SQLAccessible`, and each parent-key form one ending in `By` and the link:

```go
const (
    SQLAccessibleIssueCanRead                                               = "accessible_issue_can_read"
    SQLAccessiblePullRequestCanReadSecurityAdvisoriesAndVulnerabilityReports = "accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c"
)

const (
    // SQLAccessibleIssueCanReadByRepo lists the repository IDs through which the subject holds can_read on issue objects linked by repo.
    SQLAccessibleIssueCanReadByRepo = "accessible_issue_can_read_by_repo"
)

// SQLAccessibleFunction returns the name of the accessible function installed
// for a relation on a type, and false when there is none.
func SQLAccessibleFunction(objectType melange.ObjectType, relation melange.Relation) (string, bool)
```

The values are the names migrate creates, including names longer than PostgreSQL's 63 bytes, which are truncated and hashed: use the constants instead of building names from the type and relation. They are unqualified; when the functions live in a [custom database schema](../configuration/#custom-database-schema) that is not on the `search_path`, qualify them with it. `SQLAccessibleFunction` covers the per-relation form, for code that has the type and relation as values.

### Identifier Collisions

Generation fails, naming both sources, when two schema names map to the same Go identifier (type `user` gives `TypeUser`, and so does the constructor of a type named `type_user`), when a name is not a valid Go identifier, or when two relations map to one SQL function name (type `a_b` relation `c` and type `a` relation `b_c` are both `accessible_a_b_c`). Rename one of them in the schema. The generated file is formatted with `gofmt`.

### Usage

```go
import "myapp/internal/authz"

allowed, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("42"),
)

// Filter your own query by the access set
rows, err := db.QueryContext(ctx, `SELECT id, title FROM issues
    WHERE id::text IN (SELECT * FROM `+authz.SQLAccessibleIssueCanRead+`('user', $1))`, userID)
```

## TypeScript

Generates four files: `types.ts`, `schema.ts`, `sql.ts`, `index.ts`.

### types.ts

Constants and union types for object types and the [selected relations](#which-relations):

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

### sql.ts

The names of the installed accessible functions of the selected relations, keyed by the schema's type, relation and link names (quoted when a name is not a valid identifier):

```typescript
export const AccessibleFunctions = {
  issue: {
    can_read: "accessible_issue_can_read",
  },
  pull_request: {
    can_read_security_advisories_and_vulnerability_reports: "accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c",
  },
} as const;

export const AccessibleByParentFunctions = {
  issue: {
    can_read: {
      repo: "accessible_issue_can_read_by_repo",
    },
  },
} as const;
```

The values are the exact, unqualified names, as for Go. A map is `{} as const` when its function family is off. Generation fails when two type or relation names map to the same `ObjectTypes` or `Relations` key, or to the same factory function, or when a factory name is not a valid identifier, is a reserved word, `eval` or `arguments`, or is one of the names the generated files import or export by name (`ObjectTypes`, `Relations`, `ObjectType`, `Relation`, `AccessibleFunctions`, `AccessibleByParentFunctions`, `MelangeObject`).

### index.ts

Re-exports for a clean import surface:

```typescript
export { ObjectTypes, Relations } from './types.js';
export type { ObjectType, Relation } from './types.js';
export * from './schema.js';
export { AccessibleFunctions, AccessibleByParentFunctions } from './sql.js';
```

### Usage

```typescript
import { user, repository, Relations, AccessibleFunctions } from './authz';

const allowed = await checkPermission(
  user('alice'),
  Relations.CanRead,
  repository('42'),
);

const { rows } = await pool.query(
  `SELECT id, title FROM issues
   WHERE id::text IN (SELECT * FROM ${AccessibleFunctions.issue.can_read}('user', $1))`,
  [userId],
);
```

### TypeScript Notes

- The `--id-type` flag is ignored. IDs are always `string`.
- The `--package` flag is ignored. TypeScript uses ES module exports.
- Relation selection, including the deprecated `--filter`, works the same as Go.

## Regeneration

Run `melange generate client` after any schema or `sql:` change to keep the generated code in sync. The generated files include a header comment with the Melange version and source schema path for traceability.

A function name changes when its type, relation or link is renamed. The client stops emitting it when the relation stops being listable, is no longer exposed, or its family is turned off, and a parent-key form appears or disappears as the relation's grants change shape (adding a direct grant to `can_read: reader from org` removes `accessible_{type}_can_read_by_org`). After regenerating, a name that went away is a compile error in Go and a type error in TypeScript. The database function can stay installed after its name goes: `accessible_{type}_{relation}` is kept while an exposed relation resolves through it and any of `list_objects`, `accessible` or `parent_key` is on (see [What is installed](../sql-api/#exposure)), but it is no longer part of the API and a later migration can drop it. When migrate does drop a function that a view or policy calls, it refuses; see [Dependent objects](../../guides/migrations/#dependent-objects).

### Checking a client

A generated client can drift from the database in two places, and [`melange generate client --check`](../cli/#checking-a-generated-client) checks both:

1. **The files against `melange.yaml`.** `--check` regenerates the client in memory and compares it with the files in `generate.client.output`. It needs no database, so it fits a pre-merge CI step. The header's version and source lines, CRLF line endings and trailing whitespace at the end are ignored; any other change is drift, including reformatting the generated files, so exclude them from formatters.
2. **`melange.yaml` against the database.** With `--db` or `--env`, every accessible function name and relation constant the configuration generates is checked against what the database's last migration installed and exposes. Run it after `migrate`.

A client narrower than the database is fine: a relation the database exposes without a constant in the client is never reported. A client wider than the database is reported by what it would do: an accessible function that is not installed, or a relation `melange.yaml` exposes that the database does not, is an error (the call fails with `42883` or `M2003`); a constant for a relation that neither `melange.yaml` nor the database exposes is info, since constants are also used for writing tuples.

`melange doctor` runs the same check when `generate.client.runtime` is configured, from `melange.yaml` only. Keep the relation selection in `generate.client.relations` rather than in `--include-relation`, and the families in `sql.functions` rather than in `migrate`'s `--no-<family>` flags, so the client, `--check`, doctor and `migrate` all see one configuration.

## Configuration

All options can be set in `melange.yaml` under `generate.client`:

```yaml
generate:
  client:
    runtime: go
    output: internal/authz
    package: authz
    id_type: string
    relations:            # optional; inherits sql.relations when left out
      include: ["can_*"]
```

`generate.client.output` is also where `generate client --check` and doctor look for the files. See [Configuration](../configuration/) for the full reference.

## Next Steps

- [Go API](../go-api/): the runtime types that generated code builds on
- [Checking Permissions](../../guides/checking-permissions/): using generated code in permission checks
- [CLI Reference](../cli/): `generate client` command flags
