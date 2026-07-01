package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pthm/melange/lib/sqlgen"
	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// checkViewDefinition parses the melange_tuples view and emits checks for
// view structure, UNION ALL usage, and discovered source tables.
func (d *Doctor) checkViewDefinition(ctx context.Context, report *Report) error { //nolint:unparam // error return kept for consistent checker interface
	var viewSQL string
	err := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT pg_get_viewdef(%s::regclass, true)`, sqldsl.QuoteLiteral(d.prefixIdent("melange_tuples"))),
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

// existingIndex captures the structural details of an index discovered via
// pg_indexes that the coverage check cares about: ordered column list plus
// the partial-index predicate (empty for full indexes).
type existingIndex struct {
	columns   []string
	predicate string // empty if not a partial index
}

// checkTableIndexes verifies that melange_tuples has indexes covering the
// access patterns generated by melange. The recommendations come from
// sqlgen.RecommendIndexes() so they always match the user's actual schema
// rather than a hardcoded subset.
func (d *Doctor) checkTableIndexes(ctx context.Context, report *Report) error { //nolint:unparam // error return kept for consistent checker interface
	analyses := d.getAnalyses()
	if analyses == nil {
		// No parsed schema means recommendations are impossible. The schema
		// file check has already reported the underlying problem.
		return nil
	}
	recs := sqlgen.RecommendIndexes(analyses)
	if len(recs) == 0 {
		return nil
	}

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

	existing := parseExistingIndexes(indexDefs)
	rowCount, _ := d.getTableRowCount(ctx, "melange_tuples")

	allCovered := true
	for _, rec := range recs {
		if indexCoversRecommendation(existing, rec) {
			continue
		}
		allCovered = false
		severity, sizeNote := rowCountSeverity(rowCount)

		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "table_indexes",
			Status:   severity,
			Message:  fmt.Sprintf("Missing recommended index on (%s)%s (%s)", strings.Join(rec.Columns, ", "), partialSuffix(rec), sizeNote),
			Details:  fmt.Sprintf("Benefits %d generated function(s): %s", len(rec.BenefitsFunctions), strings.Join(rec.BenefitsFunctions, ", ")),
			FixHint:  rec.DDL,
		})
	}

	if allCovered {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "table_indexes",
			Status:   StatusPass,
			Message:  fmt.Sprintf("melange_tuples has all %d recommended indexes", len(recs)),
		})
	}

	return nil
}

// checkExpandFanoutAdvisory emits a schema-derived advisory when the
// schema has relations that can produce large Expand responses. The
// heuristic is intentionally coarse — Expand's response size is
// data-driven (bounded by tuple counts, not schema shape) so we can't
// precisely predict which relations will explode. What we CAN detect:
//
//   - Wildcard grants (`[user:*]`): every Expand for that (object,
//     relation) surfaces the wildcard, and downstream consumers that
//     enumerate the wildcard client-side hit unbounded fan-out.
//   - Recursive TTU (`viewer from parent` where parent chain can be
//     long): ExpandRecursive walks the pointer chain, so a deep parent
//     hierarchy multiplies the round-trip cost.
//
// When any relation matches either signal, we surface one advisory
// that recommends `melange.max_expand_leaf` as a session guardrail.
// StatusPass — never escalates — because the schema signal is a hint,
// not a diagnosed problem.
func (d *Doctor) checkExpandFanoutAdvisory(_ context.Context, report *Report) error { //nolint:unparam // error return kept for consistent checker interface
	analyses := d.getAnalyses()
	if analyses == nil {
		return nil
	}

	var wildcardRels, recursiveRels []string
	for _, a := range analyses {
		if a.Features.HasWildcard {
			wildcardRels = append(wildcardRels, a.ObjectType+"#"+a.Relation)
		}
		if a.Features.HasRecursive {
			recursiveRels = append(recursiveRels, a.ObjectType+"#"+a.Relation)
		}
	}
	if len(wildcardRels) == 0 && len(recursiveRels) == 0 {
		return nil
	}

	var details strings.Builder
	details.WriteString("Expand responses on these relations can grow large depending on tuple counts. ")
	details.WriteString("Consider setting a per-leaf cap so admin flows don't blow past a reasonable ")
	details.WriteString("response size:\n\n")
	details.WriteString("    SET melange.max_expand_leaf = 1000;\n")
	details.WriteString("    -- or per-call: Checker.Expand(..., WithExpandMaxLeaf(1000))\n\n")
	if len(wildcardRels) > 0 {
		fmt.Fprintf(&details, "Wildcard grants (%d relation(s)) — every Expand surfaces the wildcard entry (`user:*`):\n", len(wildcardRels))
		for _, r := range wildcardRels {
			details.WriteString("  - " + r + "\n")
		}
	}
	if len(recursiveRels) > 0 {
		if len(wildcardRels) > 0 {
			details.WriteByte('\n')
		}
		fmt.Fprintf(&details, "Recursive TTU (%d relation(s)) — ExpandRecursive walks the parent chain per call:\n", len(recursiveRels))
		for _, r := range recursiveRels {
			details.WriteString("  - " + r + "\n")
		}
	}

	report.AddCheck(CheckResult{
		Category: "Performance",
		Name:     "expand_fanout_advisory",
		Status:   StatusPass,
		Message:  fmt.Sprintf("%d relation(s) can produce large Expand responses — see --verbose for guardrail", len(wildcardRels)+len(recursiveRels)),
		Details:  details.String(),
	})
	return nil
}

// checkSourceTableIndexAdvisory emits the schema-derived index recommendations
// as advisory output when melange_tuples is a view. PostgreSQL won't allow
// CREATE INDEX directly on a view, so the user must translate the DDL to the
// source tables that back the view's UNION ALL branches — but they need to
// know which indexes to create, and this is the most discoverable place.
//
// Unlike checkTableIndexes, this never escalates to fail: even if all
// recommendations are missing, we can't auto-detect coverage across multiple
// source tables, and the user owns those schemas.
func (d *Doctor) checkSourceTableIndexAdvisory(_ context.Context, report *Report) error { //nolint:unparam // error return kept for consistent checker interface
	analyses := d.getAnalyses()
	if analyses == nil {
		return nil
	}
	recs := sqlgen.RecommendIndexes(analyses)
	if len(recs) == 0 {
		return nil
	}

	var details strings.Builder
	details.WriteString("Apply these to whichever source table(s) back the melange_tuples view. ")
	details.WriteString("Replace 'melange_tuples' with the actual table name:\n\n")
	for i, rec := range recs {
		if i > 0 {
			details.WriteByte('\n')
		}
		details.WriteString(rec.DDL)
	}

	report.AddCheck(CheckResult{
		Category: "Performance",
		Name:     "source_table_indexes_advisory",
		Status:   StatusPass,
		Message:  fmt.Sprintf("%d schema-derived index recommendation(s) — see --verbose for DDL", len(recs)),
		Details:  details.String(),
	})
	return nil
}

// parseExistingIndexes converts raw pg_indexes definitions into the
// structural form the coverage check needs. Expression-only indexes are
// dropped (parseIndexColumns returns nil), matching today's behavior.
func parseExistingIndexes(defs []string) []existingIndex {
	out := make([]existingIndex, 0, len(defs))
	for _, def := range defs {
		cols := parseIndexColumns(def)
		if len(cols) == 0 {
			continue
		}
		out = append(out, existingIndex{
			columns:   cols,
			predicate: parseIndexPredicate(def),
		})
	}
	return out
}

// indexCoversRecommendation reports whether any existing index already
// satisfies the recommendation. For partial recommendations (those with a
// WHERE clause), an existing partial index must have a matching predicate;
// a full index without the WHERE filter is not equivalent.
func indexCoversRecommendation(existing []existingIndex, rec sqlgen.IndexRecommendation) bool {
	for _, ex := range existing {
		if !hasColumnPrefix(ex.columns, rec.Columns) {
			continue
		}
		if rec.WhereClause == "" {
			// Full recommendation: any covering index works (PG handles the
			// extra rows the index might index but the query doesn't need).
			return true
		}
		// Partial recommendation: require the existing index to also be
		// partial with the same predicate. Full indexes are a superset in
		// rows but PG can't use them as a substitute for a partial index
		// with a matching predicate when the query also has the predicate.
		if predicatesEquivalent(ex.predicate, rec.WhereClause) {
			return true
		}
	}
	return false
}

// predicatesEquivalent reports whether two index predicates are semantically
// the same. PostgreSQL renders predicates in canonical form (e.g.
// "(subject_id = '*'::text)") via pg_indexes; our recommendations use a
// minimal form ("subject_id = '*'"). We strip parens, casts, and whitespace
// from both and compare.
func predicatesEquivalent(have, want string) bool {
	return canonicalizePredicate(have) == canonicalizePredicate(want)
}

func canonicalizePredicate(p string) string {
	p = strings.ToLower(p)
	p = strings.ReplaceAll(p, "::text", "")
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "(", "")
	p = strings.ReplaceAll(p, ")", "")
	return p
}

// parseIndexPredicate extracts the WHERE clause from a CREATE INDEX
// definition string. Returns empty if the index is not partial.
//
// pg_indexes renders partial indexes as: `CREATE INDEX ... USING btree (cols) WHERE (predicate)`.
// The predicate itself may contain parens (PG's canonical form wraps the
// predicate, e.g. `WHERE (subject_id = '*'::text)`), so we cannot rely on
// LastIndex(")") to find the column-list boundary — that paren is at the
// end of the predicate. Splitting on the `) WHERE ` separator gives the
// correct cut.
func parseIndexPredicate(indexdef string) string {
	_, pred := splitIndexKeysAndPredicate(indexdef)
	return pred
}

// splitIndexKeysAndPredicate splits a CREATE INDEX definition into the
// keys portion (`CREATE INDEX ... (cols)`) and an optional predicate
// (everything after the WHERE keyword, trimmed). Empty keys signal an
// unparseable definition.
func splitIndexKeysAndPredicate(indexdef string) (keys, predicate string) {
	// Find " WHERE " (or " where ") that sits at the top level — i.e. not
	// inside any parens. PG always emits it with spaces and the keys-closing
	// `)` immediately before, so requiring `) WHERE ` is reliable enough.
	for _, sep := range []string{") WHERE ", ") where "} {
		if i := strings.Index(indexdef, sep); i != -1 {
			return indexdef[:i+1], strings.TrimSpace(indexdef[i+len(sep):])
		}
	}
	return indexdef, ""
}

// partialSuffix returns " (partial)" for partial-index recommendations and
// the empty string for full indexes; used in human-readable Messages.
func partialSuffix(rec sqlgen.IndexRecommendation) string {
	if rec.WhereClause != "" {
		return " WHERE " + rec.WhereClause
	}
	return ""
}

// parseIndexColumns extracts the ordered column names from a CREATE INDEX
// definition string. Returns nil for expression indexes or unparseable input.
//
// Handles both full indexes (`CREATE INDEX ... USING btree (cols)`) and
// partial indexes (`CREATE INDEX ... USING btree (cols) WHERE (pred)`). For
// partials, the WHERE clause is stripped before column extraction so the
// predicate's own parens don't confuse the column parser.
func parseIndexColumns(indexdef string) []string {
	keys, _ := splitIndexKeysAndPredicate(indexdef)
	if keys == "" {
		return nil
	}
	openParen := strings.LastIndex(keys, "(")
	if openParen == -1 {
		return nil
	}
	closeParen := strings.LastIndex(keys, ")")
	if closeParen <= openParen {
		return nil
	}
	colStr := keys[openParen+1 : closeParen]

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
