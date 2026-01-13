package sqldsl

import "strings"

// CTEDef represents a single Common Table Expression definition.
// Used within WithCTE to define named subqueries.
type CTEDef struct {
	Name    string   // CTE name (e.g., "accessible", "base_results")
	Columns []string // Optional column names (e.g., ["object_id", "depth"])
	Query   SQLer    // The CTE query body
}

// SQL renders the CTE definition as "name [(columns)] AS (query)".
func (c CTEDef) SQL() string {
	var sb strings.Builder
	sb.WriteString(c.Name)
	if len(c.Columns) > 0 {
		sb.WriteString("(")
		sb.WriteString(strings.Join(c.Columns, ", "))
		sb.WriteString(")")
	}
	sb.WriteString(" AS (\n")
	sb.WriteString(IndentLines(c.Query.SQL(), "    "))
	sb.WriteString("\n)")
	return sb.String()
}

// WithCTE represents a WITH clause wrapping a final query.
// Supports both regular and recursive CTEs.
//
// Example:
//
//	WithCTE{
//	    Recursive: true,
//	    CTEs: []CTEDef{{Name: "accessible", Columns: []string{"object_id", "depth"}, Query: cteQuery}},
//	    Query: finalSelect,
//	}
//
// Renders:
//
//	WITH RECURSIVE accessible(object_id, depth) AS (
//	    <cte query>
//	)
//	<final query>
type WithCTE struct {
	Recursive bool     // If true, renders WITH RECURSIVE
	CTEs      []CTEDef // One or more CTE definitions
	Query     SQLer    // The final SELECT that uses the CTEs
}

// SQL renders the complete WITH clause and final query.
func (w WithCTE) SQL() string {
	if len(w.CTEs) == 0 {
		return w.Query.SQL()
	}

	var sb strings.Builder
	sb.WriteString("WITH ")
	if w.Recursive {
		sb.WriteString("RECURSIVE ")
	}

	cteParts := make([]string, len(w.CTEs))
	for i, cte := range w.CTEs {
		cteParts[i] = cte.SQL()
	}
	sb.WriteString(strings.Join(cteParts, ",\n"))
	sb.WriteString("\n")
	sb.WriteString(w.Query.SQL())

	return sb.String()
}

// RecursiveCTE is a convenience constructor for a single recursive CTE.
func RecursiveCTE(name string, columns []string, cteQuery, finalQuery SQLer) WithCTE {
	return WithCTE{
		Recursive: true,
		CTEs:      []CTEDef{{Name: name, Columns: columns, Query: cteQuery}},
		Query:     finalQuery,
	}
}

// SimpleCTE is a convenience constructor for a single non-recursive CTE.
func SimpleCTE(name string, cteQuery, finalQuery SQLer) WithCTE {
	return WithCTE{
		Recursive: false,
		CTEs:      []CTEDef{{Name: name, Query: cteQuery}},
		Query:     finalQuery,
	}
}

// MultiCTE creates a WITH clause with multiple CTEs.
// Useful for complex queries with subject_pool, base_results, has_wildcard, etc.
func MultiCTE(recursive bool, ctes []CTEDef, finalQuery SQLer) WithCTE {
	return WithCTE{
		Recursive: recursive,
		CTEs:      ctes,
		Query:     finalQuery,
	}
}

// CommentedSQL wraps a query with a SQL comment prefix.
// Useful for adding descriptive comments to parts of a UNION or CTE body.
//
// Example:
//
//	CommentedSQL{Comment: "Base case: seed with starting value", Query: baseQuery}
//
// Renders:
//
//	-- Base case: seed with starting value
//	<base query>
type CommentedSQL struct {
	Comment string // Comment text (without -- prefix)
	Query   SQLer  // The query to render after the comment
}

// SQL renders the comment followed by the query.
func (c CommentedSQL) SQL() string {
	return "-- " + c.Comment + "\n" + c.Query.SQL()
}

// MultiLineComment creates a CommentedSQL with multiple comment lines.
// The query follows after all comment lines.
func MultiLineComment(comments []string, query SQLer) CommentedSQL {
	// Join comments into a single comment block
	commentText := strings.Join(comments, "\n-- ")
	return CommentedSQL{Comment: commentText, Query: query}
}

// SelectIntoVar wraps a query for use with PL/pgSQL SELECT INTO.
// Appends "INTO <variable>" after the query.
//
// Example:
//
//	SelectIntoVar{Query: cteQuery, Variable: "v_max_depth"}
//
// Renders:
//
//	<query> INTO v_max_depth
type SelectIntoVar struct {
	Query    SQLer  // The query (e.g., a CTE)
	Variable string // Variable name to select into
}

// SQL renders the query with INTO clause appended.
func (s SelectIntoVar) SQL() string {
	return s.Query.SQL() + " INTO " + s.Variable
}
