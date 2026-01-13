# compiler

Package `compiler` provides public APIs for compiling OpenFGA schemas to SQL.

This is a thin wrapper around `internal/sqlgen` that exposes only the public types and functions needed by external consumers. For most use cases, prefer `pkg/migrator` which handles the full migration workflow.

## Package Responsibilities

- Expose SQL generation types for advanced use cases
- Provide access to relation analysis for tooling
- Enable custom SQL generation pipelines

## Public API

### Types

```go
// GeneratedSQL contains all SQL generated from a schema for check functions.
type GeneratedSQL struct {
    Functions            []string // Specialized check functions per relation
    NoWildcardFunctions  []string // Check functions excluding wildcard matches
    Dispatcher           string   // Main check_permission dispatcher
    DispatcherNoWildcard string   // Dispatcher excluding wildcards
}

// ListGeneratedSQL contains all SQL generated for list functions.
type ListGeneratedSQL struct {
    ListObjectsFunctions   []string // list_objects_* functions
    ListSubjectsFunctions  []string // list_subjects_* functions
    ListObjectsDispatcher  string   // list_accessible_objects dispatcher
    ListSubjectsDispatcher string   // list_accessible_subjects dispatcher
}

// RelationAnalysis contains the analyzed features of a relation.
type RelationAnalysis struct {
    ObjectType  string
    Relation    string
    CanGenerate bool
    // Feature flags: HasDirect, HasImplied, HasWildcard, etc.
}
```

### Functions

```go
// AnalyzeRelations classifies all relations and gathers data needed for SQL generation.
var AnalyzeRelations func(types []schema.TypeDefinition, closure []schema.ClosureRow) []RelationAnalysis

// ComputeCanGenerate computes which relations can have functions generated.
var ComputeCanGenerate func(analyses []RelationAnalysis) []RelationAnalysis

// GenerateSQL generates specialized check_permission functions from relation analyses.
var GenerateSQL func(analyses []RelationAnalysis, inline InlineSQLData) (GeneratedSQL, error)

// GenerateListSQL generates specialized list functions from relation analyses.
var GenerateListSQL func(analyses []RelationAnalysis, inline InlineSQLData) (ListGeneratedSQL, error)

// CollectFunctionNames returns all generated function names for tracking.
var CollectFunctionNames func(analyses []RelationAnalysis) []string

// BuildInlineSQLData builds inline SQL data from closure and analyses.
var BuildInlineSQLData func(closure []schema.ClosureRow, analyses []RelationAnalysis) InlineSQLData
```

## Usage Examples

### Generate SQL for Inspection

```go
import (
    "github.com/pthm/melange/pkg/parser"
    "github.com/pthm/melange/pkg/schema"
    "github.com/pthm/melange/pkg/compiler"
)

// Parse schema
types, _ := parser.ParseSchema("schema.fga")

// Compute closure
closure := schema.ComputeRelationClosure(types)

// Analyze relations
analyses := compiler.AnalyzeRelations(types, closure)
analyses = compiler.ComputeCanGenerate(analyses)

// Build inline data
inline := compiler.BuildInlineSQLData(closure, analyses)

// Generate SQL
sql, err := compiler.GenerateSQL(analyses, inline)
if err != nil {
    log.Fatal(err)
}

// Inspect generated functions
for i, fn := range sql.Functions {
    fmt.Printf("Function %d:\n%s\n\n", i, fn)
}

// Inspect dispatcher
fmt.Printf("Dispatcher:\n%s\n", sql.Dispatcher)
```

### Analyze Relation Features

```go
types, _ := parser.ParseSchema("schema.fga")
closure := schema.ComputeRelationClosure(types)
analyses := compiler.AnalyzeRelations(types, closure)

for _, a := range analyses {
    fmt.Printf("%s.%s: CanGenerate=%v\n",
        a.ObjectType, a.Relation, a.CanGenerate)
}
```

### Custom SQL Pipeline

```go
// For advanced use: generate SQL without applying to database
types, _ := parser.ParseSchema("schema.fga")
closure := schema.ComputeRelationClosure(types)

analyses := compiler.AnalyzeRelations(types, closure)
analyses = compiler.ComputeCanGenerate(analyses)
inline := compiler.BuildInlineSQLData(closure, analyses)

checkSQL, _ := compiler.GenerateSQL(analyses, inline)
listSQL, _ := compiler.GenerateListSQL(analyses, inline)

// Write to files for review
os.WriteFile("check_functions.sql", []byte(checkSQL.Dispatcher), 0644)
os.WriteFile("list_functions.sql", []byte(listSQL.ListObjectsDispatcher), 0644)
```

## When to Use This Package

**Use `pkg/migrator` instead** for most use cases:

```go
// Simple: one function handles everything
err := migrator.Migrate(ctx, db, "schema.fga")
```

Use `pkg/compiler` when you need:

- **SQL inspection** - View generated SQL before applying
- **Custom pipelines** - Generate SQL for non-PostgreSQL targets
- **Tooling** - Build analyzers or documentation generators
- **Dry-run output** - Generate migration scripts for review

## Relationship to Other Packages

```
pkg/parser  ->  pkg/schema  ->  pkg/compiler  ->  pkg/migrator
   |               |               |                  |
   v               v               v                  v
 Parse FGA      Transform      Generate SQL      Apply to DB
```

The compiler package sits between schema analysis and database migration, providing the SQL generation step.

## Dependency Information

This package re-exports types from `internal/sqlgen`. It depends on:

- `github.com/pthm/melange/internal/sqlgen` - SQL generation implementation
- `github.com/pthm/melange/pkg/schema` - Schema types (via sqlgen)
