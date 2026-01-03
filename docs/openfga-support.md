# OpenFGA DSL Support

This document outlines Melange's support for the OpenFGA modeling language specification.

---

## Feature Comparison

| Feature                                  | OpenFGA Spec | Melange Status   | Notes                                         |
| ---------------------------------------- | ------------ | ---------------- | --------------------------------------------- |
| **Direct assignment** `[user]`           | Yes          | **Full**         | Subjects explicitly granted via tuples        |
| **Userset references** `[type#relation]` | Yes          | **Partial**      | Parsed via metadata, not evaluated at runtime |
| **Wildcards** `[user:*]`                 | Yes          | **Full**         | Public access, `subject_id = '*'` in tuples   |
| **Union/OR**                             | Yes          | **Full**         | Any rule matches                              |
| **Intersection/AND**                     | Yes          | **Not enforced** | Parsed but treated as union                   |
| **Exclusion/BUT NOT**                    | Yes          | **Full**         | Recursive with parent inheritance             |
| **Computed relations**                   | Yes          | **Full**         | `implied_by` with transitive closure          |
| **Tuple-to-userset (FROM)**              | Yes          | **Full**         | Parent inheritance                            |
| **Conditions**                           | Yes (1.2+)   | **None**         | CEL expressions, contextual params            |
| **Modular models**                       | Yes (1.2+)   | **None**         | Multi-file, `module`, `extend type`           |
| **Schema 1.0**                           | Yes          | **Untested**     | Parser may handle, not validated              |
| **Schema 1.1**                           | Yes          | **Full**         | Primary supported version                     |
| **Schema 1.2**                           | Yes          | **None**         | Conditions, modules                           |
| **Grouping `()`**                        | Yes          | **Partial**      | Parser handles, flattened on extraction       |

---

## Supported Features (Detail)

### Direct Assignment `[user]`

Subjects explicitly granted a relation via tuples.

```fga
type organization
  relations
    define owner: [user]
    define member: [user]
```

**Implementation**:

- `RelationDefinition.SubjectTypes` stores allowed subject types
- Converted to `melange_model` rows with `subject_type` set
- SQL queries match tuples directly via `melange_relation_closure` join

**Code references**:

- `tooling/parser.go:130` - Direct assignment parsing
- `sql/functions.sql:92-105` - Tuple matching query

---

### Union/OR

Multiple rules combined with OR - permission granted if ANY rule matches.

```fga
define admin: [user] or owner
define reader: [user] or writer or can_read from org
```

**Implementation**:

- Multiple `RelationDefinition` entries created for each rule
- `extractUserset()` recursively processes each child in `Union.GetChild()`
- `melange_relation_closure` table contains all satisfying relations

**Code references**:

- `tooling/parser.go:143-147` - Union extraction
- `closure.go` - Transitive closure computation

---

### Inheritance/FROM (Tuple-to-Userset)

Permission inherited from related parent objects.

```fga
type issue
  relations
    define repo: [repository]
    define can_read: can_read from repo
```

**Implementation**:

- `RelationDefinition.ParentRelation` = relation to check on parent (e.g., "can_read")
- `RelationDefinition.ParentType` = linking relation name (e.g., "repo")
- SQL `check_permission()` recursively checks parent

**Code references**:

- `tooling/parser.go:137-141` - TupleToUserset parsing
- `sql/functions.sql:133-161` - Parent relation loop

---

### Role Hierarchy (Implied-By with Transitive Closure)

Relations implied by other relations.

```fga
define admin: [user] or owner     # admin implies member via owner
define member: [user] or admin    # owner transitively implies member
```

**Implementation**:

- `RelationDefinition.ImpliedBy` stores relations that grant this one
- Transitive closure computed during schema load via `computeTransitiveClosure()`
- Each transitive implier creates separate `AuthzModel` row
- `melange_relation_closure` table precomputes all satisfying relations

**Code references**:

- `schema.go:140-203` - Transitive closure computation
- `closure.go` - Closure table generation

---

### Exclusion/BUT NOT

Deny permission for subjects with excluded relation.

```fga
define can_review: can_read from repo but not author
```

**Implementation**:

- `RelationDefinition.ExcludedRelation` stores the excluding relation
- SQL `check_exclusion()` function handles recursive exclusion checking
- Handles direct tuples, implied-by relations, and parent inheritance

**Code references**:

- `tooling/parser.go:156-166` - Difference parsing
- `sql/functions.sql:16-67` - `check_exclusion()` recursive CTE

---

### Wildcard/Public Access

Public/unauthenticated access using wildcard subjects.

```fga
define public: [user:*]
define can_read: public or member
```

**Implementation**:

- Subject type stored as `"user:*"` (with `:*` suffix)
- Wildcard stripped for storage: `user:*` becomes `user`
- SQL queries match: `WHERE t.subject_id = p_subject_id OR t.subject_id = '*'`

**Code references**:

- `tooling/parser.go:72-75` - Wildcard parsing
- `sql/functions.sql:102` - Wildcard matching
