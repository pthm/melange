// Package main provides a CLI for managing Melange authorization schemas.
//
// The CLI supports:
//   - validate: Check .fga schema syntax using the OpenFGA parser
//   - generate client: Produce type-safe client code for Go, TypeScript, or Python
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
	"path/filepath"
	"strings"

	_ "github.com/lib/pq"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/pkg/clientgen"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Handle subcommands
	switch os.Args[1] {
	case "validate":
		runValidate(os.Args[2:])
	case "generate":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: melange generate client [flags]")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "client":
			runGenerateClient(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "Unknown generate subcommand: %s\n", os.Args[2])
			fmt.Fprintln(os.Stderr, "Usage: melange generate client [flags]")
			os.Exit(1)
		}
	case "migrate":
		runMigrate(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("melange - PostgreSQL Fine-Grained Authorization")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  melange <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  generate client  Generate type-safe client code from schema")
	fmt.Println("  migrate          Apply schema to database")
	fmt.Println("  validate         Validate schema syntax")
	fmt.Println("  status           Show current schema status")
	fmt.Println("  doctor           Run health checks on authorization infrastructure")
	fmt.Println()
	fmt.Println("Run 'melange <command> --help' for more information on a command.")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Validate schema syntax")
	fmt.Println("  melange validate --schema schemas/schema.fga")
	fmt.Println()
	fmt.Println("  # Generate Go client code")
	fmt.Println("  melange generate client --runtime go --schema schemas/schema.fga --output internal/authz/")
	fmt.Println()
	fmt.Println("  # Apply schema to database")
	fmt.Println("  melange migrate --db postgres://localhost/mydb --schemas-dir schemas")
	fmt.Println()
	fmt.Println("  # Check status")
	fmt.Println("  melange status --db postgres://localhost/mydb")
	fmt.Println()
	fmt.Println("  # Run health checks")
	fmt.Println("  melange doctor --db postgres://localhost/mydb --verbose")
}

// runValidate checks .fga schema syntax using the OpenFGA parser.
func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	schemaPath := fs.String("schema", "", "Path to schema.fga file (required)")
	fs.Usage = func() {
		fmt.Println("Usage: melange validate --schema <path>")
		fmt.Println()
		fmt.Println("Validate schema syntax using the OpenFGA parser.")
		fmt.Println()
		fmt.Println("Flags:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *schemaPath == "" {
		// Try legacy schemas-dir pattern
		if _, err := os.Stat("schemas/schema.fga"); err == nil {
			*schemaPath = "schemas/schema.fga"
		} else {
			fmt.Fprintln(os.Stderr, "Error: --schema is required")
			fs.Usage()
			os.Exit(1)
		}
	}

	if _, err := os.Stat(*schemaPath); err != nil {
		fmt.Printf("Schema not found: %s\n", *schemaPath)
		os.Exit(1)
	}

	types, err := parser.ParseSchema(*schemaPath)
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
	fmt.Printf("  fga model validate --file %s\n", *schemaPath)
}

// runGenerateClient produces client code from .fga schema.
func runGenerateClient(args []string) {
	fs := flag.NewFlagSet("generate client", flag.ExitOnError)
	runtime := fs.String("runtime", "", "Target runtime: "+strings.Join(clientgen.ListRuntimes(), ", ")+" (required)")
	schemaPath := fs.String("schema", "", "Path to schema.fga file (required)")
	output := fs.String("output", "", "Output directory or file path (default: stdout)")
	pkg := fs.String("package", "authz", "Package/module name for generated code")
	filter := fs.String("filter", "", "Relation prefix filter (e.g., can_)")
	idType := fs.String("id-type", "string", "ID type for constructors (Go only: string, int64, etc.)")

	fs.Usage = func() {
		fmt.Println("Usage: melange generate client --runtime <lang> --schema <path> [flags]")
		fmt.Println()
		fmt.Println("Generate type-safe client code from an authorization schema.")
		fmt.Println()
		fmt.Println("Supported runtimes:")
		for _, r := range clientgen.ListRuntimes() {
			fmt.Printf("  - %s\n", r)
		}
		fmt.Println()
		fmt.Println("Flags:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  # Generate Go code to a directory")
		fmt.Println("  melange generate client --runtime go --schema schemas/schema.fga --output internal/authz/")
		fmt.Println()
		fmt.Println("  # Generate with custom package name")
		fmt.Println("  melange generate client --runtime go --schema schemas/schema.fga --output . --package myauthz")
		fmt.Println()
		fmt.Println("  # Generate only permission relations (can_*)")
		fmt.Println("  melange generate client --runtime go --schema schemas/schema.fga --output . --filter can_")
		fmt.Println()
		fmt.Println("  # Output to stdout")
		fmt.Println("  melange generate client --runtime go --schema schemas/schema.fga")
	}
	_ = fs.Parse(args)

	// Validate required flags
	if *runtime == "" {
		fmt.Fprintln(os.Stderr, "Error: --runtime is required")
		fs.Usage()
		os.Exit(1)
	}

	if *schemaPath == "" {
		fmt.Fprintln(os.Stderr, "Error: --schema is required")
		fs.Usage()
		os.Exit(1)
	}

	// Check if runtime is supported
	if !clientgen.Registered(*runtime) {
		fmt.Fprintf(os.Stderr, "Error: unknown runtime %q\n", *runtime)
		fmt.Fprintf(os.Stderr, "Supported runtimes: %s\n", strings.Join(clientgen.ListRuntimes(), ", "))
		os.Exit(1)
	}

	// Parse schema
	if _, err := os.Stat(*schemaPath); err != nil {
		fmt.Printf("Schema not found: %s\n", *schemaPath)
		os.Exit(1)
	}

	types, err := parser.ParseSchema(*schemaPath)
	if err != nil {
		fmt.Printf("Parse error: %v\n", err)
		os.Exit(1)
	}

	// Build config
	cfg := &clientgen.Config{
		Package:        *pkg,
		RelationFilter: *filter,
		IDType:         *idType,
	}

	// Generate code
	files, err := clientgen.Generate(*runtime, types, cfg)
	if err != nil {
		fmt.Printf("Generation error: %v\n", err)
		os.Exit(1)
	}

	// Output files
	if *output == "" {
		// Write to stdout (only works for single-file outputs)
		if len(files) > 1 {
			fmt.Fprintln(os.Stderr, "Error: --output is required for multi-file generation")
			os.Exit(1)
		}
		for _, content := range files {
			os.Stdout.Write(content)
		}
	} else {
		// Write to output directory
		if err := os.MkdirAll(*output, 0o755); err != nil {
			fmt.Printf("Creating output directory: %v\n", err)
			os.Exit(1)
		}

		for filename, content := range files {
			outPath := filepath.Join(*output, filename)
			if err := os.WriteFile(outPath, content, 0o644); err != nil {
				fmt.Printf("Writing %s: %v\n", outPath, err)
				os.Exit(1)
			}
			fmt.Printf("Generated %s\n", outPath)
		}
	}
}

// runMigrate applies the schema to the database.
func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbURL := fs.String("db", os.Getenv("DATABASE_URL"), "Database URL (or set DATABASE_URL)")
	schemasDir := fs.String("schemas-dir", "schemas", "Directory containing schema.fga")
	dryRun := fs.Bool("dry-run", false, "Output migration SQL without applying")
	force := fs.Bool("force", false, "Force migration even if schema unchanged")

	fs.Usage = func() {
		fmt.Println("Usage: melange migrate --db <url> [flags]")
		fmt.Println()
		fmt.Println("Apply authorization schema to PostgreSQL database.")
		fmt.Println()
		fmt.Println("Flags:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  melange migrate --db postgres://localhost/mydb")
		fmt.Println("  melange migrate --db postgres://localhost/mydb --dry-run")
		fmt.Println("  melange migrate --db postgres://localhost/mydb --force")
	}
	_ = fs.Parse(args)

	if *dbURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --db or DATABASE_URL required")
		fs.Usage()
		os.Exit(1)
	}

	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	opts := migrator.MigrateOptions{
		Force: *force,
	}

	if *dryRun {
		opts.DryRun = os.Stdout
		fmt.Fprintln(os.Stderr, "-- Dry-run mode: SQL will be output but not applied")
		fmt.Fprintln(os.Stderr, "")
	} else {
		fmt.Println("Applying authz infrastructure...")
	}

	skipped, err := migrator.MigrateWithOptions(ctx, db, *schemasDir, opts)
	if err != nil {
		log.Fatalf("migrating: %v", err)
	}

	if *dryRun {
		return
	}

	if skipped {
		fmt.Println("Schema unchanged, migration skipped.")
		fmt.Println("Use --force to re-apply.")
	} else {
		fmt.Println("Authz schema applied successfully.")
	}

	// Check for melange_tuples and warn if missing
	m := migrator.NewMigrator(db, *schemasDir)
	status, err := m.GetStatus(ctx)
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

// runStatus queries the database for current migration state.
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dbURL := fs.String("db", os.Getenv("DATABASE_URL"), "Database URL (or set DATABASE_URL)")
	schemasDir := fs.String("schemas-dir", "schemas", "Directory containing schema.fga")

	fs.Usage = func() {
		fmt.Println("Usage: melange status --db <url> [flags]")
		fmt.Println()
		fmt.Println("Show current schema and migration status.")
		fmt.Println()
		fmt.Println("Flags:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *dbURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --db or DATABASE_URL required")
		fs.Usage()
		os.Exit(1)
	}

	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, *schemasDir)

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

// runDoctor performs health checks on the authorization infrastructure.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dbURL := fs.String("db", os.Getenv("DATABASE_URL"), "Database URL (or set DATABASE_URL)")
	schemasDir := fs.String("schemas-dir", "schemas", "Directory containing schema.fga")
	verbose := fs.Bool("verbose", false, "Show detailed output")

	fs.Usage = func() {
		fmt.Println("Usage: melange doctor --db <url> [flags]")
		fmt.Println()
		fmt.Println("Run health checks on authorization infrastructure.")
		fmt.Println()
		fmt.Println("Flags:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *dbURL == "" {
		fmt.Fprintln(os.Stderr, "Error: --db or DATABASE_URL required")
		fs.Usage()
		os.Exit(1)
	}

	db, err := sql.Open("postgres", *dbURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	fmt.Println("melange doctor - Health Check")

	d := doctor.New(db, *schemasDir)
	report, err := d.Run(ctx)
	if err != nil {
		log.Fatalf("running doctor: %v", err)
	}

	report.Print(os.Stdout, *verbose)

	if report.HasErrors() {
		os.Exit(1)
	}
}
