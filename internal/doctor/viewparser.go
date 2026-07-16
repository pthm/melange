package doctor

import (
	"fmt"
	"strings"
)

// TableRef identifies a source table, optionally schema-qualified.
type TableRef struct {
	Schema string // e.g., "public"
	Name   string // e.g., "organization_members"
}

// String returns the optionally schema-qualified table name.
func (t TableRef) String() string {
	if t.Schema != "" {
		return t.Schema + "." + t.Name
	}
	return t.Name
}

// ViewDefinition represents a parsed melange_tuples view definition.
type ViewDefinition struct {
	Branches []ViewBranch
	HasUnion bool   // bare UNION detected (not UNION ALL)
	RawSQL   string // original SQL from pg_get_viewdef
}

// ViewBranch represents one SELECT in the UNION ALL view.
type ViewBranch struct {
	SourceTables  []TableRef        // tables referenced in the FROM clause
	ColumnMapping map[string]string // view alias -> source expression
	CastColumns   []CastColumn
}

// CastColumn represents a column with a ::text cast in the view.
type CastColumn struct {
	ViewColumn   string // e.g., "object_id"
	SourceColumn string // e.g., "organization_id"
	CastType     string // e.g., "text"
}

// parseViewSQL parses the SQL output of pg_get_viewdef() for a melange_tuples view.
// It extracts source tables, column mappings, and cast expressions from each UNION ALL branch.
func parseViewSQL(sql string) (*ViewDefinition, error) {
	if strings.TrimSpace(sql) == "" {
		return nil, fmt.Errorf("empty view definition")
	}

	vd := &ViewDefinition{RawSQL: sql}

	// Detect bare UNION (not UNION ALL).
	// We check for "UNION" that is NOT followed by "ALL" (case-insensitive).
	vd.HasUnion = hasBareUnion(sql)

	// Split on UNION ALL boundaries (case-insensitive).
	branches := splitUnionAll(sql)
	if len(branches) == 0 {
		return nil, fmt.Errorf("no SELECT branches found in view definition")
	}

	for i, branchSQL := range branches {
		branch, err := parseBranch(branchSQL)
		if err != nil {
			return nil, fmt.Errorf("parsing branch %d: %w", i+1, err)
		}
		vd.Branches = append(vd.Branches, *branch)
	}

	return vd, nil
}

// hasBareUnion returns true if the SQL contains UNION without ALL.
func hasBareUnion(sql string) bool {
	upper := strings.ToUpper(sql)
	idx := 0
	for {
		pos := strings.Index(upper[idx:], "UNION")
		if pos == -1 {
			return false
		}
		pos += idx
		// Check what follows UNION (skip whitespace)
		after := strings.TrimLeft(upper[pos+5:], " \t\n\r")
		if !strings.HasPrefix(after, "ALL") {
			return true
		}
		idx = pos + 5
	}
}

// splitUnionAll splits SQL on UNION boundaries (both UNION ALL and bare UNION),
// returning the individual SELECT statements.
func splitUnionAll(sql string) []string {
	upper := strings.ToUpper(sql)

	var result []string
	remaining := sql
	remainingUpper := upper

	for {
		pos := strings.Index(remainingUpper, "UNION")
		if pos == -1 {
			if trimmed := strings.TrimSpace(remaining); trimmed != "" {
				result = append(result, trimmed)
			}
			break
		}

		if trimmed := strings.TrimSpace(remaining[:pos]); trimmed != "" {
			result = append(result, trimmed)
		}

		// Skip "UNION" and optional "ALL"
		skip := 5 // len("UNION")
		after := strings.TrimLeft(remainingUpper[pos+5:], " \t\n\r")
		if strings.HasPrefix(after, "ALL") {
			// Find position of "ALL" in original
			allPos := strings.Index(remainingUpper[pos+5:], "ALL")
			skip = 5 + allPos + 3
		}

		remaining = remaining[pos+skip:]
		remainingUpper = remainingUpper[pos+skip:]
	}
	return result
}

// parseBranch parses a single SELECT branch from the view.
func parseBranch(sql string) (*ViewBranch, error) {
	branch := &ViewBranch{
		ColumnMapping: make(map[string]string),
	}

	// Remove trailing semicolons
	sql = strings.TrimRight(strings.TrimSpace(sql), ";")

	// First pass: join multiline CASE...END into single logical lines.
	lines := joinCaseExpressions(strings.Split(sql, "\n"))
	// Second pass: join multi-line FROM clauses (comma-separated tables).
	lines = joinFromClauses(lines)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		// Skip empty lines, comments, WHERE clauses
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.HasPrefix(upper, "WHERE") || strings.HasPrefix(upper, "AND ") {
			continue
		}

		// Parse FROM clause
		if strings.HasPrefix(upper, "FROM ") {
			branch.SourceTables = parseFromTables(trimmed)
			continue
		}

		// Strip leading SELECT keyword; skip if nothing follows.
		expr := trimmed
		if strings.HasPrefix(upper, "SELECT") {
			expr = strings.TrimSpace(trimmed[len("SELECT"):])
			if expr == "" {
				continue
			}
		}

		// Remove trailing comma
		expr = strings.TrimRight(expr, ",")
		expr = strings.TrimSpace(expr)

		if expr == "" {
			continue
		}

		alias, sourceExpr := parseColumnExpr(expr)
		if alias != "" && sourceExpr != "" {
			branch.ColumnMapping[alias] = sourceExpr

			// Detect ::text casts
			if cast := detectCast(alias, sourceExpr); cast != nil {
				branch.CastColumns = append(branch.CastColumns, *cast)
			}
		}
	}

	if len(branch.SourceTables) == 0 {
		return nil, fmt.Errorf("no FROM clause found")
	}

	return branch, nil
}

// joinCaseExpressions merges multiline CASE...END blocks into single lines.
func joinCaseExpressions(lines []string) []string {
	var result []string
	var caseBuffer []string
	depth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		if depth > 0 {
			caseBuffer = append(caseBuffer, trimmed)
			if hasWord(upper, "CASE") {
				depth++
			}
			if hasWord(upper, "END") {
				depth--
			}
			if depth <= 0 {
				result = append(result, strings.Join(caseBuffer, " "))
				caseBuffer = nil
				depth = 0
			}
			continue
		}

		// Check if this line starts a CASE expression that doesn't close on the same line
		if hasWord(upper, "CASE") && !hasWord(upper, "END") {
			depth = 1
			caseBuffer = []string{trimmed}
			continue
		}

		result = append(result, line)
	}

	// If we still have a buffer (shouldn't happen with valid SQL), flush it
	if len(caseBuffer) > 0 {
		result = append(result, strings.Join(caseBuffer, " "))
	}

	return result
}

// hasWord checks if the word appears as a standalone word in the string.
func hasWord(s, word string) bool {
	idx := strings.Index(s, word)
	if idx == -1 {
		return false
	}
	// Check boundaries
	if idx > 0 && isIdentChar(s[idx-1]) {
		return false
	}
	end := idx + len(word)
	if end < len(s) && isIdentChar(s[end]) {
		return false
	}
	return true
}

// joinFromClauses joins multi-line FROM clauses into single lines.
// Handles two continuation patterns:
//  1. Comma-separated tables: FROM a,\n  b  →  FROM a, b
//  2. Explicit JOINs: FROM a\n  JOIN b ON ...  →  FROM a JOIN b ON ...
func joinFromClauses(lines []string) []string {
	var result []string
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		upper := strings.ToUpper(trimmed)

		if !strings.HasPrefix(upper, "FROM ") {
			result = append(result, lines[i])
			i++
			continue
		}

		joined := trimmed
		for i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			nextUpper := strings.ToUpper(next)

			// Comma continuation: current line ends with comma, next is a table name.
			if strings.HasSuffix(strings.TrimSpace(joined), ",") {
				if next == "" ||
					strings.HasPrefix(nextUpper, "WHERE") ||
					strings.HasPrefix(nextUpper, "SELECT") ||
					strings.HasPrefix(nextUpper, "UNION") {
					break
				}
				i++
				joined = joined + " " + next
				continue
			}

			// JOIN continuation: next line starts with a JOIN keyword.
			if isJoinKeyword(nextUpper) {
				i++
				joined = joined + " " + next
				continue
			}

			break
		}
		result = append(result, joined)
		i++
	}
	return result
}

// isJoinKeyword reports whether the line starts with a SQL JOIN keyword.
func isJoinKeyword(upper string) bool {
	prefixes := []string{
		"JOIN ", "INNER JOIN ", "CROSS JOIN ",
		"LEFT JOIN ", "LEFT OUTER JOIN ",
		"RIGHT JOIN ", "RIGHT OUTER JOIN ",
		"FULL JOIN ", "FULL OUTER JOIN ",
		"NATURAL JOIN ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// parseFromTables extracts all table references from a FROM clause.
// Handles comma-separated cross joins, explicit JOINs, the ONLY keyword,
// schema-qualified names, and quoted identifiers.
func parseFromTables(line string) []TableRef {
	upper := strings.ToUpper(line)
	idx := strings.Index(upper, "FROM ")
	if idx == -1 {
		return nil
	}
	rest := strings.TrimSpace(line[idx+5:])

	// Split on JOIN keywords first, then on commas within each segment.
	segments := splitOnJoins(rest)

	var tables []TableRef
	for _, seg := range segments {
		// Remove ON/USING conditions so they don't interfere with comma splitting.
		seg = cutJoinCondition(seg)

		for _, part := range strings.Split(seg, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// First word is the table name; ignore aliases.
			words := strings.Fields(part)
			if len(words) == 0 {
				continue
			}
			name := strings.TrimRight(words[0], ";")

			// Skip PostgreSQL's ONLY keyword (table inheritance).
			if strings.EqualFold(name, "ONLY") {
				if len(words) > 1 {
					name = strings.TrimRight(words[1], ";")
				} else {
					continue
				}
			}

			if name == "" {
				continue
			}

			ref := parseTableRef(name)
			tables = append(tables, ref)
		}
	}
	return tables
}

// splitOnJoins splits a FROM clause body on JOIN keywords, returning each
// segment (the text before/between/after JOINs). The JOIN keyword itself
// is consumed.
func splitOnJoins(s string) []string {
	upper := strings.ToUpper(s)
	var segments []string
	remaining := s
	remainingUpper := upper

	for {
		best := -1
		bestLen := 0
		for _, kw := range joinKeywords {
			if pos := strings.Index(remainingUpper, kw); pos != -1 && (best == -1 || pos < best) {
				best = pos
				bestLen = len(kw)
			}
		}
		if best == -1 {
			segments = append(segments, remaining)
			break
		}
		segments = append(segments, remaining[:best])
		remaining = remaining[best+bestLen:]
		remainingUpper = remainingUpper[best+bestLen:]
	}
	return segments
}

var joinKeywords = []string{
	" NATURAL JOIN ", " CROSS JOIN ",
	" LEFT OUTER JOIN ", " RIGHT OUTER JOIN ", " FULL OUTER JOIN ",
	" INNER JOIN ", " LEFT JOIN ", " RIGHT JOIN ", " FULL JOIN ",
	" JOIN ",
}

// cutJoinCondition removes an ON or USING clause from a FROM/JOIN segment
// so that table names can be extracted cleanly.
func cutJoinCondition(s string) string {
	upper := strings.ToUpper(s)
	if idx := strings.Index(upper, " ON "); idx != -1 {
		return s[:idx]
	}
	if idx := strings.Index(upper, " USING "); idx != -1 {
		return s[:idx]
	}
	return s
}

// parseTableRef parses a possibly schema-qualified, possibly quoted table name
// like `"MySchema"."MyTable"` or `public.users` into a TableRef.
func parseTableRef(name string) TableRef {
	// Split on "." that separates quoted identifiers (e.g., "schema"."table").
	if idx := strings.Index(name, "\".\""); idx != -1 {
		return TableRef{
			Schema: unquoteIdentifier(name[:idx+1]),
			Name:   unquoteIdentifier(name[idx+2:]),
		}
	}
	// Split on plain dot for unquoted schema.table.
	if dotParts := strings.SplitN(name, ".", 2); len(dotParts) == 2 {
		return TableRef{
			Schema: unquoteIdentifier(dotParts[0]),
			Name:   unquoteIdentifier(dotParts[1]),
		}
	}
	return TableRef{Name: unquoteIdentifier(name)}
}

// unquoteIdentifier strips surrounding double quotes from a SQL identifier.
func unquoteIdentifier(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// parseColumnExpr parses "expression AS alias" and returns (alias, expression).
func parseColumnExpr(expr string) (alias, sourceExpr string) {
	// Find the last " AS " (case-insensitive) that's not inside a CASE...END
	upper := strings.ToUpper(expr)

	// Find AS outside of CASE/END blocks
	asPos := findLastAS(upper)
	if asPos == -1 {
		return "", ""
	}

	sourceExpr = strings.TrimSpace(expr[:asPos])
	alias = strings.TrimSpace(expr[asPos+4:]) // len(" AS ") == 4

	return alias, sourceExpr
}

// findLastAS finds the position of the last " AS " in the expression,
// skipping any that appear inside CASE...END blocks.
func findLastAS(upper string) int {
	// Find all " AS " positions
	lastPos := -1
	depth := 0 // CASE nesting depth
	i := 0
	for i < len(upper) {
		if i+4 <= len(upper) && upper[i:i+4] == "CASE" && (i == 0 || !isIdentChar(upper[i-1])) {
			depth++
			i += 4
			continue
		}
		if i+3 <= len(upper) && upper[i:i+3] == "END" && (i == 0 || !isIdentChar(upper[i-1])) {
			if depth > 0 {
				depth--
			}
			i += 3
			continue
		}
		if depth == 0 && i+4 <= len(upper) && upper[i:i+4] == " AS " {
			lastPos = i
		}
		i++
	}
	return lastPos
}

func isIdentChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

// detectCast checks if a source expression contains a ::text cast on a column
// and returns the CastColumn. It scans for all ::text occurrences and picks
// the first that casts a real column (not a string literal).
// Handles patterns like:
//   - "user_id::text"
//   - "(user_id)::text"
//   - "CASE WHEN ... THEN '*'::text ELSE user_id::text END"
func detectCast(alias, expr string) *CastColumn {
	upper := strings.ToUpper(expr)
	searchFrom := 0

	for {
		idx := strings.Index(upper[searchFrom:], "::TEXT")
		if idx == -1 {
			return nil
		}
		idx += searchFrom

		col := extractCastSource(expr[:idx])
		if col != "" {
			return &CastColumn{
				ViewColumn:   alias,
				SourceColumn: col,
				CastType:     "text",
			}
		}

		searchFrom = idx + 6 // skip past "::TEXT"
	}
}

// extractCastSource extracts the column name from before a ::text cast.
// Returns the column name if the cast is on a real column, or empty string
// if the cast is on a string literal or complex expression.
func extractCastSource(before string) string {
	before = strings.TrimSpace(before)
	if before == "" {
		return ""
	}

	// Skip string literals like 'value'
	if strings.HasSuffix(before, "'") {
		return ""
	}

	// Handle CASE WHEN ... ELSE col — extract the last token
	upper := strings.ToUpper(before)
	if strings.Contains(upper, "CASE") {
		if idx := strings.LastIndex(upper, "ELSE"); idx != -1 {
			before = strings.TrimSpace(before[idx+4:])
		} else if idx := strings.LastIndex(upper, "THEN"); idx != -1 {
			before = strings.TrimSpace(before[idx+4:])
		} else {
			return ""
		}
	}

	// Remove surrounding parentheses
	if strings.HasPrefix(before, "(") && strings.HasSuffix(before, ")") {
		before = before[1 : len(before)-1]
	}

	before = strings.TrimSpace(before)

	// Skip if it's a string literal
	if strings.HasPrefix(before, "'") {
		return ""
	}

	// Handle table-qualified names: table.column -> column
	if isValidQualifiedName(before) {
		if dot := strings.LastIndex(before, "."); dot != -1 {
			return before[dot+1:]
		}
		return before
	}

	return ""
}

// isValidIdentifier checks if a string looks like a SQL identifier.
func isValidIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if i == 0 {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' {
				return false
			}
		} else {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
				return false
			}
		}
	}
	return true
}

// isValidQualifiedName checks if a string looks like a SQL identifier,
// optionally table-qualified (e.g., "table.column" or just "column").
func isValidQualifiedName(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) > 3 { // schema.table.column at most
		return false
	}
	for _, p := range parts {
		if !isValidIdentifier(p) {
			return false
		}
	}
	return true
}
