package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AppendGateMetric appends one quality-gate record to
// .nightgauge/health/gate-metrics.jsonl — the canonical signal the
// deterministic FeatureValidateGate already consumes
// (ReadGateMetricsForIssue).
//
// This is the writer side the feature-validate adversarial-review phase uses
// (#4097): the LLM critics run as a skill preflight and record their verdict
// (result "pass" or "catch") here, so a "catch" trips validation through the
// existing gate. The gate itself stays pure (no LLM, no network) — the
// non-deterministic judgment arrives via this artifact, following the
// "network/LLM checks are NOT StageGates" precedent in docs/STAGE_GATES.md.
//
// timestamp is supplied by the caller (the CLI, an I/O boundary) so this
// function never reads the clock and stays deterministic for tests.
func AppendGateMetric(workspaceRoot string, issueNumber int, gateName, result, errorSummary, timestamp string) error {
	if result != "pass" && result != "catch" {
		return fmt.Errorf("gate metric result must be \"pass\" or \"catch\", got %q", result)
	}
	if gateName == "" {
		return fmt.Errorf("gate metric requires a non-empty gate name")
	}

	dir := filepath.Join(workspaceRoot, ".nightgauge", "health")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create health dir: %w", err)
	}

	rec := gateMetricRecord{
		SchemaVersion: "1.0",
		Timestamp:     timestamp,
		IssueNumber:   issueNumber,
		GateName:      gateName,
		Result:        result,
	}
	if errorSummary != "" {
		rec.ErrorSummary = &errorSummary
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal gate metric: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "gate-metrics.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open gate-metrics.jsonl: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write gate metric: %w", err)
	}
	return nil
}
