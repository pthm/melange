package doctor

import (
	"fmt"
	"strings"
)

// ViewDefinition represents a parsed melange_tuples view definition.
type ViewDefinition struct {
	Branches []ViewBranch
	HasUnion bool   // bare UNION detected (not UNION ALL)
	RawSQL   string // original SQL from pg_get_viewdef
}

// ViewBranch represents one SELECT in the UNION ALL view.
type ViewBranch struct {
	SourceTable   string            // e.g., "organization_members"
	SourceSchema  string            // e.g., "public"
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

	var parts []string
	remaining := sql
	remainingUpper := upper

	for {
		pos := strings.Index(remainingUpper, "UNION")
		if pos == -1 {
			parts = append(parts, remaining)
			break
		}

		parts = append(parts, remaining[:pos])

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

	// Filter empty parts
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
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
			table := parseFromClause(trimmed)
			if table != "" {
				if parts := strings.SplitN(table, ".", 2); len(parts) == 2 {
					branch.SourceSchema = parts[0]
					branch.SourceTable = parts[1]
				} else {
					branch.SourceTable = table
				}
			}
			continue
		}

		// Skip SELECT keyword line
		if upper == "SELECT" {
			continue
		}

		// Parse column expression: `expr AS alias` or `SELECT expr AS alias`
		expr := trimmed
		if strings.HasPrefix(upper, "SELECT") {
			expr = strings.TrimSpace(trimmed[6:])
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

	if branch.SourceTable == "" {
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

// parseFromClause extracts the table name from a FROM line.
func parseFromClause(line string) string {
	// "FROM schema.table" or "FROM table" possibly with trailing alias
	upper := strings.ToUpper(line)
	idx := strings.Index(upper, "FROM ")
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(line[idx+5:])
	// Take first word (table name), stop at whitespace or semicolon
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimRight(parts[0], ";")
}

// parseColumnExpr parses "expression AS alias" and returns (alias, expression).
func parseColumnExpr(expr string) (alias, sourceExpr string) {
	// Find the last " AS " (case-insensitive) that's not inside a CASE...END
	upper := strings.ToUpper(expr)

	// Find AS outside of CASE/END blocks
	asPos := findLastAS(expr, upper)
	if asPos == -1 {
		return "", ""
	}

	sourceExpr = strings.TrimSpace(expr[:asPos])
	alias = strings.TrimSpace(expr[asPos+4:]) // len(" AS ") == 4

	return alias, sourceExpr
}

// findLastAS finds the position of the last " AS " in the expression,
// skipping any that appear inside CASE...END blocks.
func findLastAS(_, upper string) int {
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
