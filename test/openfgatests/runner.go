package openfgatests

import (
	"context"
	"embed"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/assets"
	"github.com/openfga/openfga/pkg/testutils"
	"github.com/openfga/openfga/pkg/typesystem"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"sigs.k8s.io/yaml"
)

//go:embed testdata/*.yaml
var localTests embed.FS

const writeMaxChunkSize = 40

// bulkEligibleAssertions returns check assertions that can be evaluated via
// check_permission_bulk: those without error codes or contextual tuples.
func bulkEligibleAssertions(assertions []*CheckAssertion) []*CheckAssertion {
	var eligible []*CheckAssertion
	for _, a := range assertions {
		if a.ErrorCode == 0 && len(a.ContextualTuples) == 0 {
			eligible = append(eligible, a)
		}
	}
	return eligible
}

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

// loadTestsFrom reads a YAML test file from fs and returns its test cases.
// If prefix is non-empty, each test name is prefixed with it.
func loadTestsFrom(fs embed.FS, path, prefix string) ([]TestCase, error) {
	b, err := fs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var tf testFile
	if err := yaml.Unmarshal(b, &tf); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if prefix != "" {
		for i := range tf.Tests {
			tf.Tests[i].Name = prefix + tf.Tests[i].Name
		}
	}

	return tf.Tests, nil
}

// LoadTests loads all test cases from the embedded OpenFGA test files
// and any local melange-specific YAML test files in testdata/.
func LoadTests() ([]TestCase, error) {
	// 1. Official OpenFGA tests
	officialFiles := []string{
		"tests/consolidated_1_1_tests.yaml",
		// "tests/abac_tests.yaml", // We do not support ABAC tests yet so this remains commented out
	}

	var allTests []TestCase

	for _, file := range officialFiles {
		tests, err := loadTestsFrom(assets.EmbedTests, file, "")
		if err != nil {
			return nil, err
		}
		allTests = append(allTests, tests...)
	}

	// 2. Local melange-specific tests (prefixed with "melange/")
	localEntries, err := localTests.ReadDir("testdata")
	if err != nil {
		return nil, fmt.Errorf("reading local testdata: %w", err)
	}

	for _, entry := range localEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		tests, err := loadTestsFrom(localTests, "testdata/"+entry.Name(), "melange/")
		if err != nil {
			return nil, err
		}
		allTests = append(allTests, tests...)
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

// BenchTest runs a benchmark for a single test case with separate sub-benchmarks
// for Check and List operations. Setup (model + tuples) is done once, then each
// operation type gets its own measured benchmark.
func BenchTest(b *testing.B, tc TestCase) {
	// Setup: create client, store, and load all stages
	client := NewClient(b)
	ctx := context.Background()

	resp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
	if err != nil {
		b.Fatalf("create store: %v", err)
	}
	storeID := resp.GetId()

	// Prepared operations ready for benchmarking
	type checkOp struct {
		modelID   string
		assertion *CheckAssertion
	}
	type listObjOp struct {
		modelID   string
		assertion *ListObjectsAssertion
	}
	type listUserOp struct {
		modelID   string
		assertion *ListUsersAssertion
	}

	var checkOps []checkOp
	var listObjOps []listObjOp
	var listUserOps []listUserOp

	// Setup all stages
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

		// Write tuples in chunks
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

		// Collect operations (skip error cases)
		for _, a := range stage.CheckAssertions {
			if a.ErrorCode == 0 {
				checkOps = append(checkOps, checkOp{modelID: modelID, assertion: a})
			}
		}
		for _, a := range stage.ListObjectsAssertions {
			if a.ErrorCode == 0 {
				listObjOps = append(listObjOps, listObjOp{modelID: modelID, assertion: a})
			}
		}
		for _, a := range stage.ListUsersAssertions {
			if a.ErrorCode == 0 {
				listUserOps = append(listUserOps, listUserOp{modelID: modelID, assertion: a})
			}
		}
	}

	// Benchmark Check operations
	if len(checkOps) > 0 {
		b.Run("Check", func(b *testing.B) {
			for i, op := range checkOps {
				name := fmt.Sprintf("%d", i)
				if op.assertion.Name != "" {
					name = op.assertion.Name
				}
				b.Run(name, func(b *testing.B) {
					var tupleKey *openfgav1.CheckRequestTupleKey
					if op.assertion.Tuple != nil {
						tupleKey = &openfgav1.CheckRequestTupleKey{
							User:     op.assertion.Tuple.GetUser(),
							Relation: op.assertion.Tuple.GetRelation(),
							Object:   op.assertion.Tuple.GetObject(),
						}
					}

					b.ResetTimer()
					for j := 0; j < b.N; j++ {
						_, err := client.Check(ctx, &openfgav1.CheckRequest{
							StoreId:              storeID,
							AuthorizationModelId: op.modelID,
							TupleKey:             tupleKey,
							ContextualTuples: &openfgav1.ContextualTupleKeys{
								TupleKeys: op.assertion.ContextualTuples,
							},
							Context: op.assertion.Context,
						})
						if err != nil {
							b.Fatalf("check failed: %v", err)
						}
					}
				})
			}
		})
	}

	// Benchmark BulkCheck operations
	if len(checkOps) > 0 {
		allCheckAssertions := make([]*CheckAssertion, 0, len(checkOps))
		for _, op := range checkOps {
			allCheckAssertions = append(allCheckAssertions, op.assertion)
		}
		bulkAssertions := bulkEligibleAssertions(allCheckAssertions)
		if len(bulkAssertions) > 0 {
			b.Run("BulkCheck", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := client.CheckBulk(ctx, storeID, bulkAssertions)
					if err != nil {
						b.Fatalf("bulk check failed: %v", err)
					}
				}
			})
		}
	}

	// Benchmark ListObjects operations
	if len(listObjOps) > 0 {
		b.Run("ListObjects", func(b *testing.B) {
			for i, op := range listObjOps {
				b.Run(fmt.Sprintf("%d", i), func(b *testing.B) {
					b.ResetTimer()
					for j := 0; j < b.N; j++ {
						_, err := client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
							StoreId:              storeID,
							AuthorizationModelId: op.modelID,
							Type:                 op.assertion.Request.Type,
							Relation:             op.assertion.Request.Relation,
							User:                 op.assertion.Request.User,
							ContextualTuples: &openfgav1.ContextualTupleKeys{
								TupleKeys: op.assertion.ContextualTuples,
							},
						})
						if err != nil {
							b.Fatalf("list objects failed: %v", err)
						}
					}
				})
			}
		})
	}

	// Benchmark ListUsers operations
	if len(listUserOps) > 0 {
		b.Run("ListUsers", func(b *testing.B) {
			for i, op := range listUserOps {
				b.Run(fmt.Sprintf("%d", i), func(b *testing.B) {
					var objType, objID string
					for j := 0; j < len(op.assertion.Request.Object); j++ {
						if op.assertion.Request.Object[j] == ':' {
							objType = op.assertion.Request.Object[:j]
							objID = op.assertion.Request.Object[j+1:]
							break
						}
					}

					filters := make([]*openfgav1.UserTypeFilter, 0, len(op.assertion.Request.Filters))
					for _, f := range op.assertion.Request.Filters {
						filters = append(filters, &openfgav1.UserTypeFilter{Type: f})
					}

					b.ResetTimer()
					for j := 0; j < b.N; j++ {
						_, err := client.ListUsers(ctx, &openfgav1.ListUsersRequest{
							StoreId:              storeID,
							AuthorizationModelId: op.modelID,
							Object: &openfgav1.Object{
								Type: objType,
								Id:   objID,
							},
							Relation:         op.assertion.Request.Relation,
							UserFilters:      filters,
							ContextualTuples: op.assertion.ContextualTuples,
						})
						if err != nil {
							b.Fatalf("list users failed: %v", err)
						}
					}
				})
			}
		})
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

// RunTest runs a single test case with its own isolated database.
// Each test gets a fresh database to enable parallel execution.
// The passed client's DatabaseSchema is propagated to the per-test client.
func RunTest(t *testing.T, parent *Client, tc TestCase) {
	t.Run(tc.Name, func(t *testing.T) {
		t.Parallel()

		// Create a new client with its own isolated database for this test,
		// preserving the schema configuration from the parent client.
		client := NewClientWithSchema(t, parent.DatabaseSchema())
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

				// Run bulk check assertions
				t.Run("bulk_check", func(t *testing.T) {
					bulkAssertions := bulkEligibleAssertions(stage.CheckAssertions)
					if len(bulkAssertions) == 0 {
						t.Skip("no assertions eligible for bulk check")
					}
					results, err := client.CheckBulk(ctx, storeID, bulkAssertions)
					require.NoError(t, err)
					for i, a := range bulkAssertions {
						name := a.Name
						if name == "" {
							name = fmt.Sprintf("check_%d", i)
						}
						require.Equal(t, a.Expectation, results[i],
							"bulk check mismatch for %s (user=%s relation=%s object=%s)",
							name, a.Tuple.GetUser(), a.Tuple.GetRelation(), a.Tuple.GetObject())
					}
				})

				// Run list objects assertions
				for i, assertion := range stage.ListObjectsAssertions {
					assertionName := fmt.Sprintf("listobjects_%d", i)

					t.Run(assertionName, func(t *testing.T) {
						// Contextual tuples with list operations are not supported with custom schemas:
						// generated SQL functions use schema-qualified melange_tuples references,
						// which bypass the temp view shadow created by contextual tuples.
						if len(assertion.ContextualTuples) > 0 && client.DatabaseSchema() != "" {
							t.Skipf("contextual tuples with list operations not supported with custom schema %q", client.DatabaseSchema())
						}

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

						// Contextual tuples with list operations are not supported with custom schemas:
						// generated SQL functions use schema-qualified melange_tuples references,
						// which bypass the temp view shadow created by contextual tuples.
						if len(assertion.ContextualTuples) > 0 && client.DatabaseSchema() != "" {
							t.Skipf("contextual tuples with list operations not supported with custom schema %q", client.DatabaseSchema())
						}

						// Convert filters to UserTypeFilter
						filters := make([]*openfgav1.UserTypeFilter, 0, len(assertion.Request.Filters))
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

				// Run derived list objects assertions (generated from check assertions)
				runDerivedListObjectsAssertions(t, ctx, client, storeID, modelID, stage.CheckAssertions)

				// Run derived list users assertions (generated from check assertions)
				runDerivedListUsersAssertions(t, ctx, client, storeID, modelID, stage.CheckAssertions)
			})
		}
	})
}
