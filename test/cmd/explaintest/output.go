package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// formatTextOutput formats results as human-readable text.
func formatTextOutput(testName string, results []*ExplainResult) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Test: %s\n", testName))
	sb.WriteString(strings.Repeat("-", len(testName)+6) + "\n\n")

	if len(results) == 0 {
		sb.WriteString("No assertions processed (empty test or all assertions filtered)\n")
		return sb.String()
	}

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("=== Assertion %d (%s) ===\n",
			r.AssertionIndex, r.AssertionType))
		sb.WriteString(fmt.Sprintf("Query: %s(%s)\n", r.Query, strings.Join(r.Parameters, ", ")))
		sb.WriteString(fmt.Sprintf("Expected: %s\n\n", r.Expected))

		sb.WriteString("Metrics:\n")
		sb.WriteString(fmt.Sprintf("  Execution Time: %.3f ms\n", r.Metrics.ExecutionTimeMS))
		sb.WriteString(fmt.Sprintf("  Planning Time:  %.3f ms\n", r.Metrics.PlanningTimeMS))
		sb.WriteString(fmt.Sprintf("  Buffer Hits:    %d\n", r.Metrics.BufferHits))
		if r.Metrics.BufferReads > 0 {
			sb.WriteString(fmt.Sprintf("  Buffer Reads:   %d\n", r.Metrics.BufferReads))
		}
		sb.WriteString(fmt.Sprintf("  Rows:           %d\n", r.Metrics.Rows))
		sb.WriteString("\n")

		sb.WriteString("Query Plan:\n")
		sb.WriteString(r.Plan)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// formatJSONOutput formats results as JSON.
func formatJSONOutput(testName string, results []*ExplainResult) (string, error) {
	output := struct {
		Test    string           `json:"test"`
		Results []*ExplainResult `json:"results"`
	}{
		Test:    testName,
		Results: results,
	}

	b, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
