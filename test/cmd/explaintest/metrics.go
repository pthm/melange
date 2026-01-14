package main

import (
	"regexp"
	"strconv"
)

// Metrics holds performance metrics extracted from EXPLAIN ANALYZE output.
type Metrics struct {
	ExecutionTimeMS float64 `json:"execution_time_ms"`
	PlanningTimeMS  float64 `json:"planning_time_ms"`
	BufferHits      int     `json:"buffer_hits"`
	BufferReads     int     `json:"buffer_reads"`
	Rows            int     `json:"rows"`
}

var (
	execTimeRe  = regexp.MustCompile(`Execution Time: ([\d.]+) ms`)
	planTimeRe  = regexp.MustCompile(`Planning Time: ([\d.]+) ms`)
	buffersRe   = regexp.MustCompile(`Buffers: shared hit=(\d+)(?: read=(\d+))?`)
	rowsRe      = regexp.MustCompile(`rows=(\d+)`)
)

// extractMetrics extracts performance metrics from an EXPLAIN ANALYZE plan.
func extractMetrics(plan string) Metrics {
	var m Metrics

	// Extract execution time
	if match := execTimeRe.FindStringSubmatch(plan); match != nil {
		m.ExecutionTimeMS, _ = strconv.ParseFloat(match[1], 64)
	}

	// Extract planning time
	if match := planTimeRe.FindStringSubmatch(plan); match != nil {
		m.PlanningTimeMS, _ = strconv.ParseFloat(match[1], 64)
	}

	// Extract buffer statistics
	if match := buffersRe.FindStringSubmatch(plan); match != nil {
		m.BufferHits, _ = strconv.Atoi(match[1])
		if len(match) > 2 && match[2] != "" {
			m.BufferReads, _ = strconv.Atoi(match[2])
		}
	}

	// Extract row count (find the first occurrence)
	if match := rowsRe.FindStringSubmatch(plan); match != nil {
		m.Rows, _ = strconv.Atoi(match[1])
	}

	return m
}
