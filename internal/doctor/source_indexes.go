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
		indexDefs, err := d.getTableIndexDefs(ctx, rec.Schema, rec.Table)
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

// qualifiedRef reads the table qualifier of a column reference, as in
// "t.subject_id" or "(oc.id)::text"; "" when there is none.
var qualifiedRef = regexp.MustCompile(`^\(?"?(\w+)"?\.`)

// subjectLookupKey is the index key one view branch's subject lookups need:
// the column its subject id comes from, on the table that has it.
type subjectLookupKey struct {
	Table   TableRef
	Key     sourceKey
	Relabel bool // the cast to text is a relabel, so a plain index serves it
	// ExprChecked: checkExpressionIndexes sees the cast and reports it when
	// the column has no index at all.
	ExprChecked bool
	Branch      string
}

// checkSubjectLookupIndexes reports view branches whose subject column has no
// index leading with it. list_accessible_objects and the accessible_*
// functions look tuples up by a set of subject ids, as subject_id =
// ANY(ARRAY(...)), which every branch filters on its own; with an index on
// the subject column that is one probe per id, without one each row is
// compared with the whole set, which is far slower than the scan an IN
// semi-join would do. Joined branches count too: the filter reaches the table
// the subject column comes from.
//
// A cast column with no usable index at all is left to checkExpressionIndexes,
// which recommends the same index; this check adds the branches it cannot
// see, whose subject column is not cast or is indexed only behind other keys.
func (d *Doctor) checkSubjectLookupIndexes(ctx context.Context, report *Report) error {
	if d.viewDef == nil {
		return nil
	}
	keys, err := d.subjectLookupKeys(ctx)
	if err != nil {
		return err
	}
	missing := 0
	for _, k := range keys {
		indexDefs, err := d.getTableIndexDefs(ctx, k.Table.Schema, k.Table.Name)
		if err != nil {
			return fmt.Errorf("getting indexes for %s: %w", k.Table.Name, err)
		}
		if leadsWithSubject(k, indexDefs) {
			continue
		}
		// A cast column checkExpressionIndexes already reports.
		if k.ExprChecked && !castIndexed(k.Key.Column, k.Relabel, indexDefs) {
			continue
		}
		missing++
		rowCount, _ := d.getTableRowCount(ctx, k.Table.Schema, k.Table.Name) // non-fatal: 0 on error
		severity, sizeNote := rowCountSeverity(rowCount)
		suffix := ""
		if k.Key.Expr {
			suffix = "_text"
		}
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "subject_lookup_indexes",
			Status:   severity,
			Message:  fmt.Sprintf("Missing index on %s (%s) for subject lookups (%s)", k.Table, k.Key.ddl(), sizeNote),
			Details: fmt.Sprintf("The %s branch of melange_tuples reads its subject id from %s.%s. list_accessible_objects and the "+
				"accessible_* functions look tuples up by a set of subject ids; without an index leading with this column, "+
				"each row of the branch is compared with the whole set.", k.Branch, k.Table, k.Key.Column),
			FixHint: fmt.Sprintf("CREATE INDEX idx_%s_%s%s ON %s (%s);", k.Table.Name, k.Key.Column, suffix, k.Table, k.Key.ddl()),
		})
	}
	if missing == 0 && len(keys) > 0 {
		report.AddCheck(CheckResult{
			Category: "Performance",
			Name:     "subject_lookup_indexes",
			Status:   StatusPass,
			Message:  fmt.Sprintf("Subject columns are indexed for subject lookups (%d checked)", len(keys)),
		})
	}
	return nil
}

// subjectLookupKeys finds, for each branch whose subject id is a column
// (optionally cast to text), the table that column belongs to, deduplicated.
// In a join the owner is the one source table with the column, or the one the
// reference's qualifier (alias or table name) names when several have it; a
// branch whose owner can't be told is skipped.
func (d *Doctor) subjectLookupKeys(ctx context.Context) ([]subjectLookupKey, error) {
	var out []subjectLookupKey
	seen := map[string]bool{}
	for _, b := range d.viewDef.Branches {
		expr := b.ColumnMapping["subject_id"]
		literal, name, cast, ok := classifyRef(expr)
		if !ok || literal {
			continue
		}
		var owners []subjectLookupKey
		for _, t := range b.SourceTables {
			colType, relabel, err := d.castToText(ctx, t.Schema, t.Name, name)
			if err != nil {
				return nil, fmt.Errorf("reading the type of %s.%s: %w", t.Name, name, err)
			}
			if colType == "" {
				continue
			}
			owners = append(owners, subjectLookupKey{
				Table:   t,
				Key:     sourceKey{TupleColumn: "subject_id", Column: name, Expr: cast && !relabel},
				Relabel: cast && relabel,
				ExprChecked: slices.ContainsFunc(b.CastColumns, func(c CastColumn) bool {
					return c.ViewColumn == "subject_id" && strings.EqualFold(c.SourceColumn, name)
				}),
				Branch: branchLabel(b),
			})
		}
		if len(owners) > 1 {
			m := qualifiedRef.FindStringSubmatch(strings.TrimSpace(expr))
			owners = slices.DeleteFunc(owners, func(o subjectLookupKey) bool {
				return len(m) < 2 || !o.Table.Names(m[1])
			})
		}
		if len(owners) != 1 {
			continue
		}
		k := owners[0]
		if id := k.Table.String() + "(" + k.Key.ddl() + ")"; !seen[id] {
			seen[id] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// leadsWithSubject reports whether a full index's first key serves a lookup
// by the subject column: the ::text expression for a converting cast, else
// the plain column (or its relabelled ::text form).
func leadsWithSubject(k subjectLookupKey, indexDefs []string) bool {
	col := strings.ToLower(k.Key.Column)
	for _, def := range indexDefs {
		if _, pred := splitIndexKeysAndPredicate(def); pred != "" {
			continue
		}
		ix := normalizedIndexKeys(def)
		if len(ix) == 0 {
			continue
		}
		if ix[0] == k.Key.normalized() || (!k.Key.Expr && ix[0] == col+"::text") {
			return true
		}
	}
	return false
}
