package openfgatests_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/test/openfgatests"
)

// =============================================================================
// Supported Feature Tests
// These test patterns cover features that Melange fully supports.
// See docs/openfga-support.md for the full feature matrix.
// =============================================================================

func TestOpenFGA_All(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunAll(t, client)
}

// TestOpenFGA_DirectAssignment tests direct relation assignment [user].
// This is the most basic pattern: explicitly granting a relation via tuples.
func TestOpenFGA_DirectAssignment(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "this")
}

// TestOpenFGA_ComputedUserset tests computed relations (role hierarchy via implied_by).
// Pattern: define admin: [user] or owner (owner implies admin)
func TestOpenFGA_ComputedUserset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "computed_userset|computeduserset")
}

// TestOpenFGA_TupleToUserset tests parent inheritance (FROM pattern).
// Pattern: define can_read: can_read from org
func TestOpenFGA_TupleToUserset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "tuple_to_userset|ttu_")
}

// TestOpenFGA_Wildcards tests public access via wildcards [user:*].
// Pattern: define public: [user:*]
func TestOpenFGA_Wildcards(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "wildcard|public")
}

// TestOpenFGA_Exclusion tests the BUT NOT pattern.
// Pattern: define can_review: can_read but not author
func TestOpenFGA_Exclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "exclusion|butnot|but_not")
}

// TestOpenFGA_Union tests the OR pattern.
// Pattern: define viewer: [user] or editor or admin
func TestOpenFGA_Union(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "union")
}

// TestOpenFGA_Intersection tests the AND pattern.
// Pattern: define viewer: [user] and editor and admin
func TestOpenFGA_Intersection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "intersection")
}

// TestOpenFGA_UsersetReferences tests userset references [type#relation].
// Pattern: define viewer: [group#member] means members of groups can be viewers.
func TestOpenFGA_UsersetReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "userset")
}

// TestOpenFGA_CycleHandling tests that cycles are handled correctly.
func TestOpenFGA_CycleHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "cycle|recursive")
}

// TestOpenFGA_ContextualTuples tests contextual tuples (temporary tuples in requests).
// Contextual tuples are not persisted but are evaluated as part of the check request.
func TestOpenFGA_ContextualTuples(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "contextual")
}

// TestOpenFGA_Validation tests validation of policies.
func TestOpenFGA_Validation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, "validation|invalid|error|err_")
}

// =============================================================================
// Helper Tests
// =============================================================================

// TestOpenFGAByName runs a specific test by exact name.
// Use: OPENFGA_TEST_NAME=wildcard_direct go test -run TestOpenFGAByName ./openfgatests/...
// Or:  just test-openfga-name wildcard_direct
func TestOpenFGAByName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	name := os.Getenv("OPENFGA_TEST_NAME")
	if name == "" {
		name = "this" // default test
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestByName(t, client, name)
}

// TestOpenFGAByPattern runs tests matching a regex pattern.
// Use: OPENFGA_TEST_PATTERN="^wildcard" go test -run TestOpenFGAByPattern ./openfgatests/...
// Or:  just test-openfga-pattern "^wildcard"
func TestOpenFGAByPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pattern := os.Getenv("OPENFGA_TEST_PATTERN")
	if pattern == "" {
		pattern = "^computed_userset$" // default pattern
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, pattern)
}

// TestOpenFGAListAvailableTests prints all available test names (useful for discovery).
func TestOpenFGAListAvailableTests(t *testing.T) {
	names, err := openfgatests.ListTestNames()
	require.NoError(t, err)

	t.Logf("Available OpenFGA tests (%d total):", len(names))
	for _, name := range names {
		t.Logf("  - %s", name)
	}
}
