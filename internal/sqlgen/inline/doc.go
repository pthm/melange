// Package inline generates VALUES clauses for inlining metadata into SQL functions.
//
// # Overview
//
// Rather than querying database tables for closure and userset metadata during
// permission checks, Melange inlines this data directly into generated functions
// as VALUES clauses. This eliminates JOINs and improves query performance.
//
// # Inline Data Types
//
// The package generates two types of inline data:
//
//  1. Closure data: Maps (object_type, relation) to satisfying relations
//  2. Userset data: Maps (object_type, relation) to allowed (subject_type, subject_relation) patterns
//
// # Example
//
// For a schema with:
//
//	type document
//	  define owner: [user]
//	  define editor: [user] or owner
//	  define viewer: [user] or editor
//
// The closure data for viewer would be:
//
//	(VALUES
//	  ('document', 'viewer', 'viewer'),
//	  ('document', 'viewer', 'editor'),
//	  ('document', 'viewer', 'owner')
//	) AS c(object_type, relation, satisfying_relation)
//
// # Integration with SQL Generation
//
// The generated VALUES clauses are embedded in check functions as CTEs:
//
//	WITH closure AS (
//	  <inline closure data>
//	)
//	SELECT 1 FROM melange_tuples t
//	INNER JOIN closure c ON c.satisfying_relation = t.relation
//	WHERE c.object_type = 'document' AND c.relation = 'viewer' ...
//
// This approach keeps authorization metadata versioned with the schema rather
// than stored in separate tables that could become inconsistent.
package inline
