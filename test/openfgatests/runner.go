package openfgatests

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/assets"
	"github.com/openfga/openfga/pkg/testutils"
	"github.com/openfga/openfga/pkg/typesystem"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"sigs.k8s.io/yaml"
)

const writeMaxChunkSize = 40

// TestCase represents a single test from the OpenFGA test suite.
type TestCase struct {
	Name   string   `json:"name"`
	Stages []*Stage `json:"stages"`
}

// CheckAssertion represents an expected result for a Check call.
type CheckAssertion struct {
	Name             string                `json:"name"`
	Tuple            *openfgav1.TupleKey   `json:"tuple"`
	ContextualTuples []*openfgav1.TupleKey `json:"contextualTuples"`
	Context          *structpb.Struct      `json:"context"`
	Expectation      bool                  `json:"expectation"`
	ErrorCode        int                   `json:"errorCode"`
}

// Stage represents a stage within a test case.
type Stage struct {
	Name                  string                  `json:"name"`
	Model                 string                  `json:"model"`
	Tuples                []*openfgav1.TupleKey   `json:"tuples"`
	CheckAssertions       []*CheckAssertion       `json:"checkAssertions"`
	ListObjectsAssertions []*ListObjectsAssertion `json:"listObjectsAssertions"`
	ListUsersAssertions   []*ListUsersAssertion   `json:"listUsersAssertions"`
}

// ListObjectsAssertion represents an expected result for ListObjects.
type ListObjectsAssertion struct {
	Request          ListObjectsRequest    `json:"request"`
	ContextualTuples []*openfgav1.TupleKey `json:"contextualTuples"`
	Expectation      []string              `json:"expectation"`
	ErrorCode        int                   `json:"errorCode"`
}

// ListObjectsRequest represents a ListObjects request.
type ListObjectsRequest struct {
	User     string `json:"user"`
	Type     string `json:"type"`
	Relation string `json:"relation"`
}

// ListUsersAssertion represents an expected result for ListUsers.
type ListUsersAssertion struct {
	Request          ListUsersRequest      `json:"request"`
	ContextualTuples []*openfgav1.TupleKey `json:"contextualTuples"`
	Expectation      []string              `json:"expectation"`
	ErrorCode        int                   `json:"errorCode"`
}

// ListUsersRequest represents a ListUsers request.
type ListUsersRequest struct {
	Filters  []string `json:"filters"`
	Object   string   `json:"object"`
	Relation string   `json:"relation"`
}

// testFile represents the structure of the YAML test files.
type testFile struct {
	Tests []TestCase `json:"tests"`
}

// LoadTests loads all test cases from the embedded OpenFGA test files.
func LoadTests() ([]TestCase, error) {
	files := []string{
		"tests/consolidated_1_1_tests.yaml",
		// "tests/abac_tests.yaml", // We do not support ABAC tests yet so this remains commented out
	}

	var allTests []TestCase

	for _, file := range files {
		b, err := assets.EmbedTests.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file, err)
		}

		var tf testFile
		if err := yaml.Unmarshal(b, &tf); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}

		allTests = append(allTests, tf.Tests...)
	}

	return allTests, nil
}

// ListTestNames returns the names of all available tests.
func ListTestNames() ([]string, error) {
	tests, err := LoadTests()
	if err != nil {
		return nil, err
	}

	names := make([]string, len(tests))
	for i, tc := range tests {
		names[i] = tc.Name
	}
	return names, nil
}

// RunAll runs all available tests.
func RunAll(t *testing.T, client *Client) {
	tests, err := LoadTests()
	require.NoError(t, err, "loading tests")
	for _, tc := range tests {
		RunTest(t, client, tc)
	}
}

// RunTestsByPattern runs tests whose names match the given regex pattern.
func RunTestsByPattern(t *testing.T, client *Client, pattern string) {
	re, err := regexp.Compile(pattern)
	require.NoError(t, err, "invalid pattern")

	tests, err := LoadTests()
	require.NoError(t, err, "loading tests")

	var matched int
	for _, tc := range tests {
		if re.MatchString(tc.Name) {
			matched++
			RunTest(t, client, tc)
		}
	}

	if matched == 0 {
		t.Logf("no tests matched pattern %q", pattern)
	} else {
		t.Logf("ran %d tests matching pattern %q", matched, pattern)
	}
}

// RunTestsByNegativePattern runs tests whose names do NOT match the given regex pattern.
func RunTestsByNegativePattern(t *testing.T, client *Client, pattern string) {
	re, err := regexp.Compile(pattern)
	require.NoError(t, err, "invalid pattern")

	tests, err := LoadTests()
	require.NoError(t, err, "loading tests")

	var matched int
	for _, tc := range tests {
		if !re.MatchString(tc.Name) {
			matched++
			RunTest(t, client, tc)
		}
	}

	if matched == 0 {
		t.Logf("no tests matched negative pattern %q", pattern)
	} else {
		t.Logf("ran %d tests not matching pattern %q", matched, pattern)
	}
}

// RunTestByName runs a specific test by exact name.
func RunTestByName(t *testing.T, client *Client, name string) {
	tests, err := LoadTests()
	require.NoError(t, err, "loading tests")

	for _, tc := range tests {
		if tc.Name == name {
			RunTest(t, client, tc)
			return
		}
	}

	t.Fatalf("test %q not found", name)
}

// =============================================================================
// Benchmark Support
// =============================================================================

// BenchmarkResult holds the results of a benchmark run.
type BenchmarkResult struct {
	TestName      string
	CheckCount    int
	ListObjCount  int
	ListUserCount int
}

// BenchTestsByPattern runs benchmarks for tests whose names match the given regex pattern.
func BenchTestsByPattern(b *testing.B, pattern string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		b.Fatalf("invalid pattern: %v", err)
	}

	tests, err := LoadTests()
	if err != nil {
		b.Fatalf("loading tests: %v", err)
	}

	var matched []TestCase
	for _, tc := range tests {
		if re.MatchString(tc.Name) {
			matched = append(matched, tc)
		}
	}

	if len(matched) == 0 {
		b.Skipf("no tests matched pattern %q", pattern)
		return
	}

	for _, tc := range matched {
		b.Run(tc.Name, func(b *testing.B) {
			BenchTest(b, tc)
		})
	}
}

// BenchTestByName runs a benchmark for a specific test by exact name.
func BenchTestByName(b *testing.B, name string) {
	tests, err := LoadTests()
	if err != nil {
		b.Fatalf("loading tests: %v", err)
	}

	for _, tc := range tests {
		if tc.Name == name {
			BenchTest(b, tc)
			return
		}
	}

	b.Fatalf("test %q not found", name)
}

// BenchTest runs a benchmark for a single test case.
// Setup (model + tuples) is done once, then assertions are run b.N times.
func BenchTest(b *testing.B, tc TestCase) {
	// Setup: create client, store, and load all stages
	client := NewClient(b)
	ctx := context.Background()

	resp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
	if err != nil {
		b.Fatalf("create store: %v", err)
	}
	storeID := resp.GetId()

	// Collect all assertions across all stages
	type preparedStage struct {
		modelID           string
		checkAssertions   []*CheckAssertion
		listObjAssertions []*ListObjectsAssertion
		listUserAssertion []*ListUsersAssertion
	}
	stages := make([]preparedStage, 0, len(tc.Stages))

	for _, stage := range tc.Stages {
		// Write model
		model := testutils.MustTransformDSLToProtoWithID(stage.Model)
		writeModelResp, err := client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
			StoreId:         storeID,
			SchemaVersion:   typesystem.SchemaVersion1_1,
			TypeDefinitions: model.GetTypeDefinitions(),
			Conditions:      model.GetConditions(),
		})
		if err != nil {
			b.Fatalf("write model: %v", err)
		}
		modelID := writeModelResp.GetAuthorizationModelId()

		// Write tuples in chunks
		tuples := stage.Tuples
		for i := 0; i < len(tuples); i += writeMaxChunkSize {
			end := int(math.Min(float64(i+writeMaxChunkSize), float64(len(tuples))))
			chunk := tuples[i:end]
			_, err = client.Write(ctx, &openfgav1.WriteRequest{
				StoreId:              storeID,
				AuthorizationModelId: modelID,
				Writes: &openfgav1.WriteRequestWrites{
					TupleKeys: chunk,
				},
			})
			if err != nil {
				b.Fatalf("write tuples: %v", err)
			}
		}

		stages = append(stages, preparedStage{
			modelID:           modelID,
			checkAssertions:   stage.CheckAssertions,
			listObjAssertions: stage.ListObjectsAssertions,
			listUserAssertion: stage.ListUsersAssertions,
		})
	}

	// Count total assertions for reporting
	var totalChecks, totalListObjs, totalListUsers int
	for _, s := range stages {
		totalChecks += len(s.checkAssertions)
		totalListObjs += len(s.listObjAssertions)
		totalListUsers += len(s.listUserAssertion)
	}

	b.ReportMetric(float64(totalChecks), "checks/op")
	b.ReportMetric(float64(totalListObjs), "listobjs/op")
	b.ReportMetric(float64(totalListUsers), "listusers/op")

	// Reset timer after setup
	b.ResetTimer()

	// Run benchmark
	for i := 0; i < b.N; i++ {
		for _, stage := range stages {
			// Run check assertions
			for _, assertion := range stage.checkAssertions {
				if assertion.ErrorCode != 0 {
					continue // Skip error cases in benchmarks
				}

				var tupleKey *openfgav1.CheckRequestTupleKey
				if assertion.Tuple != nil {
					tupleKey = &openfgav1.CheckRequestTupleKey{
						User:     assertion.Tuple.GetUser(),
						Relation: assertion.Tuple.GetRelation(),
						Object:   assertion.Tuple.GetObject(),
					}
				}

				_, err := client.Check(ctx, &openfgav1.CheckRequest{
					StoreId:              storeID,
					AuthorizationModelId: stage.modelID,
					TupleKey:             tupleKey,
					ContextualTuples: &openfgav1.ContextualTupleKeys{
						TupleKeys: assertion.ContextualTuples,
					},
					Context: assertion.Context,
				})
				if err != nil {
					b.Fatalf("check failed: %v", err)
				}
			}

			// Run list objects assertions
			for _, assertion := range stage.listObjAssertions {
				_, err := client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
					StoreId:              storeID,
					AuthorizationModelId: stage.modelID,
					Type:                 assertion.Request.Type,
					Relation:             assertion.Request.Relation,
					User:                 assertion.Request.User,
				})
				if err != nil {
					b.Fatalf("list objects failed: %v", err)
				}
			}

			// Run list users assertions
			for _, assertion := range stage.listUserAssertion {
				var objType, objID string
				for j := 0; j < len(assertion.Request.Object); j++ {
					if assertion.Request.Object[j] == ':' {
						objType = assertion.Request.Object[:j]
						objID = assertion.Request.Object[j+1:]
						break
					}
				}

				var filters []*openfgav1.UserTypeFilter
				for _, f := range assertion.Request.Filters {
					filters = append(filters, &openfgav1.UserTypeFilter{Type: f})
				}

				_, err := client.ListUsers(ctx, &openfgav1.ListUsersRequest{
					StoreId:              storeID,
					AuthorizationModelId: stage.modelID,
					Object: &openfgav1.Object{
						Type: objType,
						Id:   objID,
					},
					Relation:    assertion.Request.Relation,
					UserFilters: filters,
				})
				if err != nil {
					b.Fatalf("list users failed: %v", err)
				}
			}
		}
	}
}

// BenchAllTests runs benchmarks for all OpenFGA tests.
// This is useful for getting a comprehensive performance profile.
func BenchAllTests(b *testing.B) {
	tests, err := LoadTests()
	if err != nil {
		b.Fatalf("loading tests: %v", err)
	}

	for _, tc := range tests {
		b.Run(tc.Name, func(b *testing.B) {
			BenchTest(b, tc)
		})
	}
}

// BenchChecksOnly runs benchmarks focusing only on Check operations.
// This is useful for isolating Check performance from List operations.
func BenchChecksOnly(b *testing.B, pattern string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		b.Fatalf("invalid pattern: %v", err)
	}

	tests, err := LoadTests()
	if err != nil {
		b.Fatalf("loading tests: %v", err)
	}

	// Filter tests that have check assertions
	var matched []TestCase
	for _, tc := range tests {
		if !re.MatchString(tc.Name) {
			continue
		}
		hasChecks := false
		for _, stage := range tc.Stages {
			if len(stage.CheckAssertions) > 0 {
				hasChecks = true
				break
			}
		}
		if hasChecks {
			matched = append(matched, tc)
		}
	}

	if len(matched) == 0 {
		b.Skipf("no tests with checks matched pattern %q", pattern)
		return
	}

	for _, tc := range matched {
		b.Run(tc.Name, func(b *testing.B) {
			benchChecksOnlyForTest(b, tc)
		})
	}
}

// benchChecksOnlyForTest runs only Check assertions for a single test.
func benchChecksOnlyForTest(b *testing.B, tc TestCase) {
	client := NewClient(b)
	ctx := context.Background()

	resp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
	if err != nil {
		b.Fatalf("create store: %v", err)
	}
	storeID := resp.GetId()

	type checkSetup struct {
		modelID    string
		assertions []*CheckAssertion
	}
	var allChecks []checkSetup

	for _, stage := range tc.Stages {
		model := testutils.MustTransformDSLToProtoWithID(stage.Model)
		writeModelResp, err := client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
			StoreId:         storeID,
			SchemaVersion:   typesystem.SchemaVersion1_1,
			TypeDefinitions: model.GetTypeDefinitions(),
			Conditions:      model.GetConditions(),
		})
		if err != nil {
			b.Fatalf("write model: %v", err)
		}
		modelID := writeModelResp.GetAuthorizationModelId()

		tuples := stage.Tuples
		for i := 0; i < len(tuples); i += writeMaxChunkSize {
			end := int(math.Min(float64(i+writeMaxChunkSize), float64(len(tuples))))
			_, err = client.Write(ctx, &openfgav1.WriteRequest{
				StoreId:              storeID,
				AuthorizationModelId: modelID,
				Writes:               &openfgav1.WriteRequestWrites{TupleKeys: tuples[i:end]},
			})
			if err != nil {
				b.Fatalf("write tuples: %v", err)
			}
		}

		// Only collect non-error check assertions
		var checks []*CheckAssertion
		for _, a := range stage.CheckAssertions {
			if a.ErrorCode == 0 {
				checks = append(checks, a)
			}
		}
		if len(checks) > 0 {
			allChecks = append(allChecks, checkSetup{modelID: modelID, assertions: checks})
		}
	}

	var totalChecks int
	for _, cs := range allChecks {
		totalChecks += len(cs.assertions)
	}
	b.ReportMetric(float64(totalChecks), "checks/op")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for _, cs := range allChecks {
			for _, assertion := range cs.assertions {
				var tupleKey *openfgav1.CheckRequestTupleKey
				if assertion.Tuple != nil {
					tupleKey = &openfgav1.CheckRequestTupleKey{
						User:     assertion.Tuple.GetUser(),
						Relation: assertion.Tuple.GetRelation(),
						Object:   assertion.Tuple.GetObject(),
					}
				}

				_, err := client.Check(ctx, &openfgav1.CheckRequest{
					StoreId:              storeID,
					AuthorizationModelId: cs.modelID,
					TupleKey:             tupleKey,
					ContextualTuples: &openfgav1.ContextualTupleKeys{
						TupleKeys: assertion.ContextualTuples,
					},
					Context: assertion.Context,
				})
				if err != nil {
					b.Fatalf("check failed: %v", err)
				}
			}
		}
	}
}

// RunTest runs a single test case with its own isolated database.
// Each test gets a fresh database to enable parallel execution.
func RunTest(t *testing.T, _ *Client, tc TestCase) {
	t.Run(tc.Name, func(t *testing.T) {
		t.Parallel()

		// Create a new client with its own isolated database for this test
		client := NewClient(t)
		ctx := context.Background()

		resp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
		require.NoError(t, err)
		storeID := resp.GetId()

		for stageNum, stage := range tc.Stages {
			stageName := stage.Name
			if stageName == "" {
				stageName = fmt.Sprintf("stage_%d", stageNum)
			}

			t.Run(stageName, func(t *testing.T) {
				// Write model
				model := testutils.MustTransformDSLToProtoWithID(stage.Model)
				writeModelResp, err := client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
					StoreId:         storeID,
					SchemaVersion:   typesystem.SchemaVersion1_1,
					TypeDefinitions: model.GetTypeDefinitions(),
					Conditions:      model.GetConditions(),
				})
				require.NoError(t, err)
				modelID := writeModelResp.GetAuthorizationModelId()

				// Write tuples in chunks
				tuples := stage.Tuples
				for i := 0; i < len(tuples); i += writeMaxChunkSize {
					end := int(math.Min(float64(i+writeMaxChunkSize), float64(len(tuples))))
					chunk := tuples[i:end]
					_, err = client.Write(ctx, &openfgav1.WriteRequest{
						StoreId:              storeID,
						AuthorizationModelId: modelID,
						Writes: &openfgav1.WriteRequestWrites{
							TupleKeys: chunk,
						},
					})
					require.NoError(t, err)
				}

				// Run check assertions
				for i, assertion := range stage.CheckAssertions {
					assertionName := assertion.Name
					if assertionName == "" {
						assertionName = fmt.Sprintf("check_%d", i)
					}

					t.Run(assertionName, func(t *testing.T) {
						var tupleKey *openfgav1.CheckRequestTupleKey
						if assertion.Tuple != nil {
							tupleKey = &openfgav1.CheckRequestTupleKey{
								User:     assertion.Tuple.GetUser(),
								Relation: assertion.Tuple.GetRelation(),
								Object:   assertion.Tuple.GetObject(),
							}
						}

						ctxTuples := assertion.ContextualTuples
						resp, err := client.Check(ctx, &openfgav1.CheckRequest{
							StoreId:              storeID,
							AuthorizationModelId: modelID,
							TupleKey:             tupleKey,
							ContextualTuples: &openfgav1.ContextualTupleKeys{
								TupleKeys: ctxTuples,
							},
							Context: assertion.Context,
						})

						if assertion.ErrorCode == 0 {
							require.NoError(t, err)
							require.Equal(t, assertion.Expectation, resp.GetAllowed(),
								"check %s:%s on %s", tupleKey.GetUser(), tupleKey.GetRelation(), tupleKey.GetObject())
						} else {
							require.Error(t, err)
						}
					})
				}

				// Run list objects assertions
				for i, assertion := range stage.ListObjectsAssertions {
					assertionName := fmt.Sprintf("listobjects_%d", i)

					t.Run(assertionName, func(t *testing.T) {
						resp, err := client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
							StoreId:              storeID,
							AuthorizationModelId: modelID,
							Type:                 assertion.Request.Type,
							Relation:             assertion.Request.Relation,
							User:                 assertion.Request.User,
							ContextualTuples: &openfgav1.ContextualTupleKeys{
								TupleKeys: assertion.ContextualTuples,
							},
						})
						if assertion.ErrorCode == 0 {
							require.NoError(t, err)
							// Sort both for comparison
							got := resp.GetObjects()
							want := assertion.Expectation

							require.ElementsMatch(t, want, got,
								"listobjects user=%s relation=%s type=%s",
								assertion.Request.User, assertion.Request.Relation, assertion.Request.Type)
						} else {
							require.Error(t, err)
						}
					})
				}

				// Run list users assertions
				for i, assertion := range stage.ListUsersAssertions {
					assertionName := fmt.Sprintf("listusers_%d", i)

					t.Run(assertionName, func(t *testing.T) {
						// Parse object
						var objType, objID string
						for j := 0; j < len(assertion.Request.Object); j++ {
							if assertion.Request.Object[j] == ':' {
								objType = assertion.Request.Object[:j]
								objID = assertion.Request.Object[j+1:]
								break
							}
						}

						// Convert filters to UserTypeFilter
						var filters []*openfgav1.UserTypeFilter
						for _, f := range assertion.Request.Filters {
							filters = append(filters, &openfgav1.UserTypeFilter{Type: f})
						}

						resp, err := client.ListUsers(ctx, &openfgav1.ListUsersRequest{
							StoreId:              storeID,
							AuthorizationModelId: modelID,
							Object: &openfgav1.Object{
								Type: objType,
								Id:   objID,
							},
							Relation:         assertion.Request.Relation,
							UserFilters:      filters,
							ContextualTuples: assertion.ContextualTuples,
						})
						if assertion.ErrorCode == 0 {
							require.NoError(t, err)

							// Extract user strings from response
							var got []string
							for _, u := range resp.GetUsers() {
								if obj := u.GetObject(); obj != nil {
									got = append(got, obj.GetType()+":"+obj.GetId())
								}
							}

							want := assertion.Expectation
							client.debugUserset(t, storeID, objType, objID, assertion.Request.Relation, assertion.Request.Filters)
							require.ElementsMatch(t, want, got,
								"listusers object=%s relation=%s filters=%v",
								assertion.Request.Object, assertion.Request.Relation, assertion.Request.Filters)
						} else {
							require.Error(t, err)
						}
					})
				}
			})
		}
	})
}
