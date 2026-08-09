package state

import (
	"testing"
	"time"
)

// seedSnapshot writes a run's snapshot through the PRODUCTION writer. Under
// ADR-017 the filename is `runtime-{issue}-{runId}.json` and Persist refuses an
// identity-less state, so a hand-composed fixture would be a file this service
// can never actually meet.
func seedSnapshot(t *testing.T, dir string, rs *RuntimeState) {
	t.Helper()
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("seed Persist: %v", err)
	}
}

func TestLocalStateService_NoFile_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	svc := NewLocalStateService(dir)
	got := svc.GetState("99")
	if got != nil {
		t.Errorf("expected nil for missing state file, got %v", got)
	}
}

func TestLocalStateService_InvalidKey_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	svc := NewLocalStateService(dir)
	for _, key := range []string{"", "abc", "-1", "0"} {
		got := svc.GetState(key)
		if got != nil {
			t.Errorf("key %q: expected nil, got %v", key, got)
		}
	}
}

func TestLocalStateService_RunningPipeline(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("acme/myrepo", 42, "item-42", testRunID())
	rs.Stage = PipelineStage("feature-dev")
	rs.StartedAt = time.Now().UTC().Truncate(time.Second)
	seedSnapshot(t, dir, rs)

	svc := NewLocalStateService(dir)
	got := svc.GetState("42")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	m, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", got)
	}
	if m["status"] != "running" {
		t.Errorf("expected status running, got %v", m["status"])
	}
	if m["stage"] != "feature-dev" {
		t.Errorf("expected stage feature-dev, got %v", m["stage"])
	}
	if m["issueNumber"] != 42 {
		t.Errorf("expected issueNumber 42, got %v", m["issueNumber"])
	}
	if m["startedAt"] == "" || m["startedAt"] == nil {
		t.Error("expected startedAt to be set")
	}
}

// TestLocalStateService_MultipleRunsOfOneIssue_ReportsTheLiveRun puts the
// standard pick under test AT A CONSUMER, not just in its own unit test.
//
// PickPersistedStateForIssue was pinned by exactly one test — its own — so
// inverting the pick (returning the oldest, or losing the terminal preference)
// left every production call site green. This is the highest-value of the three:
// GetState answers "what is #N doing?", and answering it from a FINISHED run's
// snapshot while a live dispatch is mid-flight is the wrong-run-pick class
// ADR-017 exists to close.
//
// The pair is deliberately adversarial: the terminal run is the NEWEST, so a
// naive newest-wins pick fails here while passing a single-snapshot test.
func TestLocalStateService_MultipleRunsOfOneIssue_ReportsTheLiveRun(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)

	live := NewRuntimeState("acme/myrepo", 42, "item-live", testRunID())
	live.Stage = PipelineStage("feature-dev")
	live.StartedAt = base
	seedSnapshot(t, dir, live)

	dead := NewRuntimeState("acme/myrepo", 42, "item-dead", testRunID())
	dead.Stage = PipelineStage("pr-merge")
	dead.StartedAt = base.Add(time.Hour) // newer than the live run
	dead.MarkTerminal("complete")
	seedSnapshot(t, dir, dead)

	m, ok := NewLocalStateService(dir).GetState("42").(map[string]interface{})
	if !ok {
		t.Fatal("expected a state map for an issue with two snapshots")
	}
	if m["stage"] != "feature-dev" {
		t.Errorf("stage = %v, want the LIVE run's feature-dev — GetState answered from the terminal run", m["stage"])
	}
	if m["startedAt"] != base.Format("2006-01-02T15:04:05Z") {
		t.Errorf("startedAt = %v, want the live run's %v", m["startedAt"], base)
	}
}

func TestLocalStateService_CompletedPipeline(t *testing.T) {
	dir := t.TempDir()
	// Build a completed runtime state (6 stages completed)
	rs := NewRuntimeState("acme/myrepo", 10, "item-10", testRunID())
	rs.Stage = PipelineStage("pr-merge")
	rs.StartedAt = time.Now().UTC().Truncate(time.Second)
	stages := []PipelineStage{
		"issue-pickup", "feature-planning", "feature-dev",
		"feature-validate", "pr-create", "pr-merge",
	}
	for _, s := range stages {
		rs.CompletedStages = append(rs.CompletedStages, StageResult{Stage: s})
	}
	seedSnapshot(t, dir, rs)

	svc := NewLocalStateService(dir)
	got := svc.GetState("10")
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	m := got.(map[string]interface{})
	if m["status"] != "completed" {
		t.Errorf("expected status completed, got %v", m["status"])
	}
}
