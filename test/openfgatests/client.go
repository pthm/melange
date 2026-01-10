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

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"google.golang.org/grpc"

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
	sharedDB *sql.DB
	tb       testing.TB

	mu     sync.RWMutex
	stores map[string]*store

	storeCounter atomic.Int64
	modelCounter atomic.Int64

	sharedDBOnce    sync.Once
	sharedDBInitErr error
	dbCreateMu      sync.Mutex
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

	rows, err := store.db.QueryContext(ctx, `
		SELECT subject_type, subject_id, relation
		FROM melange_tuples
		WHERE object_type = $1 AND object_id = $2
		ORDER BY relation, subject_type, subject_id
	`, objectType, objectID)
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
func initializeMelangeSchema(db *sql.DB) error {
	ctx := context.Background()

	// Use migrator.MigrateFromString with a minimal schema to set up infrastructure
	// We use a minimal schema because OpenFGA tests provide their own models
	minimalSchema := `
model
  schema 1.1

type user
`
	if err := migrator.MigrateFromString(ctx, db, minimalSchema); err != nil {
		return fmt.Errorf("apply melange migration: %w", err)
	}

	// Create an empty tuples table and view so that checks work even before Write is called
	// This is needed because Check queries the melange_tuples view
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

// CreateStore creates a new isolated store for testing.
// Each store has its own authorization model and tuples.
func (c *Client) CreateStore(ctx context.Context, req *openfgav1.CreateStoreRequest, opts ...grpc.CallOption) (*openfgav1.CreateStoreResponse, error) {
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

// ListObjects returns all objects of a given type that the user has a relation on.
func (c *Client) ListObjects(ctx context.Context, req *openfgav1.ListObjectsRequest, opts ...grpc.CallOption) (*openfgav1.ListObjectsResponse, error) {
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
	)
	contextualTuples, err := contextualTuplesFromKeys(req.GetContextualTuples().GetTupleKeys())
	if err != nil {
		return nil, fmt.Errorf("parsing contextual tuples: %w", err)
	}
	var ids []string
	if len(contextualTuples) > 0 {
		ids, err = checker.ListObjectsWithContextualTuples(ctx, subject, melange.Relation(req.GetRelation()), melange.ObjectType(req.GetType()), contextualTuples)
	} else {
		ids, err = checker.ListObjects(ctx, subject, melange.Relation(req.GetRelation()), melange.ObjectType(req.GetType()))
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

// ListUsers returns all users that have a relation on the given object.
func (c *Client) ListUsers(ctx context.Context, req *openfgav1.ListUsersRequest, opts ...grpc.CallOption) (*openfgav1.ListUsersResponse, error) {
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
		filterType := filter.GetType()
		subjectType := melange.ObjectType(filterType)

		// Parse userset filter: "group#member" has type "group" and relation "member"
		// The output type should be just "group", not "group#member"
		outputType := filterType
		if idx := strings.Index(filterType, "#"); idx != -1 {
			outputType = filterType[:idx] // Extract just the type part
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
		)
		contextualTuples, err := contextualTuplesFromKeys(req.GetContextualTuples())
		if err != nil {
			return nil, fmt.Errorf("parsing contextual tuples: %w", err)
		}
		var ids []string
		if len(contextualTuples) > 0 {
			ids, err = checker.ListSubjectsWithContextualTuples(ctx, object, melange.Relation(req.GetRelation()), subjectType, contextualTuples)
		} else {
			ids, err = checker.ListSubjects(ctx, object, melange.Relation(req.GetRelation()), subjectType)
		}
		if err != nil {
			return nil, fmt.Errorf("list subjects failed: %w", err)
		}

		for _, id := range ids {
			users = append(users, &openfgav1.User{
				User: &openfgav1.User_Object{
					Object: &openfgav1.Object{
						Type: outputType,
						Id:   id,
					},
				},
			})
		}
	}

	return &openfgav1.ListUsersResponse{
		Users: users,
	}, nil
}

// StreamedListObjects returns a stream of objects. For testing purposes,
// this returns a mock stream that yields results from ListObjects.
func (c *Client) StreamedListObjects(ctx context.Context, req *openfgav1.StreamedListObjectsRequest, opts ...grpc.CallOption) (openfgav1.OpenFGAService_StreamedListObjectsClient, error) {
	// Convert to regular ListObjectsRequest
	listReq := &openfgav1.ListObjectsRequest{
		StoreId:              req.GetStoreId(),
		AuthorizationModelId: req.GetAuthorizationModelId(),
		Type:                 req.GetType(),
		Relation:             req.GetRelation(),
		User:                 req.GetUser(),
		ContextualTuples:     req.GetContextualTuples(),
		Context:              req.GetContext(),
	}

	resp, err := c.ListObjects(ctx, listReq)
	if err != nil {
		return nil, err
	}

	return &streamedListObjectsClient{
		objects: resp.Objects,
		index:   0,
	}, nil
}

// streamedListObjectsClient is a mock implementation of the streaming interface.
type streamedListObjectsClient struct {
	grpc.ClientStream
	objects []string
	index   int
}

func (s *streamedListObjectsClient) Recv() (*openfgav1.StreamedListObjectsResponse, error) {
	if s.index >= len(s.objects) {
		return nil, nil // EOF
	}
	obj := s.objects[s.index]
	s.index++
	return &openfgav1.StreamedListObjectsResponse{
		Object: obj,
	}, nil
}

// loadModel loads a model's type definitions into the database using the Migrator.
func (c *Client) loadModel(ctx context.Context, db *sql.DB, m *model) error {
	if m == nil {
		return fmt.Errorf("model not found")
	}

	// Use the Migrator to apply generated SQL for this model
	// The empty string for schemasDir is fine since we're using MigrateWithTypes directly
	migrator := migrator.NewMigrator(db, "")
	return migrator.MigrateWithTypes(ctx, m.types)
}

// refreshTuples updates the melange_tuples view with the current store tuples.
func (c *Client) refreshTuples(ctx context.Context, db *sql.DB, s *store) error {
	// Drop existing test tuples table if it exists
	_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS melange_test_tuples CASCADE")
	if err != nil {
		return fmt.Errorf("dropping test tuples: %w", err)
	}

	// Create test tuples table
	_, err = db.ExecContext(ctx, `
		CREATE TABLE melange_test_tuples (
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

		_, err = db.ExecContext(ctx, `
			INSERT INTO melange_test_tuples (subject_type, subject_id, relation, object_type, object_id)
			VALUES ($1, $2, $3, $4, $5)
		`, subject.Type, subject.ID, tk.GetRelation(), object.Type, object.ID)
		if err != nil {
			return fmt.Errorf("inserting tuple: %w", err)
		}
	}

	// Create or replace the melange_tuples view
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

// Ensure Client implements the interface at compile time.
var _ interface {
	CreateStore(context.Context, *openfgav1.CreateStoreRequest, ...grpc.CallOption) (*openfgav1.CreateStoreResponse, error)
	WriteAuthorizationModel(context.Context, *openfgav1.WriteAuthorizationModelRequest, ...grpc.CallOption) (*openfgav1.WriteAuthorizationModelResponse, error)
	Write(context.Context, *openfgav1.WriteRequest, ...grpc.CallOption) (*openfgav1.WriteResponse, error)
	Check(context.Context, *openfgav1.CheckRequest, ...grpc.CallOption) (*openfgav1.CheckResponse, error)
	ListUsers(context.Context, *openfgav1.ListUsersRequest, ...grpc.CallOption) (*openfgav1.ListUsersResponse, error)
	ListObjects(context.Context, *openfgav1.ListObjectsRequest, ...grpc.CallOption) (*openfgav1.ListObjectsResponse, error)
	StreamedListObjects(context.Context, *openfgav1.StreamedListObjectsRequest, ...grpc.CallOption) (openfgav1.OpenFGAService_StreamedListObjectsClient, error)
} = (*Client)(nil)

// DB returns the underlying database connection.
func (c *Client) DB() *sql.DB {
	return c.sharedDB
}

func (c *Client) storeDB() (*sql.DB, error) {
	if c.sharedDB != nil {
		c.sharedDBOnce.Do(func() {
			c.sharedDBInitErr = initializeMelangeSchema(c.sharedDB)
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
	if err := initializeMelangeSchema(db); err != nil {
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
