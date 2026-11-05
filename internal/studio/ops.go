package studio

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// historyLimit bounds the migration audit trail shown on the History page.
const historyLimit = 50

// historyEntry is one migration in the History list, with how its schema
// relates to the one before it.
type historyEntry struct {
	Rec       migrator.MigrationRecord
	Stats     migrationStats
	Deployed  bool // the newest row: what the database runs
	Unchanged int  // the previous migration's id when the schema is identical
	RevertOf  int  // an older migration whose schema this one restores
}

// historyData is the History page: the recent migrations for the picker and
// one selected migration in detail.
type historyData struct {
	Env     string
	Entries []historyEntry
	Sel     int           // index into Entries; -1 when nothing is selected
	Prev    *storedSchema // the selected migration's predecessor, if recorded
	Cur     storedSchema
	Err     string
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	m := migrator.NewMigrator(s.db, "")
	m.SetDatabaseSchema(s.dbSchema)
	recs, err := m.GetMigrationHistory(r.Context(), historyLimit)
	d := historyData{Env: s.env, Sel: -1, Err: errString(err)}
	ids := make([]int, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	// Stats are extra: a table too old to have the schema columns just
	// shows none.
	stats, _ := s.historyStats(r.Context(), ids)
	for i, rec := range recs {
		e := historyEntry{Rec: rec, Stats: stats[rec.ID], Deployed: i == 0, RevertOf: sameSchemaAs(recs, i)}
		if i+1 < len(recs) && recs[i+1].SchemaChecksum == rec.SchemaChecksum {
			e.Unchanged = recs[i+1].ID
		}
		d.Entries = append(d.Entries, e)
	}
	if len(recs) > 0 {
		d.Sel = 0
		if want, err := strconv.Atoi(r.URL.Query().Get("id")); err == nil {
			d.Sel = slices.IndexFunc(recs, func(rec migrator.MigrationRecord) bool { return rec.ID == want })
			if d.Sel < 0 {
				d.Err = fmt.Sprintf("Migration #%d is not among the last %d.", want, historyLimit)
			}
		}
	}
	if d.Sel >= 0 {
		id := recs[d.Sel].ID
		schemas, err := s.loadSchemas(r.Context(), id, id)
		if err != nil {
			d.Err = err.Error()
		}
		if n := len(schemas); n > 0 && schemas[n-1].ID == id {
			d.Cur = schemas[n-1]
			if n == 2 {
				d.Prev = &schemas[0]
			}
		}
	}
	render(w, r, historyPage(d))
}

// health runs the same checks as `melange doctor`. Doctor only reads, and the
// connection is read-only regardless.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	d := doctor.New(s.db, s.schemaPath)
	d.SetDatabaseSchema(s.dbSchema)
	report, err := d.Run(r.Context())
	render(w, r, healthPage(s.env, s.schemaPath, report, errString(err)))
}

// healthCategory groups doctor checks for display, in first-seen order.
type healthCategory struct {
	Name   string
	Checks []doctor.CheckResult
	Worst  doctor.Status
}

func groupChecks(report *doctor.Report) []healthCategory {
	if report == nil {
		return nil
	}
	var out []healthCategory
	idx := map[string]int{}
	for _, c := range report.Checks {
		i, ok := idx[c.Category]
		if !ok {
			i = len(out)
			idx[c.Category] = i
			out = append(out, healthCategory{Name: c.Category})
		}
		out[i].Checks = append(out[i].Checks, c)
		out[i].Worst = max(out[i].Worst, c.Status)
	}
	return out
}

// statusClass maps a doctor status to a badge tone.
func statusClass(s doctor.Status) string {
	switch s {
	case doctor.StatusPass:
		return "ok"
	case doctor.StatusWarn:
		return "warn"
	default:
		return "fail"
	}
}

func statusLabel(s doctor.Status) string {
	switch s {
	case doctor.StatusPass:
		return "pass"
	case doctor.StatusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// sameSchemaAs returns the id of an earlier migration (older than recs[i]'s
// predecessor) with recs[i]'s schema checksum: recs[i] reverted the
// migrations in between. 0 when there is none. recs is newest first.
func sameSchemaAs(recs []migrator.MigrationRecord, i int) int {
	if i+1 >= len(recs) || recs[i+1].SchemaChecksum == recs[i].SchemaChecksum {
		return 0
	}
	for _, r := range recs[i+2:] {
		if r.SchemaChecksum == recs[i].SchemaChecksum {
			return r.ID
		}
	}
	return 0
}

// diffRow is one rendered line of a file diff: a hunk header or a line.
type diffRow struct{ Class, Text string }

// diffRows flattens f into display rows, so the template emits one span per
// row with no whitespace between them inside the <pre>.
func diffRows(f fileDiff) []diffRow {
	var out []diffRow
	for _, h := range f.Hunks {
		out = append(out, diffRow{"diff-hunk", fmt.Sprintf("%s · line %d", f.Name, h.NewStart)})
		for _, l := range h.Lines {
			class := "diff-ctx"
			switch l.Op {
			case '+':
				class = "diff-add"
			case '-':
				class = "diff-del"
			}
			out = append(out, diffRow{class, string(l.Op) + " " + l.Text})
		}
	}
	return out
}

// statsSummary is a migration's model change in words, e.g. "1 type added ·
// 4 relations added · 1 breaking". Empty when nothing changed.
func statsSummary(st migrationStats) string {
	var parts []string
	add := func(n int, noun, verb string) {
		if n > 0 {
			parts = append(parts, plural(n, noun)+" "+verb)
		}
	}
	add(st.TypesAdded, "type", "added")
	add(st.TypesRemoved, "type", "removed")
	add(st.RelsAdded, "relation", "added")
	add(st.RelsRemoved, "relation", "removed")
	add(st.RelsChanged, "relation", "changed")
	if st.Breaking > 0 {
		parts = append(parts, fmt.Sprintf("%s breaking", num(st.Breaking)))
	}
	return strings.Join(parts, " · ")
}

func changeClass(c schema.ChangeClass) string {
	if c == schema.ClassBreaking {
		return "warn"
	}
	return "ok"
}

// shortSummary is statsSummary for the narrow History picker: "+1 type ·
// +4 rel · ~2 rel". Breaking changes get their own badge.
func shortSummary(st migrationStats) string {
	var parts []string
	add := func(n int, sign, noun string) {
		if n > 0 {
			parts = append(parts, sign+num(n)+" "+noun)
		}
	}
	add(st.TypesAdded, "+", "type")
	add(st.TypesRemoved, "−", "type")
	add(st.RelsAdded, "+", "rel")
	add(st.RelsRemoved, "−", "rel")
	add(st.RelsChanged, "~", "rel")
	return strings.Join(parts, " · ")
}

func breakingTone(n int) string {
	if n > 0 {
		return "warn"
	}
	return "ok"
}

// codegenNote explains an unchanged schema's rerun when the codegen version
// moved: ", with codegen v0.10.1 → v0.10.2".
func codegenNote(d historyData, e historyEntry) string {
	if d.Sel+1 >= len(d.Entries) {
		return ""
	}
	prev := d.Entries[d.Sel+1].Rec.CodegenVersion
	if migrator.SameCodegenVersion(prev, e.Rec.CodegenVersion) {
		return ""
	}
	return ", with codegen " + prev + " → " + e.Rec.CodegenVersion
}

// signed is an added/removed pair as "+3 −1", leaving out a zero side
// ("+3"), or "0" when both are zero.
func signed(added, removed int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, "+"+num(added))
	}
	if removed > 0 {
		parts = append(parts, "−"+num(removed))
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, " ")
}

// changedSuffix appends a changed count, " ~2", when there is one.
func changedSuffix(n int) string {
	if n == 0 {
		return ""
	}
	return " ~" + num(n)
}
