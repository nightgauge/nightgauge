package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// gateMetricRecord is the raw JSONL record format written by the TypeScript
// feature-validate stage. Mirrors GateMetricRecordSchema from
// packages/nightgauge-vscode/src/schemas/gateMetrics.ts.
type gateMetricRecord struct {
	SchemaVersion   string  `json:"schema_version"`
	Timestamp       string  `json:"timestamp"`
	IssueNumber     int     `json:"issue_number"`
	GateName        string  `json:"gate_name"`
	Result          string  `json:"result"`
	IssueType       *string `json:"issue_type"`
	ComplexityLabel *string `json:"complexity_label"`
	DurationMs      *int64  `json:"duration_ms"`
	ErrorSummary    *string `json:"error_summary"`
}

// ReadGateMetricsForIssue reads gate-metrics.jsonl and returns GateResult records
// matching the given issue number. Returns nil, nil if the file does not exist.
// Malformed lines are skipped. Errors during reading log a warning and return
// the records collected so far.
func ReadGateMetricsForIssue(workspaceRoot string, issueNumber int) ([]GateResult, error) {
	filePath := filepath.Join(workspaceRoot, ".nightgauge", "health", "gate-metrics.jsonl")

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open gate-metrics.jsonl: %w", err)
	}
	defer f.Close()

	var results []GateResult
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw gateMetricRecord
		if err := json.Unmarshal(line, &raw); err != nil {
			// Skip malformed lines
			continue
		}
		if raw.IssueNumber != issueNumber {
			continue
		}

		gr := GateResult{
			GateName:  raw.GateName,
			Result:    raw.Result,
			Timestamp: raw.Timestamp,
		}
		if raw.DurationMs != nil {
			gr.DurationMs = *raw.DurationMs
		}
		if raw.ErrorSummary != nil {
			gr.ErrorSummary = *raw.ErrorSummary
		}
		results = append(results, gr)
	}

	if err := scanner.Err(); err != nil {
		return results, fmt.Errorf("scan gate-metrics.jsonl: %w", err)
	}

	return results, nil
}
