package sqlgen

import (
	"fmt"
	"sort"
	"strings"
)

// IndexRecommendation describes a composite index that would make one or more
// generated functions efficient against melange_tuples.
//
// The recommendation targets melange_tuples as if it were a base table (Option
// A from INDEX_RECOMMENDATIONS.md): users translate the DDL to whichever
// source tables back the UNION ALL branches of their view, since PostgreSQL
// cannot create indexes on views directly.
type IndexRecommendation struct {
	// BaseTable is the table the DDL targets. Always "melange_tuples" in the
	// schema-driven flow; doctor integration may rewrite to source tables.
	BaseTable string

	// Columns is the ordered composite — earliest-most-selective first.
	Columns []string

	// WhereClause is an optional partial-index predicate. Empty for full indexes.
	WhereClause string

	// BenefitsFunctions lists generated function names that match this index's
	// access pattern. Sorted alphabetically for stable output.
	BenefitsFunctions []string

	// DDL is the rendered CREATE INDEX IF NOT EXISTS statement.
	DDL string
}

// indexShape is the dedup key for a recommendation.
type indexShape struct {
	columns string // joined by comma
	where   string
}

// RecommendIndexes returns the minimum set of composite indexes that make the
// generated check and list functions efficient. Output is sorted by DDL string
// for deterministic results.
//
// Two index families are emitted per (type, relation) with CheckAllowed or
// ListAllowed:
//   - Object-keyed (object_type, object_id, relation, subject_type, subject_id):
//     covers check_* and list_*_sub access patterns.
//   - Subject-keyed (subject_type, subject_id, relation, object_type, object_id):
//     covers list_*_obj access patterns.
//
// Relations with HasWildcard additionally get a partial index over wildcard
// rows so wildcard membership lookups don't scan the whole subject_id space.
//
// Recommendations are deduplicated: an index that benefits multiple functions
// appears once with all function names in BenefitsFunctions.
func RecommendIndexes(analyses []RelationAnalysis) []IndexRecommendation {
	// Accumulate per shape; merge function lists across relations.
	byShape := make(map[indexShape]*IndexRecommendation)

	add := func(columns []string, where string, fns ...string) {
		shape := indexShape{columns: strings.Join(columns, ","), where: where}
		rec, ok := byShape[shape]
		if !ok {
			rec = &IndexRecommendation{
				BaseTable:   "melange_tuples",
				Columns:     columns,
				WhereClause: where,
			}
			byShape[shape] = rec
		}
		rec.BenefitsFunctions = append(rec.BenefitsFunctions, fns...)
	}

	objectKeyed := []string{"object_type", "object_id", "relation", "subject_type", "subject_id"}
	subjectKeyed := []string{"subject_type", "subject_id", "relation", "object_type", "object_id"}
	wildcardKeyed := []string{"object_type", "object_id", "relation"}

	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed && !a.Capabilities.ListAllowed {
			continue
		}

		// Object-keyed: covers check_* and list_*_sub. Always emit when either
		// is generated (check uses the same prefix as list_*_sub).
		var objBenefits []string
		if a.Capabilities.CheckAllowed {
			objBenefits = append(objBenefits,
				functionName(a.ObjectType, a.Relation),
				functionNameNoWildcard(a.ObjectType, a.Relation),
			)
		}
		if a.Capabilities.ListAllowed {
			objBenefits = append(objBenefits, listSubjectsFunctionName(a.ObjectType, a.Relation))
		}
		add(objectKeyed, "", objBenefits...)

		// Subject-keyed: covers list_*_obj only.
		if a.Capabilities.ListAllowed {
			add(subjectKeyed, "", listObjectsFunctionName(a.ObjectType, a.Relation))
		}

		// Wildcard partial: only relations that accept [type:*] grants.
		if a.Features.HasWildcard {
			var wildcardBenefits []string
			if a.Capabilities.CheckAllowed {
				wildcardBenefits = append(wildcardBenefits, functionName(a.ObjectType, a.Relation))
			}
			if a.Capabilities.ListAllowed {
				wildcardBenefits = append(wildcardBenefits, listSubjectsFunctionName(a.ObjectType, a.Relation))
			}
			add(wildcardKeyed, "subject_id = '*'", wildcardBenefits...)
		}
	}

	out := make([]IndexRecommendation, 0, len(byShape))
	for _, rec := range byShape {
		sort.Strings(rec.BenefitsFunctions)
		rec.BenefitsFunctions = dedupeStrings(rec.BenefitsFunctions)
		rec.DDL = renderIndexDDL(*rec)
		out = append(out, *rec)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].DDL < out[j].DDL })
	return out
}

// renderIndexDDL formats the CREATE INDEX statement. The index name is
// derived from the columns (and the wildcard predicate, if present) so that
// every distinct shape gets a distinct name without colliding across schemas.
func renderIndexDDL(rec IndexRecommendation) string {
	name := indexName(rec)
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE INDEX IF NOT EXISTS %s ON %s (%s)",
		name, rec.BaseTable, strings.Join(rec.Columns, ", "))
	if rec.WhereClause != "" {
		fmt.Fprintf(&b, " WHERE %s", rec.WhereClause)
	}
	b.WriteByte(';')
	return b.String()
}

// indexName builds a stable, descriptive name for a recommendation. The first
// two columns drive the name (which dimension is leading is the meaningful
// distinction); the wildcard variant gets an explicit suffix.
//
// Recommendations target melange_tuples so the column list disambiguates rather
// than the table name; PostgreSQL's 63-byte limit isn't a concern here because
// column tokens are all short and bounded by what we emit.
func indexName(rec IndexRecommendation) string {
	suffix := ""
	if rec.WhereClause != "" {
		suffix = "_wildcard"
	}
	// Use first two columns as the kind discriminator: "object_type_object_id"
	// vs. "subject_type_subject_id". The full column list isn't needed in the
	// name — the DDL itself is the authoritative spec.
	cols := rec.Columns
	if len(cols) >= 2 {
		return "idx_melange_tuples_by_" + cols[0] + "_" + cols[1] + suffix
	}
	return "idx_melange_tuples_" + strings.Join(cols, "_") + suffix
}

func dedupeStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	out := in[:0]
	var prev string
	for i, s := range in {
		if i == 0 || s != prev {
			out = append(out, s)
			prev = s
		}
	}
	return out
}
