package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/lib/cli"
	"github.com/pthm/melange/lib/version"
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
  - --previous-schema: reads previous schema from a file`,
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
		databaseSchema := resolveString(genMigrationDBSchema, cfg.Database.Schema)
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

		closureRows := schema.ComputeRelationClosure(types)
		analyses := compiler.AnalyzeRelations(types, closureRows)
		analyses = compiler.ComputeCanGenerate(analyses)
		inlineData := compiler.BuildInlineSQLData(closureRows, analyses)

		generatedSQL, err := compiler.GenerateSQL(analyses, inlineData, databaseSchema)
		if err != nil {
			return cli.GeneralError("generating check SQL", err)
		}

		listSQL, err := compiler.GenerateListSQL(analyses, inlineData, databaseSchema)
		if err != nil {
			return cli.GeneralError("generating list SQL", err)
		}

		expectedFunctions := compiler.CollectFunctionNames(analyses)
		namedFunctions := compiler.CollectNamedFunctions(generatedSQL, listSQL, analyses)

		// Resolve previous state
		opts := compiler.MigrationOptions{
			DatabaseSchema: databaseSchema,
			Version:        version.Version,
			SchemaChecksum: migrator.ComputeSchemaChecksum(string(schemaContent)),
			CodegenVersion: migrator.CodegenVersion(),
			NamedFunctions: namedFunctions,
		}

		if genMigrationDB != "" {
			prevState, err := previousStateFromDB(genMigrationDB, databaseSchema)
			if err != nil {
				return err
			}
			if prevState != nil {
				opts.PreviousFunctionNames = prevState.FunctionNames
				opts.PreviousChecksums = prevState.FunctionChecksums
				opts.PreviousSource = "database"
			}
		} else if genMigrationGitRef != "" {
			prevState, err := previousStateFromSchema(genMigrationGitRef, schemaPath, databaseSchema, true)
			if err != nil {
				return err
			}
			opts.PreviousFunctionNames = prevState.FunctionNames
			opts.PreviousChecksums = prevState.FunctionChecksums
			opts.PreviousSource = fmt.Sprintf("git:%s", genMigrationGitRef)
		} else if genMigrationPreviousSchema != "" {
			if parser.IsModularSchema(genMigrationPreviousSchema) {
				return cli.ConfigError("--previous-schema does not support modular schemas (fga.mod); use --db or --git-ref instead", nil)
			}
			prevState, err := previousStateFromSchema(genMigrationPreviousSchema, "", databaseSchema, false)
			if err != nil {
				return err
			}
			opts.PreviousFunctionNames = prevState.FunctionNames
			opts.PreviousChecksums = prevState.FunctionChecksums
			opts.PreviousSource = fmt.Sprintf("file:%s", genMigrationPreviousSchema)
		}

		// Generate migration SQL
		result := compiler.GenerateMigrationSQL(generatedSQL, listSQL, expectedFunctions, opts)

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
	f.StringVar(&genMigrationDBSchema, "db-schema", "", "database schema")
	f.StringVar(&genMigrationGitRef, "git-ref", "", "git ref for comparison (reads previous schema)")
	f.StringVar(&genMigrationPreviousSchema, "previous-schema", "", "path to previous .fga file for comparison (modular schemas not supported)")
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

// previousState holds the function inventory from a prior migration.
// It normalises the output of all three comparison modes (--db, --git-ref,
// --previous-schema) so the caller can handle them uniformly.
type previousState struct {
	FunctionNames     []string
	FunctionChecksums map[string]string
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

	return &previousState{
		FunctionNames:     rec.FunctionNames,
		FunctionChecksums: rec.FunctionChecksums,
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
func previousStateFromSchema(pathOrRef, schemaPath, databaseSchema string, isGitRef bool) (*previousState, error) {
	types, err := parsePreviousSchema(pathOrRef, schemaPath, isGitRef)
	if err != nil {
		return nil, err
	}

	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inlineData := compiler.BuildInlineSQLData(closureRows, analyses)

	genSQL, err := compiler.GenerateSQL(analyses, inlineData, databaseSchema)
	if err != nil {
		return nil, cli.GeneralError("generating check SQL for previous schema", err)
	}
	listSQL, err := compiler.GenerateListSQL(analyses, inlineData, databaseSchema)
	if err != nil {
		return nil, cli.GeneralError("generating list SQL for previous schema", err)
	}

	names := compiler.CollectFunctionNames(analyses)
	namedFns := compiler.CollectNamedFunctions(genSQL, listSQL, analyses)
	checksums := migrator.ComputeFunctionChecksums(namedFns)

	return &previousState{
		FunctionNames:     names,
		FunctionChecksums: checksums,
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

	types, err := parser.ParseSchemaString(content)
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

	types, err := parser.ParseModularSchemaFromStrings(modules, schemaVersion)
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
