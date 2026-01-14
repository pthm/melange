package main

import (
	"fmt"
	"sort"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/pkg/testutils"
)

// TestSummary holds aggregate performance metrics for a test.
type TestSummary struct {
	TestName       string
	CheckCount     int
	ListObjsCount  int
	ListUsersCount int
	AvgExecTimeMS  float64
	MaxExecTimeMS  float64
	TotalBuffers   int
	OutlierCount   int
}

// runSummaryMode executes EXPLAIN ANALYZE across all tests matching a pattern
// and displays an aggregated performance summary.
func runSummaryMode(pattern string) error {
	tests, err := loadTestsByPattern(pattern)
	if err != nil {
		return err
	}

	fmt.Printf("Running EXPLAIN ANALYZE on %d tests...\n\n", len(tests))

	summaries := make([]*TestSummary, 0, len(tests))

	for i, tc := range tests {
		fmt.Printf("[%d/%d] Processing %s...\n", i+1, len(tests), tc.Name)
		summary, err := runTestSummary(tc)
		if err != nil {
			fmt.Printf("  Warning: %v\n", err)
			continue
		}
		summaries = append(summaries, summary)
	}

	fmt.Println()

	// Sort by average execution time (descending - slowest first)
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].AvgExecTimeMS > summaries[j].AvgExecTimeMS
	})

	// Calculate median for outlier detection
	median := calculateMedian(summaries)

	// Mark outliers (>2x median)
	for _, s := range summaries {
		if s.AvgExecTimeMS > 2*median && median > 0 {
			s.OutlierCount = 1 // Flag as outlier
		}
	}

	// Print summary table
	printSummaryTable(summaries)

	return nil
}

// runTestSummary executes EXPLAIN ANALYZE on a single test and returns aggregated metrics.
func runTestSummary(tc TestCase) (*TestSummary, error) {
	// Setup database and client
	db, client, cleanup, err := setupTest(tc)
	if err != nil {
		return nil, fmt.Errorf("setup test: %w", err)
	}
	defer cleanup()

	// Process only first stage
	if len(tc.Stages) == 0 {
		return nil, fmt.Errorf("test has no stages")
	}

	stage := tc.Stages[0]

	// Initialize summary
	summary := &TestSummary{
		TestName: tc.Name,
	}

	// Create store and load model (similar to runTest)
	ctx := backgroundContext
	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: tc.Name})
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
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
		return nil, fmt.Errorf("write model: %w", err)
	}

	// Write tuples
	if len(stage.Tuples) > 0 {
		_, err = client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeResp.Id,
			Writes:  &openfgav1.WriteRequestWrites{TupleKeys: stage.Tuples},
		})
		if err != nil {
			return nil, fmt.Errorf("write tuples: %w", err)
		}
	}

	var totalExecTime float64
	var maxExecTime float64
	var totalBuffers int

	// Process check assertions
	opts := Options{Buffers: true, Timing: true}
	for _, assertion := range stage.CheckAssertions {
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainCheckAssertion(ctx, db, summary.CheckCount+1, assertion, opts)
		if err != nil {
			continue // Skip failures in summary mode
		}

		summary.CheckCount++
		totalExecTime += result.Metrics.ExecutionTimeMS
		if result.Metrics.ExecutionTimeMS > maxExecTime {
			maxExecTime = result.Metrics.ExecutionTimeMS
		}
		totalBuffers += result.Metrics.BufferHits
	}

	// Process list_objects assertions
	for _, assertion := range stage.ListObjectsAssertions {
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainListObjectsAssertion(ctx, db, 0, assertion, opts)
		if err != nil {
			continue
		}

		summary.ListObjsCount++
		totalExecTime += result.Metrics.ExecutionTimeMS
		if result.Metrics.ExecutionTimeMS > maxExecTime {
			maxExecTime = result.Metrics.ExecutionTimeMS
		}
		totalBuffers += result.Metrics.BufferHits
	}

	// Process list_users assertions
	for _, assertion := range stage.ListUsersAssertions {
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainListUsersAssertion(ctx, db, 0, assertion, opts)
		if err != nil {
			continue
		}

		summary.ListUsersCount++
		totalExecTime += result.Metrics.ExecutionTimeMS
		if result.Metrics.ExecutionTimeMS > maxExecTime {
			maxExecTime = result.Metrics.ExecutionTimeMS
		}
		totalBuffers += result.Metrics.BufferHits
	}

	// Calculate averages
	totalAssertions := summary.CheckCount + summary.ListObjsCount + summary.ListUsersCount
	if totalAssertions > 0 {
		summary.AvgExecTimeMS = totalExecTime / float64(totalAssertions)
	}
	summary.MaxExecTimeMS = maxExecTime
	summary.TotalBuffers = totalBuffers

	return summary, nil
}

// printSummaryTable prints a formatted table of test summaries.
func printSummaryTable(summaries []*TestSummary) {
	fmt.Printf("%-40s %7s %8s %8s %9s %9s\n",
		"Test", "Checks", "Avg(ms)", "Max(ms)", "Buffers", "Outlier")
	fmt.Println(strings.Repeat("-", 95))

	for _, s := range summaries {
		outlierMarker := ""
		if s.OutlierCount > 0 {
			outlierMarker = "⚠"
		}

		fmt.Printf("%-40s %7d %8.2f %8.2f %9d %9s\n",
			truncate(s.TestName, 40),
			s.CheckCount,
			s.AvgExecTimeMS,
			s.MaxExecTimeMS,
			s.TotalBuffers,
			outlierMarker,
		)
	}

	fmt.Println()
}

// truncate truncates a string to a maximum length.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// calculateMedian calculates the median average execution time.
func calculateMedian(summaries []*TestSummary) float64 {
	if len(summaries) == 0 {
		return 0
	}

	// Extract execution times
	times := make([]float64, len(summaries))
	for i, s := range summaries {
		times[i] = s.AvgExecTimeMS
	}

	// Sort times
	sort.Float64s(times)

	// Calculate median
	mid := len(times) / 2
	if len(times)%2 == 0 {
		return (times[mid-1] + times[mid]) / 2
	}
	return times[mid]
}

