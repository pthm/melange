// Package openfgatests provides an adapter to run the official OpenFGA test suite
// against the melange authorization implementation.
//
// This package implements the OpenFGA ClientInterface, allowing melange to be
// validated against the same test cases used by the official OpenFGA server.
//
// # Usage
//
// The adapter uses testutil for database setup and integrates with the existing
// test infrastructure.
//
//	func TestOpenFGACheck(t *testing.T) {
//	    client := openfgatests.NewClient(t)
//	    check.RunAllTests(t, client)
//	}
package openfgatests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lib/pq"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	openfgaserver "github.com/openfga/openfga/pkg/server"
	"github.com/openfga/openfga/pkg/storage/memory"
	"google.golang.org/grpc"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
)

// Client implements the OpenFGA ClientInterface for running tests against melange.
// It manages stores, authorization models, and tuples in PostgreSQL, routing
// permission checks through melange's Checker.
type Client struct {
	sharedDB       *sql.DB
	tb             testing.TB
	databaseSchema string

	mu     sync.RWMutex
	stores map[string]*store

	storeCounter atomic.Int64
	modelCounter atomic.Int64

	sharedDBOnce    sync.Once
	sharedDBInitErr error
	dbCreateMu      sync.Mutex

	// openfgaBackend, when non-nil, makes this Client a thin passthrough to a
	// real in-process OpenFGA server instead of melange — the conformance
	// "oracle". The six gRPC methods delegate to it verbatim, so the same test
	// runner validates a YAML case's expected values against real OpenFGA.
	openfgaBackend *openfgaserver.Server
}

// NewOpenFGAClient returns a Client backed by a real in-process OpenFGA server
// (memory datastore) instead of melange. Used as the conformance oracle: running
// the same YAML cases through it proves the expected values are OpenFGA-correct,
// and any melange divergence shows up as a melange-mode failure on the same case.
func NewOpenFGAClient(tb testing.TB) *Client {
	tb.Helper()
	srv, err := openfgaserver.NewServerWithOpts(openfgaserver.WithDatastore(memory.New()))
	if err != nil {
		tb.Fatalf("new openfga server: %v", err)
	}
	tb.Cleanup(srv.Close)
	return &Client{
		tb:             tb,
		stores:         make(map[string]*store),
		openfgaBackend: srv,
	}
}

// store represents an isolated OpenFGA store with its own model and tuples.
type store struct {
	id     string
	name   string
	db     *sql.DB
	models map[string]*model
	tuples []*openfgav1.TupleKey
}

// model represents an authorization model within a store.
type model struct {
	id         string
	types      []schema.TypeDefinition
	authzModel *openfgav1.AuthorizationModel
}

// NewClient creates a new test client using testutil infrastructure.
// The database is automatically set up with melange schema and cleaned up
// when the test completes.
func NewClient(tb testing.TB) *Client {
	tb.Helper()

	return &Client{
		tb:     tb,
		stores: make(map[string]*store),
	}
}

// forSubtest returns a client for an isolated per-test run. When this client is
// an OpenFGA oracle it shares the same in-process server (each test isolates via
// its own CreateStore); otherwise it returns a fresh melange client for the
// configured schema. Preserving the backend is what makes oracle mode actually
// exercise real OpenFGA rather than silently falling back to melange.
func (c *Client) forSubtest(tb testing.TB) *Client {
	if c.openfgaBackend != nil {
		return &Client{
			tb:             tb,
			stores:         make(map[string]*store),
			openfgaBackend: c.openfgaBackend,
		}
	}
	return NewClientWithSchema(tb, c.databaseSchema)
}

// NewClientWithSchema creates a test client that installs melange objects
// in the given Postgres schema. An empty string uses the default (public) schema.
func NewClientWithSchema(tb testing.TB, databaseSchema string) *Client {
	tb.Helper()

	return &Client{
		tb:             tb,
		databaseSchema: databaseSchema,
		stores:         make(map[string]*store),
	}
}

// DatabaseSchema returns the configured database schema.
func (c *Client) DatabaseSchema() string {
	return c.databaseSchema
}

// NewClientWithDB creates a test client with an existing database connection.
// Use this when you need more control over the database setup.
func NewClientWithDB(db *sql.DB) *Client {
	return &Client{
		sharedDB: db,
		stores:   make(map[string]*store),
	}
}

func (c *Client) debugUserset(
	tb testing.TB,
	storeID string,
	objectType string,
	objectID string,
	relation string,
	filters []string,
) {
	if os.Getenv("MELANGE_DEBUG_USERSET") == "" {
		return
	}

	tb.Helper()
	ctx := context.Background()

	store, ok := c.storeByID(storeID)
	if !ok {
		tb.Logf("debug userset: store not found: %s", storeID)
		return
	}

	tb.Logf("debug userset: object=%s:%s relation=%s filters=%v", objectType, objectID, relation, filters)
	tb.Logf("debug userset: inline schema data enabled (no model tables to query)")

	rows, err := store.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT subject_type, subject_id, relation
		FROM %s
		WHERE object_type = $1 AND object_id = $2
		ORDER BY relation, subject_type, subject_id
	`, sqldsl.PrefixIdent("melange_tuples", c.databaseSchema)), objectType, objectID)
	if err != nil {
		tb.Logf("debug tuples error: %v", err)
		return
	}
	for rows.Next() {
		var subjType, subjID, rel string
		if scanErr := rows.Scan(&subjType, &subjID, &rel); scanErr != nil {
			tb.Logf("debug tuples scan error: %v", scanErr)
			break
		}
		tb.Logf("tuple: subject_type=%s subject_id=%s relation=%s", subjType, subjID, rel)
	}
	_ = rows.Close()
}

// initializeMelangeSchema applies the melange DDL without domain-specific tables.
func initializeMelangeSchema(db *sql.DB, databaseSchema string) error {
	ctx := context.Background()

	// Create target schema if configured
	if databaseSchema != "" {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", sqldsl.QuoteIdent(databaseSchema))); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}

	// Use migrator with schema support to set up infrastructure
	// We use a minimal schema because OpenFGA tests provide their own models
	minimalSchema := `
model
  schema 1.1

type user
`
	types, err := parser.ParseSchemaString(minimalSchema)
	if err != nil {
		return fmt.Errorf("parsing minimal schema: %w", err)
	}
	mig := migrator.NewMigrator(db, "")
	mig.SetDatabaseSchema(databaseSchema)
	if err := mig.MigrateWithTypes(ctx, types); err != nil {
		return fmt.Errorf("apply melange migration: %w", err)
	}

	tableName := sqldsl.PrefixIdent("melange_test_tuples", databaseSchema)
	viewName := sqldsl.PrefixIdent("melange_tuples", databaseSchema)

	// Create an empty tuples table and view so that checks work even before Write is called
	// This is needed because Check queries the melange_tuples view
	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			subject_type TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			object_type TEXT NOT NULL,
			object_id TEXT NOT NULL
		)
	`, tableName))
	if err != nil {
		return fmt.Errorf("creating test tuples table: %w", err)
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		CREATE OR REPLACE VIEW %s AS
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM %s
	`, viewName, tableName))
	if err != nil {
		return fmt.Errorf("creating tuples view: %w", err)
	}

	return nil
}

// CreateStore creates a new isolated store for testing.
// Each store has its own authorization model and tuples.
func (c *Client) CreateStore(ctx context.Context, req *openfgav1.CreateStoreRequest, opts ...grpc.CallOption) (*openfgav1.CreateStoreResponse, error) {
	if c.openfgaBackend != nil {
		return c.openfgaBackend.CreateStore(ctx, req)
	}
	id := fmt.Sprintf("store_%d", c.storeCounter.Add(1))

	db, err := c.storeDB()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.stores[id] = &store{
		id:     id,
		name:   req.GetName(),
		db:     db,
		models: make(map[string]*model),
		tuples: nil,
	}
	c.mu.Unlock()

	return &openfgav1.CreateStoreResponse{
		Id:   id,
		Name: req.GetName(),
	}, nil
}

// WriteAuthorizationModel writes an authorization model to the store.
// The model is parsed and stored for use in permission checks.
func (c *Client) WriteAuthorizationModel(ctx context.Context, req *openfgav1.WriteAuthorizationModelRequest, opts ...grpc.CallOption) (*openfgav1.WriteAuthorizationModelResponse, error) {
	if c.openfgaBackend != nil {
		return c.openfgaBackend.WriteAuthorizationModel(ctx, req)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	s, ok := c.stores[req.GetStoreId()]
	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	// Convert the protobuf model to schema TypeDefinitions
	types := convertProtoModel(req)

	modelID := fmt.Sprintf("model_%d", c.modelCounter.Add(1))
	s.models[modelID] = &model{
		id:    modelID,
		types: types,
		authzModel: &openfgav1.AuthorizationModel{
			Id:              modelID,
			SchemaVersion:   req.GetSchemaVersion(),
			TypeDefinitions: req.GetTypeDefinitions(),
			Conditions:      req.GetConditions(),
		},
	}

	// Load this model into the database
	if err := c.loadModel(ctx, s.db, s.models[modelID]); err != nil {
		return nil, fmt.Errorf("loading model: %w", err)
	}

	return &openfgav1.WriteAuthorizationModelResponse{
		AuthorizationModelId: modelID,
	}, nil
}

// Write writes or deletes tuples in the store.
func (c *Client) Write(ctx context.Context, req *openfgav1.WriteRequest, opts ...grpc.CallOption) (*openfgav1.WriteResponse, error) {
	if c.openfgaBackend != nil {
		return c.openfgaBackend.Write(ctx, req)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	s, ok := c.stores[req.GetStoreId()]
	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	// Process deletes first
	if deletes := req.GetDeletes(); deletes != nil {
		for _, tk := range deletes.GetTupleKeys() {
			s.tuples = removeTuple(s.tuples, tk)
		}
	}

	// Then process writes
	if writes := req.GetWrites(); writes != nil {
		s.tuples = append(s.tuples, writes.GetTupleKeys()...)
	}

	// Refresh the tuples view in the database
	if err := c.refreshTuples(ctx, s.db, s); err != nil {
		return nil, fmt.Errorf("refreshing tuples: %w", err)
	}

	return &openfgav1.WriteResponse{}, nil
}

// Check evaluates whether a user has a specific relation on an object.
func (c *Client) Check(ctx context.Context, req *openfgav1.CheckRequest, opts ...grpc.CallOption) (*openfgav1.CheckResponse, error) {
	if c.openfgaBackend != nil {
		return c.openfgaBackend.Check(ctx, req)
	}
	store, ok := c.storeByID(req.GetStoreId())
	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	// Parse the tuple key
	tk := req.GetTupleKey()
	subject, err := parseSubject(tk.GetUser())
	if err != nil {
		return nil, fmt.Errorf("parsing subject: %w", err)
	}
	object, err := parseObject(tk.GetObject())
	if err != nil {
		return nil, fmt.Errorf("parsing object: %w", err)
	}
	relation := tk.GetRelation()

	// Perform the check using melange
	validator, err := validatorForStore(store, req.GetAuthorizationModelId())
	if err != nil {
		return nil, err
	}
	checker := melange.NewChecker(
		store.db,
		melange.WithUsersetValidation(),
		melange.WithRequestValidation(),
		melange.WithValidator(validator),
		melange.WithDatabaseSchema(c.databaseSchema),
	)
	contextualTuples, err := contextualTuplesFromKeys(req.GetContextualTuples().GetTupleKeys())
	if err != nil {
		return nil, fmt.Errorf("parsing contextual tuples: %w", err)
	}
	var allowed bool
	if len(contextualTuples) > 0 {
		allowed, err = checker.CheckWithContextualTuples(ctx, subject, melange.Relation(relation), object, contextualTuples)
	} else {
		allowed, err = checker.Check(ctx, subject, melange.Relation(relation), object)
	}
	if err != nil {
		return nil, fmt.Errorf("check failed: %w", err)
	}

	return &openfgav1.CheckResponse{
		Allowed: allowed,
	}, nil
}

// Explain returns the resolution Trace for a single check, mirroring the
// shape of Check above. Used by the OpenFGA-suite parity sweep to assert
// `trace.Result == checkAssertion.Expectation` for every eligible
// assertion across every YAML — see explain_parity.go.
//
// Contextual tuples are not yet wired through the Explain SQL function, so
// callers should skip assertions carrying contextual tuples rather than
// route them here.
func (c *Client) Explain(ctx context.Context, req *openfgav1.CheckRequest, opts ...grpc.CallOption) (*melange.Trace, error) {
	store, ok := c.storeByID(req.GetStoreId())
	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	tk := req.GetTupleKey()
	subject, err := parseSubject(tk.GetUser())
	if err != nil {
		return nil, fmt.Errorf("parsing subject: %w", err)
	}
	object, err := parseObject(tk.GetObject())
	if err != nil {
		return nil, fmt.Errorf("parsing object: %w", err)
	}
	relation := tk.GetRelation()

	validator, err := validatorForStore(store, req.GetAuthorizationModelId())
	if err != nil {
		return nil, err
	}
	checker := melange.NewChecker(
		store.db,
		melange.WithUsersetValidation(),
		melange.WithRequestValidation(),
		melange.WithValidator(validator),
		melange.WithDatabaseSchema(c.databaseSchema),
	)
	return checker.Explain(ctx, subject, melange.Relation(relation), object)
}

// ExpandRecursive returns the flat deduplicated user-string list for an
// (object, relation) pair, chasing every Leaf.Computed and
// Leaf.TupleToUserset pointer in the response tree via additional
// Expand calls. Used by the OpenFGA-suite parity sweep — see
// expand_parity.go.
//
// Same eligibility/skip rules as Explain: skip the dispatcher's
// empty-leaf sentinel (relations gated out of slice 2.x Expand) and
// contextual tuples (not supported by Expand SQL).
func (c *Client) ExpandRecursive(ctx context.Context, storeID, modelID, object, relation string) ([]string, error) {
	store, ok := c.storeByID(storeID)
	if !ok {
		return nil, fmt.Errorf("store not found: %s", storeID)
	}
	obj, err := parseObject(object)
	if err != nil {
		return nil, fmt.Errorf("parsing object: %w", err)
	}
	// modelID is unused by Expand (no validator) but kept in the
	// signature for symmetry with Explain — both methods take the
	// canonical (storeID, modelID, object, relation) tuple OpenFGA
	// requests carry. Skipping the validator here is safe because the
	// OpenFGA suite already validates the model + tuples through the
	// typesystem before any check runs, and the SQL dispatcher rejects
	// unknown (object_type, relation) pairs via its sentinel.
	_ = modelID
	checker := melange.NewChecker(
		store.db,
		melange.WithDatabaseSchema(c.databaseSchema),
	)
	return checker.ExpandRecursive(ctx, obj, melange.Relation(relation))
}

// Expand returns the raw UsersetTree for an (object, relation) pair
// without chasing pointers. Used by the parity sweep's sentinel
// detection — when the renderer is not yet eligible for the relation,
// the dispatcher's empty-leaf sentinel surfaces an empty Leaf.Users
// at the root, which we use as the skip signal.
func (c *Client) Expand(ctx context.Context, storeID, modelID, object, relation string) (*melange.UsersetTree, error) {
	store, ok := c.storeByID(storeID)
	if !ok {
		return nil, fmt.Errorf("store not found: %s", storeID)
	}
	obj, err := parseObject(object)
	if err != nil {
		return nil, fmt.Errorf("parsing object: %w", err)
	}
	// See the ExpandRecursive comment for why we skip the validator —
	// modelID is kept in the signature for caller symmetry only.
	_ = modelID
	checker := melange.NewChecker(
		store.db,
		melange.WithDatabaseSchema(c.databaseSchema),
	)
	return checker.Expand(ctx, obj, melange.Relation(relation))
}

// ListObjects returns all objects of a given type that the user has a relation on.
func (c *Client) ListObjects(ctx context.Context, req *openfgav1.ListObjectsRequest, opts ...grpc.CallOption) (*openfgav1.ListObjectsResponse, error) {
	if c.openfgaBackend != nil {
		return c.openfgaBackend.ListObjects(ctx, req)
	}
	store, ok := c.storeByID(req.GetStoreId())
	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	subject, err := parseSubject(req.GetUser())
	if err != nil {
		return nil, fmt.Errorf("parsing subject: %w", err)
	}

	validator, err := validatorForStore(store, req.GetAuthorizationModelId())
	if err != nil {
		return nil, err
	}
	checker := melange.NewChecker(
		store.db,
		melange.WithUsersetValidation(),
		melange.WithRequestValidation(),
		melange.WithValidator(validator),
		melange.WithDatabaseSchema(c.databaseSchema),
	)
	contextualTuples, err := contextualTuplesFromKeys(req.GetContextualTuples().GetTupleKeys())
	if err != nil {
		return nil, fmt.Errorf("parsing contextual tuples: %w", err)
	}
	var ids []string
	if len(contextualTuples) > 0 {
		ids, _, err = checker.ListObjectsWithContextualTuples(ctx, subject, melange.Relation(req.GetRelation()), melange.ObjectType(req.GetType()), contextualTuples, melange.PageOptions{})
	} else {
		ids, err = checker.ListObjectsAll(ctx, subject, melange.Relation(req.GetRelation()), melange.ObjectType(req.GetType()))
	}
	if err != nil {
		return nil, fmt.Errorf("list objects failed: %w", err)
	}

	objects := make([]string, len(ids))
	for i, id := range ids {
		objects[i] = req.GetType() + ":" + id
	}

	return &openfgav1.ListObjectsResponse{
		Objects: objects,
	}, nil
}

// parseUserTypeFilter converts a test filter string into an OpenFGA UserTypeFilter.
// A userset filter is written "type#relation" (e.g. "group#member") and maps to
// {Type:"group", Relation:"member"} — the canonical shape that makes ListUsers
// return stored usersets; a plain type maps to {Type:type}.
func parseUserTypeFilter(f string) *openfgav1.UserTypeFilter {
	if i := strings.Index(f, "#"); i != -1 {
		return &openfgav1.UserTypeFilter{Type: f[:i], Relation: f[i+1:]}
	}
	return &openfgav1.UserTypeFilter{Type: f}
}

// userString renders a ListUsers result User as its OpenFGA string form:
// "type:id" for an object, "type:id#relation" for a stored userset, and
// "type:*" for a typed wildcard. The melange adapter emits a wildcard as an
// object with id "*", but real OpenFGA (the oracle backend) returns a
// User_Wildcard, so both arms are required for the oracle to see wildcards.
func userString(u *openfgav1.User) string {
	if o := u.GetObject(); o != nil {
		return o.GetType() + ":" + o.GetId()
	}
	if w := u.GetWildcard(); w != nil {
		return w.GetType() + ":*"
	}
	if us := u.GetUserset(); us != nil {
		return us.GetType() + ":" + us.GetId() + "#" + us.GetRelation()
	}
	return ""
}

// ListUsers returns all users that have a relation on the given object.
func (c *Client) ListUsers(ctx context.Context, req *openfgav1.ListUsersRequest, opts ...grpc.CallOption) (*openfgav1.ListUsersResponse, error) {
	if c.openfgaBackend != nil {
		return c.openfgaBackend.ListUsers(ctx, req)
	}
	store, ok := c.storeByID(req.GetStoreId())
	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	object, err := parseObject(req.GetObject().GetType() + ":" + req.GetObject().GetId())
	if err != nil {
		return nil, fmt.Errorf("parsing object: %w", err)
	}

	// Get all user filters and list subjects for each
	var users []*openfgav1.User
	for _, filter := range req.GetUserFilters() {
		// The returned object type is just the principal type, without any
		// userset relation suffix.
		outputType := filter.GetType()

		// A userset filter carries a relation ({type:"group", relation:"member"});
		// melange's list_subjects addresses that as the subject_type "group#member".
		filterType := outputType
		if rel := filter.GetRelation(); rel != "" {
			filterType = filterType + "#" + rel
		}
		subjectType := melange.ObjectType(filterType)

		validator, err := validatorForStore(store, req.GetAuthorizationModelId())
		if err != nil {
			return nil, err
		}
		checker := melange.NewChecker(
			store.db,
			melange.WithUsersetValidation(),
			melange.WithRequestValidation(),
			melange.WithValidator(validator),
			melange.WithDatabaseSchema(c.databaseSchema),
		)
		contextualTuples, err := contextualTuplesFromKeys(req.GetContextualTuples())
		if err != nil {
			return nil, fmt.Errorf("parsing contextual tuples: %w", err)
		}
		var ids []string
		if len(contextualTuples) > 0 {
			ids, _, err = checker.ListSubjectsWithContextualTuples(ctx, object, melange.Relation(req.GetRelation()), subjectType, contextualTuples, melange.PageOptions{})
		} else {
			ids, err = checker.ListSubjectsAll(ctx, object, melange.Relation(req.GetRelation()), subjectType)
		}
		if err != nil {
			return nil, fmt.Errorf("list subjects failed: %w", err)
		}

		for _, id := range ids {
			// A stored-userset subject ("g1#member") must be returned as a
			// User_Userset to match OpenFGA's ListUsers contract, not a User_Object.
			if idx := strings.Index(id, "#"); idx != -1 {
				users = append(users, &openfgav1.User{
					User: &openfgav1.User_Userset{
						Userset: &openfgav1.UsersetUser{
							Type:     outputType,
							Id:       id[:idx],
							Relation: id[idx+1:],
						},
					},
				})
			} else {
				users = append(users, &openfgav1.User{
					User: &openfgav1.User_Object{
						Object: &openfgav1.Object{Type: outputType, Id: id},
					},
				})
			}
		}
	}

	return &openfgav1.ListUsersResponse{
		Users: users,
	}, nil
}

// loadModel loads a model's type definitions into the database using the Migrator.
func (c *Client) loadModel(ctx context.Context, db *sql.DB, m *model) error {
	if m == nil {
		return fmt.Errorf("model not found")
	}

	// Use the Migrator to apply generated SQL for this model
	// The empty string for schemasDir is fine since we're using MigrateWithTypes directly
	mig := migrator.NewMigrator(db, "")
	mig.SetDatabaseSchema(c.databaseSchema)
	return mig.MigrateWithTypes(ctx, m.types)
}

// refreshTuples updates the melange_tuples view with the current store tuples.
func (c *Client) refreshTuples(ctx context.Context, db *sql.DB, s *store) error {
	tableName := sqldsl.PrefixIdent("melange_test_tuples", c.databaseSchema)
	viewName := sqldsl.PrefixIdent("melange_tuples", c.databaseSchema)

	// Drop existing test tuples table if it exists
	_, err := db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", tableName))
	if err != nil {
		return fmt.Errorf("dropping test tuples: %w", err)
	}

	// Create test tuples table
	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			subject_type TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			object_type TEXT NOT NULL,
			object_id TEXT NOT NULL
		)
	`, tableName))
	if err != nil {
		return fmt.Errorf("creating test tuples table: %w", err)
	}

	// Insert all tuples
	for _, tk := range s.tuples {
		subject, err := parseSubject(tk.GetUser())
		if err != nil {
			return fmt.Errorf("parsing tuple subject: %w", err)
		}
		object, err := parseObject(tk.GetObject())
		if err != nil {
			return fmt.Errorf("parsing tuple object: %w", err)
		}

		_, err = db.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (subject_type, subject_id, relation, object_type, object_id)
			VALUES ($1, $2, $3, $4, $5)
		`, tableName), subject.Type, subject.ID, tk.GetRelation(), object.Type, object.ID)
		if err != nil {
			return fmt.Errorf("inserting tuple: %w", err)
		}
	}

	// Create or replace the melange_tuples view
	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		CREATE OR REPLACE VIEW %s AS
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM %s
	`, viewName, tableName))
	if err != nil {
		return fmt.Errorf("creating tuples view: %w", err)
	}

	return nil
}

// convertProtoModel converts a WriteAuthorizationModelRequest to schema TypeDefinitions.
// Uses parser.ConvertProtoModel to ensure test parsing matches production parsing.
func convertProtoModel(req *openfgav1.WriteAuthorizationModelRequest) []schema.TypeDefinition {
	model := &openfgav1.AuthorizationModel{
		SchemaVersion:   req.GetSchemaVersion(),
		TypeDefinitions: req.GetTypeDefinitions(),
		Conditions:      req.GetConditions(),
	}

	return parser.ConvertProtoModel(model)
}

// parseSubject parses an OpenFGA user string (e.g., "user:123" or "team:456#member").
func parseSubject(user string) (melange.Object, error) {
	// Handle userset format: "type:id#relation"
	// For userset references like "group:x#member", we need to preserve the full ID
	// The relation part (#member) indicates that this is a userset reference
	for i := 0; i < len(user); i++ {
		if user[i] == ':' {
			// Found the type:id separator
			// The ID may include a #relation suffix for userset references
			return melange.Object{
				Type: melange.ObjectType(user[:i]),
				ID:   user[i+1:], // Includes #relation if present
			}, nil
		}
	}
	return melange.Object{}, fmt.Errorf("invalid subject format: %s", user)
}

func contextualTuplesFromKeys(keys []*openfgav1.TupleKey) ([]melange.ContextualTuple, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	tuples := make([]melange.ContextualTuple, 0, len(keys))
	for _, key := range keys {
		subject, err := parseSubject(key.GetUser())
		if err != nil {
			return nil, err
		}
		object, err := parseObject(key.GetObject())
		if err != nil {
			return nil, err
		}
		tuples = append(tuples, melange.ContextualTuple{
			Subject:  subject,
			Relation: melange.Relation(key.GetRelation()),
			Object:   object,
		})
	}
	return tuples, nil
}

// parseObject parses an OpenFGA object string (e.g., "document:123").
func parseObject(obj string) (melange.Object, error) {
	for i := 0; i < len(obj); i++ {
		if obj[i] == ':' {
			return melange.Object{
				Type: melange.ObjectType(obj[:i]),
				ID:   obj[i+1:],
			}, nil
		}
	}
	return melange.Object{}, fmt.Errorf("invalid object format: %s", obj)
}

// removeTuple removes a tuple from the slice if it matches.
// The toRemove parameter uses TupleKeyWithoutCondition as that's what the delete API uses.
func removeTuple(tuples []*openfgav1.TupleKey, toRemove *openfgav1.TupleKeyWithoutCondition) []*openfgav1.TupleKey {
	result := make([]*openfgav1.TupleKey, 0, len(tuples))
	for _, t := range tuples {
		if t.GetUser() != toRemove.GetUser() ||
			t.GetRelation() != toRemove.GetRelation() ||
			t.GetObject() != toRemove.GetObject() {
			result = append(result, t)
		}
	}
	return result
}

// CheckBulk evaluates multiple permission checks in a single SQL call using
// check_permission_bulk. It takes a store ID and a slice of check assertions
// (without contextual tuples or error codes), builds parallel arrays, and
// returns a map from assertion index to whether the check was allowed.
func (c *Client) CheckBulk(ctx context.Context, storeID string, assertions []*CheckAssertion) (map[int]bool, error) {
	store, ok := c.storeByID(storeID)
	if !ok {
		return nil, fmt.Errorf("store not found: %s", storeID)
	}

	n := len(assertions)
	subjectTypes := make([]string, n)
	subjectIDs := make([]string, n)
	relations := make([]string, n)
	objectTypes := make([]string, n)
	objectIDs := make([]string, n)

	for i, a := range assertions {
		tk := a.Tuple
		subject, err := parseSubject(tk.GetUser())
		if err != nil {
			return nil, fmt.Errorf("parsing subject for assertion %d: %w", i, err)
		}
		object, err := parseObject(tk.GetObject())
		if err != nil {
			return nil, fmt.Errorf("parsing object for assertion %d: %w", i, err)
		}
		subjectTypes[i] = string(subject.Type)
		subjectIDs[i] = subject.ID
		relations[i] = tk.GetRelation()
		objectTypes[i] = string(object.Type)
		objectIDs[i] = object.ID
	}

	funcName := sqldsl.PrefixIdent("check_permission_bulk", c.databaseSchema)
	rows, err := store.db.QueryContext(ctx,
		fmt.Sprintf("SELECT idx, allowed FROM %s($1, $2, $3, $4, $5)", funcName),
		pq.Array(subjectTypes), pq.Array(subjectIDs), pq.Array(relations),
		pq.Array(objectTypes), pq.Array(objectIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("check_permission_bulk query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	results := make(map[int]bool, n)
	for rows.Next() {
		var idx, allowed int
		if err := rows.Scan(&idx, &allowed); err != nil {
			return nil, fmt.Errorf("scanning bulk result: %w", err)
		}
		// Convert 1-based ordinality to 0-based index
		results[idx-1] = allowed == 1
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating bulk results: %w", err)
	}

	return results, nil
}

// Ensure Client implements the interface at compile time.
var _ interface {
	CreateStore(context.Context, *openfgav1.CreateStoreRequest, ...grpc.CallOption) (*openfgav1.CreateStoreResponse, error)
	WriteAuthorizationModel(context.Context, *openfgav1.WriteAuthorizationModelRequest, ...grpc.CallOption) (*openfgav1.WriteAuthorizationModelResponse, error)
	Write(context.Context, *openfgav1.WriteRequest, ...grpc.CallOption) (*openfgav1.WriteResponse, error)
	Check(context.Context, *openfgav1.CheckRequest, ...grpc.CallOption) (*openfgav1.CheckResponse, error)
	ListUsers(context.Context, *openfgav1.ListUsersRequest, ...grpc.CallOption) (*openfgav1.ListUsersResponse, error)
	ListObjects(context.Context, *openfgav1.ListObjectsRequest, ...grpc.CallOption) (*openfgav1.ListObjectsResponse, error)
} = (*Client)(nil)

func (c *Client) storeDB() (*sql.DB, error) {
	if c.sharedDB != nil {
		c.sharedDBOnce.Do(func() {
			c.sharedDBInitErr = initializeMelangeSchema(c.sharedDB, c.databaseSchema)
		})
		if c.sharedDBInitErr != nil {
			return nil, fmt.Errorf("init shared db: %w", c.sharedDBInitErr)
		}
		return c.sharedDB, nil
	}

	if c.tb == nil {
		return nil, fmt.Errorf("missing test handle for store db creation")
	}
	c.dbCreateMu.Lock()
	defer c.dbCreateMu.Unlock()

	db := testutil.EmptyDB(c.tb)
	if err := initializeMelangeSchema(db, c.databaseSchema); err != nil {
		return nil, fmt.Errorf("init store db: %w", err)
	}
	return db, nil
}

func (c *Client) storeByID(id string) (*store, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.stores[id]
	return s, ok
}
