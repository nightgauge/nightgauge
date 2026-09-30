package state

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// GateMetricsName is the gate-metrics log's name inside CHECKOUT
// (.git/nightgauge-worktree/health/gate-metrics.jsonl, ADR-024 § 7).
const GateMetricsName = layout.CheckoutHealth + "/gate-metrics.jsonl"

// AppendGateMetric appends one quality-gate record to the checkout's
// health/gate-metrics.jsonl — the canonical signal the
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

	if _, err := layout.AppendCheckoutFile(workspaceRoot, GateMetricsName, bytes.NewReader(append(line, '\n'))); err != nil {
		return fmt.Errorf("write gate metric: %w", err)
	}
	return nil
}
