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
//	query := Tuples("", "t").
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
// For each relation the exposure installs (every relation by default; see
// Exposure), the generator produces:
//
//  1. check_{type}_{relation}(subject_type, subject_id, object_id, visited), and a
//     _nw variant for a relation that has or reaches a wildcard
//  2. list_{type}_{relation}_obj(subject_type, subject_id, limit, after)
//  3. list_{type}_{relation}_sub(object_id, subject_type, limit, after)
//  4. accessible_{type}_{relation}(subject_type, subject_id[, userset]), and for an
//     exposed relation with a parent link accessible_{type}_{relation}_by_{link}(subject_type, subject_id)
//  5. explain_{type}_{relation} and expand_{type}_{relation} for eligible relations
//
// The dispatchers are check_permission, check_permission_nw, check_permission_bulk,
// list_accessible_objects, list_accessible_subjects, accessible_objects,
// explain_permission and expand_permission. They route requests to the
// appropriate specialized function based on object type and relation name.
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
//  3. Call GenerateSQL for the check, explain and expand functions and GenerateListSQL
//     for the list and accessible functions (the *WithOptions variants take an Exposure)
//  4. Apply the generated SQL during migration
//
// # Implementation Notes
//
// The DSL uses the builder pattern with method chaining for fluent composition.
// All DSL types implement either Expr (for expressions) or SQLer (for statements).
// The SQL() method renders the final PostgreSQL syntax.
//
// Cycle detection for recursive patterns uses a visited array passed from
// one check function to the next. Depth limits prevent unbounded recursion:
// every check function raises M2002 once the array reaches the limit.
//
// For relations with complex patterns (deep TTU chains, exclusions on excluded
// relations), a body checks the other relation by calling its check function.
// The call is direct when the object type and relation are known when the SQL
// is generated (CheckPermission, relationGraph.checkFunction) and goes through
// check_permission_internal otherwise, which routes by the runtime type.
package sqlgen
