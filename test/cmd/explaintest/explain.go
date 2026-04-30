package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/pkg/testutils"
	"google.golang.org/grpc"
)

// ExplainResult holds the results of an EXPLAIN ANALYZE query.
type ExplainResult struct {
	Stage          int      `json:"stage"`           // 1-based index of the stage that produced the result
	AssertionIndex int      `json:"assertion_index"` // 1-based index within the stage
	AssertionType  string   `json:"assertion_type"`  // "Check", "ListObjects", "ListUsers"
	Query          string   `json:"query"`
	Parameters     []string `json:"parameters"`
	Expected       string   `json:"expected"`
	Plan           string   `json:"plan"`
	Metrics        Metrics  `json:"metrics"`
}

// openfgaClient is the subset of the OpenFGA client used by runTest. Defined
// as an interface so loadStage can be unit-tested without standing up a real
// gRPC server, and so the dependency surface is explicit.
type openfgaClient interface {
	CreateStore(context.Context, *openfgav1.CreateStoreRequest, ...grpc.CallOption) (*openfgav1.CreateStoreResponse, error)
	WriteAuthorizationModel(context.Context, *openfgav1.WriteAuthorizationModelRequest, ...grpc.CallOption) (*openfgav1.WriteAuthorizationModelResponse, error)
	Write(context.Context, *openfgav1.WriteRequest, ...grpc.CallOption) (*openfgav1.WriteResponse, error)
}

// runTest executes EXPLAIN ANALYZE on all assertions across every stage of a
// test case. Stage 1's assertions appear first in the output, and the Stage
// field on each ExplainResult identifies which stage produced it.
//
// OpenFGA test stages share a store but each stage writes its own model and
// tuples; subsequent stages overlay the previous model rather than replacing
// the underlying tuple set. We mirror the openfgatests runner's behavior
// here so EXPLAIN sees the same data layout the integration tests do.
func runTest(tc TestCase, opts Options) error {
	db, client, cleanup, err := setupTest(tc)
	if err != nil {
		return fmt.Errorf("setup test: %w", err)
	}
	defer cleanup()

	if len(tc.Stages) == 0 {
		return fmt.Errorf("test has no stages")
	}

	ctx := context.Background()
	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}
	storeID := storeResp.GetId()

	if opts.Stage != -1 && (opts.Stage < 1 || opts.Stage > len(tc.Stages)) {
		return fmt.Errorf("stage %d out of range (test has %d stages)", opts.Stage, len(tc.Stages))
	}

	var results []*ExplainResult

	// Per-stage assertion indexing keeps cursor-style output stable: each
	// stage starts numbering at 1 so users can reference assertion 1 of
	// stage 3 without doing arithmetic against earlier stages.
	for stageNum, stage := range tc.Stages {
		oneBasedStage := stageNum + 1
		if opts.Stage != -1 && opts.Stage != oneBasedStage {
			continue
		}

		if err := loadStage(ctx, client, storeID, stage); err != nil {
			return fmt.Errorf("stage %d: %w", oneBasedStage, err)
		}

		stageResults, err := runStage(ctx, db, oneBasedStage, stage, opts)
		if err != nil {
			return err
		}
		results = append(results, stageResults...)
	}

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

// loadStage writes the stage's model and tuples to the store.
func loadStage(ctx context.Context, client openfgaClient, storeID string, stage Stage) error {
	model := testutils.MustTransformDSLToProtoWithID(stage.Model)
	if _, err := client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeID,
		SchemaVersion:   model.SchemaVersion,
		TypeDefinitions: model.GetTypeDefinitions(),
		Conditions:      model.GetConditions(),
	}); err != nil {
		return fmt.Errorf("write model: %w", err)
	}

	if len(stage.Tuples) > 0 {
		if _, err := client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeID,
			Writes:  &openfgav1.WriteRequestWrites{TupleKeys: stage.Tuples},
		}); err != nil {
			return fmt.Errorf("write tuples: %w", err)
		}
	}
	return nil
}

// runStage runs EXPLAIN ANALYZE for every assertion in the stage. The
// assertion index restarts at 1 per stage; the Stage field on each result
// identifies which stage produced it.
func runStage(ctx context.Context, db *sql.DB, stageNum int, stage Stage, opts Options) ([]*ExplainResult, error) {
	var results []*ExplainResult

	for i, assertion := range stage.CheckAssertions {
		if opts.Assertion != -1 && opts.Assertion != i+1 {
			continue
		}
		if assertion.ErrorCode != 0 {
			continue
		}
		result, err := explainCheckAssertion(ctx, db, i+1, assertion, opts)
		if err != nil {
			return nil, fmt.Errorf("stage %d check assertion %d: %w", stageNum, i+1, err)
		}
		result.Stage = stageNum
		results = append(results, result)
	}

	for i, assertion := range stage.ListObjectsAssertions {
		assertionNum := len(stage.CheckAssertions) + i + 1
		if opts.Assertion != -1 && opts.Assertion != assertionNum {
			continue
		}
		if assertion.ErrorCode != 0 {
			continue
		}
		result, err := explainListObjectsAssertion(ctx, db, assertionNum, assertion, opts)
		if err != nil {
			return nil, fmt.Errorf("stage %d list_objects assertion %d: %w", stageNum, assertionNum, err)
		}
		result.Stage = stageNum
		results = append(results, result)
	}

	for i, assertion := range stage.ListUsersAssertions {
		assertionNum := len(stage.CheckAssertions) + len(stage.ListObjectsAssertions) + i + 1
		if opts.Assertion != -1 && opts.Assertion != assertionNum {
			continue
		}
		if assertion.ErrorCode != 0 {
			continue
		}
		result, err := explainListUsersAssertion(ctx, db, assertionNum, assertion, opts)
		if err != nil {
			return nil, fmt.Errorf("stage %d list_users assertion %d: %w", stageNum, assertionNum, err)
		}
		result.Stage = stageNum
		results = append(results, result)
	}

	return results, nil
}

// runExplain executes an EXPLAIN query, joining the plan output and extracting
// metrics. The query template should contain a single %s for the EXPLAIN options.
func runExplain(ctx context.Context, db *sql.DB, opts Options, queryTemplate string, args ...any) (string, Metrics, error) {
	query := fmt.Sprintf(queryTemplate, buildExplainOptions(opts))

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", Metrics{}, fmt.Errorf("execute EXPLAIN: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var planLines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", Metrics{}, fmt.Errorf("scan plan line: %w", err)
		}
		planLines = append(planLines, line)
	}
	if err := rows.Err(); err != nil {
		return "", Metrics{}, fmt.Errorf("iterate plan lines: %w", err)
	}

	plan := strings.Join(planLines, "\n")
	return plan, extractMetrics(plan), nil
}

// explainCheckAssertion runs EXPLAIN ANALYZE on a Check assertion.
func explainCheckAssertion(ctx context.Context, db *sql.DB, index int, assertion CheckAssertion, opts Options) (*ExplainResult, error) {
	subjectType, subjectID := parseEntity(assertion.Tuple.User)
	objectType, objectID := parseEntity(assertion.Tuple.Object)

	plan, metrics, err := runExplain(ctx, db, opts,
		"EXPLAIN (%s) SELECT check_permission($1::TEXT, $2::TEXT, $3::TEXT, $4::TEXT, $5::TEXT)",
		subjectType, subjectID, assertion.Tuple.Relation, objectType, objectID,
	)
	if err != nil {
		return nil, err
	}

	expected := "DENY"
	if assertion.Expectation {
		expected = "ALLOW"
	}

	return &ExplainResult{
		AssertionIndex: index,
		AssertionType:  "Check",
		Query:          "check_permission",
		Parameters:     []string{subjectType, subjectID, assertion.Tuple.Relation, objectType, objectID},
		Expected:       expected,
		Plan:           plan,
		Metrics:        metrics,
	}, nil
}

// explainListObjectsAssertion runs EXPLAIN ANALYZE on a ListObjects assertion.
func explainListObjectsAssertion(ctx context.Context, db *sql.DB, index int, assertion ListObjectsAssertion, opts Options) (*ExplainResult, error) {
	subjectType, subjectID := parseEntity(assertion.Request.User)

	plan, metrics, err := runExplain(ctx, db, opts,
		"EXPLAIN (%s) SELECT * FROM list_accessible_objects($1::TEXT, $2::TEXT, $3::TEXT, $4::TEXT)",
		subjectType, subjectID, assertion.Request.Relation, assertion.Request.Type,
	)
	if err != nil {
		return nil, err
	}

	expected := fmt.Sprintf("%d objects", len(assertion.Expectation))
	if len(assertion.Expectation) == 0 {
		expected = "empty"
	}

	return &ExplainResult{
		AssertionIndex: index,
		AssertionType:  "ListObjects",
		Query:          "list_accessible_objects",
		Parameters:     []string{subjectType, subjectID, assertion.Request.Relation, assertion.Request.Type},
		Expected:       expected,
		Plan:           plan,
		Metrics:        metrics,
	}, nil
}

// explainListUsersAssertion runs EXPLAIN ANALYZE on a ListUsers assertion.
func explainListUsersAssertion(ctx context.Context, db *sql.DB, index int, assertion ListUsersAssertion, opts Options) (*ExplainResult, error) {
	objectType, objectID := parseEntity(assertion.Request.Object)

	// Filter type (defaults to "user"). First filter is "user" or "user:*".
	filterType := "user"
	if len(assertion.Request.Filters) > 0 {
		parts := strings.SplitN(assertion.Request.Filters[0], ":", 2)
		filterType = parts[0]
	}

	// list_accessible_subjects takes 6 params (object_type, object_id, relation, subject_type, limit, cursor).
	plan, metrics, err := runExplain(ctx, db, opts,
		"EXPLAIN (%s) SELECT * FROM list_accessible_subjects($1::TEXT, $2::TEXT, $3::TEXT, $4::TEXT, $5::INT, $6::TEXT)",
		objectType, objectID, assertion.Request.Relation, filterType, nil, nil,
	)
	if err != nil {
		return nil, err
	}

	expected := fmt.Sprintf("%d users", len(assertion.Expectation))
	if len(assertion.Expectation) == 0 {
		expected = "empty"
	}

	return &ExplainResult{
		AssertionIndex: index,
		AssertionType:  "ListUsers",
		Query:          "list_accessible_subjects",
		Parameters:     []string{objectType, objectID, assertion.Request.Relation, filterType, "NULL", "NULL"},
		Expected:       expected,
		Plan:           plan,
		Metrics:        metrics,
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
func parseEntity(entity string) (typ, id string) {
	parts := strings.SplitN(entity, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "user", entity // Default to user if no type specified
}
