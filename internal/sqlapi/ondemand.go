package sqlapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// Explain and expand on demand.
//
// A database migrated with sql.functions.explain (or expand) off has none of
// that family's functions. Their bodies call only functions the migration
// installs whatever the flag (check_permission_internal and the check
// functions of the exposed relations' closure, see sqlgen.GenerateTraceSQL),
// so the tools compile them from the recorded model with the family switched
// on and create them in pg_temp, for one transaction:
//
//	BEGIN (read write)
//	CREATE FUNCTION pg_temp.explain_... (and the dispatchers)
//	setup, if any (studio's what-if view), then DISCARD PLANS
//	SET TRANSACTION READ ONLY
//	SELECT pg_temp.explain_permission(...)
//	ROLLBACK
//
// The functions are created by one multi-statement query (the simple query
// protocol), in a transaction that is read-write even on studio's read-only
// connection, so a name in the script could commit statements of its own. A
// recorded model is therefore compiled only when every name in it is one the
// schema language allows (invalidName), as melange migrate recorded it.
//
// The call runs through the runtime's Checker with its schema set to
// pg_temp, so Explain, Expand and ExpandRecursive decode, map errors and
// chase pointers exactly as they do against installed functions. The pinned
// runtime (v0.10.3) probes "<schema>".check_permission once per process for
// each schema a Checker is created with; in a transaction a failed probe
// would abort it, so pg_temp also gets a check_permission that forwards to
// the installed one.
//
// PL/pgSQL keeps each function it compiles for the life of the backend,
// keyed by OID, and only frees an entry when that OID is called again. The
// rolled-back functions get new OIDs on every call, so their compiled
// bodies and plans would pile up on a pooled connection (studio's). The
// call therefore runs on a connection of its own that is closed afterwards.

// pgTemp is the schema on-demand functions are created in.
const pgTemp = "pg_temp"

// DB is the database handle Trace runs on, such as *sql.DB.
type DB interface {
	melange.Querier
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Trace runs fn with a Checker whose Explain, Expand and ExpandRecursive
// answer for family f (Explain or Expand) against databaseSchema.
//
// With the family installed, the Checker runs on db. With the family off and
// OnDemand true, it runs in a transaction that holds the functions compiled
// into pg_temp, is read-only while fn runs, and is always rolled back, so
// fn must not keep the Checker. When db is a *sql.DB, that transaction runs
// on a connection taken from the pool and closed afterwards, never returned
// to it. With the family off and no way to compile it, Trace returns the
// *Unavailable that Unavailable reports.
//
// setup, when not nil, runs in that transaction before it turns read-only,
// after the on-demand functions exist, and is followed by DISCARD PLANS so
// cached plans resolve melange_tuples again (studio's what-if view). It puts
// the call in a transaction even when the family is installed.
func (a *API) Trace(ctx context.Context, db DB, databaseSchema string, f Family, setup func(context.Context, *sql.Tx) error, fn func(*melange.Checker) error) error {
	onDemand := !a.Installed(f)
	if onDemand {
		if k := a.cannotCompile(f); k != 0 {
			return a.unavailable(f, "", "", k)
		}
	}
	if !onDemand && setup == nil {
		return fn(melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema)))
	}

	failed := func(err error) error { return &Unavailable{Family: f, kind: onDemandFailed, cause: err} }

	var ddl string
	if onDemand {
		var err error
		if ddl, err = a.traceDDL(databaseSchema, f); err != nil {
			return fmt.Errorf("compiling the %s functions: %w", f.name, err)
		}
		if pool, ok := db.(*sql.DB); ok {
			conn, err := pool.Conn(ctx)
			if err != nil {
				return failed(err)
			}
			defer discard(conn)
			db = conn
		}
	}
	// lib/pq sends BEGIN READ WRITE for nil options, which overrides a
	// read-only default (studio's connection) for this transaction only. A
	// hot standby refuses it.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if onDemand {
			return failed(err)
		}
		// %v, not %w, so a caller that reduces a driver error to its bare
		// message keeps the hint.
		return fmt.Errorf("starting a read-write transaction, which needs a writable primary: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if onDemand {
		// CREATE FUNCTION is refused in a read-only transaction, even in
		// pg_temp, and needs the TEMP privilege.
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return failed(err)
		}
	}
	if setup != nil {
		if err := setup(ctx, tx); err != nil {
			return err
		}
		// Cached PL/pgSQL statements keep the melange_tuples they resolved.
		if _, err := tx.ExecContext(ctx, "DISCARD PLANS"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return err
	}
	schemaOfCall := databaseSchema
	if onDemand {
		schemaOfCall = pgTemp
	}
	return fn(melange.NewChecker(tx, melange.WithDatabaseSchema(schemaOfCall)))
}

// discard closes conn's backend instead of returning it to the pool:
// database/sql drops a connection whose Raw callback reports
// driver.ErrBadConn.
func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// traceDDL returns the statements that create family f's functions in
// pg_temp for databaseSchema, as one script, compiling them on a cache miss.
// The cache key is the model itself, not the record's checksum, so two
// databases can never share a script unless their models are the same.
func (a *API) traceDDL(databaseSchema string, f Family) (string, error) {
	model, err := json.Marshal(struct {
		Types    []schema.TypeDefinition
		Exposure sqlgen.Exposure
		Schema   string
		Family   string
	}{a.types, a.exposure.Normalized(), databaseSchema, f.key})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(model)
	key := hex.EncodeToString(sum[:])
	if ddl, ok := ddlCache.get(key); ok {
		return ddl, nil
	}
	ddl, err := compileTrace(a.types, a.exposure, databaseSchema, f)
	if err != nil {
		return "", err
	}
	ddlCache.put(key, ddl)
	return ddl, nil
}

// compileTrace compiles family f's functions from types into pg_temp, after
// the check_permission forwarder the runtime's schema probe calls.
func compileTrace(types []schema.TypeDefinition, exposure sqlgen.Exposure, databaseSchema string, f Family) (string, error) {
	closure := schema.ComputeRelationClosure(types)
	analyses := sqlgen.ComputeCanGenerate(sqlgen.AnalyzeRelations(types, closure))
	trace, err := sqlgen.GenerateTraceSQL(analyses, sqlgen.BuildInlineSQLData(closure, analyses), databaseSchema, sqlgen.TraceSQLOptions{
		Explain:        f.key == Explain.key,
		Expand:         f.key == Expand.key,
		FunctionSchema: pgTemp,
		Exposure:       exposure,
	})
	if err != nil {
		return "", err
	}
	return strings.Join(append([]string{checkPermissionForwarder(databaseSchema)}, trace.Statements()...), "\n\n"), nil
}

// checkPermissionForwarder is pg_temp.check_permission, calling the
// installed check_permission. An unqualified call cannot resolve to pg_temp,
// where PostgreSQL never looks up functions.
func checkPermissionForwarder(databaseSchema string) string {
	target := "check_permission"
	if databaseSchema != "" {
		target = pq.QuoteIdentifier(databaseSchema) + "." + target
	}
	return `CREATE OR REPLACE FUNCTION pg_temp.check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT ` + target + `(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id);
$$ LANGUAGE sql STABLE;`
}

// ddlCache keeps compiled on-demand scripts, keyed by the model, the
// exposure, the data schema and the family, so studio, which explains on
// every click, compiles a model once. Compiling a large model takes most of
// a second.
var ddlCache = &scriptCache{max: 8}

type scriptCache struct {
	mu      sync.Mutex
	max     int
	scripts map[string]string
}

func (c *scriptCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.scripts[key]
	return s, ok
}

// put stores a script, starting over when the cache is full: it only ever
// holds the few models one process talks to.
func (c *scriptCache) put(key, script string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scripts == nil || len(c.scripts) >= c.max {
		c.scripts = make(map[string]string)
	}
	c.scripts[key] = script
}
