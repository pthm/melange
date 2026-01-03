package openfgatests_test

import (
	"testing"

	_ "github.com/lib/pq" // PostgreSQL driver

	"github.com/pthm/melange/tests/openfgatests"
)

// TestBasicOperations runs basic tests to verify the adapter works correctly.
// These tests validate that the melange implementation correctly handles:
// - Direct tuple checks
// - Role hierarchy (implied relations)
// - Parent inheritance (tuple-to-userset)
//
// Run with: go test -v ./tests/openfgatests -run TestBasicOperations
// Requires: MELANGE_TEST_DB environment variable or local PostgreSQL
func TestBasicOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := openfgatests.SetupTestDB(t)
	client := openfgatests.NewClient(db)
	openfgatests.RunBasicTests(t, client)
}

// TestOpenFGACheckSuite runs the official OpenFGA check test suite.
// This validates melange against the same tests used by the OpenFGA server.
//
// To run this test, you need to:
// 1. Have a PostgreSQL database available
// 2. Set MELANGE_TEST_DB environment variable (optional, defaults to localhost)
// 3. Run: go test -v ./tests/openfgatests -run TestOpenFGACheckSuite
//
// Note: Some tests may fail if melange doesn't support all OpenFGA features.
// This is expected and helps identify gaps in the implementation.
func TestOpenFGACheckSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}

	t.Skip("OpenFGA test suite integration pending - use TestBasicOperations for now")

	// TODO: When ready to run full OpenFGA tests:
	// db := openfgatests.SetupTestDB(t)
	// client := openfgatests.NewClient(db)
	// check.RunAllTests(t, client)
}

// TestOpenFGAListObjectsSuite runs the official OpenFGA list objects test suite.
func TestOpenFGAListObjectsSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}

	t.Skip("OpenFGA test suite integration pending - use TestBasicOperations for now")

	// TODO: When ready to run full OpenFGA tests:
	// db := openfgatests.SetupTestDB(t)
	// client := openfgatests.NewClient(db)
	// listobjects.RunAllTests(t, client)
}

// TestOpenFGAListUsersSuite runs the official OpenFGA list users test suite.
func TestOpenFGAListUsersSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}

	t.Skip("OpenFGA test suite integration pending - use TestBasicOperations for now")

	// TODO: When ready to run full OpenFGA tests:
	// db := openfgatests.SetupTestDB(t)
	// client := openfgatests.NewClient(db)
	// listusers.RunAllTests(t, client)
}
