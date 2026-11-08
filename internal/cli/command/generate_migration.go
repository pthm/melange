package command

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

var (
	genMigrationSchema         string
	genMigrationOutput         string
	genMigrationName           string
	genMigrationFormat         string
	genMigrationUp             bool
	genMigrationDown           bool
	genMigrationDB             string
	genMigrationDBSchema       string
	genMigrationGitRef         string
	genMigrationPreviousSchema string
)

var generateMigrationCmd = &cobra.Command{
	Use:   "migration",
	Short: "Generate versioned migration SQL files",
	Long: `Generate UP and DOWN SQL migration files from an OpenFGA schema.

The generated files can be used with any migration framework (golang-migrate,
sqlx, Flyway, etc.) instead of running 'melange migrate' directly.

Three comparison modes determine orphaned functions to drop:
  - (default) Full mode: no comparison, outputs all functions
  - --db: reads previous state from the database (most reliable)
  - --git-ref: reads previous schema from git history
  - --previous-schema: reads previous schema from a file

The SQL is generated under the local SQL API exposure: the sql section of the
configuration with the exposure flags on top. A comparison mode emits only
the functions that changed, which needs the previous exposure: --db reads the
recorded one and --git-ref the sql section of the config file at that ref.
When the previous exposure is unknown (--previous-schema, or a config file
that cannot be read at the ref), every function is emitted and orphans are
still dropped.`,
	Example: `  # Generate UP and DOWN files
  melange generate migration --schema schema.fga --output migrations/

  # Output UP SQL to stdout (for piping)
  melange generate migration --schema schema.fga --up

  # With database comparison (detects orphaned functions)
  melange generate migration --schema schema.fga --output migrations/ --db postgres://localhost/mydb

  # With git comparison
  melange generate migration --schema schema.fga --output migrations/ --git-ref HEAD~1

  # With file comparison
  melange generate migration --schema schema.fga --output migrations/ --previous-schema old.fga`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve values: flags > config > defaults
		databaseSchema := resolveString(genMigrationDBSchema, cfg.Database.Schema, "public")
		schemaPath := resolveString(genMigrationSchema, cfg.Schema)
		output := resolveString(genMigrationOutput, cfg.Generate.Migration.Output)
		name := resolveString(genMigrationName, cfg.Generate.Migration.Name, "melange")
		format := resolveString(genMigrationFormat, cfg.Generate.Migration.Format, "split")

		// Validate required fields
		if schemaPath == "" {
			return cli.ConfigError("--schema is required", nil)
		}

		// Validate format
		if format != "split" && format != "single" {
			return cli.ConfigError(fmt.Sprintf("--format must be \"split\" or \"single\", got %q", format), nil)
		}

		// Validate mutual exclusivity of comparison flags
		comparisonFlags := boolCount(genMigrationDB != "", genMigrationGitRef != "", genMigrationPreviousSchema != "")
		if comparisonFlags > 1 {
			return cli.ConfigError("--db, --git-ref, and --previous-schema are mutually exclusive", nil)
		}

		// Validate stdout mode requires --up or --down
		if output == "" && !genMigrationUp && !genMigrationDown {
			return cli.ConfigError("stdout mode requires --up or --down flag", nil)
		}

		exposure, err := resolveExposure(cmd, cfg.SQL)
		if err != nil {
			return err
		}

		// Parse current schema (supports both .fga files and fga.mod manifests)
		if _, err := os.Stat(schemaPath); err != nil {
			return cli.SchemaParseError(fmt.Sprintf("schema not found: %s", schemaPath), nil)
		}

		schemaContent, err := parser.ReadSchemaContent(schemaPath)
		if err != nil {
			return cli.GeneralError("reading schema", err)
		}

		types, err := parser.ParseSchema(schemaPath)
		if err != nil {
			return cli.SchemaParseError("parsing schema", err)
		}

		// Run compilation pipeline
		if err := schema.DetectCycles(types); err != nil {
			return cli.SchemaParseError("schema has cycles", err)
		}

		// Resolve previous state
		opts := compiler.MigrationOptions{
			DatabaseSchema: databaseSchema,
			Version:        version.Version,
			SchemaChecksum: migrator.ComputeSchemaChecksum(string(schemaContent)),
			CodegenVersion: migrator.CodegenVersion(),
			Exposure:       exposure,
		}

		// previousTypes drives the semantic-diff header, when a previous model can
		// be reconstructed from the chosen comparison source.
		var previousTypes []schema.TypeDefinition

		if genMigrationDB != "" {
			prevState, err := previousStateFromDB(genMigrationDB, databaseSchema)
			if err != nil {
				return err
			}
			if prevState != nil {
				opts.PreviousFunctionNames = prevState.FunctionNames
				opts.PreviousChecksums = prevState.FunctionChecksums
				opts.PreviousSource = "database"
				opts.PreviousExposure = prevState.Exposure
				previousTypes = prevState.Types
			}
		} else if genMigrationGitRef != "" {
			prevExposure, known := previousExposureAtRef(genMigrationGitRef)
			prevState, err := previousStateFromSchema(genMigrationGitRef, schemaPath, databaseSchema, true, prevExposure)
			if err != nil {
				return err
			}
			opts.PreviousFunctionNames = prevState.FunctionNames
			opts.PreviousChecksums = prevState.FunctionChecksums
			opts.PreviousSource = fmt.Sprintf("git:%s", genMigrationGitRef)
			opts.PreviousExposure = prevState.Exposure
			opts.PreviousExposureUnknown = !known
			previousTypes = prevState.Types
		} else if genMigrationPreviousSchema != "" {
			if parser.IsModularSchema(genMigrationPreviousSchema) {
				return cli.ConfigError("--previous-schema does not support modular schemas (fga.mod); use --db or --git-ref instead", nil)
			}
			prevState, err := previousStateFromSchema(genMigrationPreviousSchema, "", databaseSchema, false, nil)
			if err != nil {
				return err
			}
			opts.PreviousFunctionNames = prevState.FunctionNames
			opts.PreviousChecksums = prevState.FunctionChecksums
			opts.PreviousSource = fmt.Sprintf("file:%s", genMigrationPreviousSchema)
			opts.PreviousExposureUnknown = true
			previousTypes = prevState.Types
		}

		current, err := compileSchema(types, databaseSchema, opts.Exposure)
		if err != nil {
			return cli.GeneralError("compiling schema", err)
		}
		opts.NamedFunctions = current.named

		// Generate migration SQL
		result := compiler.GenerateMigrationSQL(current.check, current.list, current.names, opts)

		// Document what the migration changes, when a previous model is known.
		if previousTypes != nil {
			result.Up = semanticDiffHeader(schema.Diff(previousTypes, types)) + result.Up
		}

		// Write output
		if output == "" {
			return writeStdout(result)
		}
		return writeFiles(result, output, name, format)
	},
}

func init() {
	f := generateMigrationCmd.Flags()
	f.StringVar(&genMigrationSchema, "schema", "", "path to .fga file or fga.mod manifest")
	f.StringVar(&genMigrationOutput, "output", "", "output directory (default: stdout)")
	f.StringVar(&genMigrationName, "name", "", "migration name suffix (default: melange)")
	f.StringVar(&genMigrationFormat, "format", "", `"split" (.up.sql/.down.sql) or "single" (default: split)`)
	f.BoolVar(&genMigrationUp, "up", false, "output only the UP migration")
	f.BoolVar(&genMigrationDown, "down", false, "output only the DOWN migration")
	f.StringVar(&genMigrationDB, "db", "", "database URL for comparison (reads previous state)")
	f.StringVar(&genMigrationDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&genMigrationGitRef, "git-ref", "", "git ref for comparison (reads previous schema)")
	f.StringVar(&genMigrationPreviousSchema, "previous-schema", "", "path to previous .fga file for comparison (modular schemas not supported)")
	addExposureFlags(f)
}

func writeStdout(result compiler.MigrationSQL) error {
	if genMigrationUp {
		if _, err := fmt.Fprint(os.Stdout, result.Up); err != nil {
			return cli.GeneralError("writing to stdout", err)
		}
	}
	if genMigrationDown {
		if _, err := fmt.Fprint(os.Stdout, result.Down); err != nil {
			return cli.GeneralError("writing to stdout", err)
		}
	}
	return nil
}

func writeFiles(result compiler.MigrationSQL, output, name, format string) error {
	if err := os.MkdirAll(output, 0o755); err != nil {
		return cli.GeneralError("creating output directory", err)
	}

	timestamp := time.Now().Format("20060102150405")
	writeUp := genMigrationUp || !genMigrationDown   // write up unless only --down
	writeDown := genMigrationDown || !genMigrationUp // write down unless only --up

	if format == "single" {
		content := ""
		if writeUp {
			content += result.Up
		}
		if writeDown {
			content += result.Down
		}
		path := filepath.Join(output, fmt.Sprintf("%s_%s.sql", timestamp, name))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return cli.GeneralError(fmt.Sprintf("writing %s", path), err)
		}
		if !quiet {
			fmt.Printf("Generated %s\n", path)
		}
	} else {
		// split format
		if writeUp {
			path := filepath.Join(output, fmt.Sprintf("%s_%s.up.sql", timestamp, name))
			if err := os.WriteFile(path, []byte(result.Up), 0o644); err != nil {
				return cli.GeneralError(fmt.Sprintf("writing %s", path), err)
			}
			if !quiet {
				fmt.Printf("Generated %s\n", path)
			}
		}
		if writeDown {
			path := filepath.Join(output, fmt.Sprintf("%s_%s.down.sql", timestamp, name))
			if err := os.WriteFile(path, []byte(result.Down), 0o644); err != nil {
				return cli.GeneralError(fmt.Sprintf("writing %s", path), err)
			}
			if !quiet {
				fmt.Printf("Generated %s\n", path)
			}
		}
	}

	return nil
}

// semanticDiffHeader renders a SQL comment block summarizing how the migration
// changes the model relative to the previous one. Returns "" when there are no
// changes, so an unchanged (version-only) migration gets no header.
func semanticDiffHeader(diff schema.SchemaDiff) string {
	if diff.Empty() {
		return ""
	}
	additive, breaking := diff.Counts()
	var b strings.Builder
	fmt.Fprintf(&b, "-- Schema changes vs previous model: %d breaking, %d additive\n", breaking, additive)
	for _, c := range diff.Changes {
		fmt.Fprintf(&b, "--   [%s] %s\n", c.Class, c.Summary)
	}
	b.WriteString("--\n")
	return b.String()
}

// previousState holds the function inventory from a prior migration.
// It normalises the output of all three comparison modes (--db, --git-ref,
// --previous-schema) so the caller can handle them uniformly.
type previousState struct {
	FunctionNames     []string
	FunctionChecksums map[string]string

	// Types is the previous parsed model, when it can be reconstructed. It drives
	// the semantic-diff header. Nil when unavailable (e.g. a database migrated
	// before model storage existed), in which case no header is emitted.
	Types []schema.TypeDefinition

	// Exposure is the exposure the previous functions were generated with: the
	// recorded one for --db, the one read at the ref for --git-ref, nil
	// (unknown) otherwise, when the previous schema is compiled under the
	// default exposure.
	Exposure *compiler.Exposure
}

// previousStateFromDB reads function names and checksums from the most recent
// melange_migrations record. Returns nil without error when no record exists,
// which causes the caller to omit PreviousFunctionNames and emit a full migration.
func previousStateFromDB(dsn, databaseSchema string) (*previousState, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)
	rec, err := m.GetLastMigration(ctx)
	if err != nil {
		return nil, cli.GeneralError("reading last migration from database", err)
	}
	if rec == nil {
		return nil, nil // No previous migration — fall back to full mode
	}

	// Warn if the database was previously managed by `melange migrate`
	if rec.MelangeVersion != "" && !quiet {
		fmt.Fprintln(os.Stderr, "WARNING: This database has migrations applied by 'melange migrate' (builtin mode).")
		fmt.Fprintln(os.Stderr, "         Switching to generated migrations means 'melange migrate' will no longer")
		fmt.Fprintln(os.Stderr, "         track state correctly. Do not mix both approaches on the same database.")
		fmt.Fprintln(os.Stderr)
	}

	// Reconstruct the previous model for the semantic-diff header (best-effort).
	// A model that can't be recovered — never recorded, or a modular DSL, which
	// won't parse as a single schema — leaves prevTypes nil, and the migration is
	// generated without a header rather than failing.
	prevTypes, terr := migrator.DeployedTypes(rec)
	if terr != nil {
		prevTypes = nil
	}

	return &previousState{
		FunctionNames:     rec.FunctionNames,
		FunctionChecksums: rec.FunctionChecksums,
		Types:             prevTypes,
		Exposure:          &rec.Exposure,
	}, nil
}

// previousStateFromSchema compiles a previous schema into a function inventory.
// Both the git-ref and file comparison modes reduce to the same operation: obtain
// schema content, run the full compilation pipeline, and collect function names
// and checksums. The isGitRef flag determines how the content is retrieved.
//
// When isGitRef is true, pathOrRef is a git ref and schemaPath is the repo-relative
// path to the schema file. When false, pathOrRef is a local file path and
// schemaPath is unused.
//
// exposure is the exposure the previous schema was deployed with, nil when
// unknown. An unknown one compiles under the default exposure, which installs
// every function any other does apart from the UnexposedFunctionName helper.
// The helper is added to the names, so every function the deployment may
// have left behind is dropped as an orphan (DROP IF EXISTS).
func previousStateFromSchema(pathOrRef, schemaPath, databaseSchema string, isGitRef bool, exposure *compiler.Exposure) (*previousState, error) {
	types, err := parsePreviousSchema(pathOrRef, schemaPath, isGitRef)
	if err != nil {
		return nil, err
	}

	var compileExposure compiler.Exposure
	if exposure != nil {
		compileExposure = *exposure
	}
	prev, err := compileSchema(types, databaseSchema, compileExposure)
	if err != nil {
		return nil, cli.GeneralError("compiling previous schema", err)
	}

	names := prev.names
	if exposure == nil && !slices.Contains(names, compiler.UnexposedFunctionName) {
		names = append(names, compiler.UnexposedFunctionName)
	}
	return &previousState{
		FunctionNames:     names,
		FunctionChecksums: migrator.ComputeFunctionChecksums(prev.named),
		Types:             types,
		Exposure:          exposure,
	}, nil
}

// previousExposureAtRef returns the exposure the config file had at a git
// ref: its sql section, under the same defaults and MELANGE_SQL_* variables
// as now, but not this run's exposure flags, which do not persist. With
// --config the file is that one, at its path relative to the repository
// root. Otherwise it is the file config discovery finds at the ref, so
// deleting a restricted config file still widens; when there is none there
// either, the previous exposure is the defaults and variables alone. known
// is false when the file cannot be read at the ref (untracked or invalid),
// or when discovery at the ref finds a different file from the one in use
// now: the one in use may be untracked, and a file that shadowed it at the
// ref may have been deleted since. The reason is printed, and the caller
// must then emit every function.
func previousExposureAtRef(ref string) (exposure *compiler.Exposure, known bool) {
	unknown := func(reason string, err error) (*compiler.Exposure, bool) {
		if !quiet {
			fmt.Fprintf(os.Stderr, "note: %s at %s (%v); the previous SQL API exposure is unknown, so every function is included\n", reason, ref, err)
		}
		return nil, false
	}
	var rel string
	if configPath != "" {
		r, err := gitRelativePath(configPath)
		if err != nil {
			return unknown("cannot locate "+configPath+" in the repository", err)
		}
		rel = filepath.ToSlash(r)
	}
	if cfgFile == "" {
		found, err := configPathAtRef(ref)
		if err != nil {
			return unknown("cannot look for a config file", err)
		}
		if configPath != "" && found != rel {
			if found == "" {
				found = "none"
			}
			return unknown("config discovery does not find "+rel, fmt.Errorf("it finds %s", found))
		}
		rel = found
	}
	var content []byte
	if rel != "" {
		c, err := gitShowFile(ref, rel)
		if err != nil {
			return unknown("cannot read "+rel, err)
		}
		content = []byte(c)
	}
	prev, err := cli.ParseConfig(content)
	if err != nil {
		return unknown("cannot load the config file", err)
	}
	e := prev.SQL.Exposure()
	return &e, true
}

// configPathAtRef returns the config file discovery finds at a git ref from
// the current directory, as a path relative to the repository root, or ""
// when the ref has none.
func configPathAtRef(ref string) (string, error) {
	prefix, err := exec.Command("git", "rev-parse", "--show-prefix").Output() //nolint:gosec // no user input
	if err != nil {
		return "", err
	}
	candidates := cli.ConfigSearchPaths(strings.TrimSpace(string(prefix)))
	args := append([]string{"ls-tree", "--full-tree", "--name-only", ref, "--"}, candidates...)
	out, err := exec.Command("git", args...).Output() //nolint:gosec // ref is from a trusted CLI flag
	if err != nil {
		return "", err
	}
	found := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		found[line] = true
	}
	for _, c := range candidates {
		if found[c] {
			return c, nil
		}
	}
	return "", nil
}

// compiledSchema is a schema's generated SQL under one exposure, with the
// function names and name/SQL pairs a migration tracks.
type compiledSchema struct {
	check compiler.GeneratedSQL
	list  compiler.ListGeneratedSQL
	names []string
	named []compiler.NamedFunction
}

// compileSchema runs the compilation pipeline for types under exposure.
func compileSchema(types []schema.TypeDefinition, databaseSchema string, exposure compiler.Exposure) (compiledSchema, error) {
	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inlineData := compiler.BuildInlineSQLData(closureRows, analyses)
	opts := compiler.GenerateSQLOptions{Exposure: exposure}

	check, err := compiler.GenerateSQLWithOptions(analyses, inlineData, databaseSchema, opts)
	if err != nil {
		return compiledSchema{}, fmt.Errorf("generating check SQL: %w", err)
	}
	list, err := compiler.GenerateListSQLWithOptions(analyses, inlineData, databaseSchema, opts)
	if err != nil {
		return compiledSchema{}, fmt.Errorf("generating list SQL: %w", err)
	}
	return compiledSchema{
		check: check,
		list:  list,
		names: compiler.CollectFunctionNamesWithOptions(analyses, opts),
		named: compiler.CollectNamedFunctionsWithOptions(check, list, analyses, opts),
	}, nil
}

// parsePreviousSchema reads and parses a previous schema from either a git ref
// or a local file path. Supports both single .fga files and fga.mod manifests
// (manifests only via git-ref; --previous-schema rejects them earlier).
func parsePreviousSchema(pathOrRef, schemaPath string, isGitRef bool) ([]schema.TypeDefinition, error) {
	if isGitRef && parser.IsModularSchema(schemaPath) {
		return parseModularSchemaFromGit(pathOrRef, schemaPath)
	}

	var content string
	if isGitRef {
		c, err := gitShowFile(pathOrRef, schemaPath)
		if err != nil {
			return nil, cli.GeneralError(
				fmt.Sprintf("reading schema from git ref %q (path: %s)", pathOrRef, schemaPath),
				fmt.Errorf("%w — ensure the ref exists and the schema path is relative to the repo root", err),
			)
		}
		content = c
	} else {
		raw, err := os.ReadFile(pathOrRef) //nolint:gosec // path is from trusted CLI flag
		if err != nil {
			return nil, cli.GeneralError(fmt.Sprintf("reading previous schema: %s", pathOrRef), err)
		}
		content = string(raw)
	}

	types, err := parser.ParseRecordedSchemaString(content)
	if err != nil {
		return nil, cli.SchemaParseError("parsing previous schema", err)
	}
	return types, nil
}

// parseModularSchemaFromGit reads a modular schema (fga.mod + module files)
// from a git ref by fetching the manifest, parsing its entries, then reading
// each referenced module file from the same ref.
func parseModularSchemaFromGit(gitRef, manifestPath string) ([]schema.TypeDefinition, error) {
	manifestContent, err := gitShowFile(gitRef, manifestPath)
	if err != nil {
		return nil, cli.GeneralError(
			fmt.Sprintf("reading manifest from git ref %q (path: %s)", gitRef, manifestPath),
			fmt.Errorf("%w — ensure the ref exists and the manifest path is relative to the repo root", err),
		)
	}

	schemaVersion, modulePaths, err := parser.ParseManifestEntries(manifestContent)
	if err != nil {
		return nil, cli.SchemaParseError("parsing manifest from git ref", err)
	}

	baseDir := filepath.Dir(manifestPath)
	modules := make(map[string]string, len(modulePaths))
	for _, p := range modulePaths {
		gitPath := filepath.Join(baseDir, p)
		content, err := gitShowFile(gitRef, gitPath)
		if err != nil {
			return nil, cli.GeneralError(
				fmt.Sprintf("reading module %s from git ref %q", p, gitRef),
				err,
			)
		}
		modules[p] = content
	}

	types, err := parser.ParseRecordedModularSchemaFromStrings(modules, schemaVersion)
	if err != nil {
		return nil, cli.SchemaParseError("parsing modular schema from git ref", err)
	}
	return types, nil
}

// gitShowFile reads a file from a git ref using "git show ref:path".
func gitShowFile(ref, path string) (string, error) {
	cmd := exec.Command("git", "show", ref+":"+path) //nolint:gosec // ref and path are from trusted CLI flags
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// gitRelativePath converts a schema path to one relative to the git repo root,
// as required by "git show ref:path". A relative path is resolved against the
// current working directory first, so it works from any subdirectory.
func gitRelativePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", cli.GeneralError("resolving schema path", err)
	}
	// git reports the root with symlinks resolved (macOS /var is
	// /private/var), so resolve the path too when it exists.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output() //nolint:gosec // no user input
	if err != nil {
		return "", cli.GeneralError("locating git repository root", err)
	}
	rel, err := filepath.Rel(strings.TrimSpace(string(out)), abs)
	if err != nil {
		return "", cli.GeneralError("resolving schema path relative to repo root", err)
	}
	return rel, nil
}
