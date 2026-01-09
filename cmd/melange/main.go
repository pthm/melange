// Package main provides a CLI for managing Melange authorization schemas.
//
// The CLI supports:
//   - validate: Check .fga schema syntax using the OpenFGA parser
//   - generate: Produce Go code with type-safe constants from schema
//   - migrate: Load schema into PostgreSQL (creates tables and functions)
//   - status: Check current migration state
//   - doctor: Run health checks on authorization infrastructure
//
// This tool is typically run during development and deployment to keep
// the database schema synchronized with .fga files.
//
// Usage:
//
//	melange [flags] <command>
//
// Commands that require database access (migrate, status) need -db or DATABASE_URL.
// Commands that only work with files (validate, generate) do not need database access.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"

	"github.com/pthm/melange/doctor"
	"github.com/pthm/melange/tooling/schema"
	"github.com/pthm/melange/tooling"
)

func main() {
	var (
		dbURL          = flag.String("db", os.Getenv("DATABASE_URL"), "Database URL")
		schemasDir     = flag.String("schemas-dir", "schemas", "Schemas directory")
		generateDir    = flag.String("generate-dir", "authz", "Output directory for generated code")
		generatePkg    = flag.String("generate-pkg", "authz", "Package name for generated code")
		idType         = flag.String("id-type", "string", "ID type for generated constructors (e.g., string, int64)")
		relationPrefix = flag.String("relation-prefix", "", "Prefix filter for relation constants (e.g., can_)")
		configFile     = flag.String("config", "melange.yaml", "Config file (optional)")
		dryRun         = flag.Bool("dry-run", false, "Output migration SQL without applying (migrate only)")
		force          = flag.Bool("force", false, "Force migration even if schema unchanged (migrate only)")
		verbose        = flag.Bool("verbose", false, "Show detailed output (doctor only)")
	)
	flag.Parse()

	// Try to load config file if it exists
	_ = configFile // TODO: implement config file loading with viper

	if flag.NArg() < 1 {
		printUsage()
		os.Exit(1)
	}

	// Commands that don't need database
	switch flag.Arg(0) {
	case "validate":
		validate(*schemasDir)
		return
	case "generate":
		generate(*schemasDir, *generateDir, *generatePkg, *idType, *relationPrefix)
		return
	}

	if *dbURL == "" {
		log.Fatal("DATABASE_URL or -db required")
	}

	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	switch flag.Arg(0) {
	case "status":
		migrator := schema.NewMigrator(db, *schemasDir)
		status(ctx, migrator)
	case "migrate":
		migrate(ctx, db, *schemasDir, *dryRun, *force)
	case "doctor":
		runDoctor(ctx, db, *schemasDir, *verbose)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", flag.Arg(0))
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("melange - PostgreSQL Fine-Grained Authorization")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  melange [flags] <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  migrate     Apply schema to database")
	fmt.Println("  generate    Generate Go types from schema")
	fmt.Println("  validate    Validate schema syntax")
	fmt.Println("  status      Show current schema status")
	fmt.Println("  doctor      Run health checks on authorization infrastructure")
	fmt.Println()
	fmt.Println("Global Flags:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Validate schema syntax")
	fmt.Println("  melange validate --schemas-dir internal/authz/schemas")
	fmt.Println()
	fmt.Println("  # Generate Go code")
	fmt.Println("  melange generate --schemas-dir schemas --generate-dir internal/authz --generate-pkg authz")
	fmt.Println()
	fmt.Println("  # Apply schema to database")
	fmt.Println("  melange migrate --db postgres://localhost/mydb --schemas-dir schemas")
	fmt.Println()
	fmt.Println("  # Preview migration SQL without applying")
	fmt.Println("  melange migrate --db postgres://localhost/mydb --dry-run")
	fmt.Println()
	fmt.Println("  # Force re-migration even if schema unchanged")
	fmt.Println("  melange migrate --db postgres://localhost/mydb --force")
	fmt.Println()
	fmt.Println("  # Check status")
	fmt.Println("  melange status --db postgres://localhost/mydb")
	fmt.Println()
	fmt.Println("  # Run health checks")
	fmt.Println("  melange doctor --db postgres://localhost/mydb")
	fmt.Println()
	fmt.Println("  # Run health checks with verbose output")
	fmt.Println("  melange doctor --db postgres://localhost/mydb --verbose")
}

// status queries the database for current migration state.
// Checks filesystem (schema.fga) and melange_tuples availability.
func status(ctx context.Context, m *schema.Migrator) {
	s, err := m.GetStatus(ctx)
	if err != nil {
		log.Fatalf("getting status: %v", err)
	}

	if s.SchemaExists {
		fmt.Println("Schema file:  present")
	} else {
		fmt.Println("Schema file:  missing")
	}
	if s.TuplesExists {
		fmt.Println("Tuples view:  present")
	} else {
		fmt.Println("Tuples view:  missing")
	}

	if !s.SchemaExists {
		fmt.Println("\nNo schema found. Create schemas/schema.fga to start.")
	} else if !s.TuplesExists {
		fmt.Println("\nTuples view not found.")
		fmt.Println("Create melange_tuples before running checks.")
	}
}

// migrate applies the schema to the database.
// Idempotent - safe to run multiple times.
func migrate(ctx context.Context, db *sql.DB, schemasDir string, dryRun, force bool) {
	opts := tooling.MigrateOptions{
		Force: force,
	}

	if dryRun {
		opts.DryRun = os.Stdout
		fmt.Fprintln(os.Stderr, "-- Dry-run mode: SQL will be output but not applied")
		fmt.Fprintln(os.Stderr, "")
	} else {
		fmt.Println("Applying authz infrastructure...")
	}

	skipped, err := tooling.MigrateWithOptions(ctx, db, schemasDir, opts)
	if err != nil {
		log.Fatalf("migrating: %v", err)
	}

	if dryRun {
		// Dry-run output was written to stdout
		return
	}

	if skipped {
		fmt.Println("Schema unchanged, migration skipped.")
		fmt.Println("Use --force to re-apply.")
	} else {
		fmt.Println("Authz schema applied successfully.")
	}

	// Check for melange_tuples and warn if missing
	migrator := schema.NewMigrator(db, schemasDir)
	status, err := migrator.GetStatus(ctx)
	if err != nil {
		log.Printf("Warning: could not check status: %v", err)
		return
	}
	if !status.TuplesExists {
		fmt.Println()
		fmt.Println("WARNING: melange_tuples view/table does not exist.")
		fmt.Println("         Permission checks will fail until you create it.")
	}
}

// validate checks .fga schema syntax using the OpenFGA parser.
// Does not require database connection.
func validate(schemasDir string) {
	schemaPath := schemasDir + "/schema.fga"
	if _, err := os.Stat(schemaPath); err != nil {
		fmt.Println("No schema found at", schemaPath)
		os.Exit(1)
	}

	// Parse the schema to check for syntax errors
	types, err := tooling.ParseSchema(schemaPath)
	if err != nil {
		fmt.Printf("Parse error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Schema is valid. Found %d types:\n", len(types))
	for _, t := range types {
		fmt.Printf("  - %s (%d relations)\n", t.Name, len(t.Relations))
	}

	fmt.Println()
	fmt.Println("For full validation, install OpenFGA CLI:")
	fmt.Println("  go install github.com/openfga/cli/cmd/fga@latest")
	fmt.Printf("  fga model validate --file %s\n", schemaPath)
}

// generate produces Go code from .fga schema.
// Does not require database connection.
// Outputs to generateDir with package name generatePkg.
func generate(schemasDir, generateDir, generatePkg, idType, relationPrefix string) {
	schemaPath := schemasDir + "/schema.fga"
	if _, err := os.Stat(schemaPath); err != nil {
		fmt.Println("No schema found at", schemaPath)
		os.Exit(1)
	}

	types, err := tooling.ParseSchema(schemaPath)
	if err != nil {
		fmt.Printf("Parse error: %v\n", err)
		os.Exit(1)
	}

	// Create output directory if needed
	if err := os.MkdirAll(generateDir, 0o755); err != nil {
		fmt.Printf("Creating output directory: %v\n", err)
		os.Exit(1)
	}

	// Generate to schema_gen.go in the output directory
	outPath := generateDir + "/schema_gen.go"
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Printf("Creating output file: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()

	cfg := &tooling.GenerateConfig{
		Package:              generatePkg,
		RelationPrefixFilter: relationPrefix,
		IDType:               idType,
	}

	if err := tooling.GenerateGo(f, types, cfg); err != nil {
		fmt.Printf("Generating code: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated %s from %s\n", outPath, schemaPath)
}

// runDoctor performs health checks on the authorization infrastructure.
// Validates schema files, database state, generated functions, and data health.
func runDoctor(ctx context.Context, db *sql.DB, schemasDir string, verbose bool) {
	fmt.Println("melange doctor - Health Check")

	d := doctor.New(db, schemasDir)
	report, err := d.Run(ctx)
	if err != nil {
		log.Fatalf("running doctor: %v", err)
	}

	report.Print(os.Stdout, verbose)

	if report.HasErrors() {
		os.Exit(1)
	}
}
