---
title: Modelling Guide
weight: 10
---

Melange uses the [OpenFGA DSL](https://openfga.dev/docs/configuration-language) to define authorization models. This guide walks through building a model from scratch, with full examples showing the schema, tuples view, and permission checks together.

For DSL syntax reference, see [Authorization Modelling](../../concepts/modelling/). For comprehensive modelling guidance, see the [OpenFGA modelling documentation](https://openfga.dev/docs/modeling).

## Core ideas

Authorization in Melange is based on relationships between entities. Instead of asking "does user X have role Y", you ask "does user X have relation Y with object Z".

Three things define your authorization model:

1. **Types** are the entities in your system: users, documents, organizations.
2. **Relations** define how entities connect: `owner`, `viewer`, `member`.
3. **Tuples** are the data. Each tuple is a fact: "alice is an owner of document:1".

You write the types and relations in a `.fga` schema file. The tuples come from your existing database tables via the `melange_tuples` view. Melange compiles the schema into SQL functions that query the view at runtime.

For background on relationship-based access control and how it compares to RBAC, see the [OpenFGA introduction](https://openfga.dev/docs/fga).

## Building a model step by step

This section builds a document-sharing model from scratch. The goal: users can own documents, share them for editing or viewing, and organize them into folders that grant view access to all documents inside.

### Define the types

Start by listing the entities involved:

```fga
model
  schema 1.1

type user

type folder

type document
```

`user` has no relations because it is always the subject (the "who") in a permission check, never the object.

### Add direct relations

Add relations for direct assignment. The type restriction in brackets specifies what types can be assigned:

```fga
type folder
  relations
    define owner: [user]

type document
  relations
    define owner: [user]
    define editor: [user]
    define viewer: [user]
```

With this schema, you can assign a user as an `owner`, `editor`, or `viewer` of a specific document. Each assignment requires an explicit tuple in the database.

### Add role hierarchy

Owners should automatically be editors, and editors should automatically be viewers. Use `or` to create a union:

```fga
type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
```

Now a user who is an `owner` is implicitly an `editor` and `viewer` too. Melange resolves this chain at compile time, so there is no runtime traversal.

### Add parent inheritance

Documents belong to folders. Users who can view a folder should be able to view its documents. Use `from` to inherit permissions from a parent:

```fga
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

`viewer from parent` means: look up the document's `parent` relation to find the folder, then check `viewer` on that folder. If the user is a viewer of the parent folder, they are a viewer of the document.

### The complete schema

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

### Map it to your database

Create a `melange_tuples` view that maps your existing tables to tuples. Each `UNION ALL` section produces tuples for one relationship:

```sql
CREATE OR REPLACE VIEW melange_tuples AS
-- Folder ownership
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'folder' AS object_type,
    folder_id::text AS object_id
FROM folder_owners

UNION ALL

-- Document ownership
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'document' AS object_type,
    document_id::text AS object_id
FROM documents
WHERE user_id IS NOT NULL

UNION ALL

-- Document editors (from a sharing table)
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'editor' AS relation,
    'document' AS object_type,
    document_id::text AS object_id
FROM document_shares
WHERE role = 'editor'

UNION ALL

-- Document viewers (from the same sharing table)
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'viewer' AS relation,
    'document' AS object_type,
    document_id::text AS object_id
FROM document_shares
WHERE role = 'viewer'

UNION ALL

-- Document -> Folder parent link
SELECT
    'folder' AS subject_type,
    folder_id::text AS subject_id,
    'parent' AS relation,
    'document' AS object_type,
    id::text AS object_id
FROM documents
WHERE folder_id IS NOT NULL;
```

Note the parent link at the bottom. For `from` relations to work, the tuples view must include a tuple that connects the child to the parent. Here, each document row with a `folder_id` produces a tuple like `(folder, 5, parent, document, 12)`.

### Check permissions

After running `melange migrate`, you can check permissions:

```sql
-- Can alice view document 12?
SELECT check_permission('user', 'alice', 'viewer', 'document', '12');
```

If alice owns folder 5, and document 12 is in folder 5, this returns 1. Melange follows the `from` chain automatically: it finds that document 12 has `parent = folder:5`, then checks that alice is a `viewer` (or `owner`) of folder 5.

## How indirect relationships resolve

This section addresses a common question: how do you check permissions that flow through an intermediate entity when you only have the subject and the target object?

Consider this scenario:

- Organizations have members
- Folders belong to organizations
- Organization members should be able to read all folders in their organization

You only have a `user_id` and a `folder_id` at check time. You do not have the `organization_id`.

### The schema

```fga
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define member: [user] or owner

type folder
  relations
    define org: [organization]
    define owner: [user]
    define can_read: owner or member from org
```

`member from org` means: find the folder's organization (via the `org` relation), then check if the user is a `member` of that organization.

### The tuples

Given this data:

| Fact | Tuple |
|------|-------|
| alice is a member of org acme | `(user, alice, member, organization, acme)` |
| folder 7 belongs to org acme | `(organization, acme, org, folder, 7)` |
| bob owns folder 7 | `(user, bob, owner, folder, 7)` |

### The check

```sql
SELECT check_permission('user', 'alice', 'can_read', 'folder', '7');
-- Returns: 1
```

You pass only the user and the folder. Melange resolves the rest:

1. Check if alice is an `owner` of folder 7. She is not.
2. Evaluate `member from org`:
   a. Look up the `org` relation on folder 7. Find `organization:acme`.
   b. Check if alice is a `member` of organization acme. She is.
3. Return 1 (allowed).

```sql
SELECT check_permission('user', 'bob', 'can_read', 'folder', '7');
-- Returns: 1
```

Bob is allowed because he is an `owner` of folder 7 directly.

```sql
SELECT check_permission('user', 'charlie', 'can_read', 'folder', '7');
-- Returns: 0
```

Charlie has no relationship to folder 7 or organization acme.

### The tuples view

```sql
CREATE OR REPLACE VIEW melange_tuples AS
-- Organization members
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Folder -> Organization parent link
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'folder' AS object_type,
    id::text AS object_id
FROM folders

UNION ALL

-- Folder owners
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'folder' AS object_type,
    folder_id::text AS object_id
FROM folder_owners;
```

The `org` parent link is what makes the indirect resolution work. Without it, Melange has no way to find which organization a folder belongs to.

## Pattern reference

Each pattern below is a building block. They can be combined in a single relation definition.

| Pattern | Schema | What it does | OpenFGA guide |
|---------|--------|-------------|---------------|
| Direct assignment | `define viewer: [user]` | Requires an explicit tuple | [Direct Access](https://openfga.dev/docs/modeling/direct-access) |
| Role hierarchy | `define viewer: [user] or editor` | Union of direct assignment and another relation | [Roles and Permissions](https://openfga.dev/docs/modeling/roles-and-permissions) |
| Parent inheritance | `define viewer: viewer from parent` | Inherit a relation from a linked object | [Parent-Child](https://openfga.dev/docs/modeling/parent-child) |
| Exclusion | `define viewer: editor but not blocked` | Allow unless a deny relation exists | [Blocklists](https://openfga.dev/docs/modeling/blocklists) |
| Wildcard | `define public: [user:*]` | Match any user of a given type | [Public Access](https://openfga.dev/docs/modeling/public-access) |
| Userset reference | `define admin: [team#member]` | Grant access to members of a group | [User Groups](https://openfga.dev/docs/modeling/user-groups) |
| Intersection | `define can_review: editor and approved` | Require multiple relations simultaneously | [Multiple Restrictions](https://openfga.dev/docs/modeling/multiple-restrictions) |

For supported features and limitations in Melange, see [OpenFGA Compatibility](../../reference/openfga-compatibility/).

## Mapping schemas to your database

Each directly assignable relation in your schema needs a corresponding section in your `melange_tuples` view. A simple rule:

- Every `define X: [type]` needs tuples that produce `(type, id, X, object_type, object_id)`.
- Every `define parent: [other_type]` (a linking relation used with `from`) needs tuples that produce `(other_type, id, parent, this_type, object_id)`.
- Implied relations (`define viewer: editor`) do not need their own tuples. Melange resolves them from the relations they reference.

For a full walkthrough of building the tuples view, see [Creating Your Tuples View](../../getting-started/tuples-view/).

## Further reading

- [Authorization Modelling](../../concepts/modelling/) for DSL syntax reference
- [OpenFGA Modelling Guides](https://openfga.dev/docs/modeling) for in-depth pattern documentation
- [OpenFGA Playground](https://play.fga.dev) for interactive schema editing and testing
- [OpenFGA Configuration Language](https://openfga.dev/docs/configuration-language) for full DSL specification
- [OpenFGA Compatibility](../../reference/openfga-compatibility/) for Melange's feature support matrix
