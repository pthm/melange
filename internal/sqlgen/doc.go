// Package sqlgen generates specialized SQL functions for OpenFGA authorization checks.
//
// # Overview
//
// This package is the SQL code generator for Melange, an OpenFGA-to-PostgreSQL compiler.
// It analyzes OpenFGA authorization models and generates optimized PostgreSQL functions
// that evaluate permission checks and list operations without recursive graph traversal.
//
// # Architecture
//
// The generator operates in three phases:
//
//  1. Analysis: Parse relation features (direct, implied, userset, TTU, exclusion, intersection)
//  2. Planning: Determine which SQL patterns to use based on feature combinations
//  3. Rendering: Generate PL/pgSQL functions using the SQL DSL
//
// # Subpackages
//
// The package is organized into focused subpackages:
//
//   - analysis: Relation analysis and feature detection
//   - sqldsl: Type-safe SQL DSL for building queries
//   - plpgsql: PL/pgSQL function builders
//   - tuples: Tuple table query builders
//   - inline: Inline data generation for closure and userset tables
//
// # SQL DSL
//
// The sqldsl subpackage provides a domain-specific DSL for constructing PostgreSQL queries.
// Rather than building SQL strings directly, code uses typed DSL elements that compose
// together and render to SQL.
//
// Example - Direct tuple check:
//
//	query := Tuples("t").
//	    ObjectType("document").
//	    Relations("viewer").
//	    WhereSubjectType(SubjectType).
//	    WhereSubjectID(SubjectID, true).  // true = allow wildcards
//	    Select("1").
//	    Limit(1)
//	exists := Exists{Query: query.Build()}
//	sql := exists.SQL()  // Renders to PostgreSQL
//
// # Generated Functions
//
// For each relation in the schema, the generator produces:
//
//  1. Specialized check function: check_{type}_{relation}(subject_type, subject_id, object_id)
//  2. No-wildcard variant: check_{type}_{relation}_no_wildcard(...)
//  3. List objects function: list_{type}_{relation}_objects(subject_type, subject_id, limit, cursor)
//  4. List subjects function: list_{type}_{relation}_subjects(object_id, subject_type, limit, cursor)
//
// The generator also produces dispatcher functions (check_permission, list_accessible_objects,
// list_accessible_subjects) that route requests to the appropriate specialized function
// based on object type and relation name.
//
// # Relation Patterns
//
// The generator handles all OpenFGA relation patterns:
//
//   - Direct: [user] - direct tuple lookup
//   - Implied: viewer: editor - closure-based lookup
//   - Wildcard: [user:*] - match any subject_id
//   - Userset: [group#member] - JOIN to expand group membership
//   - TTU (Tuple-to-userset): viewer from parent - check permission on linked objects
//   - Exclusion: but not blocked - anti-join or function call to deny access
//   - Intersection: writer and editor - INTERSECT of multiple checks
//
// # Code Generation Flow
//
// The typical flow for generating SQL:
//
//  1. Call AnalyzeRelations to classify all relations and detect features
//  2. Call ComputeCanGenerate to determine generation eligibility and populate metadata
//  3. Call GenerateSQL to produce all function definitions
//  4. Apply the generated SQL during migration
//
// # Implementation Notes
//
// The DSL uses the builder pattern with method chaining for fluent composition.
// All DSL types implement either Expr (for expressions) or SQLer (for statements).
// The SQL() method renders the final PostgreSQL syntax.
//
// Cycle detection for recursive patterns uses a visited array passed through
// check_permission_internal calls. Depth limits prevent unbounded recursion.
//
// For relations with complex patterns (deep TTU chains, exclusions on excluded relations),
// the generator delegates to check_permission_internal which handles the full
// authorization semantics including fallback to the generic implementation.
package sqlgen
