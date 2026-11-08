package typescript_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/clientgen/typescript"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
)

var updateFixtures = flag.Bool("update-fixtures", false, "rewrite the generated TypeScript fixtures in clients/typescript/test/fixtures/clientgen")

// fixturesDir holds generated clients that the TypeScript package's unit
// tests type-check with tsc (src/generated-client.test.ts).
const fixturesDir = "../../../clients/typescript/test/fixtures/clientgen"

// TestGenerator_Fixtures keeps the type-checked fixtures equal to what the
// generator produces for test/testutil/testdata/schema.fga, under the
// default exposure and with both accessible families off (empty maps). Run
// `just update-golden` after an intended change.
func TestGenerator_Fixtures(t *testing.T) {
	types, err := parser.ParseSchema("../../../test/testutil/testdata/schema.fga")
	if err != nil {
		t.Fatal(err)
	}
	off := false
	for dir, cfg := range map[string]*clientgen.Config{
		"schema": {SourcePath: "test/testutil/testdata/schema.fga"},
		"schema-no-accessible": {
			SourcePath: "test/testutil/testdata/schema.fga",
			Exposure:   &compiler.Exposure{Functions: compiler.FunctionFamilies{Accessible: &off, ParentKey: &off}},
			Relations:  &compiler.RelationPatterns{Include: []string{"can_*"}},
		},
	} {
		t.Run(dir, func(t *testing.T) {
			files, err := (&typescript.Generator{}).Generate(types, cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixturesDir, dir)
			if *updateFixtures {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				for name, content := range files {
					if err := os.WriteFile(filepath.Join(path, name), content, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				t.Fatalf("%v (run just update-golden)", err)
			}
			onDisk := make([]string, 0, len(entries))
			for _, e := range entries {
				onDisk = append(onDisk, e.Name())
			}
			generated := make([]string, 0, len(files))
			for name := range files {
				generated = append(generated, name)
			}
			slices.Sort(generated)
			if !slices.Equal(onDisk, generated) {
				t.Fatalf("%s holds %v, the generator writes %v (run just update-golden)", path, onDisk, generated)
			}
			for name, content := range files {
				got, err := os.ReadFile(filepath.Join(path, name))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, content) {
					t.Errorf("%s differs from the generated %s (run just update-golden)", filepath.Join(path, name), name)
				}
			}
		})
	}
}
