// Package inline provides inline SQL model data and typed VALUES rows.
package inline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pthm/melange/internal/sqlgen/analysis"
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// InlineSQLData contains SQL VALUES payloads that replace database-backed model tables.
// Rationale: Model data is inlined into SQL VALUES clauses rather than querying
// database tables. This eliminates the need for persistent melange_model tables
// and ensures generated functions are self-contained. When the schema changes,
// migration regenerates all functions with updated inline data. This approach
// trades function size for runtime simplicity and removes a JOIN from every check.
type InlineSQLData struct {
	// ClosureValues contains tuples of (object_type, relation, satisfying_relation).
	// Deprecated: Use ClosureRows for new code.
	ClosureValues string
	// UsersetValues contains tuples of (object_type, relation, subject_type, subject_relation).
	// Deprecated: Use UsersetRows for new code.
	UsersetValues string

	// ClosureRows contains typed expression rows for closure data.
	// Each row has 3 columns: object_type, relation, satisfying_relation.
	ClosureRows []sqldsl.ValuesRow
	// UsersetRows contains typed expression rows for userset data.
	// Each row has 4 columns: object_type, relation, subject_type, subject_relation.
	UsersetRows []sqldsl.ValuesRow
}

func buildInlineSQLData(closureRows []analysis.ClosureRow, analyses []analysis.RelationAnalysis) InlineSQLData {
	return InlineSQLData{
		ClosureValues: BuildClosureValues(closureRows),
		UsersetValues: buildUsersetValues(analyses),
		ClosureRows:   BuildClosureTypedRows(closureRows),
		UsersetRows:   BuildUsersetTypedRows(analyses),
	}
}

// BuildInlineSQLData exposes inline SQL generation for tools and tests.
func BuildInlineSQLData(closureRows []analysis.ClosureRow, analyses []analysis.RelationAnalysis) InlineSQLData {
	return buildInlineSQLData(closureRows, analyses)
}

// BuildClosureValues builds string-based closure VALUES content.
func BuildClosureValues(closureRows []analysis.ClosureRow) string {
	if len(closureRows) == 0 {
		return "(NULL::TEXT, NULL::TEXT, NULL::TEXT)"
	}

	rows := make([]string, 0, len(closureRows))
	for _, row := range closureRows {
		rows = append(rows, fmt.Sprintf("('%s', '%s', '%s')",
			escapeSQLLiteral(row.ObjectType),
			escapeSQLLiteral(row.Relation),
			escapeSQLLiteral(row.SatisfyingRelation),
		))
	}
	sort.Strings(rows)
	return strings.Join(rows, ", ")
}

func buildUsersetValues(analyses []analysis.RelationAnalysis) string {
	seen := make(map[string]struct{})
	var rows []string

	for _, a := range analyses {
		for _, pattern := range a.UsersetPatterns {
			key := strings.Join([]string{
				a.ObjectType,
				a.Relation,
				pattern.SubjectType,
				pattern.SubjectRelation,
			}, "\x00")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', '%s')",
				escapeSQLLiteral(a.ObjectType),
				escapeSQLLiteral(a.Relation),
				escapeSQLLiteral(pattern.SubjectType),
				escapeSQLLiteral(pattern.SubjectRelation),
			))
		}
	}

	if len(rows) == 0 {
		return "(NULL::TEXT, NULL::TEXT, NULL::TEXT, NULL::TEXT)"
	}

	sort.Strings(rows)
	return strings.Join(rows, ", ")
}

func escapeSQLLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// =============================================================================
// Typed Row Builders (Phase 5)
// =============================================================================

// BuildClosureTypedRows builds typed ValuesRow slices for closure data.
// Returns nil for empty input (TypedValuesTable handles empty case).
func BuildClosureTypedRows(closureRows []analysis.ClosureRow) []sqldsl.ValuesRow {
	if len(closureRows) == 0 {
		return nil
	}

	// Build rows with sort keys
	type rowWithKey struct {
		key string
		row sqldsl.ValuesRow
	}
	keyed := make([]rowWithKey, 0, len(closureRows))
	for _, cr := range closureRows {
		keyed = append(keyed, rowWithKey{
			key: cr.ObjectType + "\x00" + cr.Relation + "\x00" + cr.SatisfyingRelation,
			row: sqldsl.ValuesRow{sqldsl.Lit(cr.ObjectType), sqldsl.Lit(cr.Relation), sqldsl.Lit(cr.SatisfyingRelation)},
		})
	}

	// Sort by key for deterministic output
	sort.Slice(keyed, func(i, j int) bool {
		return keyed[i].key < keyed[j].key
	})

	// Extract sorted rows
	result := make([]sqldsl.ValuesRow, len(keyed))
	for i, k := range keyed {
		result[i] = k.row
	}
	return result
}

// BuildUsersetTypedRows builds typed ValuesRow slices for userset data.
// Returns nil for empty input (TypedValuesTable handles empty case).
func BuildUsersetTypedRows(analyses []analysis.RelationAnalysis) []sqldsl.ValuesRow {
	seen := make(map[string]struct{})
	type rowWithKey struct {
		key string
		row sqldsl.ValuesRow
	}
	var keyed []rowWithKey

	for _, a := range analyses {
		for _, pattern := range a.UsersetPatterns {
			key := strings.Join([]string{
				a.ObjectType,
				a.Relation,
				pattern.SubjectType,
				pattern.SubjectRelation,
			}, "\x00")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keyed = append(keyed, rowWithKey{
				key: key,
				row: sqldsl.ValuesRow{
					sqldsl.Lit(a.ObjectType),
					sqldsl.Lit(a.Relation),
					sqldsl.Lit(pattern.SubjectType),
					sqldsl.Lit(pattern.SubjectRelation),
				},
			})
		}
	}

	if len(keyed) == 0 {
		return nil
	}

	// Sort by key for deterministic output
	sort.Slice(keyed, func(i, j int) bool {
		return keyed[i].key < keyed[j].key
	})

	// Extract sorted rows
	result := make([]sqldsl.ValuesRow, len(keyed))
	for i, k := range keyed {
		result[i] = k.row
	}
	return result
}
