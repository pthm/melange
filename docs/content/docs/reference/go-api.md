---
title: Go API
weight: 3
---

Complete reference for the `github.com/pthm/melange/melange` Go runtime package. This package has zero external dependencies (stdlib only).

## Core Types

### ObjectType

```go
type ObjectType string
```

String alias for type names (e.g., `"user"`, `"repository"`).

### Object

```go
type Object struct {
    Type ObjectType
    ID   string
}
```

Represents a typed resource. Implements both `ObjectLike` and `SubjectLike`, so it can be used on either side of a permission check.

- `String() string` returns `"type:id"`
- `FGAObject() Object` implements `ObjectLike`
- `FGASubject() Object` implements `SubjectLike`

### Relation

```go
type Relation string
```

String alias for relation names (e.g., `"owner"`, `"can_read"`).

- `String() string` returns the relation name
- `FGARelation() Relation` implements `RelationLike`

### ContextualTuple

```go
type ContextualTuple struct {
    Subject  Object
    Relation Relation
    Object   Object
}
```

A temporary tuple injected at check time. Not persisted. Requires a `*sql.Tx` or `*sql.Conn` as the Querier (not `*sql.DB`).

### PageOptions

```go
type PageOptions struct {
    Limit int
    After *string
}
```

Controls pagination for list operations. `Limit` of 0 or negative means no limit. `After` is a cursor from a previous page.

## Interfaces

### ObjectLike

```go
type ObjectLike interface {
    FGAObject() Object
}
```

Implement this on your domain types so they can be passed directly to `Check`, `ListObjects`, etc.

### SubjectLike

```go
type SubjectLike interface {
    FGASubject() Object
}
```

Same as `ObjectLike` but for the subject side of a permission check.

### RelationLike

```go
type RelationLike interface {
    FGARelation() Relation
}
```

Implement on your relation types. The generated client code produces constants that implement this.

### Querier

```go
type Querier interface {
    QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
    QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
```

Satisfied by `*sql.DB`, `*sql.Tx`, and `*sql.Conn`.

| Type | Contextual Tuples | Sees Uncommitted Data |
|------|-------------------|----------------------|
| `*sql.DB` | No (returns `ErrContextualTuplesUnsupported`) | No |
| `*sql.Tx` | Yes | Yes |
| `*sql.Conn` | Yes | No |

### Execer

```go
type Execer interface {
    Querier
    ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
```

Extends `Querier` with write capability. Required internally for contextual tuple injection.

### Cache

```go
type Cache interface {
    Get(subject Object, relation Relation, object Object) (allowed bool, err error, ok bool)
    Set(subject Object, relation Relation, object Object, allowed bool, err error)
}
```

Implement this interface for custom cache backends (e.g., Redis). The built-in `CacheImpl` satisfies this interface.

### Validator

```go
type Validator interface {
    ValidateUsersetSubject(subject Object) error
    ValidateCheckRequest(subject Object, relation Relation, object Object) error
    ValidateListUsersRequest(relation Relation, object Object, subjectType ObjectType) error
    ValidateContextualTuple(tuple ContextualTuple) error
}
```

Schema-aware validation. The generated client code can provide a validator; use `WithValidator()` to supply it.

## Checker

### Creating a Checker

```go
func NewChecker(q Querier, opts ...Option) *Checker
```

Options:

| Option | Description |
|--------|-------------|
| `WithCache(c Cache)` | Enable caching for permission checks |
| `WithDecision(d Decision)` | Set a static decision override (Allow or Deny) |
| `WithContextDecision()` | Enable context-based decision overrides |
| `WithUsersetValidation()` | Validate userset subjects before checking |
| `WithRequestValidation()` | Validate all check requests before executing |
| `WithValidator(v Validator)` | Supply a schema-aware validator |
| `WithDatabaseSchema(s string)` | Set the PostgreSQL schema where melange objects live (see [Custom Database Schema](../configuration/#custom-database-schema)) |

### Permission Checks

```go
func (c *Checker) Check(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike) (bool, error)
```

Returns `true` if the subject has the relation on the object.

```go
func (c *Checker) CheckWithContextualTuples(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike, tuples []ContextualTuple) (bool, error)
```

Same as `Check` but with temporary tuples injected for this call only. Requires `*sql.Tx` or `*sql.Conn`.

```go
func (c *Checker) Must(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike)
```

Panics if the check is denied or returns an error. Use for internal invariants, not request handling.

### List Objects

```go
func (c *Checker) ListObjects(ctx context.Context, subject SubjectLike, relation RelationLike, objectType ObjectType, page PageOptions) (ids []string, nextCursor *string, err error)

func (c *Checker) ListObjectsAll(ctx context.Context, subject SubjectLike, relation RelationLike, objectType ObjectType) ([]string, error)

func (c *Checker) ListObjectsWithContextualTuples(ctx context.Context, subject SubjectLike, relation RelationLike, objectType ObjectType, tuples []ContextualTuple, page PageOptions) (ids []string, nextCursor *string, err error)
```

`ListObjects` returns object IDs of the given type that the subject can access. `ListObjectsAll` auto-paginates to return all results.

### List Subjects

```go
func (c *Checker) ListSubjects(ctx context.Context, object ObjectLike, relation RelationLike, subjectType ObjectType, page PageOptions) (ids []string, nextCursor *string, err error)

func (c *Checker) ListSubjectsAll(ctx context.Context, object ObjectLike, relation RelationLike, subjectType ObjectType) ([]string, error)

func (c *Checker) ListSubjectsWithContextualTuples(ctx context.Context, object ObjectLike, relation RelationLike, subjectType ObjectType, tuples []ContextualTuple, page PageOptions) (ids []string, nextCursor *string, err error)
```

`ListSubjects` returns subject IDs of the given type that have the relation on the object. `ListSubjectsAll` auto-paginates.

## Bulk Check

### Building a Bulk Check

```go
func (c *Checker) NewBulkCheck(ctx context.Context) *BulkCheckBuilder
```

```go
func (b *BulkCheckBuilder) Add(subject SubjectLike, relation RelationLike, object ObjectLike) *BulkCheckBuilder
func (b *BulkCheckBuilder) AddWithID(id string, subject SubjectLike, relation RelationLike, object ObjectLike) *BulkCheckBuilder
func (b *BulkCheckBuilder) AddMany(subject SubjectLike, relation RelationLike, objects ...ObjectLike) *BulkCheckBuilder
func (b *BulkCheckBuilder) WithContextualTuples(tuples ...ContextualTuple) *BulkCheckBuilder
func (b *BulkCheckBuilder) Execute() (*BulkCheckResults, error)
```

`Add` assigns an auto-generated ID (the index as a string). `AddWithID` lets you supply your own ID (panics on duplicate or empty ID). `AddMany` adds multiple objects for one subject+relation pair. All checks execute in a single SQL call.

```go
const MaxBulkCheckSize = 10000
```

Maximum number of checks per bulk operation.

### Reading Results

```go
func (r *BulkCheckResults) Len() int
func (r *BulkCheckResults) Get(index int) *BulkCheckResult
func (r *BulkCheckResults) GetByID(id string) *BulkCheckResult
func (r *BulkCheckResults) All() bool
func (r *BulkCheckResults) Any() bool
func (r *BulkCheckResults) None() bool
func (r *BulkCheckResults) Results() []*BulkCheckResult
func (r *BulkCheckResults) Allowed() []*BulkCheckResult
func (r *BulkCheckResults) Denied() []*BulkCheckResult
func (r *BulkCheckResults) Errors() []error
func (r *BulkCheckResults) AllOrError() error
```

`AllOrError` returns `nil` if every check was allowed, or a `*BulkCheckDeniedError` wrapping `ErrBulkCheckDenied`.

```go
func (r *BulkCheckResult) ID() string
func (r *BulkCheckResult) Index() int
func (r *BulkCheckResult) Subject() Object
func (r *BulkCheckResult) Relation() Relation
func (r *BulkCheckResult) Object() Object
func (r *BulkCheckResult) IsAllowed() bool
func (r *BulkCheckResult) Err() error
```

### Example

```go
results, err := checker.NewBulkCheck(ctx).
    Add(user, "can_read", repo1).
    Add(user, "can_write", repo1).
    Add(user, "can_delete", repo1).
    Execute()

if results.All() {
    // Full access
}

for _, r := range results.Denied() {
    log.Printf("denied: %s on %s", r.Relation(), r.Object())
}
```

## Cache

```go
func NewCache(opts ...CacheOption) *CacheImpl
func WithTTL(ttl time.Duration) CacheOption
```

```go
func (c *CacheImpl) Get(subject Object, relation Relation, object Object) (bool, error, bool)
func (c *CacheImpl) Set(subject Object, relation Relation, object Object, allowed bool, err error)
func (c *CacheImpl) Size() int
func (c *CacheImpl) Clear()
```

The built-in cache is an in-memory map, thread-safe, unbounded within the TTL window. Entries expire individually based on their insertion time.

```go
cache := melange.NewCache(melange.WithTTL(5 * time.Minute))
checker := melange.NewChecker(db, melange.WithCache(cache))
```

## Decision Overrides

```go
const (
    DecisionUnset Decision = iota // No override, perform normal check
    DecisionAllow                  // Always allow
    DecisionDeny                   // Always deny
)
```

**Checker-level override** (applies to all checks):

```go
checker := melange.NewChecker(db, melange.WithDecision(melange.DecisionAllow))
```

**Context-level override** (per-request):

```go
checker := melange.NewChecker(db, melange.WithContextDecision())

ctx := melange.WithDecisionContext(ctx, melange.DecisionAllow)
allowed, _ := checker.Check(ctx, subject, relation, object) // always true
```

```go
func WithDecisionContext(ctx context.Context, decision Decision) context.Context
func GetDecisionContext(ctx context.Context) Decision
```

## Errors

See [Errors Reference](../errors/) for the full error type and code reference.

## Next Steps

- [Checking Permissions](../../guides/checking-permissions/): usage patterns, caching, transactions
- [Errors Reference](../errors/): sentinel errors, validation codes, error helpers
- [SQL API](../sql-api/): calling permission functions directly from SQL
- [Generated Code](../generated-code/): what `melange generate client` produces
