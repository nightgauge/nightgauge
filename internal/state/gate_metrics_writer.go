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

// GateMetricsHistoryName is the cross-run gate-metrics log's name inside CLONE
// logs (<git-common-dir>/nightgauge/logs/gate-metrics.jsonl). Every record goes
// to both destinations, and each reader reads exactly one:
//
//   - CHECKOUT GateMetricsName is the in-run gate input. It is per-checkout, so a
//     pipeline worktree's records vanish with the worktree, and a retry in a
//     fresh worktree never sees an earlier run's "catch".
//   - CLONE logs GateMetricsHistoryName is the cross-run history that
//     `nightgauge health gate-metrics` reads. Every linked worktree resolves it
//     to the main clone, so it survives worktree cleanup (#2535).
const GateMetricsHistoryName = "gate-metrics.jsonl"

// AppendGateMetric appends one quality-gate record to the checkout's
// health/gate-metrics.jsonl — the canonical signal the
// deterministic FeatureValidateGate already consumes
// (ReadGateMetricsForIssue) — and then to the clone's cross-run history
// (GateMetricsHistoryName). CHECKOUT is written first, so a failed history
// write never leaves the gate input missing.
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

	line = append(line, '\n')
	if _, err := layout.AppendCheckoutFile(workspaceRoot, GateMetricsName, bytes.NewReader(line)); err != nil {
		return fmt.Errorf("write gate metric to checkout: %w", err)
	}
	if _, err := layout.AppendClassFile(workspaceRoot, layout.ClassLogs, GateMetricsHistoryName, bytes.NewReader(line)); err != nil {
		return fmt.Errorf("write gate metric to clone history: %w", err)
	}
	return nil
}
