// Package analysis provides relation analysis and feature detection for SQL generation.
//
// # Overview
//
// This package analyzes OpenFGA authorization models to determine what SQL
// patterns are needed to evaluate permission checks and list operations.
// It classifies each relation by its features (direct, implied, userset, TTU,
// exclusion, intersection) and computes derived metadata used by the code generator.
//
// # Analysis Pipeline
//
// The analysis happens in three stages:
//
//  1. AnalyzeRelations: Classify relations and detect features
//  2. ComputeCanGenerate: Determine generation eligibility and populate metadata
//  3. DetermineListStrategy: Select the appropriate list generation template
//
// # Relation Features
//
// Each relation is analyzed to detect which OpenFGA patterns it uses:
//
//   - HasDirect: [user] - allows direct subject type grants
//   - HasImplied: viewer: editor - satisfied by another relation via closure
//   - HasWildcard: [user:*] - allows wildcard grants for any subject_id
//   - HasUserset: [group#member] - grants via membership in another object
//   - HasRecursive: viewer from parent - grants inherited through relationships (TTU)
//   - HasExclusion: but not blocked - denies access based on conditions
//   - HasIntersection: writer and editor - requires all parts to be satisfied
//
// Multiple features can coexist. For example, a relation can have both Direct
// and Userset features, meaning access is granted either by direct tuple or
// group membership. The generator produces SQL that ORs these paths together.
//
// # Closure and Dependencies
//
// Implied relations create a transitive closure where one relation satisfies another.
// For example, if "owner" implies "editor" and "editor" implies "viewer", then
// the closure for "viewer" includes ["viewer", "editor", "owner"].
//
// The analysis partitions closure relations into:
//
//   - SimpleClosureRelations: Can use direct tuple lookup (relation IN (...))
//   - ComplexClosureRelations: Need function calls (have exclusions, usersets, TTU, intersection)
//
// This enables efficient tuple lookups for simple cases while delegating to
// specialized functions for complex authorization logic.
//
// # Generation Capabilities
//
// After analysis, each relation has computed Capabilities indicating what can be generated:
//
//   - CheckAllowed: Can generate check_{type}_{relation} function (always true)
//   - ListAllowed: Can generate list functions (requires compatible features)
//   - ListStrategy: Which list generation approach to use (Direct, Userset, Recursive, etc.)
//
// Check functions are generated for all relations. List functions have stricter
// requirements - all relations in the closure must have compatible features.
//
// # List Strategies
//
// The DetermineListStrategy function selects the template based on relation features:
//
//   - ListStrategyDirect: Direct tuple lookup, possibly with exclusions
//   - ListStrategyUserset: Expands usersets via JOINs
//   - ListStrategyRecursive: Uses recursive CTEs for parent-child traversal
//   - ListStrategySelfRefUserset: Recursive CTEs for self-referential usersets
//   - ListStrategyComposed: Composes through TTU/userset paths to reach anchor
//   - ListStrategyIntersection: Uses INTERSECT for AND patterns
//   - ListStrategyDepthExceeded: Immediately raises depth limit error
//
// # Indirect Anchors
//
// For relations without direct/implied access (pure TTU or pure userset patterns),
// the analysis traces through the pattern to find an anchor relation with direct grants.
// This enables list generation by composing list functions along the path.
//
// For example, document.viewer: viewer from folder where folder.viewer: [user]
// creates an indirect anchor at folder.viewer. The list function for document.viewer
// composes with the list function for folder.viewer.
//
// # Metadata Propagation
//
// The analysis propagates metadata through the dependency graph:
//
//   - AllowedSubjectTypes: Union of subject types from satisfying relations
//   - HasWildcard: True if any relation in the closure supports wildcards
//   - UsersetPatterns: Enriched with SatisfyingRelations and IsComplex flags
//   - MaxUsersetDepth: Maximum chain depth for userset patterns
//
// This metadata drives code generation decisions and optimization opportunities.
package analysis
