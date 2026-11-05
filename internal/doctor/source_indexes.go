package doctor

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// sourceKey is one column of a recommended source-table index: the tuple
// column it feeds and how to index it.
type sourceKey struct {
	TupleColumn string // object_id, relation or subject_id
	Column      string // the source table's column
	Expr        bool   // index (col::text) rather than col: the cast converts the value
}

// normalized is the key as normalizeIndexKey renders an index's key.
func (k sourceKey) normalized() string {
	if k.Expr {
		return strings.ToLower(k.Column) + "::text"
	}
	return strings.ToLower(k.Column)
}

// ddl is the key as written in CREATE INDEX.
func (k sourceKey) ddl() string {
	if k.Expr {
		return "(" + k.Column + "::text)"
	}
	return k.Column
}

// sourceIndexRec is a composite index one view branch's object lookups need:
// object id, then relation when it comes from a column, then subject id.
type sourceIndexRec struct {
	Schema, Table string
	Keys          []sourceKey
	Branch        string // "object_type → relation", to say which branch it serves
}

func (r sourceIndexRec) qualified() string {
	if r.Schema != "" {
		return r.Schema + "." + r.Table
	}
	return r.Table
}

func (r sourceIndexRec) columnsDDL() string {
	parts := make([]string, len(r.Keys))
	for i, k := range r.Keys {
		parts[i] = k.ddl()
	}
	return strings.Join(parts, ", ")
}

var (
	// 'organization'::text, 'x'
	literalRef = regexp.MustCompile(`^'(?:[^']|'')*'(?:::\w+)?$`)
	// trails.organization_id::text, (t.id)::text, "Col", role
	columnRef = regexp.MustCompile(`^\(?(?:"?\w+"?\.)?"?(\w+)"?\)?(?:::(\w+))?$`)
)

// classifyRef reads a branch's expression for one tuple column: a literal
// (which the planner uses to skip whole branches, so it needs no index), a
// plain column (optionally cast), or something else.
func classifyRef(expr string) (literal bool, column string, cast, ok bool) {
	expr = strings.TrimSpace(expr)
	if literalRef.MatchString(expr) {
		return true, "", false, true
	}
	m := columnRef.FindStringSubmatch(expr)
	if len(m) != 3 || (m[2] != "" && !strings.EqualFold(m[2], "text")) {
		return false, "", false, false
	}
	return false, m[1], m[2] != "", true
}

// checkSourceTableIndexes recommends, for each single-table branch of the
// melange_tuples view, the composite index its object lookups need. Check and
// list-subjects queries filter on object id, relation and subject id; on a
// membership-style table (many subjects per object) an index on the object
// column alone still reads every member. The per-column expression index
// check covers the leading columns themselves, and list-objects lookups,
// which only need an index leading with the subject column.
//
// A branch is skipped when its object or subject column is unique (a lookup
// by it returns at most one row, so its own index is enough), when it joins
// tables (no one index spans them), or when an id is computed rather than a
// column.
func (d *Doctor) checkSourceTableIndexes(ctx context.Context, report *Report) error {
	if d.viewDef == nil {
		return nil
	}
	recs, err := d.sourceIndexRecs(ctx)
	if err != nil {
		return err
	}
	missing := 0
	for _, rec := range recs {
		indexDefs, err := d.getTableIndexDefs(ctx, rec.Table)
		if err != nil {
			return fmt.Errorf("getting indexes for %s: %w", rec.Table, err)
		}
		if uniqueOn(rec.Keys[0], indexDefs) || uniqueOn(rec.Keys[len(rec.Keys)-1], indexDefs) || coversObjectLookup(rec.Keys, indexDefs) {
			continue
		}
		missing++
		names := make([]string, len(rec.Keys))
		for i, k := range rec.Keys {
			names[i] = k.TupleColumn + " = " + k.Column
		}
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "source_table_indexes",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Missing index on %s (%s) for object lookups", rec.qualified(), rec.columnsDDL()),
			Details: fmt.Sprintf("The %s branch of melange_tuples reads %s (%s). check_permission and list_accessible_subjects "+
				"look tuples up by object, then subject; with an index on the object column alone, each check reads every row for that object.",
				rec.Branch, rec.qualified(), strings.Join(names, ", ")),
			FixHint: fmt.Sprintf("CREATE INDEX idx_%s_by_object ON %s (%s);", rec.Table, rec.qualified(), rec.columnsDDL()),
		})
	}
	if missing == 0 && len(recs) > 0 {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "source_table_indexes",
			Status:   StatusPass,
			Message:  fmt.Sprintf("Source tables have composite indexes for object lookups (%d checked)", len(recs)),
		})
	}
	return nil
}

// sourceIndexRecs derives the object-lookup index for each single-table
// branch whose object and subject ids are columns, deduplicated per table.
func (d *Doctor) sourceIndexRecs(ctx context.Context) ([]sourceIndexRec, error) {
	var out []sourceIndexRec
	seen := map[string]bool{}
	for _, b := range d.viewDef.Branches {
		if len(b.SourceTables) != 1 {
			continue
		}
		t := b.SourceTables[0]
		var keys []sourceKey
		usable := true
		for _, col := range []string{"object_id", "relation", "subject_id"} {
			literal, name, cast, ok := classifyRef(b.ColumnMapping[col])
			switch {
			case !ok:
				usable = false
			case literal:
				if col != "relation" {
					usable = false // a fixed id: nothing to look up by
				}
			default:
				key := sourceKey{TupleColumn: col, Column: name}
				if cast {
					colType, relabel, err := d.castToText(ctx, t.Schema, t.Name, name)
					if err != nil {
						return nil, fmt.Errorf("reading the type of %s.%s: %w", t.Name, name, err)
					}
					if colType == "" {
						usable = false // not this table's column after all
					}
					key.Expr = !relabel
				}
				keys = append(keys, key)
			}
		}
		if !usable {
			continue
		}
		rec := sourceIndexRec{Schema: t.Schema, Table: t.Name, Keys: keys, Branch: branchLabel(b)}
		id := rec.qualified() + "(" + rec.columnsDDL() + ")"
		if !seen[id] {
			seen[id] = true
			out = append(out, rec)
		}
	}
	return out, nil
}

// branchLabel names a branch by what it produces, e.g. "organization →
// member", or "organization → role" when the relation is read from the role
// column, falling back to the raw expression.
func branchLabel(b ViewBranch) string {
	unquote := func(expr string) string {
		literal, col, _, ok := classifyRef(expr)
		switch {
		case ok && literal:
			s := expr[1:]
			return s[:strings.LastIndexByte(s, '\'')]
		case ok:
			return col
		}
		return expr
	}
	return unquote(b.ColumnMapping["object_type"]) + " → " + unquote(b.ColumnMapping["relation"])
}

// uniqueOn reports whether a unique index's only key is k's column, in either
// form: lookups by it return at most one row, so no composite is needed.
func uniqueOn(k sourceKey, indexDefs []string) bool {
	col := strings.ToLower(k.Column)
	for _, def := range indexDefs {
		if !strings.Contains(strings.ToUpper(def), "UNIQUE INDEX") {
			continue
		}
		if _, pred := splitIndexKeysAndPredicate(def); pred != "" {
			continue
		}
		if keys := normalizedIndexKeys(def); len(keys) == 1 && (keys[0] == col || keys[0] == col+"::text") {
			return true
		}
	}
	return false
}

// coversObjectLookup reports whether a full index leads with the object key
// and has the subject key right after it, or after the relation.
func coversObjectLookup(keys []sourceKey, indexDefs []string) bool {
	obj, subj := keys[0].normalized(), keys[len(keys)-1].normalized()
	for _, def := range indexDefs {
		if _, pred := splitIndexKeysAndPredicate(def); pred != "" {
			continue
		}
		ix := normalizedIndexKeys(def)
		if len(ix) >= 2 && ix[0] == obj && slices.Contains(ix[1:min(3, len(ix))], subj) {
			return true
		}
	}
	return false
}

// normalizedIndexKeys returns an index definition's keys, lowercased and
// without quotes, parens or spaces: "organization_id", "user_id::text".
func normalizedIndexKeys(indexdef string) []string {
	keys, _ := splitIndexKeysAndPredicate(indexdef)
	start := strings.Index(strings.ToUpper(keys), " USING ")
	if start < 0 {
		return nil
	}
	open := strings.IndexByte(keys[start:], '(')
	if open < 0 {
		return nil
	}
	body := keys[start+open+1:]
	var out []string
	depth, from := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return append(out, normalizeIndexKey(body[from:i]))
			}
			depth--
		case ',':
			if depth == 0 {
				out = append(out, normalizeIndexKey(body[from:i]))
				from = i + 1
			}
		}
	}
	return out
}

func normalizeIndexKey(k string) string {
	return strings.ToLower(strings.NewReplacer("(", "", ")", "", `"`, "", " ", "").Replace(k))
}
