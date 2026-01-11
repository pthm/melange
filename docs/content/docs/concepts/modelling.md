---
title: Authorization Modelling
weight: 2
---

Melange uses [OpenFGA](https://openfga.dev)'s DSL syntax to define authorization models. This page covers the basics - for comprehensive modelling guidance, see the [OpenFGA documentation](https://openfga.dev/docs/modeling).

## Schema Structure

A Melange schema file (`schema.fga`) has this structure:

```fga
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
```

Key elements:

- **`model`** - Required header
- **`schema 1.1`** - Schema version (Melange supports 1.1)
- **`type`** - Defines an object type (users, resources, etc.)
- **`relations`** - Permissions and roles for that type

## Types

Types represent entities in your system:

```fga
type user
type organization
type repository
type team
```

Types with no relations (like `user`) are typically subjects - the "who" in permission checks.

## Relations

Relations define how subjects connect to objects:

### Direct Assignment

Allow specific subject types to be directly assigned:

```fga
type document
  relations
    define owner: [user]
    define editor: [user, team]
```

- `[user]` - Only users can be directly assigned
- `[user, team]` - Users or teams can be assigned

### Role Hierarchy (or)

Build role hierarchies where higher roles imply lower ones:

```fga
type organization
  relations
    define owner: [user]
    define admin: [user] or owner    # admins + all owners
    define member: [user] or admin   # members + all admins (+ owners)
```

The `or` keyword creates a union - permission is granted if ANY condition matches.

### Parent Inheritance (from)

Inherit permissions from a related parent object:

```fga
type repository
  relations
    define org: [organization]           # parent relationship
    define can_read: can_read from org   # inherit from org
    define can_write: can_write from org
```

The `from` keyword creates a tuple-to-userset relationship:

1. Look up the `org` relation to find the parent organization
2. Check `can_read` on that organization

### Exclusion (but not)

Deny permission when a condition is true:

```fga
type pull_request
  relations
    define repo: [repository]
    define author: [user]
    define can_review: can_read from repo but not author
```

Users can review if they have `can_read` on the repo AND are NOT the author.

### Wildcard/Public Access

Allow public access using wildcards:

```fga
type repository
  relations
    define public: [user:*]
    define can_read: public or member
```

The `[user:*]` syntax means "any user". In your tuples view, map public resources to `subject_id = '*'`.

## Common Patterns

### Organization with Teams

```fga
model
  schema 1.1

type user

type team
  relations
    define member: [user]

type organization
  relations
    define owner: [user]
    define admin: [user, team#member] or owner
    define member: [user, team#member] or admin

type repository
  relations
    define org: [organization]
    define can_read: member from org
    define can_write: admin from org
    define can_delete: owner from org
```

The `team#member` syntax means "users who are members of a team".

### Document Sharing

```fga
model
  schema 1.1

type user

type folder
  relations
    define owner: [user]
    define viewer: [user] or owner

type document
  relations
    define parent: [folder]
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor or viewer from parent
```

Documents inherit viewer access from their parent folder.

### Multi-Tenant SaaS

```fga
model
  schema 1.1

type user

type tenant
  relations
    define admin: [user]
    define member: [user] or admin

type project
  relations
    define tenant: [tenant]
    define owner: [user]
    define member: [user] or owner or member from tenant
    define can_read: member
    define can_write: owner or admin from tenant
```

## Validation

Validate your schema syntax:

```bash
melange validate --schema schemas/schema.fga
```

For full validation including semantic checks, use the OpenFGA CLI:

```bash
# Install OpenFGA CLI
go install github.com/openfga/cli/cmd/fga@latest

# Validate schema
fga model validate --file schemas/schema.fga
```

## Best Practices

### 1. Start with Roles, Add Permissions

Define roles first, then derive permissions from them:

```fga
type repository
  relations
    # Roles (assigned directly)
    define owner: [user]
    define admin: [user] or owner
    define writer: [user] or admin
    define reader: [user] or writer

    # Permissions (derived from roles)
    define can_read: reader
    define can_write: writer
    define can_delete: owner
```

### 2. Use Consistent Naming

- **Roles**: `owner`, `admin`, `member`, `viewer`
- **Permissions**: `can_read`, `can_write`, `can_delete`, `can_invite`
- **Parent relations**: `org`, `parent`, `folder`, `repo`

### 3. Avoid Deep Inheritance Chains

Each `from` hop adds latency. Keep inheritance shallow:

```fga
# Good: 1 hop
define can_read: can_read from org

# Avoid: Multiple hops
define can_read: can_read from parent from grandparent
```

### 4. Use Exclusion Sparingly

Exclusion patterns (`but not`) are slower than positive checks. Use them only when necessary:

```fga
# Necessary: prevent self-review
define can_review: can_read from repo but not author

# Consider alternatives for simple cases
```

## Further Reading

- [OpenFGA Modelling Guide](https://openfga.dev/docs/modeling) - Comprehensive modelling documentation
- [OpenFGA Playground](https://play.fga.dev) - Interactive schema editor and testing
- [Building Blocks](https://openfga.dev/docs/modeling/building-blocks) - Detailed explanation of relations
- [OpenFGA Compatibility](../reference/openfga-compatibility.md) - What features Melange supports
