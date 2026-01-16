package main

import (
	"fmt"
	"sort"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/pkg/testutils"
)

// EndpointMetrics holds performance metrics for a specific endpoint type.
type EndpointMetrics struct {
	Count         int
	AvgExecTimeMS float64
	MaxExecTimeMS float64
	TotalBuffers  int
}

// TestSummary holds aggregate performance metrics for a test, split by endpoint type.
type TestSummary struct {
	TestName     string
	Check        EndpointMetrics
	ListObjects  EndpointMetrics
	ListSubjects EndpointMetrics
	OutlierCount int
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

	// Sort by overall average execution time (descending - slowest first)
	sort.Slice(summaries, func(i, j int) bool {
		return getOverallAvgTime(summaries[i]) > getOverallAvgTime(summaries[j])
	})

	// Calculate median for outlier detection
	median := calculateMedian(summaries)

	// Mark outliers (>2x median)
	for _, s := range summaries {
		if getOverallAvgTime(s) > 2*median && median > 0 {
			s.OutlierCount = 1 // Flag as outlier
		}
	}

	// Print summary table
	printSummaryTable(summaries)

	return nil
}

// getOverallAvgTime calculates the overall average execution time across all endpoint types.
func getOverallAvgTime(s *TestSummary) float64 {
	totalTime := s.Check.AvgExecTimeMS*float64(s.Check.Count) +
		s.ListObjects.AvgExecTimeMS*float64(s.ListObjects.Count) +
		s.ListSubjects.AvgExecTimeMS*float64(s.ListSubjects.Count)
	totalCount := s.Check.Count + s.ListObjects.Count + s.ListSubjects.Count
	if totalCount == 0 {
		return 0
	}
	return totalTime / float64(totalCount)
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

	// Separate metrics for each endpoint type
	var checkExecTime, listObjsExecTime, listUsersExecTime float64
	var checkBuffers, listObjsBuffers, listUsersBuffers int

	// Process check assertions
	opts := Options{Buffers: true, Timing: true}
	for _, assertion := range stage.CheckAssertions {
		if assertion.ErrorCode != 0 {
			continue
		}

		result, err := explainCheckAssertion(ctx, db, summary.Check.Count+1, assertion, opts)
		if err != nil {
			continue // Skip failures in summary mode
		}

		summary.Check.Count++
		checkExecTime += result.Metrics.ExecutionTimeMS
		if result.Metrics.ExecutionTimeMS > summary.Check.MaxExecTimeMS {
			summary.Check.MaxExecTimeMS = result.Metrics.ExecutionTimeMS
		}
		checkBuffers += result.Metrics.BufferHits
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

		summary.ListObjects.Count++
		listObjsExecTime += result.Metrics.ExecutionTimeMS
		if result.Metrics.ExecutionTimeMS > summary.ListObjects.MaxExecTimeMS {
			summary.ListObjects.MaxExecTimeMS = result.Metrics.ExecutionTimeMS
		}
		listObjsBuffers += result.Metrics.BufferHits
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

		summary.ListSubjects.Count++
		listUsersExecTime += result.Metrics.ExecutionTimeMS
		if result.Metrics.ExecutionTimeMS > summary.ListSubjects.MaxExecTimeMS {
			summary.ListSubjects.MaxExecTimeMS = result.Metrics.ExecutionTimeMS
		}
		listUsersBuffers += result.Metrics.BufferHits
	}

	// Calculate averages for each endpoint type
	if summary.Check.Count > 0 {
		summary.Check.AvgExecTimeMS = checkExecTime / float64(summary.Check.Count)
		summary.Check.TotalBuffers = checkBuffers
	}
	if summary.ListObjects.Count > 0 {
		summary.ListObjects.AvgExecTimeMS = listObjsExecTime / float64(summary.ListObjects.Count)
		summary.ListObjects.TotalBuffers = listObjsBuffers
	}
	if summary.ListSubjects.Count > 0 {
		summary.ListSubjects.AvgExecTimeMS = listUsersExecTime / float64(summary.ListSubjects.Count)
		summary.ListSubjects.TotalBuffers = listUsersBuffers
	}

	return summary, nil
}

// printSummaryTable prints a formatted table of test summaries with split metrics.
func printSummaryTable(summaries []*TestSummary) {
	// Header
	fmt.Println("Performance Summary by Endpoint Type")
	fmt.Println()

	// Column headers
	fmt.Printf("%-40s | %-30s | %-30s | %-30s | %7s\n",
		"Test",
		"Check",
		"ListObjects",
		"ListSubjects",
		"Outlier",
	)
	fmt.Printf("%-40s | %5s %8s %8s %6s | %5s %8s %8s %6s | %5s %8s %8s %6s | %7s\n",
		"",
		"Cnt", "Avg(ms)", "Max(ms)", "Bufs",
		"Cnt", "Avg(ms)", "Max(ms)", "Bufs",
		"Cnt", "Avg(ms)", "Max(ms)", "Bufs",
		"",
	)
	fmt.Println(strings.Repeat("-", 180))

	for _, s := range summaries {
		outlierMarker := ""
		if s.OutlierCount > 0 {
			outlierMarker = "⚠"
		}

		// Format each endpoint's metrics
		checkStr := formatEndpointMetrics(s.Check)
		listObjsStr := formatEndpointMetrics(s.ListObjects)
		listSubjectsStr := formatEndpointMetrics(s.ListSubjects)

		fmt.Printf("%-40s | %30s | %30s | %30s | %7s\n",
			truncate(s.TestName, 40),
			checkStr,
			listObjsStr,
			listSubjectsStr,
			outlierMarker,
		)
	}

	fmt.Println()
}

// formatEndpointMetrics formats endpoint metrics into a string.
func formatEndpointMetrics(m EndpointMetrics) string {
	if m.Count == 0 {
		return fmt.Sprintf("%5s %8s %8s %6s", "-", "-", "-", "-")
	}
	return fmt.Sprintf("%5d %8.2f %8.2f %6d",
		m.Count,
		m.AvgExecTimeMS,
		m.MaxExecTimeMS,
		m.TotalBuffers,
	)
}

// truncate truncates a string to a maximum length.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// calculateMedian calculates the median overall average execution time.
func calculateMedian(summaries []*TestSummary) float64 {
	if len(summaries) == 0 {
		return 0
	}

	// Extract overall execution times
	times := make([]float64, len(summaries))
	for i, s := range summaries {
		times[i] = getOverallAvgTime(s)
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
