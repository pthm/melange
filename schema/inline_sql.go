package schema

import (
	"fmt"
	"sort"
	"strings"
)

// InlineSQLData contains SQL VALUES payloads that replace database-backed model tables.
type InlineSQLData struct {
	// ClosureValues contains tuples of (object_type, relation, satisfying_relation).
	ClosureValues string
	// UsersetValues contains tuples of (object_type, relation, subject_type, subject_relation).
	UsersetValues string
}

func buildInlineSQLData(closureRows []ClosureRow, analyses []RelationAnalysis) InlineSQLData {
	return InlineSQLData{
		ClosureValues: buildClosureValues(closureRows),
		UsersetValues: buildUsersetValues(analyses),
	}
}

// BuildInlineSQLData exposes inline SQL generation for tools and tests.
func BuildInlineSQLData(closureRows []ClosureRow, analyses []RelationAnalysis) InlineSQLData {
	return buildInlineSQLData(closureRows, analyses)
}

func buildClosureValues(closureRows []ClosureRow) string {
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

func buildUsersetValues(analyses []RelationAnalysis) string {
	seen := make(map[string]struct{})
	var rows []string

	for _, analysis := range analyses {
		for _, pattern := range analysis.UsersetPatterns {
			key := strings.Join([]string{
				analysis.ObjectType,
				analysis.Relation,
				pattern.SubjectType,
				pattern.SubjectRelation,
			}, "\x00")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', '%s')",
				escapeSQLLiteral(analysis.ObjectType),
				escapeSQLLiteral(analysis.Relation),
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
