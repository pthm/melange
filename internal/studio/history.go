package studio

import (
	"context"

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
