package studio

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/pkg/migrator"
)

// historyLimit bounds the migration audit trail shown on the History page.
const historyLimit = 50

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	m := migrator.NewMigrator(s.db, "")
	m.SetDatabaseSchema(s.dbSchema)
	recs, err := m.GetMigrationHistory(r.Context(), historyLimit)
	// History rows omit the DSL; the deployed one comes from the model read.
	deployed, _ := s.loadModel(r.Context())
	render(w, r, historyPage(s.env, recs, deployed, errString(err)))
}

// historyDiff renders the schema change from the migration before id to id,
// for a History row's disclosure.
func (s *Server) historyDiff(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	table := pq.QuoteIdentifier(s.dbSchema) + ".melange_migrations"
	var cur, format sql.NullString
	err = s.db.QueryRowContext(r.Context(), `SELECT schema_dsl, schema_format FROM `+table+` WHERE id = $1`, id).Scan(&cur, &format)
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	var prevID int
	var prev sql.NullString
	err = s.db.QueryRowContext(r.Context(),
		`SELECT id, schema_dsl FROM `+table+` WHERE id < $1 ORDER BY id DESC LIMIT 1`, id).Scan(&prevID, &prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		render(w, r, errorBox(err.Error()))
		return
	}
	render(w, r, schemaDiff(prevID, cur.String, prev.String, format.String == "modular"))
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
