package migrator

import (
	"context"
	"database/sql"
)

// Execer is the minimal interface needed for schema migration operations.
// Implemented by *sql.DB, *sql.Tx, and *sql.Conn.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
