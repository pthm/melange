package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/pkg/testutils"
)

// ExplainResult holds the results of an EXPLAIN ANALYZE query.
type ExplainResult struct {
	AssertionIndex int         `json:"assertion_index"`
	AssertionType  string      `json:"assertion_type"` // "Check", "ListObjects", "ListUsers"
	Query          string      `json:"query"`
	Parameters     []string    `json:"parameters"`
	Expected       string      `json:"expected"`
	Plan           string      `json:"plan"`
	Metrics        Metrics     `json:"metrics"`
}

// runTest executes EXPLAIN ANALYZE on all assertions in a test case.
func runTest(tc TestCase, opts Options) error {
	// Setup database and client
	db, client, cleanup, err := setupTest(tc)
	if err != nil {
		return fmt.Errorf("setup test: %w", err)
	}
	defer cleanup()

	// Process only first stage (most tests have a single stage)
	if len(tc.Stages) == 0 {
		return fmt.Errorf("test has no stages")
	}

	stage := tc.Stages[0]
	if len(tc.Stages) > 1 {
		fmt.Fprintf(os.Stderr, "Warning: test has %d stages, processing only first stage\n", len(tc.Stages))
	}

	// Create store and load model
	ctx := context.Background()
	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	// Parse model using OpenFGA testutils
	model := testutils.MustTransformDSLToProtoWithID(stage.Model)

	_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeResp.Id,
		SchemaVersion:   model.SchemaVersion,
		TypeDefinitions: model.GetTypeDefinitions(),
		Conditions:      model.GetConditions(),
	})
	if err != nil {
		return fmt.Errorf("write model: %w", err)
	}

	// Write tuples
	if len(stage.Tuples) > 0 {
		_, err = client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeResp.Id,
			Writes:  &openfgav1.WriteRequestWrites{TupleKeys: stage.Tuples},
		})
		if err != nil {
			return fmt.Errorf("write tuples: %w", err)
		}
	}

	// Run EXPLAIN ANALYZE on assertions
	var results []*ExplainResult

	// Check assertions
	for i, assertion := range stage.CheckAssertions {
		// Skip if filtering by assertion index
		if opts.Assertion != -1 && opts.Assertion != i+1 {
			continue
		}

		// Skip error cases
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainCheckAssertion(ctx, db, i+1, assertion, opts)
		if err != nil {
			return fmt.Errorf("explain check assertion %d: %w", i+1, err)
		}
		results = append(results, result)
	}

	// ListObjects assertions
	for i, assertion := range stage.ListObjectsAssertions {
		// Skip if filtering by assertion index
		assertionNum := len(stage.CheckAssertions) + i + 1
		if opts.Assertion != -1 && opts.Assertion != assertionNum {
			continue
		}

		// Skip error cases
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainListObjectsAssertion(ctx, db, assertionNum, assertion, opts)
		if err != nil {
			return fmt.Errorf("explain list_objects assertion %d: %w", assertionNum, err)
		}
		results = append(results, result)
	}

	// ListUsers assertions
	for i, assertion := range stage.ListUsersAssertions {
		// Skip if filtering by assertion index
		assertionNum := len(stage.CheckAssertions) + len(stage.ListObjectsAssertions) + i + 1
		if opts.Assertion != -1 && opts.Assertion != assertionNum {
			continue
		}

		// Skip error cases
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainListUsersAssertion(ctx, db, assertionNum, assertion, opts)
		if err != nil {
			return fmt.Errorf("explain list_users assertion %d: %w", assertionNum, err)
		}
		results = append(results, result)
	}

	// Output results
	switch opts.Format {
	case "json":
		output, err := formatJSONOutput(tc.Name, results)
		if err != nil {
			return fmt.Errorf("format JSON: %w", err)
		}
		fmt.Println(output)
	case "text":
		output := formatTextOutput(tc.Name, results)
		fmt.Print(output)
	default:
		return fmt.Errorf("unknown format: %s", opts.Format)
	}

	return nil
}

// explainCheckAssertion runs EXPLAIN ANALYZE on a Check assertion.
func explainCheckAssertion(ctx context.Context, db *sql.DB, index int, assertion CheckAssertion, opts Options) (*ExplainResult, error) {
	// Parse subject
	subjectType, subjectID := parseEntity(assertion.Tuple.User)

	// Parse object
	objectType, objectID := parseEntity(assertion.Tuple.Object)

	// Build EXPLAIN query with explicit type casts
	explainOpts := buildExplainOptions(opts)
	query := fmt.Sprintf(
		"EXPLAIN (%s) SELECT check_permission($1::TEXT, $2::TEXT, $3::TEXT, $4::TEXT, $5::TEXT)",
		explainOpts,
	)

	// Execute with timeout
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, query,
		subjectType, subjectID, assertion.Tuple.Relation, objectType, objectID)
	if err != nil {
		return nil, fmt.Errorf("execute EXPLAIN: %w", err)
	}
	defer rows.Close()

	// Collect plan lines
	var planLines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("scan plan line: %w", err)
		}
		planLines = append(planLines, line)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plan lines: %w", err)
	}

	plan := strings.Join(planLines, "\n")
	metrics := extractMetrics(plan)

	// Build expected string
	expected := "DENY"
	if assertion.Expectation {
		expected = "ALLOW"
	}

	return &ExplainResult{
		AssertionIndex: index,
		AssertionType:  "Check",
		Query:          "check_permission",
		Parameters: []string{
			subjectType,
			subjectID,
			assertion.Tuple.Relation,
			objectType,
			objectID,
		},
		Expected: expected,
		Plan:     plan,
		Metrics:  metrics,
	}, nil
}

// explainListObjectsAssertion runs EXPLAIN ANALYZE on a ListObjects assertion.
func explainListObjectsAssertion(ctx context.Context, db *sql.DB, index int, assertion ListObjectsAssertion, opts Options) (*ExplainResult, error) {
	// Parse subject
	subjectType, subjectID := parseEntity(assertion.Request.User)

	// Build EXPLAIN query with explicit type casts
	explainOpts := buildExplainOptions(opts)
	query := fmt.Sprintf(
		"EXPLAIN (%s) SELECT * FROM list_accessible_objects($1::TEXT, $2::TEXT, $3::TEXT, $4::TEXT)",
		explainOpts,
	)

	// Execute with timeout
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, query,
		subjectType, subjectID, assertion.Request.Relation, assertion.Request.Type)
	if err != nil {
		return nil, fmt.Errorf("execute EXPLAIN: %w", err)
	}
	defer rows.Close()

	// Collect plan lines
	var planLines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("scan plan line: %w", err)
		}
		planLines = append(planLines, line)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plan lines: %w", err)
	}

	plan := strings.Join(planLines, "\n")
	metrics := extractMetrics(plan)

	// Build expected string
	expected := fmt.Sprintf("%d objects", len(assertion.Expectation))
	if len(assertion.Expectation) == 0 {
		expected = "empty"
	}

	return &ExplainResult{
		AssertionIndex: index,
		AssertionType:  "ListObjects",
		Query:          "list_accessible_objects",
		Parameters: []string{
			subjectType,
			subjectID,
			assertion.Request.Relation,
			assertion.Request.Type,
		},
		Expected: expected,
		Plan:     plan,
		Metrics:  metrics,
	}, nil
}

// explainListUsersAssertion runs EXPLAIN ANALYZE on a ListUsers assertion.
func explainListUsersAssertion(ctx context.Context, db *sql.DB, index int, assertion ListUsersAssertion, opts Options) (*ExplainResult, error) {
	// Parse object
	objectType, objectID := parseEntity(assertion.Request.Object)

	// Filter type (defaults to "user")
	filterType := "user"
	if len(assertion.Request.Filters) > 0 {
		// Parse first filter: "user" or "user:*"
		parts := strings.SplitN(assertion.Request.Filters[0], ":", 2)
		filterType = parts[0]
	}

	// Build EXPLAIN query with explicit type casts
	// Note: list_accessible_subjects takes 6 params (object_type, object_id, relation, subject_type, limit, cursor)
	explainOpts := buildExplainOptions(opts)
	query := fmt.Sprintf(
		"EXPLAIN (%s) SELECT * FROM list_accessible_subjects($1::TEXT, $2::TEXT, $3::TEXT, $4::TEXT, $5::INT, $6::TEXT)",
		explainOpts,
	)

	// Execute with timeout
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, query,
		objectType, objectID, assertion.Request.Relation, filterType, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("execute EXPLAIN: %w", err)
	}
	defer rows.Close()

	// Collect plan lines
	var planLines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("scan plan line: %w", err)
		}
		planLines = append(planLines, line)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plan lines: %w", err)
	}

	plan := strings.Join(planLines, "\n")
	metrics := extractMetrics(plan)

	// Build expected string
	expected := fmt.Sprintf("%d users", len(assertion.Expectation))
	if len(assertion.Expectation) == 0 {
		expected = "empty"
	}

	return &ExplainResult{
		AssertionIndex: index,
		AssertionType:  "ListUsers",
		Query:          "list_accessible_subjects",
		Parameters: []string{
			objectType,
			objectID,
			assertion.Request.Relation,
			filterType,
			"NULL", // limit
			"NULL", // cursor
		},
		Expected: expected,
		Plan:     plan,
		Metrics:  metrics,
	}, nil
}

// buildExplainOptions constructs the EXPLAIN options string.
func buildExplainOptions(opts Options) string {
	var parts []string
	parts = append(parts, "ANALYZE")

	if opts.Buffers {
		parts = append(parts, "BUFFERS")
	}
	if opts.Timing {
		parts = append(parts, "TIMING")
	}
	if opts.Verbose {
		parts = append(parts, "VERBOSE")
	}
	if opts.Settings {
		parts = append(parts, "SETTINGS")
	}
	if opts.WAL {
		parts = append(parts, "WAL")
	}

	// Always include COSTS for completeness
	parts = append(parts, "COSTS")

	return strings.Join(parts, ", ")
}

// parseEntity parses an OpenFGA entity string into type and ID.
// Example: "user:123" -> ("user", "123")
// Example: "group:eng#member" -> ("group", "eng#member")
func parseEntity(entity string) (typ string, id string) {
	parts := strings.SplitN(entity, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "user", entity // Default to user if no type specified
}
