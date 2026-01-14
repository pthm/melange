package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/test/openfgatests"
)

var backgroundContext = context.Background()

// Global container state (similar to testutil)
var (
	containerDSN string
	containerErr error
)

// setupTest creates a database and client for a test case.
// Returns the database, client, and a cleanup function.
func setupTest(tc TestCase) (*sql.DB, *openfgatests.Client, func(), error) {
	// Get database connection (container or external)
	adminDSN, err := getDatabaseDSN()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get database DSN: %w", err)
	}

	// Generate unique database name
	dbName := uniqueDBName("explaintest")

	// Create empty database
	err = createDatabase(adminDSN, dbName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create database: %w", err)
	}

	// Connect to the new database
	dbDSN := replaceDBName(adminDSN, dbName)
	db, err := sql.Open("pgx", dbDSN)
	if err != nil {
		dropDatabase(context.Background(), adminDSN, dbName)
		return nil, nil, nil, fmt.Errorf("connect to database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		dropDatabase(context.Background(), adminDSN, dbName)
		return nil, nil, nil, fmt.Errorf("ping database: %w", err)
	}

	// Get the model from first stage
	if len(tc.Stages) == 0 {
		db.Close()
		dropDatabase(context.Background(), adminDSN, dbName)
		return nil, nil, nil, fmt.Errorf("test has no stages")
	}

	// Initialize melange schema with the test's model
	if err := initializeMelangeSchema(db, tc.Stages[0].Model); err != nil {
		db.Close()
		dropDatabase(context.Background(), adminDSN, dbName)
		return nil, nil, nil, fmt.Errorf("initialize schema: %w", err)
	}

	// Create client
	client := openfgatests.NewClientWithDB(db)

	cleanup := func() {
		db.Close()
		go dropDatabase(context.Background(), adminDSN, dbName)
	}

	return db, client, cleanup, nil
}

// getDatabaseDSN returns the database DSN (from env or testcontainer).
func getDatabaseDSN() (string, error) {
	// Check for external DATABASE_URL
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, nil
	}

	// Use testcontainer (lazy initialization)
	if containerDSN != "" {
		return containerDSN, containerErr
	}

	// Start PostgreSQL container
	ctx := context.Background()
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
		containerErr = fmt.Errorf("start PostgreSQL container: %w", err)
		return "", containerErr
	}

	dsn, err := container.ConnectionString(ctx)
	if err != nil {
		container.Terminate(ctx)
		containerErr = fmt.Errorf("get connection string: %w", err)
		return "", containerErr
	}

	// Append sslmode=disable for local testing
	dsn += "sslmode=disable"

	containerDSN = dsn
	return containerDSN, nil
}

// initializeMelangeSchema sets up the melange schema in the database.
func initializeMelangeSchema(db *sql.DB, modelDSL string) error {
	ctx := context.Background()

	// Migrate the actual test model to generate the correct SQL functions
	if err := migrator.MigrateFromString(ctx, db, modelDSL); err != nil {
		return fmt.Errorf("apply melange migration: %w", err)
	}

	// Create test tuples table and view
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS melange_test_tuples (
			subject_type TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			object_type TEXT NOT NULL,
			object_id TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("creating test tuples table: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		CREATE OR REPLACE VIEW melange_tuples AS
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM melange_test_tuples
	`)
	if err != nil {
		return fmt.Errorf("creating tuples view: %w", err)
	}

	return nil
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
	defer db.Close()

	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE %s", name))
	return err
}

// dropDatabase drops a database.
func dropDatabase(ctx context.Context, adminDSN, name string) error {
	db, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	// Force disconnect all users
	_, _ = db.ExecContext(ctx, fmt.Sprintf(`
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = '%s' AND pid <> pg_backend_pid()
	`, name))

	_, err = db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name))
	return err
}

// replaceDBName replaces the database name in a PostgreSQL DSN.
func replaceDBName(dsn, newDB string) string {
	// DSN format: postgres://user:pass@host:port/dbname?params
	for i := len(dsn) - 1; i >= 0; i-- {
		if dsn[i] == '/' {
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
