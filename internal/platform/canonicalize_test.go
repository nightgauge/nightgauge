package platform

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestCanonicalizeRuns_Issue1014Trio folds the three records the local history
// writes for one logical run (issue 1014) into a single canonical run with the
// terminal outcome, the non-zero cost, the populated stages, and the real
// branch/labels — even though they arrive as a cancelled record, a complete
// record with zero tokens, and a complete "automatic" record with empty stages
// and a synthetic UTC startedAt.
func TestCanonicalizeRuns_Issue1014Trio(t *testing.T) {
	records := []state.V2RunRecord{
		// (a) cancelled headless record — full stages + tokens, interim outcome.
		{
			SchemaVersion: "2",
			RecordType:    "run",
			IssueNumber:   1014,
			Branch:        "feat/1014-thing",
			Labels:        []string{"type:feature"},
			ExecutionMode: "headless",
			StartedAt:     "2026-03-15T04:00:00-06:00", // == 10:00:00Z
			CompletedAt:   "2026-03-15T04:30:00-06:00",
			TotalDuration: 1800000,
			Outcome:       "cancelled",
			Stages: map[string]state.V2StageDetail{
				"issue-pickup":     {Status: "complete"},
				"feature-planning": {Status: "complete"},
				"feature-dev":      {Status: "complete"},
			},
			Tokens:     state.V2Tokens{EstimatedCostUSD: 1.23},
			RecordedAt: "2026-03-15T04:30:00-06:00",
		},
		// (b) complete headless record — full stages, ZERO tokens, later outcome.
		{
			SchemaVersion: "2",
			RecordType:    "run",
			IssueNumber:   1014,
			Branch:        "feat/1014-thing",
			Labels:        []string{"type:feature"},
			ExecutionMode: "headless",
			StartedAt:     "2026-03-15T04:00:00-06:00", // same instant
			CompletedAt:   "2026-03-15T04:35:00-06:00",
			TotalDuration: 2100000,
			Outcome:       "complete",
			Stages: map[string]state.V2StageDetail{
				"issue-pickup":     {Status: "complete"},
				"feature-planning": {Status: "complete"},
				"feature-dev":      {Status: "complete"},
				"feature-validate": {Status: "complete"},
			},
			Tokens:     state.V2Tokens{EstimatedCostUSD: 0},
			RecordedAt: "2026-03-15T04:35:00-06:00",
		},
		// (c) complete automatic record — empty stages, tokens populated,
		// synthetic UTC startedAt (same absolute instant), latest recorded_at.
		{
			SchemaVersion: "2",
			RecordType:    "run",
			IssueNumber:   1014,
			Branch:        "",
			ExecutionMode: "automatic",
			StartedAt:     "2026-03-15T10:00:00Z", // same instant, UTC-synthetic
			CompletedAt:   "2026-03-15T10:35:00Z",
			TotalDuration: 0,
			Outcome:       "complete",
			Stages:        map[string]state.V2StageDetail{},
			Tokens:        state.V2Tokens{EstimatedCostUSD: 1.50},
			RecordedAt:    "2026-03-15T10:36:00Z",
		},
	}

	out, res := CanonicalizeRuns(records)

	if res.Input != 3 {
		t.Errorf("Input = %d, want 3", res.Input)
	}
	if res.Groups != 1 {
		t.Errorf("Groups = %d, want 1 (the trio folds to one run)", res.Groups)
	}
	if res.DroppedNoise != 0 {
		t.Errorf("DroppedNoise = %d, want 0", res.DroppedNoise)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}

	run := out[0]
	if run.IssueNumber != 1014 {
		t.Errorf("IssueNumber = %d, want 1014", run.IssueNumber)
	}
	if run.Outcome != "complete" {
		t.Errorf("Outcome = %q, want complete (terminal outcome wins over cancelled)", run.Outcome)
	}
	if run.Tokens.EstimatedCostUSD != 1.50 {
		t.Errorf("EstimatedCostUSD = %v, want 1.50 (max cost across group)", run.Tokens.EstimatedCostUSD)
	}
	if run.TotalDuration != 2100000 {
		t.Errorf("TotalDuration = %d, want 2100000 (max duration)", run.TotalDuration)
	}
	if len(run.Stages) != 4 {
		t.Errorf("Stages = %d entries, want 4 (member with most stages wins)", len(run.Stages))
	}
	if run.Branch != "feat/1014-thing" {
		t.Errorf("Branch = %q, want feat/1014-thing (first non-empty)", run.Branch)
	}
	if len(run.Labels) != 1 || run.Labels[0] != "type:feature" {
		t.Errorf("Labels = %v, want [type:feature]", run.Labels)
	}
	if run.ExecutionMode != "headless" {
		t.Errorf("ExecutionMode = %q, want headless (real mode preferred over automatic)", run.ExecutionMode)
	}
}

// TestCanonicalizeRuns_DropsPureNoise drops a zero-everything record and keeps a
// real cancelled run.
func TestCanonicalizeRuns_DropsPureNoise(t *testing.T) {
	records := []state.V2RunRecord{
		// Pure noise: no stages, no cost, no duration.
		{
			SchemaVersion: "2",
			IssueNumber:   100,
			StartedAt:     "2026-04-01T10:00:00Z",
			Outcome:       "complete",
			Stages:        map[string]state.V2StageDetail{},
		},
		// Real cancelled run: has stages, cost, duration.
		{
			SchemaVersion: "2",
			IssueNumber:   200,
			StartedAt:     "2026-04-02T10:00:00Z",
			TotalDuration: 600000,
			Outcome:       "cancelled",
			Stages:        map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}},
			Tokens:        state.V2Tokens{EstimatedCostUSD: 0.42},
		},
	}

	out, res := CanonicalizeRuns(records)

	if res.DroppedNoise != 1 {
		t.Errorf("DroppedNoise = %d, want 1", res.DroppedNoise)
	}
	if res.Merged != 1 || len(out) != 1 {
		t.Fatalf("Merged = %d, len(out) = %d, want 1 each", res.Merged, len(out))
	}
	if out[0].IssueNumber != 200 {
		t.Errorf("kept issue %d, want 200 (the real cancelled run)", out[0].IssueNumber)
	}
	if out[0].Outcome != "cancelled" {
		t.Errorf("Outcome = %q, want cancelled (cancelled runs are real history)", out[0].Outcome)
	}
}

// TestCanonicalizeRuns_ParseSkip counts records whose StartedAt does not parse
// and excludes them from the output.
func TestCanonicalizeRuns_ParseSkip(t *testing.T) {
	records := []state.V2RunRecord{
		{IssueNumber: 1, StartedAt: "not-a-time", Stages: map[string]state.V2StageDetail{"x": {Status: "complete"}}},
		{
			IssueNumber:   2,
			StartedAt:     "2026-04-03T10:00:00Z",
			TotalDuration: 1000,
			Stages:        map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}},
		},
	}

	out, res := CanonicalizeRuns(records)

	if res.ParseSkipped != 1 {
		t.Errorf("ParseSkipped = %d, want 1", res.ParseSkipped)
	}
	if len(out) != 1 || out[0].IssueNumber != 2 {
		t.Fatalf("expected only issue 2 to survive, got %+v", out)
	}
}

// TestCanonicalizeRuns_Deterministic ensures output ordering and merge results
// do not depend on map iteration order — repeated calls yield identical output.
func TestCanonicalizeRuns_Deterministic(t *testing.T) {
	records := []state.V2RunRecord{
		{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", TotalDuration: 1, Stages: map[string]state.V2StageDetail{"a": {Status: "complete"}}},
		{IssueNumber: 2, StartedAt: "2026-04-01T11:00:00Z", TotalDuration: 1, Stages: map[string]state.V2StageDetail{"b": {Status: "complete"}}},
		{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", TotalDuration: 2, Outcome: "complete", Stages: map[string]state.V2StageDetail{"a": {Status: "complete"}, "c": {Status: "complete"}}},
	}

	out1, _ := CanonicalizeRuns(records)
	out2, _ := CanonicalizeRuns(records)

	if len(out1) != len(out2) {
		t.Fatalf("non-deterministic length: %d vs %d", len(out1), len(out2))
	}
	for i := range out1 {
		if out1[i].IssueNumber != out2[i].IssueNumber || out1[i].Outcome != out2[i].Outcome || len(out1[i].Stages) != len(out2[i].Stages) {
			t.Errorf("non-deterministic output at %d: %+v vs %+v", i, out1[i], out2[i])
		}
	}
}
