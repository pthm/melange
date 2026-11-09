package melange

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakePgError is a driver error carrying a SQLSTATE the way lib/pq's
// *pq.Error and pgx's *pgconn.PgError do: a SQLState method and a Message
// field, with Error() decorating the message.
type fakePgError struct {
	Code    string
	Message string
}

func (e *fakePgError) Error() string    { return "pq: " + e.Message + " (" + e.Code + ")" }
func (e *fakePgError) SQLState() string { return e.Code }

const notExposedMsg = `relation "viewer" on type "document" is not exposed`

func notExposedErr() error { return &fakePgError{Code: pgNotExposed, Message: notExposedMsg} }

// requireNotExposed asserts err is the ValidationError M2003 maps to.
func requireNotExposed(t *testing.T, err error) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	if ve.Code != ErrorCodeValidation {
		t.Errorf("code: got %d, want %d", ve.Code, ErrorCodeValidation)
	}
	if ve.Message != notExposedMsg {
		t.Errorf("message: got %q, want the database message %q", ve.Message, notExposedMsg)
	}
}

func TestMapError_NotExposed(t *testing.T) {
	c := &Checker{}
	t.Run("driver error", func(t *testing.T) {
		requireNotExposed(t, c.mapError("check_permission", notExposedErr()))
	})
	t.Run("wrapped driver error", func(t *testing.T) {
		requireNotExposed(t, c.mapError("check_permission", fmt.Errorf("query: %w", notExposedErr())))
	})
	t.Run("SQLSTATE only in the text", func(t *testing.T) {
		err := c.mapError("check_permission", errors.New("ERROR: "+notExposedMsg+" (SQLSTATE M2003)"))
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != ErrorCodeValidation {
			t.Fatalf("want ValidationError 2000, got %T: %v", err, err)
		}
		if !strings.Contains(ve.Message, notExposedMsg) {
			t.Errorf("message %q does not carry the database message", ve.Message)
		}
	})
	if got := GetValidationErrorCode(c.mapError("explain_permission", notExposedErr())); got != ErrorCodeValidation {
		t.Errorf("GetValidationErrorCode: got %d, want %d", got, ErrorCodeValidation)
	}
}

func TestMapError_MissingFunction(t *testing.T) {
	c := &Checker{}
	for _, fn := range []string{"check_permission", "check_permission_bulk", "list_accessible_objects", "list_accessible_subjects", "explain_permission", "expand_permission"} {
		t.Run(fn, func(t *testing.T) {
			err := c.mapError(fn, &fakePgError{Code: pgUndefinedFunction, Message: "function " + fn + "(unknown, unknown) does not exist"})
			if !errors.Is(err, ErrMissingFunction) {
				t.Errorf("want ErrMissingFunction, got %v", err)
			}
		})
	}
	t.Run("other function", func(t *testing.T) {
		err := c.mapError("check_permission", &fakePgError{Code: pgUndefinedFunction, Message: "function my_helper(integer) does not exist"})
		if errors.Is(err, ErrMissingFunction) {
			t.Errorf("an unrelated missing function is not ErrMissingFunction: %v", err)
		}
	})
}

func TestPgMessage(t *testing.T) {
	if got := pgMessage(notExposedErr()); got != notExposedMsg {
		t.Errorf("driver error: got %q", got)
	}
	if got := pgMessage(fmt.Errorf("wrapped: %w", notExposedErr())); got != notExposedMsg {
		t.Errorf("wrapped driver error: got %q", got)
	}
	if got := pgMessage(errors.New("plain")); got != "plain" {
		t.Errorf("plain error: got %q", got)
	}
	var nilErr *fakePgError
	if got := pgMessage(fmt.Errorf("wrapped nil: %w", nilErr)); got == "" {
		t.Error("a nil driver error in the chain falls back to Error()")
	}
}

// faultDriver is a database/sql driver whose every melange function call
// fails with a configured error, either when the query is sent or while its
// rows are read (lib/pq reports a set-returning function's error from
// rows.Next). The tuples-schema lookup and statements succeed, so the
// contextual-tuple paths reach their dispatcher call.
type faultDriver struct{}

type fault struct {
	err       error
	whileRead bool
}

var (
	registerFaultDriver sync.Once
	faults              sync.Map // DSN -> fault
)

func (faultDriver) Open(dsn string) (driver.Conn, error) {
	f, ok := faults.Load(dsn)
	if !ok {
		return nil, fmt.Errorf("no fault for %q", dsn)
	}
	return &faultConn{f: f.(fault)}, nil
}

type faultConn struct{ f fault }

func (c *faultConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *faultConn) Close() error                        { return nil }
func (c *faultConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *faultConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (c *faultConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "pg_class") {
		return &faultRows{values: []driver.Value{"public"}}, nil
	}
	if !c.f.whileRead {
		return nil, c.f.err
	}
	return &faultRows{err: c.f.err}, nil
}

// faultRows yields values as one row, or fails with err on the first Next.
type faultRows struct {
	values []driver.Value
	err    error
	done   bool
}

func (r *faultRows) Columns() []string {
	if r.values != nil {
		return []string{"nspname"}
	}
	return []string{"a", "b"}
}

func (r *faultRows) Close() error { return nil }

func (r *faultRows) Next(dest []driver.Value) error {
	if r.err != nil {
		return r.err
	}
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

// faultChecker returns a Checker over a database whose melange calls fail
// with err.
func faultChecker(t *testing.T, err error, whileRead bool) *Checker {
	t.Helper()
	registerFaultDriver.Do(func() { sql.Register("melange-fault", faultDriver{}) })
	dsn := t.Name()
	faults.Store(dsn, fault{err: err, whileRead: whileRead})
	t.Cleanup(func() { faults.Delete(dsn) })
	db, openErr := sql.Open("melange-fault", dsn)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewChecker(db, WithDatabaseSchema("fault"))
}

// checkerCalls are the Checker's entry points that call a public dispatcher.
func checkerCalls() map[string]func(context.Context, *Checker) error {
	user := Object{Type: "user", ID: "alice"}
	doc := Object{Type: "document", ID: "1"}
	tuples := []ContextualTuple{{Subject: user, Relation: "viewer", Object: doc}}
	page := PageOptions{Limit: 10}
	return map[string]func(context.Context, *Checker) error{
		"Check": func(ctx context.Context, c *Checker) error {
			_, err := c.Check(ctx, user, Relation("viewer"), doc)
			return err
		},
		"CheckWithContextualTuples": func(ctx context.Context, c *Checker) error {
			_, err := c.CheckWithContextualTuples(ctx, user, Relation("viewer"), doc, tuples)
			return err
		},
		"BulkCheck": func(ctx context.Context, c *Checker) error {
			_, err := c.NewBulkCheck(ctx).Add(user, Relation("viewer"), doc).Execute()
			return err
		},
		"BulkCheckWithContextualTuples": func(ctx context.Context, c *Checker) error {
			_, err := c.NewBulkCheck(ctx).Add(user, Relation("viewer"), doc).WithContextualTuples(tuples...).Execute()
			return err
		},
		"ListObjects": func(ctx context.Context, c *Checker) error {
			_, _, err := c.ListObjects(ctx, user, Relation("viewer"), "document", page)
			return err
		},
		"ListObjectsAll": func(ctx context.Context, c *Checker) error {
			_, err := c.ListObjectsAll(ctx, user, Relation("viewer"), "document")
			return err
		},
		"ListObjectsWithContextualTuples": func(ctx context.Context, c *Checker) error {
			_, _, err := c.ListObjectsWithContextualTuples(ctx, user, Relation("viewer"), "document", tuples, page)
			return err
		},
		"ListSubjects": func(ctx context.Context, c *Checker) error {
			_, _, err := c.ListSubjects(ctx, doc, Relation("viewer"), "user", page)
			return err
		},
		"ListSubjectsAll": func(ctx context.Context, c *Checker) error {
			_, err := c.ListSubjectsAll(ctx, doc, Relation("viewer"), "user")
			return err
		},
		"ListSubjectsWithContextualTuples": func(ctx context.Context, c *Checker) error {
			_, _, err := c.ListSubjectsWithContextualTuples(ctx, doc, Relation("viewer"), "user", tuples, page)
			return err
		},
		"Explain": func(ctx context.Context, c *Checker) error {
			_, err := c.Explain(ctx, user, Relation("viewer"), doc)
			return err
		},
		"Expand": func(ctx context.Context, c *Checker) error {
			_, err := c.Expand(ctx, doc, Relation("viewer"))
			return err
		},
		"ExpandRecursive": func(ctx context.Context, c *Checker) error {
			_, err := c.ExpandRecursive(ctx, doc, Relation("viewer"))
			return err
		},
	}
}

// Every entry point maps M2003 to the ValidationError, whether the driver
// reports it when the query is sent or while its rows are read.
func TestCheckerEntryPoints_MapNotExposed(t *testing.T) {
	for name, call := range checkerCalls() {
		for _, whileRead := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/whileRead=%v", name, whileRead), func(t *testing.T) {
				c := faultChecker(t, notExposedErr(), whileRead)
				requireNotExposed(t, call(context.Background(), c))
			})
		}
	}
}

// A database installed without the expand family reports ErrMissingFunction
// from Expand and ExpandRecursive.
func TestCheckerExpand_MissingFunction(t *testing.T) {
	calls := checkerCalls()
	for _, name := range []string{"Expand", "ExpandRecursive"} {
		t.Run(name, func(t *testing.T) {
			c := faultChecker(t, &fakePgError{
				Code:    pgUndefinedFunction,
				Message: `function fault.expand_permission(unknown, unknown, unknown, unknown, unknown) does not exist`,
			}, false)
			if err := calls[name](context.Background(), c); !errors.Is(err, ErrMissingFunction) {
				t.Errorf("want ErrMissingFunction, got %v", err)
			}
		})
	}
}
