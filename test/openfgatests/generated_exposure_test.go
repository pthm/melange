package openfgatests_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// nestedExposure is the SQL API a generated model is migrated with. The zero
// value migrates once with MigrateWithTypes and the default exposure. Otherwise
// the model is migrated with first (when set) and then with final, recording
// each migration as `melange migrate` does, so the second migration has to
// drop, keep or re-create what the first installed. (Orphan cleanup drops
// only recorded functions, and a migration without SchemaContent records
// nothing, so it also refuses to change the exposure.)
type nestedExposure struct {
	first, final *compiler.Exposure
}

func (x nestedExposure) String() string {
	if x.final == nil {
		return "default"
	}
	if x.first == nil {
		return exposureJSON(x.final)
	}
	return exposureJSON(x.first) + " then " + exposureJSON(x.final)
}

func exposureJSON(e *compiler.Exposure) string {
	b, _ := json.Marshal(e) // Exposure is plain data; Marshal cannot fail.
	return string(b)
}

// exposure picks the exposure from the generator's next choices. It is taken
// after the model and tuples, so the inputs saved before it existed keep
// their models and tuples. A third of inputs keep the default.
func (g *nestedGen) exposure(relations []string) nestedExposure {
	switch g.take() % 6 {
	case 2:
		return nestedExposure{final: g.restrictedExposure(relations)}
	case 3: // narrow the default
		return nestedExposure{first: &compiler.Exposure{}, final: g.restrictedExposure(relations)}
	case 4: // move between two restrictions
		first := g.restrictedExposure(relations)
		return nestedExposure{first: first, final: g.restrictedExposure(relations)}
	case 5: // widen back to the default
		return nestedExposure{first: g.restrictedExposure(relations), final: &compiler.Exposure{}}
	default:
		return nestedExposure{}
	}
}

// restrictedExposure exposes a random subset of the document relations (all
// of them when no include is picked), with exclusions by exact name, by glob
// and by plain relation name across types, and turns families off at random.
// Folder and group relations are exposed only when nothing is included, so
// document relations usually resolve through unexposed ones.
func (g *nestedGen) restrictedExposure(relations []string) *compiler.Exposure {
	e := &compiler.Exposure{}
	r := &e.Relations
	for _, rel := range relations {
		if g.take()%2 == 0 {
			r.Include = append(r.Include, "document#"+rel)
		}
	}
	if g.take()%4 == 0 {
		r.Include = append(r.Include, "document#r?")
	}
	if g.take()%4 == 0 {
		r.Exclude = append(r.Exclude, "document#"+relations[g.take()%len(relations)])
	}
	if g.take()%6 == 0 {
		r.Exclude = append(r.Exclude, "b")
	}
	off := func() *bool {
		if g.take()%4 == 0 {
			f := false
			return &f
		}
		return nil
	}
	f := &e.Functions
	f.ListObjects, f.ListSubjects, f.Accessible = off(), off(), off()
	f.ParentKey, f.Expand, f.Explain = off(), off(), off()
	return e
}

// migrate applies x to db. dsl is the schema types was parsed from.
func (x nestedExposure) migrate(t *testing.T, ctx context.Context, db *sql.DB, dsl string, types []schema.TypeDefinition) {
	t.Helper()
	m := migrator.NewMigrator(db, "")
	if x.final == nil {
		require.NoError(t, m.MigrateWithTypes(ctx, types))
		return
	}
	for _, e := range []*compiler.Exposure{x.first, x.final} {
		if e != nil {
			require.NoError(t, m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{Exposure: e, SchemaContent: dsl}),
				"migrating with exposure %s", exposureJSON(e))
		}
	}
}

// exposedAPI is what the final exposure installs and exposes, compiled as
// migrate and client generation compile it.
type exposedAPI struct {
	exposure   compiler.Exposure
	names      []string // every function the migration installs
	accessible []compiler.AccessibleFunction
}

func newExposedAPI(t *testing.T, types []schema.TypeDefinition, x nestedExposure) exposedAPI {
	t.Helper()
	var e compiler.Exposure
	if x.final != nil {
		e = *x.final
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, closure))
	inline := compiler.BuildInlineSQLData(closure, analyses)
	opts := compiler.GenerateSQLOptions{Exposure: e}
	_, err := compiler.GenerateSQLWithOptions(analyses, inline, "", opts)
	require.NoError(t, err)
	_, err = compiler.GenerateListSQLWithOptions(analyses, inline, "", opts)
	require.NoError(t, err)
	accessible, err := compiler.AccessibleFunctions(analyses, e)
	require.NoError(t, err)
	names := slices.Compact(slices.Sorted(slices.Values(compiler.CollectFunctionNamesWithOptions(analyses, opts))))
	return exposedAPI{exposure: e, names: names, accessible: accessible}
}

func on(flag *bool) bool { return flag == nil || *flag }

func (a exposedAPI) exposes(rel string) bool { return a.exposure.Exposes("document", rel) }

// accessibleFor returns the accessible functions client generation names for
// document#rel: the per-relation form first, then the parent-key forms.
func (a exposedAPI) accessibleFor(rel string) []compiler.AccessibleFunction {
	var out []compiler.AccessibleFunction
	for _, f := range a.accessible {
		if f.ObjectType == "document" && f.Relation == rel {
			out = append(out, f)
		}
	}
	return out
}

// requireConsistent checks the static contract of the final exposure: the
// database holds exactly the functions the migration generates (a function
// an earlier exposure installed is gone, one this exposure needs is back),
// and client generation names only accessible functions of exposed
// relations whose family is on.
func (a exposedAPI) requireConsistent(t *testing.T, ctx context.Context, db *sql.DB, x nestedExposure) {
	t.Helper()
	installed := queryStrings(t, ctx, db, `SELECT DISTINCT p.proname::text FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public' ORDER BY 1`)
	require.Equal(t, a.names, installed, "installed functions differ from the generated ones under exposure %s", x)
	for _, f := range a.accessible {
		require.True(t, a.exposure.Exposes(f.ObjectType, f.Relation), "%s names an unexposed relation under %s", f.Name, x)
		if f.Link == "" {
			require.True(t, on(a.exposure.Functions.Accessible), "%s named with accessible off under %s", f.Name, x)
		} else {
			require.True(t, on(a.exposure.Functions.ParentKey), "%s named with parent_key off under %s", f.Name, x)
		}
		require.Contains(t, installed, f.Name, "client names %s but migrate did not install it under %s", f.Name, x)
	}
}

// requireNotExposed asserts that query raises M2003.
func requireNotExposed(t *testing.T, ctx context.Context, db *sql.DB, x nestedExposure, query string, args ...any) {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		_ = rows.Close()
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != compiler.NotExposedErrCode {
		t.Fatalf("%s %v: want SQLSTATE %s under exposure %s, got %v",
			query, args, compiler.NotExposedErrCode, x, err)
	}
}

// accessibleQuery is the query that reads f for a subject as document
// objects. A parent-key form returns parents, so it is joined to the
// documents under them, which is exact here because every generated document
// has at most one parent per link.
func accessibleQuery(f compiler.AccessibleFunction, kind string) (query string, args []any) {
	if f.Link != "" {
		return fmt.Sprintf(`SELECT DISTINCT 'document:' || t.object_id FROM melange_tuples t
			WHERE t.object_type = 'document' AND t.relation = $3 AND t.subject_type = $4
			  AND t.subject_id IN (SELECT * FROM %s($1, $2))`, f.Name), []any{f.Link, f.ParentType}
	}
	if kind == "" {
		return fmt.Sprintf(`SELECT DISTINCT 'document:' || object_id FROM %s($1, $2)`, f.Name), nil
	}
	return fmt.Sprintf(`SELECT DISTINCT 'document:' || object_id FROM %s($1, $2, %s)`, f.Name, kind), nil
}
