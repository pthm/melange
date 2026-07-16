// Package sqldsl provides a type-safe DSL for building PostgreSQL queries.
//
// # Overview
//
// Rather than constructing SQL strings through concatenation or templating,
// this package provides typed building blocks that compose together to form
// complete queries. The DSL models authorization query patterns directly,
// making it easier to construct correct queries and avoiding common SQL
// injection vulnerabilities.
//
// # Core Interfaces
//
// All DSL types implement one of two interfaces:
//
//   - Expr: Represents SQL expressions (columns, literals, operators, function calls)
//   - SQLer: Represents complete SQL statements (SELECT, WITH, etc.)
//
// Both interfaces define a SQL() method that renders the PostgreSQL syntax.
//
// # Expression Types
//
// Basic expressions:
//
//	Param("p_subject_type")           // Function parameter reference
//	Col{Table: "t", Column: "id"}     // Column reference: t.id
//	Lit("document")                   // String literal: 'document'
//	Int(42)                           // Integer literal: 42
//	Bool(true)                        // Boolean literal: TRUE
//	Null{}                            // NULL literal
//	Raw("CURRENT_TIMESTAMP")          // Raw SQL (escape hatch)
//
// Operators:
//
//	Eq{Left: col, Right: param}       // col = param
//	In{Expr: col, Values: []string}   // col IN ('a', 'b')
//	And(expr1, expr2, expr3)          // (expr1 AND expr2 AND expr3)
//	Or(expr1, expr2)                  // (expr1 OR expr2)
//	Not(expr)                         // NOT (expr)
//	Exists{Query: subquery}           // EXISTS (subquery)
//
// Authorization-specific helpers:
//
//	SubjectIDMatch(col, id, wildcard) // Match subject_id with optional wildcard
//	HasUserset{Source: col}           // Check if subject_id contains '#'
//	UsersetObjectID{Source: col}      // Extract object ID from userset (split_part)
//
// # Statement Types
//
// SELECT statements:
//
//	SelectStmt{
//	    Columns: []string{"object_id", "relation"},
//	    From: "melange_tuples",
//	    Alias: "t",
//	    Where: And(condition1, condition2),
//	    Limit: 100,
//	}
//
// Common Table Expressions:
//
//	WithCTE{
//	    Recursive: true,
//	    CTEs: []CTEDef{
//	        {Name: "accessible", Query: cteQuery},
//	    },
//	    Query: finalSelect,
//	}
//
// # Query Builders
//
// The tuples subpackage provides a fluent builder for common tuple queries:
//
//	query := Tuples("t").
//	    ObjectType("document").
//	    Relations("viewer", "editor").
//	    WhereSubjectType(SubjectType).
//	    WhereSubjectID(SubjectID, true).
//	    Select("t.object_id").
//	    Distinct()
//
//	sql := query.SQL()
//
// # Design Rationale
//
// Type safety: The compiler catches many errors that would otherwise only
// be found at runtime when executing the generated SQL.
//
// Composition: Complex queries are built from simple parts that can be
// tested and reasoned about independently.
//
// Authorization semantics: The DSL includes domain-specific helpers like
// SubjectIDMatch and UsersetObjectID that encode OpenFGA authorization
// patterns directly, reducing boilerplate and errors.
//
// SQL visibility: Unlike heavy ORMs, the DSL stays close to SQL syntax.
// Developers familiar with PostgreSQL can easily read and write DSL code.
package sqldsl
