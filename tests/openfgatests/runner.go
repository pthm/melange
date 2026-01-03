package openfgatests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

// SetupTestDB creates a test database connection and initializes the melange schema.
// It reads the database URL from the MELANGE_TEST_DB environment variable.
// If not set, it falls back to a local PostgreSQL connection.
//
// The function also installs the melange infrastructure (model table, functions).
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbURL := os.Getenv("MELANGE_TEST_DB")
	if dbURL == "" {
		dbURL = "postgres://localhost:5432/melange_test?sslmode=disable"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}

	// Initialize melange schema
	if err := initializeSchema(db); err != nil {
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	t.Cleanup(func() {
		// Clean up test data
		ctx := context.Background()
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS melange_test_tuples CASCADE")
		_, _ = db.ExecContext(ctx, "DELETE FROM melange_model")
		db.Close()
	})

	return db
}

// initializeSchema creates the melange infrastructure in the test database.
func initializeSchema(db *sql.DB) error {
	ctx := context.Background()

	// Create melange_model table
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS melange_model (
			id BIGSERIAL PRIMARY KEY,
			object_type VARCHAR NOT NULL,
			relation VARCHAR NOT NULL,
			subject_type VARCHAR,
			implied_by VARCHAR,
			parent_relation VARCHAR,
			excluded_relation VARCHAR
		)
	`)
	if err != nil {
		return fmt.Errorf("creating melange_model: %w", err)
	}

	// Create indexes
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_melange_model_lookup
		ON melange_model (object_type, relation)`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_melange_model_implied
		ON melange_model (object_type, relation, implied_by) WHERE implied_by IS NOT NULL`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_melange_model_parent
		ON melange_model (object_type, relation, parent_relation) WHERE parent_relation IS NOT NULL`)

	// Create check_permission function
	_, err = db.ExecContext(ctx, checkPermissionSQL)
	if err != nil {
		return fmt.Errorf("creating check_permission: %w", err)
	}

	// Create list_accessible_objects function
	_, err = db.ExecContext(ctx, listAccessibleObjectsSQL)
	if err != nil {
		return fmt.Errorf("creating list_accessible_objects: %w", err)
	}

	// Create list_accessible_subjects function
	_, err = db.ExecContext(ctx, listAccessibleSubjectsSQL)
	if err != nil {
		return fmt.Errorf("creating list_accessible_subjects: %w", err)
	}

	// Create empty tuples view (will be replaced during tests)
	_, err = db.ExecContext(ctx, `
		CREATE OR REPLACE VIEW melange_tuples AS
		SELECT ''::TEXT as subject_type, ''::TEXT as subject_id,
		       ''::TEXT as relation, ''::TEXT as object_type, ''::TEXT as object_id
		WHERE false
	`)
	if err != nil {
		return fmt.Errorf("creating empty tuples view: %w", err)
	}

	return nil
}

const checkPermissionSQL = `
CREATE OR REPLACE FUNCTION check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
DECLARE
    v_found INTEGER := 0;
    v_parent_type TEXT;
    v_parent_id TEXT;
    v_parent_rel TEXT;
    v_excluded_rel TEXT;
    v_implied RECORD;
BEGIN
    -- 1. Direct tuple check (including wildcard matching)
    SELECT 1 INTO v_found
    FROM melange_tuples t
    WHERE t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
      AND t.relation = p_relation
      AND t.object_type = p_object_type
      AND t.object_id = p_object_id
    LIMIT 1;

    IF v_found = 1 THEN
        RETURN 1;
    END IF;

    -- 2. Check implied relations (role hierarchy)
    FOR v_implied IN
        SELECT am.implied_by, am.excluded_relation
        FROM melange_model am
        WHERE am.object_type = p_object_type
          AND am.relation = p_relation
          AND am.implied_by IS NOT NULL
          AND am.parent_relation IS NULL
    LOOP
        IF check_permission(p_subject_type, p_subject_id, v_implied.implied_by, p_object_type, p_object_id) = 1 THEN
            IF v_implied.excluded_relation IS NOT NULL THEN
                IF check_permission(p_subject_type, p_subject_id, v_implied.excluded_relation, p_object_type, p_object_id) = 1 THEN
                    CONTINUE;
                END IF;
            END IF;
            RETURN 1;
        END IF;
    END LOOP;

    -- 3. Check parent relations (inheritance from parent object)
    FOR v_parent_type, v_parent_id, v_parent_rel, v_excluded_rel IN
        SELECT t.subject_type, t.subject_id, am.parent_relation, am.excluded_relation
        FROM melange_tuples t
        JOIN melange_model am
          ON am.object_type = p_object_type
         AND am.relation = p_relation
         AND am.parent_relation IS NOT NULL
         AND t.relation = am.subject_type
        WHERE t.object_type = p_object_type
          AND t.object_id = p_object_id
    LOOP
        IF check_permission(p_subject_type, p_subject_id, v_parent_rel, v_parent_type, v_parent_id) = 1 THEN
            IF v_excluded_rel IS NOT NULL THEN
                IF check_permission(p_subject_type, p_subject_id, v_excluded_rel, p_object_type, p_object_id) = 1 THEN
                    CONTINUE;
                END IF;
            END IF;
            RETURN 1;
        END IF;
    END LOOP;

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;
`

const listAccessibleObjectsSQL = `
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = p_object_type
      AND check_permission(p_subject_type, p_subject_id, p_relation, p_object_type, t.object_id) = 1;
END;
$$ LANGUAGE plpgsql STABLE;
`

const listAccessibleSubjectsSQL = `
CREATE OR REPLACE FUNCTION list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT DISTINCT t.subject_id
    FROM melange_tuples t
    WHERE t.subject_type = p_subject_type
      AND check_permission(p_subject_type, t.subject_id, p_relation, p_object_type, p_object_id) = 1;
END;
$$ LANGUAGE plpgsql STABLE;
`

// RunBasicTests runs a basic set of tests to verify the adapter works correctly.
// This is useful for quick validation before running the full OpenFGA test suite.
func RunBasicTests(t *testing.T, client *Client) {
	ctx := context.Background()

	t.Run("basic check", func(t *testing.T) {
		// Create store
		storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
			Name: "test",
		})
		if err != nil {
			t.Fatalf("CreateStore failed: %v", err)
		}
		storeID := storeResp.GetId()

		// Parse DSL to model
		model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
`)
		if err != nil {
			t.Fatalf("Failed to parse DSL: %v", err)
		}

		// Write model
		_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
			StoreId:         storeID,
			TypeDefinitions: model.GetTypeDefinitions(),
			SchemaVersion:   model.GetSchemaVersion(),
			Conditions:      model.GetConditions(),
		})
		if err != nil {
			t.Fatalf("WriteAuthorizationModel failed: %v", err)
		}

		// Write tuples
		_, err = client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeID,
			Writes: &openfgav1.WriteRequestWrites{
				TupleKeys: []*openfgav1.TupleKey{
					{User: "user:alice", Relation: "viewer", Object: "document:1"},
				},
			},
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		// Check - should allow
		checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				User:     "user:alice",
				Relation: "viewer",
				Object:   "document:1",
			},
		})
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !checkResp.GetAllowed() {
			t.Error("Expected allowed=true, got false")
		}

		// Check - should deny
		checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				User:     "user:bob",
				Relation: "viewer",
				Object:   "document:1",
			},
		})
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if checkResp.GetAllowed() {
			t.Error("Expected allowed=false, got true")
		}
	})

	t.Run("role hierarchy", func(t *testing.T) {
		storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
			Name: "hierarchy",
		})
		if err != nil {
			t.Fatalf("CreateStore failed: %v", err)
		}
		storeID := storeResp.GetId()

		model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`)
		if err != nil {
			t.Fatalf("Failed to parse DSL: %v", err)
		}

		_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
			StoreId:         storeID,
			TypeDefinitions: model.GetTypeDefinitions(),
			SchemaVersion:   model.GetSchemaVersion(),
			Conditions:      model.GetConditions(),
		})
		if err != nil {
			t.Fatalf("WriteAuthorizationModel failed: %v", err)
		}

		_, err = client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeID,
			Writes: &openfgav1.WriteRequestWrites{
				TupleKeys: []*openfgav1.TupleKey{
					{User: "user:owner", Relation: "owner", Object: "document:1"},
					{User: "user:editor", Relation: "editor", Object: "document:1"},
				},
			},
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		// Owner should have viewer permission (via owner -> editor -> viewer)
		checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				User:     "user:owner",
				Relation: "viewer",
				Object:   "document:1",
			},
		})
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !checkResp.GetAllowed() {
			t.Error("Owner should have viewer permission through hierarchy")
		}

		// Editor should have viewer permission
		checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				User:     "user:editor",
				Relation: "viewer",
				Object:   "document:1",
			},
		})
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !checkResp.GetAllowed() {
			t.Error("Editor should have viewer permission through hierarchy")
		}
	})

	t.Run("parent inheritance", func(t *testing.T) {
		storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
			Name: "inheritance",
		})
		if err != nil {
			t.Fatalf("CreateStore failed: %v", err)
		}
		storeID := storeResp.GetId()

		model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type org
  relations
    define member: [user]

type repo
  relations
    define org: [org]
    define reader: [user] or member from org
`)
		if err != nil {
			t.Fatalf("Failed to parse DSL: %v", err)
		}

		_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
			StoreId:         storeID,
			TypeDefinitions: model.GetTypeDefinitions(),
			SchemaVersion:   model.GetSchemaVersion(),
			Conditions:      model.GetConditions(),
		})
		if err != nil {
			t.Fatalf("WriteAuthorizationModel failed: %v", err)
		}

		_, err = client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeID,
			Writes: &openfgav1.WriteRequestWrites{
				TupleKeys: []*openfgav1.TupleKey{
					{User: "user:alice", Relation: "member", Object: "org:acme"},
					{User: "org:acme", Relation: "org", Object: "repo:code"},
				},
			},
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		// Alice should have reader on repo through org membership
		checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				User:     "user:alice",
				Relation: "reader",
				Object:   "repo:code",
			},
		})
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !checkResp.GetAllowed() {
			t.Error("Alice should have reader permission through org membership")
		}

		// Bob should not have access
		checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID,
			TupleKey: &openfgav1.CheckRequestTupleKey{
				User:     "user:bob",
				Relation: "reader",
				Object:   "repo:code",
			},
		})
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if checkResp.GetAllowed() {
			t.Error("Bob should not have reader permission")
		}
	})
}
