package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// checkViewDefinition parses the melange_tuples view and emits checks for
// view structure, UNION ALL usage, and discovered source tables.
func (d *Doctor) checkViewDefinition(ctx context.Context, report *Report) error { //nolint:unparam // error return kept for consistent checker interface
	var viewSQL string
	err := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT pg_get_viewdef('%s'::regclass, true)`, d.prefixIdent("melange_tuples")),
	).Scan(&viewSQL)
	if err != nil {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "view_parsed",
			Status:   StatusFail,
			Message:  "Could not retrieve view definition",
			Details:  err.Error(),
		})
		return nil
	}

	vd, err := parseViewSQL(viewSQL)
	if err != nil {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "view_parsed",
			Status:   StatusFail,
			Message:  "Could not parse view definition",
			Details:  err.Error(),
		})
		return nil
	}

	d.viewDef = vd

	report.AddCheck(CheckResult{
		Category: "Performance",
		Name:     "view_parsed",
		Status:   StatusPass,
		Message:  fmt.Sprintf("View definition parsed (%d branches)", len(vd.Branches)),
	})

	// Check for bare UNION
	if vd.HasUnion {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "union_all",
			Status:   StatusWarn,
			Message:  "View uses UNION instead of UNION ALL",
			Details:  "UNION deduplicates rows with a sort, adding overhead. Tuples should be naturally unique per source table.",
			FixHint:  "Replace UNION with UNION ALL in the view definition",
		})
	} else {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "union_all",
			Status:   StatusPass,
			Message:  "View uses UNION ALL (no unnecessary deduplication)",
		})
	}

	// Report source tables
	var tables []string
	for _, b := range vd.Branches {
		for _, t := range b.SourceTables {
			tables = append(tables, t.String())
		}
	}

	report.AddCheck(CheckResult{
		Category: "Performance",
		Name:     "source_tables",
		Status:   StatusPass,
		Message:  fmt.Sprintf("Source tables: %s", strings.Join(uniqueStrings(tables), ", ")),
	})

	return nil
}

// checkExpressionIndexes verifies that source tables have expression indexes
// for ::text casts used in the view. Missing indexes cause sequential scans.
func (d *Doctor) checkExpressionIndexes(ctx context.Context, report *Report) error {
	if d.viewDef == nil {
		return nil
	}

	// Collect all cast columns grouped by table.
	type tableInfo struct {
		schema string
		casts  []CastColumn
	}
	tableCasts := make(map[string]*tableInfo)
	for _, b := range d.viewDef.Branches {
		if len(b.CastColumns) == 0 {
			continue
		}
		for _, t := range b.SourceTables {
			info, ok := tableCasts[t.Name]
			if !ok {
				info = &tableInfo{schema: t.Schema}
				tableCasts[t.Name] = info
			}
			info.casts = append(info.casts, b.CastColumns...)
		}
	}

	if len(tableCasts) == 0 {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "expr_indexes",
			Status:   StatusPass,
			Message:  "No ::text casts in view (no expression indexes needed)",
		})
		return nil
	}

	allCovered := true

	for table, info := range tableCasts {
		// Get all index definitions for this table
		indexDefs, err := d.getTableIndexDefs(ctx, table)
		if err != nil {
			return fmt.Errorf("getting indexes for %s: %w", table, err)
		}

		// Get row count for severity
		rowCount, err := d.getTableRowCount(ctx, table)
		if err != nil {
			// Non-fatal: default to warning severity
			rowCount = 0
		}

		// Deduplicate cast columns by source column
		seen := make(map[string]bool)
		var uniqueCasts []CastColumn
		for _, cc := range info.casts {
			if !seen[cc.SourceColumn] {
				seen[cc.SourceColumn] = true
				uniqueCasts = append(uniqueCasts, cc)
			}
		}

		for _, cc := range uniqueCasts {
			covered := isExpressionIndexed(cc.SourceColumn, indexDefs)
			if !covered {
				allCovered = false
				severity, sizeNote := rowCountSeverity(rowCount)

				tableName := table
				if info.schema != "" {
					tableName = info.schema + "." + table
				}

				report.AddCheck(CheckResult{
					Category: "Performance",
					Name:     "expr_indexes",
					Status:   severity,
					Message:  fmt.Sprintf("Missing expression index on %s.%s::text (%s)", tableName, cc.SourceColumn, sizeNote),
					Details:  fmt.Sprintf("Column %s is cast to text in the view (as %s) but has no expression index", cc.SourceColumn, cc.ViewColumn),
					FixHint:  fmt.Sprintf("CREATE INDEX idx_%s_%s_text ON %s ((%s::text));", table, cc.SourceColumn, tableName, cc.SourceColumn),
				})
			}
		}
	}

	if allCovered {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "expr_indexes",
			Status:   StatusPass,
			Message:  "All ::text cast columns have expression indexes",
		})
	}

	return nil
}

// getTableIndexDefs returns all index definitions for a table.
func (d *Doctor) getTableIndexDefs(ctx context.Context, table string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = %s
		  AND tablename = $1
	`, d.postgresSchema()), table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var defs []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, rows.Err()
}

// getTableRowCount returns the approximate row count from pg_class.reltuples.
func (d *Doctor) getTableRowCount(ctx context.Context, table string) (int64, error) {
	var count float64
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(reltuples, 0)::float8
		FROM pg_class
		WHERE relname = $1
		  AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = %s)
	`, d.postgresSchema()), table).Scan(&count)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	if count < 0 {
		return 0, nil
	}
	return int64(count), nil
}

// isExpressionIndexed checks if any index definition covers a ::text cast on the given column.
// Matches patterns like:
//   - (column::text)
//   - ((column)::text)
//   - composite indexes containing the expression
func isExpressionIndexed(column string, indexDefs []string) bool {
	lowerCol := strings.ToLower(column)
	patterns := []string{
		"(" + lowerCol + "::text)",
		"((" + lowerCol + ")::text)",
	}

	for _, def := range indexDefs {
		lower := strings.ToLower(def)
		for _, pattern := range patterns {
			if strings.Contains(lower, pattern) {
				return true
			}
		}
	}
	return false
}

// tableIndexRecommendation describes a recommended index for the melange_tuples table.
type tableIndexRecommendation struct {
	name    string   // short identifier for reporting
	columns []string // ordered column names
	reason  string   // why this index helps
}

// recommendedTableIndexes defines the indexes that should exist when
// melange_tuples is a regular table. Column order matches the generated
// SQL query patterns so PostgreSQL can use prefix scans.
var recommendedTableIndexes = []tableIndexRecommendation{
	{
		name:    "check_lookup",
		columns: []string{"object_type", "object_id", "relation", "subject_type", "subject_id"},
		reason:  "Covers check_permission and list_subjects queries which filter by (object_type, object_id, relation, subject_type)",
	},
	{
		name:    "list_objects",
		columns: []string{"object_type", "relation", "subject_type", "subject_id", "object_id"},
		reason:  "Covers list_accessible_objects queries which filter by (object_type, relation, subject_type, subject_id)",
	},
}

// checkTableIndexes verifies that melange_tuples has recommended indexes
// for the query patterns generated by melange.
func (d *Doctor) checkTableIndexes(ctx context.Context, report *Report) error { //nolint:unparam // error return kept for consistent checker interface
	indexDefs, err := d.getTableIndexDefs(ctx, "melange_tuples")
	if err != nil {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "table_indexes",
			Status:   StatusWarn,
			Message:  "Could not retrieve index information for melange_tuples",
			Details:  err.Error(),
		})
		return nil
	}

	// Parse column lists from all existing indexes.
	var existingIndexes [][]string
	for _, def := range indexDefs {
		if cols := parseIndexColumns(def); len(cols) > 0 {
			existingIndexes = append(existingIndexes, cols)
		}
	}

	// Get row count for severity.
	rowCount, _ := d.getTableRowCount(ctx, "melange_tuples")

	allCovered := true
	for _, rec := range recommendedTableIndexes {
		covered := false
		for _, existing := range existingIndexes {
			if hasColumnPrefix(existing, rec.columns) {
				covered = true
				break
			}
		}
		if !covered {
			allCovered = false
			severity, sizeNote := rowCountSeverity(rowCount)

			report.AddCheck(CheckResult{
				Category: "Performance",
				Name:     "table_indexes",
				Status:   severity,
				Message:  fmt.Sprintf("Missing recommended index for %s (%s)", rec.name, sizeNote),
				Details:  rec.reason,
				FixHint:  fmt.Sprintf("CREATE INDEX idx_melange_tuples_%s ON melange_tuples (%s);", rec.name, strings.Join(rec.columns, ", ")),
			})
		}
	}

	if allCovered {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "table_indexes",
			Status:   StatusPass,
			Message:  "melange_tuples has recommended indexes for query patterns",
		})
	}

	return nil
}

// parseIndexColumns extracts the ordered column names from a CREATE INDEX
// definition string. Returns nil for expression indexes or unparseable input.
func parseIndexColumns(indexdef string) []string {
	openParen := strings.LastIndex(indexdef, "(")
	if openParen == -1 {
		return nil
	}
	closeParen := strings.LastIndex(indexdef, ")")
	if closeParen <= openParen {
		return nil
	}
	colStr := indexdef[openParen+1 : closeParen]

	var columns []string
	for _, col := range strings.Split(colStr, ",") {
		col = strings.TrimSpace(col)
		if col == "" {
			continue
		}
		// Expression indexes contain parens, casts, or function calls.
		if strings.ContainsAny(col, "(::") {
			return nil
		}
		columns = append(columns, col)
	}
	return columns
}

// hasColumnPrefix reports whether an existing index's columns start with the
// recommended column sequence. A broader index satisfies a narrower recommendation.
func hasColumnPrefix(existing, recommended []string) bool {
	if len(existing) < len(recommended) {
		return false
	}
	for i, col := range recommended {
		if !strings.EqualFold(existing[i], col) {
			return false
		}
	}
	return true
}

// rowCountSeverity returns a status and human-readable size note based on the
// approximate row count of a table. Tables with >= 10000 rows are critical;
// smaller tables get a warning.
func rowCountSeverity(rowCount int64) (severity Status, sizeNote string) {
	if rowCount >= 10000 {
		return StatusFail, fmt.Sprintf("critical at current scale (~%d rows)", rowCount)
	}
	return StatusWarn, "recommended for future scaling"
}

// uniqueStrings returns unique strings preserving order.
func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
