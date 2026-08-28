package state

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// #1055 (symptom a) — phase telemetry existed only in the transient runtime
// snapshot.
//
// V2StageDetail had no phase field and BuildV2Record never read
// snap.PhaseHistory, so every phase-level observation — which phases ran, in
// what order, which were skipped, which never completed — was discarded when
// the run ended. Verified across the workspace: 40 history files, zero phase
// records, while the only holder (runtime-<issue>-<runId>.json) is removed at
// terminal state.
//
// That absence is what let a phase sit "running" past the end of its stage
// unnoticed, and is why diagnosing a phase-level defect required a human
// watching the GUI live.
func TestBuildV2Record_CarriesPhaseRecordsIntoTheDurableRecord(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1055, "item-phases", testRunID())

	start := time.Now().Add(-10 * time.Minute)
	done := start.Add(30 * time.Second)

	rs.BeginStage(StageFeaturePlanning)
	rs.PhaseHistory = []PhaseRecord{
		// Deliberately out of arrival order: markers can arrive out of order and
		// the INDEX is the authoritative position (#1008).
		{Stage: StageFeaturePlanning, Name: "documentation-analysis", Index: 6, Total: 14, Status: "complete", StartedAt: start, CompletedAt: &done},
		{Stage: StageFeaturePlanning, Name: "load-context", Index: 1, Total: 14, Status: "complete", StartedAt: start, CompletedAt: &done},
		{Stage: StageFeaturePlanning, Name: "knowledge-base-read", Index: 7, Total: 14, Status: "skipped", StartedAt: start},
		{Stage: StageFeaturePlanning, Name: "self-assessment", Index: 13, Total: 14, Status: "abandoned", StartedAt: start},
	}
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 50}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "phases", Branch: "feat/1055"}, time.Now())

	detail, ok := rec.Stages[string(StageFeaturePlanning)]
	if !ok {
		t.Fatalf("no stage detail for feature-planning; stages = %v", rec.Stages)
	}
	if len(detail.Phases) != 4 {
		t.Fatalf("durable record carries %d phase(s), want 4 — phase telemetry is being discarded", len(detail.Phases))
	}

	// Sorted by registry index, not arrival order.
	wantOrder := []int{1, 6, 7, 13}
	for i, want := range wantOrder {
		if detail.Phases[i].Index != want {
			t.Errorf("phase[%d].Index = %d, want %d (records must be ordered by registry index)", i, detail.Phases[i].Index, want)
		}
	}

	// The full status vocabulary must survive. A phase that did not run has to
	// be distinguishable from one that was never recorded, or an index gap
	// reads identically to a silent drop.
	byName := map[string]V2PhaseDetail{}
	for _, p := range detail.Phases {
		byName[p.Name] = p
	}
	if got := byName["knowledge-base-read"].Status; got != "skipped" {
		t.Errorf("skipped phase recorded as %q, want \"skipped\"", got)
	}
	if got := byName["self-assessment"].Status; got != "abandoned" {
		t.Errorf("abandoned phase recorded as %q, want \"abandoned\"", got)
	}
	if byName["load-context"].DurationMs != 30_000 {
		t.Errorf("completed phase duration = %dms, want 30000", byName["load-context"].DurationMs)
	}
	// A phase with no completion must not invent one.
	if byName["self-assessment"].CompletedAt != "" {
		t.Errorf("abandoned phase carries completed_at = %q, want empty", byName["self-assessment"].CompletedAt)
	}
}

// The field is additive: a run with no phase records must not emit an empty
// `phases` key, so records written before this change stay byte-comparable.
func TestBuildV2Record_OmitsPhasesWhenThereAreNone(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1055, "item-nophases", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1, Output: 1}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "no phases", Branch: "feat/1055"}, time.Now())

	raw, err := json.Marshal(rec.Stages[string(StageFeatureDev)])
	if err != nil {
		t.Fatalf("marshal stage detail: %v", err)
	}
	if string(raw) != "" && containsKey(raw, "phases") {
		t.Errorf("empty phase list emitted a `phases` key: %s", raw)
	}
}

func containsKey(raw []byte, key string) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
