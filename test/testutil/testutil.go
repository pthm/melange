// Package testutil provides shared test utilities for Melange integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
	"github.com/pthm/melange/pkg/clientgen"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// Embedded test fixtures
var (
	//go:embed testdata/schema.fga
	schemaFGA string

	//go:embed testdata/domain_tables.sql.tmpl
	domainTablesSQLTemplate string

	//go:embed testdata/tuples_view.sql.tmpl
	tuplesViewSQLTemplate string
)

type templateState struct {
	template string
	err      error
}

type remoteDBState struct {
	err error
}

// Singleton container state
var (
	singletonOnce sync.Once
	singletonDSN  string
	singletonErr  error

	templateMutex  sync.Mutex
	templateStates map[string]*templateState

	codegenOnce sync.Once
	codegenErr  error

	remoteDBMutex  sync.Mutex
	remoteDBStates map[string]*remoteDBState
)

// ensureSingleton lazily initializes the singleton PostgreSQL container.
// Safe for concurrent access via sync.Once.
func ensureSingleton() (string, error) {
	singletonOnce.Do(func() {
		ctx := context.Background()

		// Start PostgreSQL with increased max_connections for parallel tests
		container, err := postgres.Run(ctx,
			"postgres:18-alpine",
			postgres.WithDatabase("postgres"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			testcontainers.WithEnv(map[string]string{
				"POSTGRES_INITDB_ARGS": "--auth-host=trust",
			}),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			singletonErr = fmt.Errorf("failed to start PostgreSQL container: %w", err)
			return
		}

		dsn, err := container.ConnectionString(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			singletonErr = fmt.Errorf("failed to get PostgreSQL connection string: %w", err)
			return
		}

		// Append sslmode=disable for local testing
		dsn += "sslmode=disable"

		singletonDSN = dsn
		// Container is not stored - ryuk will handle cleanup automatically
	})

	return singletonDSN, singletonErr
}

// ensureCodegen generates the authz package from the schema.
// This runs once per test session before any tests execute.
func ensureCodegen() error {
	codegenOnce.Do(func() {
		codegenErr = runCodegen()
	})
	return codegenErr
}

// runCodegen generates the authz package from the schema.
func runCodegen() error {
	// Parse the schema
	types, err := parser.ParseSchemaString(schemaFGA)
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}

	// Generate the code
	cfg := &clientgen.Config{
		Package: "authz",
		IDType:  "int64",
	}
	files, err := clientgen.Generate("go", types, cfg)
	if err != nil {
		return fmt.Errorf("generate code: %w", err)
	}
	// Get the generated content (single file for Go)
	var content []byte
	for _, c := range files {
		content = c
		break
	}

	// Write to the authz package (test/authz from test/testutil)
	authzDir := filepath.Join(packageDir(), "..", "authz")
	if err := os.MkdirAll(authzDir, 0o755); err != nil {
		return fmt.Errorf("create authz dir: %w", err)
	}

	outPath := filepath.Join(authzDir, "schema_gen.go")
	if err := os.WriteFile(outPath, content, 0o644); err != nil {
		return fmt.Errorf("write generated code: %w", err)
	}

	return nil
}

// packageDir returns the absolute path to this package's directory.
func packageDir() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		// Fallback - should never happen
		return "."
	}
	return filepath.Dir(filename)
}

// ensureTemplate creates the template database with migrations applied.
// Safe for concurrent access via sync.Mutex.
func ensureTemplate(adminDSN, databaseSchema string) (string, error) {
	templateMutex.Lock()
	defer templateMutex.Unlock()

	state, ok := templateStates[databaseSchema]
	if ok {
		return state.template, state.err
	}

	name := "melange_template"
	if databaseSchema != "" {
		name += "_" + databaseSchema
	}

	state = &templateState{
		template: name,
	}

	if templateStates == nil {
		templateStates = make(map[string]*templateState)
	}

	templateStates[databaseSchema] = state

	// First, ensure code generation is done
	if err := ensureCodegen(); err != nil {
		state.err = fmt.Errorf("code generation failed: %w", err)
		return state.template, state.err
	}

	// Create template database
	if err := createDatabase(adminDSN, state.template); err != nil {
		state.err = fmt.Errorf("failed to create template database: %w", err)
		return state.template, state.err
	}

	// Build DSN for template database
	templateDSN := replaceDBName(adminDSN, state.template)

	// Apply melange schema migrations
	if err := applyMelangeMigrations(templateDSN, databaseSchema); err != nil {
		state.err = fmt.Errorf("failed to apply melange migrations: %w", err)
		return state.template, state.err
	}

	// Mark database as template for faster copying
	// Non-fatal if this fails: copying still works without template flag
	_ = markAsTemplate(adminDSN, state.template)
	return state.template, state.err
}

// DSN returns the connection string for an isolated test database.
// Unlike DB(), it returns the raw DSN so callers can open with any driver.
// The database is automatically cleaned up when the test completes.
func DSN(tb testing.TB) string {
	tb.Helper()

	return DSNWithDatabaseSchema(tb, "")
}

func DSNWithDatabaseSchema(tb testing.TB, databaseSchema string) string {
	tb.Helper()

	adminDSN, err := ensureSingleton()
	require.NoError(tb, err, "failed to start PostgreSQL container")

	tmpl, err := ensureTemplate(adminDSN, databaseSchema)
	require.NoError(tb, err, "failed to create template database")

	// Generate unique database name
	dbName := uniqueDBName("test")

	// Create database from template
	err = createDatabaseFromTemplate(adminDSN, dbName, tmpl)
	require.NoError(tb, err, "failed to create test database from template")

	dsn := replaceDBName(adminDSN, dbName)

	// Register cleanup to drop the database when the test completes
	tb.Cleanup(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = dropDatabase(ctx, adminDSN, dbName)
		}()
	})

	return dsn
}

// DB returns a fully migrated database connection for testing.
// Each call creates a new isolated database copied from the template.
// The database is automatically cleaned up when the test completes.
// Works with both *testing.T and *testing.B.
//
// If DATABASE_URL environment variable is set, connects to remote database instead.
func DB(tb testing.TB) *sql.DB {
	tb.Helper()

	return DBWithDatabaseSchema(tb, "")
}

func DBWithDatabaseSchema(tb testing.TB, databaseSchema string) *sql.DB {
	tb.Helper()

	// Check for remote database configuration
	config := GetDatabaseConfig()
	if config.URL != "" {
		return remoteDB(tb, config, databaseSchema)
	}

	// Local testcontainers mode (existing behavior)
	adminDSN, err := ensureSingleton()
	require.NoError(tb, err, "failed to start PostgreSQL container")

	tmpl, err := ensureTemplate(adminDSN, databaseSchema)
	require.NoError(tb, err, "failed to create template database")

	// Generate unique database name
	dbName := uniqueDBName("test")

	// Create database from template
	err = createDatabaseFromTemplate(adminDSN, dbName, tmpl)
	require.NoError(tb, err, "failed to create test database from template")

	// Connect to the new database
	dbDSN := replaceDBName(adminDSN, dbName)
	db, err := sql.Open("pgx", dbDSN)
	require.NoError(tb, err, "failed to connect to test database")

	// Verify connection
	err = db.Ping()
	require.NoError(tb, err, "failed to ping test database")

	// Register cleanup
	registerCleanup(tb, db, adminDSN, dbName)

	return db
}

// ensureRemoteDBMigrated ensures migrations are applied to remote database exactly once.
// Safe for concurrent access via sync.Mutex.
func ensureRemoteDBMigrated(config DatabaseConfig, databaseSchema string) error {
	remoteDBMutex.Lock()
	defer remoteDBMutex.Unlock()

	state, ok := remoteDBStates[databaseSchema]
	if ok {
		return state.err
	}

	state = &remoteDBState{}

	if remoteDBStates == nil {
		remoteDBStates = make(map[string]*remoteDBState)
	}

	remoteDBStates[databaseSchema] = state

	// First, ensure code generation is done
	if err := ensureCodegen(); err != nil {
		state.err = fmt.Errorf("code generation failed: %w", err)
		return state.err
	}

	// Apply migrations to remote database once
	if err := applyMelangeMigrations(config.URL, databaseSchema); err != nil {
		state.err = fmt.Errorf("failed to apply migrations to remote database: %w", err)
		return state.err
	}

	// Apply migrations to remote database once
	if err := applyMelangeMigrations(config.URL, databaseSchema); err != nil {
		state.err = fmt.Errorf("failed to apply migrations to remote database2: %w", err)
		return state.err
	}

	return state.err
}

// remoteDB connects to a remote database for testing.
// Instead of creating/dropping databases, it truncates tables for cleanup.
func remoteDB(tb testing.TB, config DatabaseConfig, databaseSchema string) *sql.DB {
	tb.Helper()
	ctx := context.Background()

	// Ensure migrations are applied once across all tests
	err := ensureRemoteDBMigrated(config, databaseSchema)
	if err != nil {
		tb.Fatalf("failed to initialize remote database: %v", err)
	}

	// Connect with retry logic
	var db *sql.DB
	maxRetries := 5

	for i := 0; i < maxRetries; i++ {
		db, err = sql.Open("pgx", config.URL)
		if err != nil {
			if i == maxRetries-1 {
				tb.Fatalf("failed to open remote database connection after %d retries: %v", maxRetries, err)
			}
			time.Sleep(time.Duration(i+1) * time.Second)
			continue
		}

		// Configure connection pool
		if config.EnablePooling {
			db.SetMaxOpenConns(config.MaxConnections)
			db.SetMaxIdleConns(config.MaxConnections / 2)
			db.SetConnMaxLifetime(5 * time.Minute)
		}

		// Verify connection with ping
		err = db.PingContext(ctx)
		if err == nil {
			break // Success
		}

		// Close and retry
		_ = db.Close()
		if i == maxRetries-1 {
			tb.Fatalf("failed to ping remote database after %d retries: %v\nEnsure DATABASE_URL is correct and database is accessible", maxRetries, err)
		}
		time.Sleep(time.Duration(i+1) * time.Second)
	}

	// Register cleanup (truncate tables, not drop database)
	tb.Cleanup(func() {
		cleanupRemoteDB(db, databaseSchema)
		_ = db.Close()
	})

	return db
}

// cleanupRemoteDB truncates all tables in the remote database.
// This is used instead of dropping the database for remote connections.
func cleanupRemoteDB(db *sql.DB, databaseSchema string) {
	ctx := context.Background()

	// List of tables to truncate (in dependency order, CASCADE handles dependencies)
	tables := []string{
		"pull_request_reviewers",
		"pull_requests",
		"issues",
		"repository_collaborators",
		"repository_bans",
		"repositories",
		"team_members",
		"teams",
		"organization_members",
		"organizations",
		"users",
	}

	// Truncate all tables
	for _, table := range tables {
		_, _ = db.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE %s CASCADE", sqldsl.PrefixIdent(table, databaseSchema)))
		// Ignore errors - table might not exist
	}
}

// EmptyDB returns an empty database connection for testing.
// Each call creates a new isolated empty database.
// The database is automatically cleaned up when the test completes.
// Works with both *testing.T and *testing.B.
func EmptyDB(tb testing.TB) *sql.DB {
	tb.Helper()

	adminDSN, err := ensureSingleton()
	require.NoError(tb, err, "failed to start PostgreSQL container")

	// Generate unique database name
	dbName := uniqueDBName("empty")

	// Create empty database
	err = createDatabase(adminDSN, dbName)
	require.NoError(tb, err, "failed to create empty database")

	// Connect to the new database
	dbDSN := replaceDBName(adminDSN, dbName)
	db, err := sql.Open("pgx", dbDSN)
	require.NoError(tb, err, "failed to connect to empty database")

	// Verify connection
	err = db.Ping()
	require.NoError(tb, err, "failed to ping empty database")

	// Register cleanup
	registerCleanup(tb, db, adminDSN, dbName)

	return db
}

// registerCleanup registers cleanup for the database connection and database itself.
// Cleanup runs in a goroutine to not block the test.
func registerCleanup(tb testing.TB, db *sql.DB, adminDSN, dbName string) {
	tb.Cleanup(func() {
		// Close the connection first
		_ = db.Close()

		// Drop database in background
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = dropDatabase(ctx, adminDSN, dbName)
		}()
	})
}

// uniqueDBName generates a unique database name with the given prefix.
func uniqueDBName(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

// createDatabase creates a new empty database.
func createDatabase(adminDSN, name string) error {
	db, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE %s", name))
	return err
}

// createDatabaseFromTemplate creates a new database from a template.
func createDatabaseFromTemplate(adminDSN, name, tmpl string) error {
	db, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// First, ensure no connections to template
	_, _ = db.Exec(fmt.Sprintf(`
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = '%s' AND pid <> pg_backend_pid()
	`, tmpl))

	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE %s WITH TEMPLATE %s", name, tmpl))
	return err
}

// markAsTemplate marks a database as a template for faster copying.
func markAsTemplate(adminDSN, name string) error {
	db, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Disconnect all users first
	_, _ = db.Exec(fmt.Sprintf(`
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = '%s' AND pid <> pg_backend_pid()
	`, name))

	_, err = db.Exec(fmt.Sprintf("ALTER DATABASE %s WITH is_template = true", name))
	return err
}

// dropDatabase drops a database.
func dropDatabase(ctx context.Context, adminDSN, name string) error {
	db, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Force disconnect all users
	_, _ = db.ExecContext(ctx, fmt.Sprintf(`
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = '%s' AND pid <> pg_backend_pid()
	`, name))

	_, err = db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name))
	return err
}

// applyMelangeMigrations applies the melange schema to the database.
func applyMelangeMigrations(dsn, databaseSchema string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	// Apply melange DDL and schema from embedded file
	types, err := parser.ParseSchemaString(schemaFGA)
	if err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}

	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)

	err = m.MigrateWithTypes(ctx, types)
	if err != nil {
		return fmt.Errorf("apply melange migration: %w", err)
	}

	// Create the domain tables for testing (must be before tuples view)
	_, err = db.ExecContext(ctx, DomainTablesSQL(databaseSchema))
	if err != nil {
		return fmt.Errorf("create domain tables: %w", err)
	}

	// Create the melange_tuples view for testing (references domain tables)
	_, err = db.ExecContext(ctx, TuplesViewSQL(databaseSchema))
	if err != nil {
		return fmt.Errorf("create tuples view: %w", err)
	}

	return nil
}

// replaceDBName replaces the database name in a PostgreSQL DSN.
func replaceDBName(dsn, newDB string) string {
	// DSN format: postgres://user:pass@host:port/dbname?params
	// We need to replace the database name

	for i := len(dsn) - 1; i >= 0; i-- {
		if dsn[i] == '/' {
			// Found the last slash before potential query params
			rest := ""
			for j := i + 1; j < len(dsn); j++ {
				if dsn[j] == '?' {
					rest = dsn[j:]
					break
				}
			}
			return dsn[:i+1] + newDB + rest
		}
	}
	return dsn
}

// DomainTablesSQL returns the embedded SQL for creating domain tables.
func DomainTablesSQL(databaseSchema string) string {
	tmpl := template.Must(template.New("domain_tables").Parse(domainTablesSQLTemplate))

	return executeTemplate(tmpl, databaseSchema)
}

// TuplesViewSQL returns the embedded SQL for creating the tuples view.
func TuplesViewSQL(databaseSchema string) string {
	tmpl := template.Must(template.New("tuples_view").Parse(tuplesViewSQLTemplate))

	return executeTemplate(tmpl, databaseSchema)
}

type templateData struct {
	DatabaseSchema string
}

func (d *templateData) Ident(name string) string {
	return sqldsl.PrefixIdent(name, d.DatabaseSchema)
}

func executeTemplate(tmpl *template.Template, databaseSchema string) string {
	var buf strings.Builder

	err := tmpl.Execute(&buf, &templateData{
		DatabaseSchema: databaseSchema,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to execute template %s: %s", tmpl.Name(), err.Error()))
	}

	return buf.String()
}

// PostgresSchema returns a literal with the schema name or "current_schema()" if empty.
func PostgresSchema(schema string) string {
	if schema == "" {
		return "current_schema()"
	}

	return pq.QuoteLiteral(schema)
}
