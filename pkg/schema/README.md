# schema

Package `schema` provides OpenFGA schema types and transformation logic for Melange.

This package contains the core data structures and algorithms for converting OpenFGA authorization models into database-friendly representations. It sits between the parser package (which parses `.fga` files) and the SQL code generation (which produces specialized check functions).

## Package Responsibilities

The schema package handles three critical transformations:

1. **Schema representation** (`TypeDefinition`, `RelationDefinition`) - parsed FGA models
2. **SQL model generation** (`ToAuthzModels`) - flattening rules for SQL generation
3. **Precomputation** (`ComputeRelationClosure`, `ToUsersetRules`) - optimizing runtime checks

## Key Types

### TypeDefinition

Represents a parsed object type from an `.fga` schema. Each type has relations that define permissions and roles.

```go
type TypeDefinition struct {
    Name      string
    Relations []RelationDefinition
}
```

### RelationDefinition

Represents a parsed relation with all its rule components:

```go
type RelationDefinition struct {
    Name              string              // Relation name: "owner", "can_read", etc.
    ImpliedBy         []string            // Relations that imply this one
    ParentRelations   []ParentRelationCheck  // Tuple-to-userset inheritance
    ExcludedRelations []string            // Exclusions ("but not")
    SubjectTypeRefs   []SubjectTypeRef    // Direct subject types
    IntersectionGroups []IntersectionGroup // AND groups
}
```

### AuthzModel

Represents a flattened authorization rule used during SQL generation:

```go
type AuthzModel struct {
    ObjectType       string
    Relation         string
    SubjectType      *string
    ImpliedBy        *string
    ParentRelation   *string
    ExcludedRelation *string
    // ... additional fields for complex patterns
}
```

### ClosureRow

Represents precomputed transitive relationships for role hierarchies:

```go
type ClosureRow struct {
    ObjectType         string
    Relation           string
    SatisfyingRelation string
    ViaPath            []string // Debug path
}
```

## Public API

### Schema Transformation

```go
// ToAuthzModels converts parsed type definitions to database models.
// Performs transitive closure of implied_by relationships.
func ToAuthzModels(types []TypeDefinition) []AuthzModel

// ComputeRelationClosure computes the transitive closure for all relations.
// Enables O(1) lookups instead of O(depth) recursion.
func ComputeRelationClosure(types []TypeDefinition) []ClosureRow

// ToUsersetRules expands userset references using the relation closure.
func ToUsersetRules(types []TypeDefinition, closureRows []ClosureRow) []UsersetRule
```

### Schema Inspection

```go
// SubjectTypes returns all types that can be subjects in authorization checks.
func SubjectTypes(types []TypeDefinition) []string

// RelationSubjects returns subject types that can have a specific relation.
func RelationSubjects(types []TypeDefinition, objectType, relation string) []string
```

### Schema Validation

```go
// DetectCycles checks for cycles in the relation graph.
// Returns ErrCyclicSchema if a cycle is found.
func DetectCycles(types []TypeDefinition) error

// IsCyclicSchemaErr returns true if err is or wraps ErrCyclicSchema.
func IsCyclicSchemaErr(err error) bool
```

## Usage Examples

### Inspecting Schema Types

```go
import (
    "github.com/pthm/melange/pkg/parser"
    "github.com/pthm/melange/pkg/schema"
)

// Parse schema
types, err := parser.ParseSchema("schema.fga")
if err != nil {
    log.Fatal(err)
}

// Find all subject types (who can have permissions)
subjects := schema.SubjectTypes(types)
fmt.Println("Subject types:", subjects) // e.g., ["user", "group"]

// Find who can be repository owners
owners := schema.RelationSubjects(types, "repository", "owner")
fmt.Println("Repository owners:", owners) // e.g., ["user"]
```

### Validating Schemas

```go
types, _ := parser.ParseSchema("schema.fga")

if err := schema.DetectCycles(types); err != nil {
    if schema.IsCyclicSchemaErr(err) {
        log.Fatalf("Schema has cycles: %v", err)
    }
    log.Fatalf("Validation error: %v", err)
}
```

### Computing Relation Closure

```go
types, _ := parser.ParseSchema("schema.fga")

// Compute transitive closure for role hierarchies
closureRows := schema.ComputeRelationClosure(types)

// Closure enables efficient permission checks:
// For owner -> admin -> member hierarchy:
// - member is satisfied by: member, admin, owner
// - admin is satisfied by: admin, owner
// - owner is satisfied by: owner
for _, row := range closureRows {
    fmt.Printf("%s.%s is satisfied by %s\n",
        row.ObjectType, row.Relation, row.SatisfyingRelation)
}
```

### Generating Database Models

```go
types, _ := parser.ParseSchema("schema.fga")

// Convert to flattened rules for SQL generation
models := schema.ToAuthzModels(types)

for _, m := range models {
    fmt.Printf("Rule: %s.%s", m.ObjectType, m.Relation)
    if m.ImpliedBy != nil {
        fmt.Printf(" implied by %s", *m.ImpliedBy)
    }
    fmt.Println()
}
```

## Relation Patterns

The schema package understands these OpenFGA patterns:

| Pattern | Rule Fields Set | Example |
|---------|-----------------|---------|
| Direct | `SubjectType` | `[user]` |
| Implied | `ImpliedBy` | `viewer: owner` |
| Parent (TTU) | `ParentRelation`, `SubjectType` | `viewer from org` |
| Exclusion | `ExcludedRelation` | `but not author` |
| Userset | `SubjectType`, `SubjectRelation` | `[group#member]` |
| Intersection | `RuleGroupID`, `RuleGroupMode` | `a and b` |

## Dependency Information

This package has **no external dependencies** (stdlib only) and is imported by:

- `pkg/parser` - adds OpenFGA DSL parsing
- `pkg/migrator` - database migration
- `pkg/clientgen` - client code generation
- `internal/sqlgen` - SQL code generation

The runtime module (`github.com/pthm/melange/melange`) does not import this package, keeping it stdlib-only for minimal dependency footprint.
