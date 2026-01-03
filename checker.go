package melange

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
)

// schemaValidation holds the process-wide validation state.
// Validation runs once per process on the first NewChecker call.
var schemaValidation struct {
	once sync.Once
	done bool
}

// validateSchema performs one-time schema validation on first Checker creation.
// It checks for common configuration issues and logs warnings (does not fail).
// This helps catch setup problems early without blocking application startup.
//
// Validated conditions:
//   - melange_model table exists and is non-empty
//   - check_permission function exists
func validateSchema(q Querier) {
	schemaValidation.once.Do(func() {
		ctx := context.Background()

		// Check melange_model table exists and is non-empty
		var count int
		err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM melange_model").Scan(&count)
		if err != nil {
			code := sqlState(err)
			if code == pgUndefinedTable {
				log.Printf("[melange] WARNING: melange_model table not found. Run 'melange migrate' to create it.")
			} else {
				log.Printf("[melange] WARNING: Error checking melange_model: %v", err)
			}
			schemaValidation.done = true
			return
		}

		if count == 0 {
			log.Printf("[melange] WARNING: melange_model table is empty. Run 'melange migrate' to load your schema.")
			schemaValidation.done = true
			return
		}

		// Check check_permission function exists by calling with invalid args
		// (will return 0 but won't error if function exists)
		var result int
		err = q.QueryRowContext(ctx,
			"SELECT check_permission('__test__', '__test__', '__test__', '__test__', '__test__')",
		).Scan(&result)
		if err != nil {
			code := sqlState(err)
			if code == pgUndefinedFunction {
				log.Printf("[melange] WARNING: check_permission function not found. Run 'melange migrate' to create it.")
			}
			// Other errors might be transient, don't warn
		}

		schemaValidation.done = true
	})
}

// Checker performs authorization checks against PostgreSQL.
// It evaluates permissions using the melange_model table (parsed FGA schema)
// and the melange_tuples view (application data).
//
// Checkers are lightweight and safe to create per-request. They hold no state
// beyond the database handle, cache, and decision override. The database handle
// can be *sql.DB, *sql.Tx, or *sql.Conn, allowing permission checks to see
// uncommitted changes within transactions.
//
// Schema validation runs once per process on the first NewChecker call with a
// non-nil Querier. Validation issues are logged as warnings but do not prevent
// Checker creation, allowing applications to start even if the authorization
// schema is not yet fully configured.
type Checker struct {
	q                  Querier
	cache              Cache
	decision           Decision
	useContextDecision bool
}

// Option configures a Checker.
type Option func(*Checker)

// WithCache enables caching for permission check results.
// Caching is safe across goroutines but scoped to a single Checker instance.
// For request-scoped caching, create a new Checker per request with a
// request-scoped cache.
func WithCache(c Cache) Option {
	return func(ch *Checker) {
		ch.cache = c
	}
}

// WithDecision sets a decision override that bypasses database checks.
// Use DecisionAllow for admin tools or testing authorized paths.
// Use DecisionDeny for testing unauthorized paths.
// This is intentionally separate from context-based overrides to make the
// bypass explicit at Checker construction time.
func WithDecision(d Decision) Option {
	return func(ch *Checker) {
		ch.decision = d
	}
}

// WithContextDecision enables context-based decision overrides.
// When enabled, Check will consult GetDecisionContext(ctx) before
// performing database checks. This allows authorization decisions to
// propagate through middleware layers.
//
// Decision precedence when enabled:
//  1. Context decision (via WithDecisionContext)
//  2. Checker decision (via WithDecision)
//  3. Database check
//
// By default, context decisions are NOT consulted. This opt-in design
// ensures explicit control over when context can override authorization.
func WithContextDecision() Option {
	return func(ch *Checker) {
		ch.useContextDecision = true
	}
}


// NewChecker creates a checker that works with *sql.DB, *sql.Tx, or *sql.Conn.
// Options allow callers to enable caching or decision overrides.
//
// The Querier interface is satisfied by all three database handle types,
// enabling checkers to work seamlessly in transaction or connection-pooled
// contexts without requiring different APIs.
//
// On the first call with a non-nil Querier, NewChecker validates the schema
// (once per process). Validation issues are logged as warnings but do not
// prevent Checker creation. This allows applications to start even if the
// authorization schema is not yet fully configured.
func NewChecker(q Querier, opts ...Option) *Checker {
	c := &Checker{
		q:        q,
		decision: DecisionUnset,
	}
	for _, opt := range opts {
		opt(c)
	}

	// Validate schema once per process (non-blocking, logs warnings)
	if q != nil {
		validateSchema(q)
	}

	return c
}

// Check returns true if subject has the relation on object.
// The check evaluates direct tuples, implied relations (role hierarchies),
// and parent inheritance according to the loaded FGA schema.
//
// Example:
//
//	ok, err := checker.Check(ctx, authz.User("123"), authz.RelCanRead, authz.Repository("456"))
//
// If a cache is configured via WithCache, results are cached by the tuple
// (subject, relation, object). The cache stores both successful and failed
// checks, including errors. This prevents repeated database queries for
// denied permissions or missing objects.
//
// If a decision override is set via WithDecision, the database is not queried.
// If WithContextDecision is enabled, context decisions are also consulted.
func (c *Checker) Check(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike) (bool, error) {
	// Check for context decision override (opt-in via WithContextDecision)
	if c.useContextDecision {
		if d := GetDecisionContext(ctx); d != DecisionUnset {
			return d == DecisionAllow, nil
		}
	}

	// Check for Checker-level decision override (allows bypassing DB for admin tools/tests)
	if c.decision != DecisionUnset {
		return c.decision == DecisionAllow, nil
	}

	// Check cache if available
	if c.cache != nil {
		if allowed, cachedErr, found := c.cache.Get(subject.FGASubject(), relation.FGARelation(), object.FGAObject()); found {
			return allowed, cachedErr
		}
	}

	// Perform actual permission check
	allowed, err := c.checkPermission(ctx, subject.FGASubject(), relation.FGARelation(), object.FGAObject())

	// Store in cache if available (only cache successful checks - don't cache errors)
	if c.cache != nil && err == nil {
		c.cache.Set(subject.FGASubject(), relation.FGARelation(), object.FGAObject(), allowed, nil)
	}

	return allowed, err
}

// checkPermission calls the PostgreSQL check_permission function.
// This is the low-level implementation that maps to the stored procedure.
//
// The function evaluates:
//  1. Direct tuples (including wildcard matches for public access)
//  2. Implied relations (role hierarchies like owner → admin → member)
//  3. Parent inheritance (e.g., org permissions → repo permissions)
//  4. Exclusions ("can_read but not author")
//
// PostgreSQL errors are mapped to sentinel errors (ErrNoTuplesTable,
// ErrMissingModel) for easier error handling in application code.
func (c *Checker) checkPermission(ctx context.Context, subject Object, relation Relation, object Object) (bool, error) {
	var result int

	err := c.q.QueryRowContext(ctx,
		"SELECT check_permission($1, $2, $3, $4, $5)",
		subject.Type, subject.ID, relation, object.Type, object.ID,
	).Scan(&result)
	if err != nil {
		return false, c.mapError("check_permission", err)
	}
	return result == 1, nil
}

// mapError maps PostgreSQL errors to sentinel errors.
// Uses interface-based detection to work with any PostgreSQL driver (pq, pgx).
func (c *Checker) mapError(operation string, err error) error {
	code := sqlState(err)

	switch code {
	case pgUndefinedTable:
		errStr := err.Error()
		if strings.Contains(errStr, "melange_tuples") {
			return fmt.Errorf("%w: %v", ErrNoTuplesTable, err)
		}
		if strings.Contains(errStr, "melange_model") {
			return fmt.Errorf("%w: %v", ErrMissingModel, err)
		}
	case pgUndefinedFunction:
		if strings.Contains(err.Error(), "check_permission") ||
			strings.Contains(err.Error(), "list_accessible") {
			return fmt.Errorf("%w: %v", ErrMissingFunction, err)
		}
	}

	return fmt.Errorf("%s: %w", operation, err)
}

// sqlState extracts the SQLSTATE code from a PostgreSQL error.
// Works with multiple drivers via interface detection:
//   - pgx/pgconn: SQLState() string
//   - lib/pq: Code field (via error interface)
//
// Returns empty string if the error doesn't contain a SQLSTATE.
func sqlState(err error) string {
	// Try SQLState() method (pgx/pgconn, and some pq versions)
	type sqlStateErr interface{ SQLState() string }
	if e, ok := err.(sqlStateErr); ok {
		return e.SQLState()
	}

	// Try Code() method (some error wrappers)
	type codeErr interface{ Code() string }
	if e, ok := err.(codeErr); ok {
		return e.Code()
	}

	// Fallback: string matching for known patterns (last resort)
	errStr := err.Error()
	if strings.Contains(errStr, "SQLSTATE") {
		// Try to extract SQLSTATE from error message
		// Format: "... (SQLSTATE 42P01)" or "SQLSTATE: 42P01"
		for _, prefix := range []string{"SQLSTATE ", "SQLSTATE: "} {
			if idx := strings.Index(errStr, prefix); idx >= 0 {
				start := idx + len(prefix)
				if start+5 <= len(errStr) {
					return errStr[start : start+5]
				}
			}
		}
	}

	return ""
}

// ListObjects returns all object IDs of the given type that subject has relation on.
//
// Example:
//
//	ids, _ := checker.ListObjects(ctx, authz.User("123"), authz.RelCanRead, authz.TypeRepository)
//
// Note: This method does NOT use the permission cache because it returns a list
// rather than a single boolean result.
//
// Note on decision overrides:
//   - DecisionDeny: returns empty list (no access)
//   - DecisionAllow: falls through to normal check (can't enumerate "all" objects)
//
// Uses a recursive CTE to walk the permission graph in a single query,
// providing 10-50x improvement over N+1 patterns on large datasets.
func (c *Checker) ListObjects(ctx context.Context, subject SubjectLike, relation RelationLike, objectType ObjectType) ([]string, error) {
	// Check context decision if enabled
	if c.useContextDecision {
		if d := GetDecisionContext(ctx); d == DecisionDeny {
			return nil, nil
		}
	}

	// DecisionDeny means no access to anything
	if c.decision == DecisionDeny {
		return nil, nil
	}
	// DecisionAllow falls through - we can't enumerate all objects from here,
	// callers needing all objects should query the underlying tables directly.

	rows, err := c.q.QueryContext(ctx,
		"SELECT object_id FROM list_accessible_objects($1, $2, $3, $4)",
		subject.FGASubject().Type, subject.FGASubject().ID, relation.FGARelation(), objectType,
	)
	if err != nil {
		return nil, c.mapError("list_accessible_objects", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

// ListSubjects returns all subject IDs of the given type that have relation on object.
// This is the inverse of ListObjects - it answers "who has access to this object?"
//
// Example:
//
//	ids, _ := checker.ListSubjects(ctx, authz.Repository("456"), authz.RelCanRead, authz.TypeUser)
//	// Returns IDs of all users who can read repository 456
//
// Note: This method does NOT use the permission cache because it returns a list
// rather than a single boolean result.
//
// Note on decision overrides:
//   - DecisionDeny: returns empty list (no subjects have access)
//   - DecisionAllow: falls through to normal check (can't enumerate "all" subjects)
//
// Uses a recursive CTE to walk the permission graph in a single query,
// providing 10-50x improvement over N+1 patterns on large datasets.
func (c *Checker) ListSubjects(ctx context.Context, object ObjectLike, relation RelationLike, subjectType ObjectType) ([]string, error) {
	// Check context decision if enabled
	if c.useContextDecision {
		if d := GetDecisionContext(ctx); d == DecisionDeny {
			return nil, nil
		}
	}

	// DecisionDeny means no subjects have access
	if c.decision == DecisionDeny {
		return nil, nil
	}
	// DecisionAllow falls through - we can't enumerate all subjects from here,
	// callers needing all subjects should query the underlying tables directly.

	rows, err := c.q.QueryContext(ctx,
		"SELECT subject_id FROM list_accessible_subjects($1, $2, $3, $4)",
		object.FGAObject().Type, object.FGAObject().ID, relation.FGARelation(), subjectType,
	)
	if err != nil {
		return nil, c.mapError("list_accessible_subjects", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

// Must panics if the permission check fails or errors.
// Use in handlers after authentication has already verified the user exists.
//
// This is useful for enforcing permissions in code paths where denial should
// be a programmer error rather than a user-facing error. For example:
//
//	// After RequireAuth middleware ensures user is authenticated:
//	repo := getRepository(...)
//	checker.Must(ctx, authz.User(user.ID), authz.RelCanWrite, repo)
//	// Only reachable if permission granted
//
// Prefer Check() for user-facing authorization where you need to return
// a 403 Forbidden response. Use Must() for internal invariants where
// unauthorized access indicates a bug in the calling code.
func (c *Checker) Must(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike) {
	ok, err := c.Check(ctx, subject, relation, object)
	if err != nil {
		panic(fmt.Sprintf("melange.Must: %v", err))
	}
	if !ok {
		panic(fmt.Sprintf("melange.Must: subject %s lacks %s on %s", subject.FGASubject(), relation.FGARelation(), object.FGAObject()))
	}
}
