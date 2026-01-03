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
	Name             string                  `json:"name"`
	Tuple            *openfgav1.TupleKey     `json:"tuple"`
	ContextualTuples []*openfgav1.TupleKey   `json:"contextualTuples"`
	Context          *structpb.Struct        `json:"context"`
	Expectation      bool                    `json:"expectation"`
	ErrorCode        int                     `json:"errorCode"`
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
	Request     ListObjectsRequest `json:"request"`
	Expectation []string           `json:"expectation"`
}

// ListObjectsRequest represents a ListObjects request.
type ListObjectsRequest struct {
	User     string `json:"user"`
	Type     string `json:"type"`
	Relation string `json:"relation"`
}

// ListUsersAssertion represents an expected result for ListUsers.
type ListUsersAssertion struct {
	Request     ListUsersRequest `json:"request"`
	Expectation []string         `json:"expectation"`
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
		"tests/abac_tests.yaml",
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
			})
		}
	})
}
