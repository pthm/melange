package test

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/clientgen"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	fgaparser "github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
)

// clientgenSchema is the database schema the clientgen test migrates into,
// so the generated names are checked against one that is not public.
const clientgenSchema = "app_authz"

// The accessible function names generated clients carry are exactly the
// public accessible functions migrate installs: every Go constant and
// TypeScript map value exists in pg_proc in the target schema, and every
// installed accessible function is named by the client unless it was
// installed only because an exposed relation resolves through it. Checked for
// the testdata schemas (schema.fga has truncated names) under the default and
// restricted exposures.
func TestClientgen_AccessibleNamesMatchInstalled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	off := false
	exposures := []struct {
		label    string
		exposure compiler.Exposure
	}{
		{"default", compiler.Exposure{}},
		{"can_*", compiler.Exposure{Relations: compiler.RelationPatterns{Include: []string{"can_*"}}}},
		{"can_*, accessible off", compiler.Exposure{
			Relations: compiler.RelationPatterns{Include: []string{"can_*"}},
			Functions: compiler.FunctionFamilies{Accessible: &off},
		}},
		{"one type, parent_key off", compiler.Exposure{
			Relations: compiler.RelationPatterns{Include: []string{"pull_request#*", "document#*"}},
			Functions: compiler.FunctionFamilies{ParentKey: &off},
		}},
	}
	for _, path := range []string{
		"testutil/testdata/schema.fga",
		"testutil/testdata/kitchen_sink_schema.fga",
		"testutil/testdata/saas_schema.fga",
	} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			db := testutil.EmptyDB(t)
			s := pq.QuoteIdentifier(clientgenSchema)
			_, err := db.ExecContext(ctx, `CREATE SCHEMA `+s+`;
				CREATE TABLE `+s+`.melange_tuples (
					subject_type TEXT NOT NULL, subject_id TEXT NOT NULL, relation TEXT NOT NULL,
					object_type TEXT NOT NULL, object_id TEXT NOT NULL)`)
			require.NoError(t, err)
			types, err := fgaparser.ParseSchema(path)
			require.NoError(t, err)
			analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, schema.ComputeRelationClosure(types)))

			var truncated []string
			for _, tc := range exposures {
				exposure := tc.exposure
				_, err := migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{
					Version: "v0.10.0", DatabaseSchema: clientgenSchema, Exposure: &exposure,
				})
				require.NoError(t, err, tc.label)

				installed := installedAccessible(t, db)
				generated := generatedAccessible(t, types, &exposure)

				// Functions installed but not named: the per-relation form of
				// a relation the accessible family does not expose.
				notPublic := map[string]bool{}
				accessibleOn := exposure.Functions.Accessible == nil || *exposure.Functions.Accessible
				for _, a := range analyses {
					if !accessibleOn || !exposure.Exposes(a.ObjectType, a.Relation) {
						notPublic[sqlgen.AccessibleObjectsFunctionName(a.ObjectType, a.Relation)] = true
					}
				}
				parentKeys := map[string]bool{}
				for _, pk := range compiler.ParentKeyFunctions(analyses) {
					parentKeys[pk.Name] = true
				}
				for _, name := range generated {
					require.Truef(t, slices.Contains(installed, name), "[%s] the client names %s, which is not installed", tc.label, name)
				}
				var leftover []string
				for _, name := range installed {
					if !slices.Contains(generated, name) {
						leftover = append(leftover, name)
						require.Truef(t, notPublic[name] && !parentKeys[name],
							"[%s] %s is installed for an exposed relation but the client does not name it", tc.label, name)
					}
				}
				if exposure.IsDefault() {
					require.Emptyf(t, leftover, "[%s] under the default exposure the client names every accessible function", tc.label)
					for _, name := range generated {
						if len(name) == 63 && hashSuffix.MatchString(name) {
							truncated = append(truncated, name)
						}
					}
				}

				// The names are callable with the database schema
				// qualifying them and melange_tuples on the search_path.
				if tc.label == "default" {
					callAccessible(t, db, generated)
				}
			}
			if path == "testutil/testdata/schema.fga" {
				// pull_request#can_read_security_advisories_and_vulnerability_reports
				// and its parent-key form.
				require.Equal(t, []string{
					"accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b",
					"accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c",
				}, truncated)
			}
		})
	}
}

// installedAccessible returns the accessible functions installed in the
// clientgen schema, without the accessible_objects dispatcher.
func installedAccessible(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname LIKE 'accessible\_%' AND p.proname <> 'accessible_objects'
		ORDER BY 1`, clientgenSchema)
	require.NoError(t, err)
	return scanStrings(t, rows)
}

var (
	tsAccessibleName = regexp.MustCompile(`"(accessible_[^"]+)"`)
	// hashSuffix ends a name codegen truncated to 63 bytes.
	hashSuffix = regexp.MustCompile(`_[0-9a-f]{8}$`)
)

// generatedAccessible returns the accessible function names the Go and
// TypeScript clients generated under exposure carry, after checking that
// both carry the same ones.
func generatedAccessible(t *testing.T, types []schema.TypeDefinition, exposure *compiler.Exposure) []string {
	t.Helper()
	goFiles, err := clientgen.Generate("go", types, &clientgen.Config{Exposure: exposure})
	require.NoError(t, err)
	f, err := parser.ParseFile(token.NewFileSet(), "schema_gen.go", goFiles["schema_gen.go"], 0)
	require.NoError(t, err)
	var goNames []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if !strings.HasPrefix(vs.Names[0].Name, "SQLAccessible") {
				continue
			}
			v, err := strconv.Unquote(vs.Values[0].(*ast.BasicLit).Value)
			require.NoError(t, err)
			goNames = append(goNames, v)
		}
	}

	tsFiles, err := clientgen.Generate("typescript", types, &clientgen.Config{Exposure: exposure})
	require.NoError(t, err)
	tsMatches := tsAccessibleName.FindAllStringSubmatch(string(tsFiles["sql.ts"]), -1)
	tsNames := make([]string, 0, len(tsMatches))
	for _, m := range tsMatches {
		tsNames = append(tsNames, m[1])
	}
	sort.Strings(goNames)
	require.ElementsMatch(t, goNames, tsNames, "the Go and TypeScript clients name the same functions")
	return goNames
}

// callAccessible calls each named function once, qualified with the
// clientgen schema.
func callAccessible(t *testing.T, db *sql.DB, names []string) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "SET search_path = "+pq.QuoteIdentifier(clientgenSchema)+", public")
	require.NoError(t, err)
	for _, name := range names {
		var n int
		require.NoErrorf(t, conn.QueryRowContext(ctx,
			`SELECT count(*) FROM `+pq.QuoteIdentifier(clientgenSchema)+`.`+pq.QuoteIdentifier(name)+`('user', 'nobody')`).Scan(&n), name)
		require.Zero(t, n, name)
	}
}
