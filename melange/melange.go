// Package melange provides PostgreSQL-based fine-grained authorization
// implementing OpenFGA/Zanzibar concepts with zero runtime dependencies.
//
// # Module Structure
//
// This is the Go runtime module (github.com/pthm/melange/melange) which has
// zero external dependencies (stdlib only). It provides the Checker API for
// permission checks at runtime.
//
// The root module (github.com/pthm/melange) contains the CLI, schema parsing,
// and migration tools. Applications typically only import this runtime module.
//
// # Zero Tuple Sync
//
// Melange uses a pure-PostgreSQL approach where permissions are derived from
// views over your existing application tables rather than maintaining separate
// tuple storage. You define a melange_tuples view over tables like users,
// repositories, etc. Permission checks query this view combined with the
// authorization model encoded in generated SQL functions.
//
// # Core Concepts
//
// Objects represent typed resources. In FGA terms, both "users" and "resources"
// are objects - there's no special Subject type.
//
//	user := melange.Object{Type: "user", ID: "123"}
//	repo := melange.Object{Type: "repository", ID: "456"}
//
// # Basic Usage
//
//	checker := melange.NewChecker(db)
//	ok, err := checker.Check(ctx, user, "can_read", repo)
//
// # Transaction Support
//
// The Checker works with *sql.DB, *sql.Tx, or *sql.Conn, enabling permission
// checks to see uncommitted changes within a transaction:
//
//	tx, _ := db.BeginTx(ctx, nil)
//	checker := melange.NewChecker(tx)
//	ok, _ := checker.Check(ctx, user, "can_write", repo)
//	// Permission check sees uncommitted transaction state
//
// # Caching
//
// Use WithCache for repeated checks:
//
//	cache := melange.NewCache(melange.WithTTL(time.Minute))
//	checker := melange.NewChecker(db, melange.WithCache(cache))
//
// # Decision Overrides
//
// Use WithDecision for testing or admin tools:
//
//	checker := melange.NewChecker(db, melange.WithDecision(melange.DecisionAllow))
//
// # Schema Management
//
// For schema parsing and migration, use the root module packages:
//
//	import (
//	    "github.com/pthm/melange/pkg/parser"
//	    "github.com/pthm/melange/pkg/migrator"
//	)
//
//	types, _ := parser.ParseSchema("schemas/schema.fga")
//	err := migrator.Migrate(ctx, db, "schemas")
//
// Or use the CLI for migrations:
//
//	melange migrate --db postgres://localhost/mydb --schemas-dir schemas
package melange

import (
	"context"
	"database/sql"
)

// ObjectType represents the type of an object.
type ObjectType string

// String returns the string representation of the object type.
func (ot ObjectType) String() string {
	return string(ot)
}

// Object represents a typed resource identifier.
// In FGA terms, both "users" and "resources" are objects - there's no
// distinction between subjects and objects at the type level.
//
// Objects are value types and safe to copy. The canonical string format
// is "type:id", used in logging and debugging.
type Object struct {
	Type ObjectType
	ID   string
}

// String returns the canonical representation "type:id".
func (o Object) String() string {
	return o.Type.String() + ":" + o.ID
}

// FGAObject returns the object itself, implementing ObjectLike.
// This allows Object to be used directly in permission checks.
func (o Object) FGAObject() Object {
	return o
}

// FGASubject returns the object itself, implementing SubjectLike.
// In FGA terms, subjects are also objects - this allows Object to be
// used as either the subject or object in permission checks.
func (o Object) FGASubject() Object {
	return o
}

// ObjectLike defines an interface for types that can be converted to Objects.
// This allows domain models to implement authorization-aware methods without
// importing the full domain layer into melange.
//
// Example:
//
//	type Repository struct { ID int64; OwnerName string }
//	func (r Repository) FGAObject() melange.Object {
//	    return melange.Object{Type: "repository", ID: fmt.Sprint(r.ID)}
//	}
//
// The Checker accepts ObjectLike rather than Object directly, enabling
// type-safe authorization checks against domain models.
type ObjectLike interface {
	FGAObject() Object
}

// SubjectLike defines an interface for types that can be used as subjects.
// In FGA terms, subjects are the "who" in "who has what relation on what object".
// Subjects are typically users but can be any typed resource.
//
// Example:
//
//	type User struct { ID int64; Username string }
//	func (u User) FGASubject() melange.Object {
//	    return melange.Object{Type: "user", ID: fmt.Sprint(u.ID)}
//	}
//
// Note: Object implements both SubjectLike and ObjectLike, allowing
// melange.Object values to be used directly in either position.
type SubjectLike interface {
	FGASubject() Object
}

// Relation represents a typed relation identifier.
// Relations can be permissions (can_read, can_write) or roles (owner, member).
// Unlike some authorization systems, melange treats all relations uniformly.
type Relation string

// String returns the canonical representation of the relation.
func (r Relation) String() string {
	return string(r)
}

// ContextualTuple represents a tuple provided at request time.
// These tuples are not persisted and only affect a single check/list call.
type ContextualTuple struct {
	Subject  Object
	Relation Relation
	Object   Object
}

// FGARelation returns the relation itself, implementing RelationLike.
func (r Relation) FGARelation() Relation {
	return r
}

// RelationLike defines an interface for types that can be converted to Relations.
// This allows generated code to provide type-safe relation constants while
// accepting custom relation types from domain models.
type RelationLike interface {
	FGARelation() Relation
}

// Querier executes queries against PostgreSQL.
// Implemented by *sql.DB, *sql.Tx, and *sql.Conn.
//
// The minimal interface allows Checker to work in transaction contexts without
// requiring a full database connection. This enables permission checks to see
// uncommitted changes within a transaction, supporting patterns like:
//
//	tx.Exec("INSERT INTO repositories ...")
//	// melange_tuples view reflects the new row
//	checker.Check(ctx, user, "can_read", repo) // sees new tuple
//	tx.Commit()
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Execer extends Querier with ExecContext for migrations.
// Only required by the CLI migrate command, not for runtime permission checks.
// Separating this interface keeps the Checker dependency minimal.
type Execer interface {
	Querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
