package pipeline

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestAggregate_RecoveryEmptyForRunsWithoutEvents asserts that runs lacking
// recovery_events produce a zero-value RecoveryAggregate (no panic, no NaN).
func TestAggregate_RecoveryEmptyForRunsWithoutEvents(t *testing.T) {
	records := []state.V2RunRecord{
		fixtureRecord(1, "2026-05-01T00:00:00Z", "2026-05-01T00:00:00Z"),
		fixtureRecord(2, "2026-05-01T00:00:00Z", "2026-05-01T00:00:00Z"),
	}
	res, _ := Aggregate(records, Options{})
	if res.Recovery.TotalEvents != 0 {
		t.Fatalf("expected TotalEvents=0, got %d", res.Recovery.TotalEvents)
	}
	if res.Recovery.RecoveryRate != 0 {
		t.Fatalf("expected RecoveryRate=0, got %f", res.Recovery.RecoveryRate)
	}
	if res.Recovery.ByAction == nil || res.Recovery.ByErrorKind == nil {
		t.Fatalf("expected non-nil maps even when empty")
	}
}

// TestAggregate_RecoveryRateAndBreakdown asserts the rate, run count, and
// per-action/per-error-kind breakdowns match the input fixtures.
func TestAggregate_RecoveryRateAndBreakdown(t *testing.T) {
	r1 := fixtureRecord(1, "2026-05-01T00:00:00Z", "2026-05-01T00:00:00Z")
	r1.RecoveryEvents = []state.RecoveryEvent{
		{IssueNumber: 1, ErrorKind: "MISSING_INPUT_FILE", Action: "restart-from-beginning"},
	}
	r2 := fixtureRecord(2, "2026-05-01T00:00:00Z", "2026-05-01T00:00:00Z")
	r2.RecoveryEvents = []state.RecoveryEvent{
		{IssueNumber: 2, ErrorKind: "MISSING_INPUT_FILE", Action: "run-producing-stage"},
		{IssueNumber: 2, ErrorKind: "CONTEXT_SCHEMA_ERROR", Action: "discard-run"},
	}
	r3 := fixtureRecord(3, "2026-05-01T00:00:00Z", "2026-05-01T00:00:00Z")
	// no events

	records := []state.V2RunRecord{r1, r2, r3}
	res, _ := Aggregate(records, Options{})

	if res.Recovery.RunsWithEvents != 2 {
		t.Fatalf("expected RunsWithEvents=2, got %d", res.Recovery.RunsWithEvents)
	}
	if res.Recovery.TotalEvents != 3 {
		t.Fatalf("expected TotalEvents=3, got %d", res.Recovery.TotalEvents)
	}
	// 2 / 3 = 0.6667 (rounded to 4)
	if res.Recovery.RecoveryRate < 0.66 || res.Recovery.RecoveryRate > 0.67 {
		t.Fatalf("expected RecoveryRate≈0.6667, got %f", res.Recovery.RecoveryRate)
	}
	if res.Recovery.ByAction["restart-from-beginning"] != 1 {
		t.Fatalf("expected by_action[restart-from-beginning]=1, got %d", res.Recovery.ByAction["restart-from-beginning"])
	}
	if res.Recovery.ByAction["run-producing-stage"] != 1 {
		t.Fatalf("expected by_action[run-producing-stage]=1, got %d", res.Recovery.ByAction["run-producing-stage"])
	}
	if res.Recovery.ByErrorKind["MISSING_INPUT_FILE"] != 2 {
		t.Fatalf("expected by_error_kind[MISSING_INPUT_FILE]=2, got %d", res.Recovery.ByErrorKind["MISSING_INPUT_FILE"])
	}
}
