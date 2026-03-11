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
		`SELECT pg_get_viewdef('melange_tuples'::regclass, true)`,
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
		name := b.SourceTable
		if b.SourceSchema != "" {
			name = b.SourceSchema + "." + name
		}
		tables = append(tables, name)
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
		info, ok := tableCasts[b.SourceTable]
		if !ok {
			info = &tableInfo{schema: b.SourceSchema}
			tableCasts[b.SourceTable] = info
		}
		info.casts = append(info.casts, b.CastColumns...)
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
				severity := StatusWarn
				sizeNote := "recommended for future scaling"
				if rowCount >= 10000 {
					severity = StatusFail
					sizeNote = fmt.Sprintf("critical at current scale (~%d rows)", rowCount)
				}

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
	rows, err := d.db.QueryContext(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND tablename = $1
	`, table)
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
	err := d.db.QueryRowContext(ctx, `
		SELECT COALESCE(reltuples, 0)::float8
		FROM pg_class
		WHERE relname = $1
		  AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())
	`, table).Scan(&count)
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
