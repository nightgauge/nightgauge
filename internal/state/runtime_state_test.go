package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewRuntimeState(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")
	if rs.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q", rs.Repo)
	}
	if rs.IssueNumber != 1311 {
		t.Errorf("IssueNumber = %d", rs.IssueNumber)
	}
	if rs.StartedAt.IsZero() {
		t.Error("StartedAt should be set")
	}
}

func TestStageLifecycle(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")

	rs.BeginStage(StageIssuePickup)
	if rs.Stage != StageIssuePickup {
		t.Errorf("Stage = %q, want %q", rs.Stage, StageIssuePickup)
	}

	rs.CompleteStage(0, 1000, 500, "")
	if len(rs.CompletedStages) != 1 {
		t.Fatalf("CompletedStages = %d, want 1", len(rs.CompletedStages))
	}
	if rs.CompletedStages[0].ExitCode != 0 {
		t.Errorf("ExitCode = %d", rs.CompletedStages[0].ExitCode)
	}
	if rs.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", rs.InputTokens)
	}
	if rs.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", rs.OutputTokens)
	}
}

func TestSkipStage(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")
	rs.SkipStage(StageFeatureValidate)

	if len(rs.SkippedStages) != 1 {
		t.Fatalf("SkippedStages = %d, want 1", len(rs.SkippedStages))
	}
	if rs.SkippedStages[0] != string(StageFeatureValidate) {
		t.Errorf("SkippedStages[0] = %q", rs.SkippedStages[0])
	}
}

func TestIsComplete(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")

	if rs.IsComplete() {
		t.Error("should not be complete initially")
	}

	// Complete 4 stages, skip 2
	for _, stage := range []PipelineStage{StageIssuePickup, StageFeaturePlanning, StageFeatureDev, StagePRCreate} {
		rs.BeginStage(stage)
		rs.CompleteStage(0, 100, 50, "")
	}
	rs.SkipStage(StageFeatureValidate)
	rs.SkipStage(StagePRMerge)

	if !rs.IsComplete() {
		t.Error("should be complete with 4 completed + 2 skipped")
	}
}

func TestSnapshot(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, 500, 200, "")

	snap := rs.Snapshot()
	if snap.Repo != rs.Repo {
		t.Error("snapshot Repo mismatch")
	}
	if len(snap.CompletedStages) != 1 {
		t.Error("snapshot should have 1 completed stage")
	}

	// Modifying snapshot should not affect original
	snap.CompletedStages = nil
	if len(rs.CompletedStages) != 1 {
		t.Error("original should still have 1 completed stage")
	}
}

func TestCompleteStageAccumulatesCost(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1845, "item-1")

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 1000, 500, "claude-haiku-4-5-20251001")

	if rs.TotalCostUSD == 0 {
		t.Error("TotalCostUSD should be non-zero after CompleteStage")
	}
	if rs.CompletedStages[0].CostUSD == 0 {
		t.Error("StageResult.CostUSD should be non-zero")
	}
	if rs.CompletedStages[0].CostUSD != rs.TotalCostUSD {
		t.Errorf("single stage cost should equal total: stage=%v total=%v",
			rs.CompletedStages[0].CostUSD, rs.TotalCostUSD)
	}

	// Add a second stage — verify accumulation
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, 2000, 1000, "claude-sonnet-4-6")
	if len(rs.CompletedStages) != 2 {
		t.Fatal("should have 2 completed stages")
	}
	expected := rs.CompletedStages[0].CostUSD + rs.CompletedStages[1].CostUSD
	if rs.TotalCostUSD != expected {
		t.Errorf("TotalCostUSD=%v, want %v", rs.TotalCostUSD, expected)
	}
}

// #230: a stage completing twice for the same occurrence (same Stage + the
// same BeginStage-stamped StageStart) must yield exactly one completedStages
// entry and must not double-count tokens/cost. Guards against the duplicate
// pipeline-start entry observed in a dogfood run.
func TestCompleteStageIdempotentPerOccurrence(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 1000, 500, "")
	// Second complete for the SAME occurrence (no BeginStage between).
	rs.CompleteStage(0, 1000, 500, "")

	if len(rs.CompletedStages) != 1 {
		t.Fatalf("CompletedStages = %d, want 1 (duplicate complete must not append)", len(rs.CompletedStages))
	}
	if rs.InputTokens != 1000 || rs.OutputTokens != 500 {
		t.Errorf("totals double-counted: input=%d output=%d, want 1000/500", rs.InputTokens, rs.OutputTokens)
	}
}

// A legitimate retry re-runs BeginStage (advancing StageStart), so its
// completion is a distinct occurrence and still appends.
func TestCompleteStageRetryStillAppends(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 100, 50, "")
	// A real retry: BeginStage stamps a new StageStart. Sleep guarantees the
	// timestamp advances so the occurrence is distinguishable.
	time.Sleep(time.Millisecond)
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 100, 50, "")

	if len(rs.CompletedStages) != 2 {
		t.Fatalf("CompletedStages = %d, want 2 (a genuine retry must append)", len(rs.CompletedStages))
	}
}

func TestConcurrentAccess(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs.BeginStage(StageFeatureDev)
			rs.CompleteStage(0, 10, 5, "")
			_ = rs.Snapshot()
			_ = rs.IsComplete()
			_ = rs.TotalDuration()
		}()
	}
	wg.Wait()
}

func TestBeginPhase(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	rs.BeginPhase(StageFeatureDev, "validate-environment", 0, 14)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory len = %d, want 1", len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Stage != StageFeatureDev {
		t.Errorf("Stage = %q, want %q", p.Stage, StageFeatureDev)
	}
	if p.Name != "validate-environment" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Index != 0 || p.Total != 14 {
		t.Errorf("Index=%d Total=%d, want 0/14", p.Index, p.Total)
	}
	if p.Status != "running" {
		t.Errorf("Status = %q, want running", p.Status)
	}
	if p.StartedAt.IsZero() {
		t.Error("StartedAt should be set")
	}
	if p.CompletedAt != nil {
		t.Error("CompletedAt should be nil")
	}
}

// #217: the same phase:start marker can be sighted more than once for a
// single emission (tool_use command echo, tool_result stdout, text
// narration). Consecutive identical sightings must collapse to one record.
func TestBeginPhase_DedupesRepeatedMarkerSighting(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory len = %d, want 1 (duplicate sightings must not append)", len(rs.PhaseHistory))
	}
}

// A clean single pass through a stage — every marker sighted twice (echo +
// tool_result) — must yield exactly one record per phase.
func TestBeginPhase_SinglePassYieldsOneRecordPerPhase(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")
	phases := []string{"validate-environment", "feedback-context-check", "ac-reconcile", "complete-stage"}
	for i, name := range phases {
		rs.BeginPhase(StageFeatureValidate, name, i, len(phases))
		rs.BeginPhase(StageFeatureValidate, name, i, len(phases)) // duplicate sighting
	}

	if len(rs.PhaseHistory) != len(phases) {
		t.Fatalf("PhaseHistory len = %d, want %d (one record per phase)", len(rs.PhaseHistory), len(phases))
	}
	for i, name := range phases {
		if rs.PhaseHistory[i].Name != name {
			t.Errorf("PhaseHistory[%d].Name = %q, want %q", i, rs.PhaseHistory[i].Name, name)
		}
	}
}

// A legitimate re-run of a phase after an intermediate phase must append —
// only CONSECUTIVE duplicates are collapsed.
func TestBeginPhase_AllowsReRunAfterIntermediatePhase(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.BeginPhase(StageFeatureDev, "testing", 4, 14)
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)

	if len(rs.PhaseHistory) != 3 {
		t.Fatalf("PhaseHistory len = %d, want 3 (re-run after another phase is legitimate)", len(rs.PhaseHistory))
	}
}

// A re-emission after the previous record completed is a real re-run, not an
// echo — it must append.
func TestBeginPhase_AllowsReRunAfterComplete(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.CompletePhase(StageFeatureDev, "implementation")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)

	if len(rs.PhaseHistory) != 2 {
		t.Fatalf("PhaseHistory len = %d, want 2 (re-run after completion is legitimate)", len(rs.PhaseHistory))
	}
}

// A re-emission outside the dedupe window is a real re-run even if the prior
// record never completed (e.g. a stalled phase retried much later).
func TestBeginPhase_AllowsReRunOutsideDedupeWindow(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.PhaseHistory[0].StartedAt = time.Now().Add(-phaseStartDedupeWindow - time.Second)
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)

	if len(rs.PhaseHistory) != 2 {
		t.Fatalf("PhaseHistory len = %d, want 2 (stale running record must not suppress a real re-run)", len(rs.PhaseHistory))
	}
}

func TestCompletePhase(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.CompletePhase(StageFeatureDev, "implementation")

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory len = %d, want 1", len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Status != "complete" {
		t.Errorf("Status = %q, want complete", p.Status)
	}
	if p.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
}

func TestCompletePhaseNoMatch(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	// Complete a different name — should not change the existing phase.
	rs.CompletePhase(StageFeatureDev, "quality-review")

	if rs.PhaseHistory[0].Status != "running" {
		t.Errorf("Status = %q, want running (no match)", rs.PhaseHistory[0].Status)
	}
}

func TestSetStageError(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	rs.SetStageError(StageFeatureDev, "exit code 1")

	msg, ok := rs.StageErrors[string(StageFeatureDev)]
	if !ok {
		t.Fatal("StageErrors should contain feature-dev")
	}
	if msg != "exit code 1" {
		t.Errorf("error message = %q", msg)
	}
}

func TestPersistAndLoad(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, 500, 200, "")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.SetStageError(StageFeaturePlanning, "timeout")

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, "runtime-1899.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing: %v", err)
	}

	// Load and verify
	loaded, err := LoadPersistedState(dir, 1899)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q", loaded.Repo)
	}
	if loaded.IssueNumber != 1899 {
		t.Errorf("IssueNumber = %d", loaded.IssueNumber)
	}
	if len(loaded.CompletedStages) != 1 {
		t.Errorf("CompletedStages = %d, want 1", len(loaded.CompletedStages))
	}
	if len(loaded.PhaseHistory) != 1 {
		t.Errorf("PhaseHistory = %d, want 1", len(loaded.PhaseHistory))
	}
	if loaded.PhaseHistory[0].Name != "implementation" {
		t.Errorf("PhaseHistory[0].Name = %q", loaded.PhaseHistory[0].Name)
	}
	if loaded.StageErrors[string(StageFeaturePlanning)] != "timeout" {
		t.Errorf("StageErrors missing feature-planning timeout")
	}
}

func TestRuntimeState_SetPaused_PersistsAndLoads(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 2008, "item-1")
	rs.SetPaused(true)

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, 2008)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if !loaded.Paused {
		t.Error("loaded Paused should be true")
	}
}

func TestRuntimeState_SetPaused_Snapshot(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 2008, "item-1")
	rs.SetPaused(true)

	snap := rs.Snapshot()
	if !snap.Paused {
		t.Error("snapshot Paused should be true")
	}
}

func TestRuntimeState_ResumeClears(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 2008, "item-1")
	rs.SetPaused(true)
	rs.SetPaused(false)

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, 2008)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.Paused {
		t.Error("loaded Paused should be false after resume")
	}
}

func TestLoadPersistedStateMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadPersistedState(dir, 9999)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestPersistAtomicity(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 42, "item-42")
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	// No .tmp file should remain
	tmpPath := filepath.Join(dir, "runtime-42.json.tmp")
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error(".tmp file should not exist after successful persist")
	}
}

func TestPersistJSON(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 100, "item-100")
	rs.BeginPhase(StageIssuePickup, "read-issue", 0, 5)
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "runtime-100.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["phaseHistory"]; !ok {
		t.Error("JSON should contain phaseHistory key")
	}
	if _, ok := raw["stageErrors"]; !ok {
		t.Error("JSON should contain stageErrors key")
	}
}

func TestSnapshotIncludesPhaseAndErrors(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.SetStageError(StageFeaturePlanning, "timeout")

	snap := rs.Snapshot()

	// Verify snapshot has phase and error data
	if len(snap.PhaseHistory) != 1 {
		t.Fatalf("snap PhaseHistory = %d, want 1", len(snap.PhaseHistory))
	}
	if snap.StageErrors[string(StageFeaturePlanning)] != "timeout" {
		t.Error("snap should have StageErrors")
	}

	// Modifying snapshot should not affect original
	snap.PhaseHistory = nil
	delete(snap.StageErrors, string(StageFeaturePlanning))
	if len(rs.PhaseHistory) != 1 {
		t.Error("original PhaseHistory should be unaffected")
	}
	if rs.StageErrors[string(StageFeaturePlanning)] != "timeout" {
		t.Error("original StageErrors should be unaffected")
	}
}

func TestSnapshotIncludesTitleBranchPrUrl(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 42, "item-42")
	rs.Title = "Add Discord notifications"
	rs.SetBranch("feat/42-discord-notifications")
	rs.SetPrUrl("https://github.com/nightgauge/nightgauge/pull/42")

	snap := rs.Snapshot()
	if snap.Title != "Add Discord notifications" {
		t.Errorf("Title = %q", snap.Title)
	}
	if snap.Branch != "feat/42-discord-notifications" {
		t.Errorf("Branch = %q", snap.Branch)
	}
	if snap.PrUrl != "https://github.com/nightgauge/nightgauge/pull/42" {
		t.Errorf("PrUrl = %q", snap.PrUrl)
	}
}

func TestSnapshotIncludesGateResults(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 42, "item-42")
	rs.SetGateResults([]GateResult{
		{GateName: "build", Result: "pass", Timestamp: "2026-03-20T10:00:00Z"},
		{GateName: "tests", Result: "catch", ErrorSummary: "2 tests failed", Timestamp: "2026-03-20T10:01:00Z"},
	})

	snap := rs.Snapshot()
	if len(snap.GateResults) != 2 {
		t.Fatalf("GateResults = %d, want 2", len(snap.GateResults))
	}
	if snap.GateResults[0].GateName != "build" || snap.GateResults[0].Result != "pass" {
		t.Errorf("GateResults[0] = %+v", snap.GateResults[0])
	}
	if snap.GateResults[1].ErrorSummary != "2 tests failed" {
		t.Errorf("GateResults[1].ErrorSummary = %q", snap.GateResults[1].ErrorSummary)
	}

	// Modifying snapshot should not affect original
	snap.GateResults = nil
	if len(rs.GateResults) != 2 {
		t.Error("original GateResults should be unaffected")
	}
}

func TestTitleBranchInJSON(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 100, "item-100")
	rs.Title = "Fix login bug"
	rs.SetBranch("fix/100-login-bug")
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "runtime-100.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["title"]; !ok {
		t.Error("JSON should contain title key")
	}
	if _, ok := raw["branch"]; !ok {
		t.Error("JSON should contain branch key")
	}
}

func TestRuntimeState_RunID_SetAndSnapshot(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3557, "item-abc")
	rs.RunID = "01966b4c-0000-7000-a000-000000000042"

	snap := rs.Snapshot()
	if snap.RunID != rs.RunID {
		t.Errorf("Snapshot RunID = %q, want %q", snap.RunID, rs.RunID)
	}
}

func TestRuntimeState_RunID_Persisted(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 3557, "item-abc")
	rs.RunID = "01966b4c-0000-7000-a000-000000000042"

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, 3557)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.RunID != rs.RunID {
		t.Errorf("loaded RunID = %q, want %q", loaded.RunID, rs.RunID)
	}
}
