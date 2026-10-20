package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// formatTextOutput formats results as human-readable text.
func formatTextOutput(testName string, results []*ExplainResult) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Test: %s\n", testName)
	sb.WriteString(strings.Repeat("-", len(testName)+6) + "\n\n")

	if len(results) == 0 {
		sb.WriteString("No assertions processed (empty test or all assertions filtered)\n")
		return sb.String()
	}

	currentStage := -1
	for _, r := range results {
		if r.Stage != currentStage {
			currentStage = r.Stage
			fmt.Fprintf(&sb, "\n##### Stage %d #####\n\n", r.Stage)
		}
		fmt.Fprintf(&sb, "=== Assertion %d (%s) ===\n",
			r.AssertionIndex, r.AssertionType)
		fmt.Fprintf(&sb, "Query: %s(%s)\n", r.Query, strings.Join(r.Parameters, ", "))
		fmt.Fprintf(&sb, "Expected: %s\n\n", r.Expected)

		sb.WriteString("Metrics:\n")
		fmt.Fprintf(&sb, "  Execution Time: %.3f ms\n", r.Metrics.ExecutionTimeMS)
		fmt.Fprintf(&sb, "  Planning Time:  %.3f ms\n", r.Metrics.PlanningTimeMS)
		fmt.Fprintf(&sb, "  Buffer Hits:    %d\n", r.Metrics.BufferHits)
		if r.Metrics.BufferReads > 0 {
			fmt.Fprintf(&sb, "  Buffer Reads:   %d\n", r.Metrics.BufferReads)
		}
		fmt.Fprintf(&sb, "  Rows:           %d\n", r.Metrics.Rows)
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
