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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"google.golang.org/grpc"

	"github.com/pthm/melange"
	"github.com/pthm/melange/test/testutil"
	"github.com/pthm/melange/tooling"
)

// Client implements the OpenFGA ClientInterface for running tests against melange.
// It manages stores, authorization models, and tuples in PostgreSQL, routing
// permission checks through melange's Checker.
type Client struct {
	db *sql.DB
	tb testing.TB

	mu     sync.RWMutex
	stores map[string]*store

	storeCounter atomic.Int64
	modelCounter atomic.Int64
}

// store represents an isolated OpenFGA store with its own model and tuples.
type store struct {
	id     string
	name   string
	models map[string]*model
	tuples []*openfgav1.TupleKey
}

// model represents an authorization model within a store.
type model struct {
	id         string
	types      []melange.TypeDefinition
	authzModel *openfgav1.AuthorizationModel
}

// NewClient creates a new test client using testutil infrastructure.
// The database is automatically set up with melange schema and cleaned up
// when the test completes.
func NewClient(tb testing.TB) *Client {
	tb.Helper()

	// Use testutil.EmptyDB since we need dynamic schemas for each test
	db := testutil.EmptyDB(tb)

	// Initialize melange infrastructure (without domain tables)
	if err := initializeMelangeSchema(db); err != nil {
		tb.Fatalf("failed to initialize melange schema: %v", err)
	}

	return &Client{
		db:     db,
		tb:     tb,
		stores: make(map[string]*store),
	}
}

// NewClientWithDB creates a test client with an existing database connection.
// Use this when you need more control over the database setup.
func NewClientWithDB(db *sql.DB) *Client {
	return &Client{
		db:     db,
		stores: make(map[string]*store),
	}
}

// initializeMelangeSchema applies the melange DDL without domain-specific tables.
func initializeMelangeSchema(db *sql.DB) error {
	ctx := context.Background()

	// Use tooling.MigrateFromString with a minimal schema to set up infrastructure
	// We use a minimal schema because OpenFGA tests provide their own models
	minimalSchema := `
model
  schema 1.1

type user
`
	if err := tooling.MigrateFromString(ctx, db, minimalSchema); err != nil {
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

	c.mu.Lock()
	c.stores[id] = &store{
		id:     id,
		name:   req.GetName(),
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

	// Convert the protobuf model to melange TypeDefinitions
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
	if err := c.loadModel(ctx, s, modelID); err != nil {
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
		for _, tk := range writes.GetTupleKeys() {
			s.tuples = append(s.tuples, tk)
		}
	}

	// Refresh the tuples view in the database
	if err := c.refreshTuples(ctx, s); err != nil {
		return nil, fmt.Errorf("refreshing tuples: %w", err)
	}

	return &openfgav1.WriteResponse{}, nil
}

// Check evaluates whether a user has a specific relation on an object.
func (c *Client) Check(ctx context.Context, req *openfgav1.CheckRequest, opts ...grpc.CallOption) (*openfgav1.CheckResponse, error) {
	c.mu.RLock()
	s, ok := c.stores[req.GetStoreId()]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	// Handle contextual tuples by temporarily adding them
	if contextualTuples := req.GetContextualTuples(); contextualTuples != nil && len(contextualTuples.GetTupleKeys()) > 0 {
		c.mu.Lock()
		originalTuples := s.tuples
		s.tuples = append(append([]*openfgav1.TupleKey{}, s.tuples...), contextualTuples.GetTupleKeys()...)
		if err := c.refreshTuples(ctx, s); err != nil {
			s.tuples = originalTuples
			c.mu.Unlock()
			return nil, fmt.Errorf("refreshing contextual tuples: %w", err)
		}
		c.mu.Unlock()

		defer func() {
			c.mu.Lock()
			s.tuples = originalTuples
			_ = c.refreshTuples(ctx, s)
			c.mu.Unlock()
		}()
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
	checker := melange.NewChecker(c.db)
	allowed, err := checker.Check(ctx, subject, melange.Relation(relation), object)
	if err != nil {
		return nil, fmt.Errorf("check failed: %w", err)
	}

	return &openfgav1.CheckResponse{
		Allowed: allowed,
	}, nil
}

// ListObjects returns all objects of a given type that the user has a relation on.
func (c *Client) ListObjects(ctx context.Context, req *openfgav1.ListObjectsRequest, opts ...grpc.CallOption) (*openfgav1.ListObjectsResponse, error) {
	c.mu.RLock()
	s, ok := c.stores[req.GetStoreId()]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	// Handle contextual tuples
	if contextualTuples := req.GetContextualTuples(); contextualTuples != nil && len(contextualTuples.GetTupleKeys()) > 0 {
		c.mu.Lock()
		originalTuples := s.tuples
		s.tuples = append(append([]*openfgav1.TupleKey{}, s.tuples...), contextualTuples.GetTupleKeys()...)
		if err := c.refreshTuples(ctx, s); err != nil {
			s.tuples = originalTuples
			c.mu.Unlock()
			return nil, fmt.Errorf("refreshing contextual tuples: %w", err)
		}
		c.mu.Unlock()

		defer func() {
			c.mu.Lock()
			s.tuples = originalTuples
			_ = c.refreshTuples(ctx, s)
			c.mu.Unlock()
		}()
	}

	subject, err := parseSubject(req.GetUser())
	if err != nil {
		return nil, fmt.Errorf("parsing subject: %w", err)
	}

	checker := melange.NewChecker(c.db)
	ids, err := checker.ListObjects(ctx, subject, melange.Relation(req.GetRelation()), melange.ObjectType(req.GetType()))
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
	c.mu.RLock()
	s, ok := c.stores[req.GetStoreId()]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("store not found: %s", req.GetStoreId())
	}

	// Handle contextual tuples
	// Note: ListUsersRequest.GetContextualTuples() returns []*TupleKey directly, not a wrapper
	if contextualTuples := req.GetContextualTuples(); len(contextualTuples) > 0 {
		c.mu.Lock()
		originalTuples := s.tuples
		s.tuples = append(append([]*openfgav1.TupleKey{}, s.tuples...), contextualTuples...)
		if err := c.refreshTuples(ctx, s); err != nil {
			s.tuples = originalTuples
			c.mu.Unlock()
			return nil, fmt.Errorf("refreshing contextual tuples: %w", err)
		}
		c.mu.Unlock()

		defer func() {
			c.mu.Lock()
			s.tuples = originalTuples
			_ = c.refreshTuples(ctx, s)
			c.mu.Unlock()
		}()
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

		checker := melange.NewChecker(c.db)
		ids, err := checker.ListSubjects(ctx, object, melange.Relation(req.GetRelation()), subjectType)
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
func (c *Client) loadModel(ctx context.Context, s *store, modelID string) error {
	m := s.models[modelID]
	if m == nil {
		return fmt.Errorf("model not found: %s", modelID)
	}

	// Use the Migrator to handle model and closure insertion
	// The empty string for schemasDir is fine since we're using MigrateWithTypes directly
	migrator := melange.NewMigrator(c.db, "")
	return migrator.MigrateWithTypes(ctx, m.types)
}

// refreshTuples updates the melange_tuples view with the current store tuples.
func (c *Client) refreshTuples(ctx context.Context, s *store) error {
	// Drop existing test tuples table if it exists
	_, err := c.db.ExecContext(ctx, "DROP TABLE IF EXISTS melange_test_tuples CASCADE")
	if err != nil {
		return fmt.Errorf("dropping test tuples: %w", err)
	}

	// Create test tuples table
	_, err = c.db.ExecContext(ctx, `
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

		_, err = c.db.ExecContext(ctx, `
			INSERT INTO melange_test_tuples (subject_type, subject_id, relation, object_type, object_id)
			VALUES ($1, $2, $3, $4, $5)
		`, subject.Type, subject.ID, tk.GetRelation(), object.Type, object.ID)
		if err != nil {
			return fmt.Errorf("inserting tuple: %w", err)
		}
	}

	// Create or replace the melange_tuples view
	_, err = c.db.ExecContext(ctx, `
		CREATE OR REPLACE VIEW melange_tuples AS
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM melange_test_tuples
	`)
	if err != nil {
		return fmt.Errorf("creating tuples view: %w", err)
	}

	return nil
}

// convertProtoModel converts a WriteAuthorizationModelRequest to melange TypeDefinitions.
func convertProtoModel(req *openfgav1.WriteAuthorizationModelRequest) []melange.TypeDefinition {
	// Build a fake AuthorizationModel to use the existing converter
	model := &openfgav1.AuthorizationModel{
		SchemaVersion:   req.GetSchemaVersion(),
		TypeDefinitions: req.GetTypeDefinitions(),
		Conditions:      req.GetConditions(),
	}

	return convertAuthzModel(model)
}

// convertAuthzModel converts an OpenFGA AuthorizationModel to melange TypeDefinitions.
// This mirrors the logic in tooling/parser.go but works with protobuf directly.
func convertAuthzModel(model *openfgav1.AuthorizationModel) []melange.TypeDefinition {
	var types []melange.TypeDefinition

	for _, td := range model.GetTypeDefinitions() {
		typeDef := melange.TypeDefinition{
			Name: td.GetType(),
		}

		// Get directly related user types from metadata
		// This extracts both simple type references [user] and userset references [group#member]
		directTypes := make(map[string][]string)
		directTypeRefs := make(map[string][]melange.SubjectTypeRef)
		if meta := td.GetMetadata(); meta != nil {
			for relName, relMeta := range meta.GetRelations() {
				for _, t := range relMeta.GetDirectlyRelatedUserTypes() {
					typeName := t.GetType()
					ref := melange.SubjectTypeRef{Type: typeName}

					switch v := t.GetRelationOrWildcard().(type) {
					case *openfgav1.RelationReference_Wildcard:
						typeName += ":*"
						ref.Wildcard = true
					case *openfgav1.RelationReference_Relation:
						// This is a userset reference like [group#member]
						ref.Relation = v.Relation
					}

					directTypes[relName] = append(directTypes[relName], typeName)
					directTypeRefs[relName] = append(directTypeRefs[relName], ref)
				}
			}
		}

		// Convert relations
		for relName, rel := range td.GetRelations() {
			relDef := convertRelation(relName, rel, directTypes[relName], directTypeRefs[relName])
			typeDef.Relations = append(typeDef.Relations, relDef)
		}

		types = append(types, typeDef)
	}

	return types
}

// convertRelation converts a protobuf Userset to a melange RelationDefinition.
func convertRelation(name string, rel *openfgav1.Userset, subjectTypes []string, subjectTypeRefs []melange.SubjectTypeRef) melange.RelationDefinition {
	relDef := melange.RelationDefinition{
		Name:            name,
		SubjectTypes:    subjectTypes,
		SubjectTypeRefs: subjectTypeRefs,
	}

	extractUserset(rel, &relDef)
	return relDef
}

// extractUserset recursively extracts relation information from a Userset.
func extractUserset(us *openfgav1.Userset, rel *melange.RelationDefinition) {
	if us == nil {
		return
	}

	switch v := us.Userset.(type) {
	case *openfgav1.Userset_This:
		// Direct assignment - subject types handled via metadata

	case *openfgav1.Userset_ComputedUserset:
		rel.ImpliedBy = append(rel.ImpliedBy, v.ComputedUserset.GetRelation())

	case *openfgav1.Userset_TupleToUserset:
		rel.ParentRelation = v.TupleToUserset.GetComputedUserset().GetRelation()
		rel.ParentType = v.TupleToUserset.GetTupleset().GetRelation()

	case *openfgav1.Userset_Union:
		for _, child := range v.Union.GetChild() {
			extractUserset(child, rel)
		}

	case *openfgav1.Userset_Intersection:
		// Intersection: permission granted if ALL children grant it
		// May produce multiple groups due to distributive expansion
		// E.g., "a and (b or c)" expands to [[a,b], [a,c]]
		groups := expandIntersection(v.Intersection, rel.Name)
		for _, group := range groups {
			if len(group.Relations) > 0 {
				rel.IntersectionGroups = append(rel.IntersectionGroups, group)
			}
		}

	case *openfgav1.Userset_Difference:
		extractUserset(v.Difference.GetBase(), rel)
		if subtract := v.Difference.GetSubtract(); subtract != nil {
			if computed := subtract.GetComputedUserset(); computed != nil {
				rel.ExcludedRelation = computed.GetRelation()
			}
		}
	}
}

// expandIntersection expands an intersection node into one or more groups.
// Returns multiple IntersectionGroups when union-in-intersection requires
// distributive expansion: A ∧ (B ∨ C) = (A ∧ B) ∨ (A ∧ C)
func expandIntersection(intersection *openfgav1.Usersets, relationName string) []melange.IntersectionGroup {
	// Start with one empty group
	groups := []melange.IntersectionGroup{{}}

	for _, child := range intersection.GetChild() {
		switch cv := child.Userset.(type) {
		case *openfgav1.Userset_ComputedUserset:
			// Computed userset: add this relation to all existing groups
			rel := cv.ComputedUserset.GetRelation()
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, rel)
			}

		case *openfgav1.Userset_This:
			// Direct assignment within intersection: "[user] and writer"
			// This means "has a direct tuple for THIS relation"
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, relationName)
			}

		case *openfgav1.Userset_TupleToUserset:
			// TTU within intersection
			rel := cv.TupleToUserset.GetComputedUserset().GetRelation()
			for i := range groups {
				groups[i].Relations = append(groups[i].Relations, rel)
			}

		case *openfgav1.Userset_Union:
			// Union within intersection: apply distributive law
			unionRels := extractUnionRelations(cv.Union)
			if len(unionRels) > 0 {
				groups = distributeUnion(groups, unionRels)
			}

		case *openfgav1.Userset_Intersection:
			// Nested intersection: flatten into existing groups
			nestedGroups := expandIntersection(cv.Intersection, relationName)
			if len(nestedGroups) > 0 {
				for i := range groups {
					groups[i].Relations = append(groups[i].Relations, nestedGroups[0].Relations...)
				}
			}
		}
	}

	return groups
}

// extractUnionRelations extracts relation names from a union node.
func extractUnionRelations(union *openfgav1.Usersets) []string {
	var rels []string
	for _, child := range union.GetChild() {
		switch cv := child.Userset.(type) {
		case *openfgav1.Userset_ComputedUserset:
			rels = append(rels, cv.ComputedUserset.GetRelation())
		case *openfgav1.Userset_Union:
			rels = append(rels, extractUnionRelations(cv.Union)...)
		}
	}
	return rels
}

// distributeUnion applies the distributive law.
func distributeUnion(groups []melange.IntersectionGroup, unionRels []string) []melange.IntersectionGroup {
	var expanded []melange.IntersectionGroup
	for _, g := range groups {
		for _, rel := range unionRels {
			newGroup := melange.IntersectionGroup{
				Relations: make([]string, len(g.Relations), len(g.Relations)+1),
			}
			copy(newGroup.Relations, g.Relations)
			newGroup.Relations = append(newGroup.Relations, rel)
			expanded = append(expanded, newGroup)
		}
	}
	return expanded
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
	return c.db
}
