package studio

import (
	"net/http"

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
