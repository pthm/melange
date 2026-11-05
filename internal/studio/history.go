package studio

import (
	"context"
	"slices"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/pkg/schema"
)

// storedSchema is a migration row's recorded schema: the DSL as migrated and
// the parsed model. Either may be missing on rows from before melange stored
// them.
type storedSchema struct {
	ID       int
	DSL      string
	Modular  bool
	Model    []schema.TypeDefinition
	HasModel bool
}

// loadSchemas returns the recorded schemas of migrations fromID..toID plus
// the one just before fromID, oldest first, so each row has its predecessor.
func (s *Server) loadSchemas(ctx context.Context, fromID, toID int) ([]storedSchema, error) {
	table := pq.QuoteIdentifier(s.dbSchema) + ".melange_migrations"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, coalesce(schema_dsl, ''), coalesce(schema_format, ''), coalesce(model_json::text, '')
		FROM `+table+`
		WHERE id <= $2 AND id >= coalesce((SELECT max(id) FROM `+table+` WHERE id < $1), $1)
		ORDER BY id`, fromID, toID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []storedSchema
	for rows.Next() {
		var st storedSchema
		var format, model string
		if err := rows.Scan(&st.ID, &st.DSL, &format, &model); err != nil {
			return nil, err
		}
		st.Modular = format == "modular"
		if types, err := schema.UnmarshalModel([]byte(model)); err == nil && len(types) > 0 {
			st.Model, st.HasModel = types, true
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// migrationStats summarizes one migration's schema change from its
// predecessor. Lines is set when both recorded their DSL, Model when both
// recorded their model.
type migrationStats struct {
	Lines                    bool
	LinesAdded, LinesRemoved int
	Model                    bool
	TypesAdded, TypesRemoved int
	RelsAdded, RelsRemoved   int
	RelsChanged, Breaking    int
	Changes                  []schema.Change
}

// statsFor compares cur against prev.
func statsFor(prev, cur storedSchema) migrationStats {
	var st migrationStats
	if prev.DSL != "" && cur.DSL != "" {
		st.Lines = true
		for _, f := range schemaFileDiffs(prev.DSL, cur.DSL, cur.Modular) {
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					switch l.Op {
					case '+':
						st.LinesAdded++
					case '-':
						st.LinesRemoved++
					}
				}
			}
		}
	}
	if prev.HasModel && cur.HasModel {
		st.Model = true
		oldRels, newRels := relationKeys(prev.Model), relationKeys(cur.Model)
		oldTypes, newTypes := typeNames(prev.Model), typeNames(cur.Model)
		for t := range newTypes {
			if !oldTypes[t] {
				st.TypesAdded++
			}
		}
		for t := range oldTypes {
			if !newTypes[t] {
				st.TypesRemoved++
			}
		}
		for k := range newRels {
			if !oldRels[k] {
				st.RelsAdded++
			}
		}
		for k := range oldRels {
			if !newRels[k] {
				st.RelsRemoved++
			}
		}
		d := schema.Diff(prev.Model, cur.Model)
		st.Changes = d.Changes
		_, st.Breaking = d.Counts()
		changed := map[string]bool{}
		for _, c := range d.Changes {
			if k := c.Type + "#" + c.Relation; c.Relation != "" && oldRels[k] && newRels[k] {
				changed[k] = true
			}
		}
		st.RelsChanged = len(changed)
	}
	return st
}

func typeNames(types []schema.TypeDefinition) map[string]bool {
	out := make(map[string]bool, len(types))
	for _, t := range types {
		out[t.Name] = true
	}
	return out
}

func relationKeys(types []schema.TypeDefinition) map[string]bool {
	out := map[string]bool{}
	for _, t := range types {
		for _, r := range t.Relations {
			out[t.Name+"#"+r.Name] = true
		}
	}
	return out
}

// historyStats computes migrationStats for each listed migration (ids
// newest first, as History lists them), keyed by id. Rows without a
// predecessor are absent.
func (s *Server) historyStats(ctx context.Context, ids []int) (map[int]migrationStats, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	schemas, err := s.loadSchemas(ctx, ids[len(ids)-1], ids[0])
	if err != nil {
		return nil, err
	}
	out := make(map[int]migrationStats, len(schemas))
	for i := 1; i < len(schemas); i++ {
		out[schemas[i].ID] = statsFor(schemas[i-1], schemas[i])
	}
	return out, nil
}

// changeGroup is one type's model changes, for the History detail: whether
// the type itself was added or removed, then its relations.
type changeGroup struct {
	Type      string
	Status    string // "added", "removed", or "" when the type changed
	Relations []relationChange
}

// relationChange is one relation's changes: added, removed, or changed with
// the grant and exclusion terms that moved.
type relationChange struct {
	Name     string
	Status   string // "added", "removed" or "changed"
	Items    []changeItem
	Breaking bool
}

// changeItem is one term a changed relation gained (+) or lost (−).
type changeItem struct {
	Sign string // "+" or "−"
	Text string
}

// groupChanges arranges schema.Diff's flat list by type, then relation, in
// first-seen order, so a relation that swaps one grant for another reads as
// one row instead of two sentences.
func groupChanges(changes []schema.Change) []changeGroup {
	var out []changeGroup
	group := func(typ string) *changeGroup {
		for i := range out {
			if out[i].Type == typ {
				return &out[i]
			}
		}
		out = append(out, changeGroup{Type: typ})
		return &out[len(out)-1]
	}
	for _, c := range changes {
		g := group(c.Type)
		if c.Relation == "" {
			g.Status = lastWord(c.Summary) // "type X added" / "type X removed"
			continue
		}
		var rc *relationChange
		for i := range g.Relations {
			if g.Relations[i].Name == c.Relation {
				rc = &g.Relations[i]
			}
		}
		if rc == nil {
			g.Relations = append(g.Relations, relationChange{Name: c.Relation, Status: "changed"})
			rc = &g.Relations[len(g.Relations)-1]
		}
		rc.Breaking = rc.Breaking || c.Class == schema.ClassBreaking
		if c.Summary == "relation "+c.Type+"."+c.Relation+" "+lastWord(c.Summary) {
			rc.Status = lastWord(c.Summary)
			continue
		}
		detail := strings.TrimPrefix(c.Summary, c.Type+"."+c.Relation+" ")
		switch {
		case strings.HasPrefix(detail, "no longer grants "):
			rc.Items = append(rc.Items, changeItem{"−", strings.TrimPrefix(detail, "no longer grants ")})
		case strings.HasPrefix(detail, "grants "):
			rc.Items = append(rc.Items, changeItem{"+", strings.TrimPrefix(detail, "grants ")})
		case strings.HasPrefix(detail, "no longer excludes "):
			rc.Items = append(rc.Items, changeItem{"−", "but not " + strings.TrimPrefix(detail, "no longer excludes ")})
		case strings.HasPrefix(detail, "excludes "):
			rc.Items = append(rc.Items, changeItem{"+", "but not " + strings.TrimPrefix(detail, "excludes ")})
		default:
			rc.Items = append(rc.Items, changeItem{"~", detail})
		}
	}
	// Lost terms before gained ones, so a swap reads old → new.
	for gi := range out {
		for ri := range out[gi].Relations {
			slices.SortStableFunc(out[gi].Relations[ri].Items, func(a, b changeItem) int {
				return strings.Compare(signRank(a.Sign), signRank(b.Sign))
			})
		}
	}
	return out
}

func signRank(s string) string {
	if s == "−" {
		return "0"
	}
	return "1"
}

func lastWord(s string) string {
	return s[strings.LastIndexByte(s, ' ')+1:]
}
