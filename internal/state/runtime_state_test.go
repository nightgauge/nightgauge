package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

func TestNewRuntimeState(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123", testRunID())

	rs.BeginStage(StageIssuePickup)
	if rs.Stage != StageIssuePickup {
		t.Errorf("Stage = %q, want %q", rs.Stage, StageIssuePickup)
	}

	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")
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
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123", testRunID())
	rs.SkipStage(StageFeatureValidate)

	if len(rs.SkippedStages) != 1 {
		t.Fatalf("SkippedStages = %d, want 1", len(rs.SkippedStages))
	}
	if rs.SkippedStages[0] != string(StageFeatureValidate) {
		t.Errorf("SkippedStages[0] = %q", rs.SkippedStages[0])
	}
}

func TestIsComplete(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123", testRunID())

	if rs.IsComplete() {
		t.Error("should not be complete initially")
	}

	// Complete 4 stages, skip 2
	for _, stage := range []PipelineStage{StageIssuePickup, StageFeaturePlanning, StageFeatureDev, StagePRCreate} {
		rs.BeginStage(stage)
		rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 50}, "", "")
	}
	rs.SkipStage(StageFeatureValidate)
	rs.SkipStage(StagePRMerge)

	if !rs.IsComplete() {
		t.Error("should be complete with 4 completed + 2 skipped")
	}
}

func TestSnapshot(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 500, Output: 200}, "", "")

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
	rs := NewRuntimeState("nightgauge/nightgauge", 1845, "item-1", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "claude-haiku-4-5-20251001", "")

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
	rs.CompleteStage(0, tokens.TokenCounts{Input: 2000, Output: 1000}, "claude-sonnet-4-6", "")
	if len(rs.CompletedStages) != 2 {
		t.Fatal("should have 2 completed stages")
	}
	expected := rs.CompletedStages[0].CostUSD + rs.CompletedStages[1].CostUSD
	if rs.TotalCostUSD != expected {
		t.Errorf("TotalCostUSD=%v, want %v", rs.TotalCostUSD, expected)
	}
}

// #585: CompleteStage must price a stage through the SERVING ADAPTER's
// provider, not an anthropic default. Pins the run 01a007d5 regression
// (issue #583): feature-planning tokens on adapter grok, band "sonnet", must
// stamp at grok-4.6's rate — not claude-sonnet's $3/$15, which is exactly
// what production stamped before this fix.
func TestCompleteStage_AdapterAwarePricing_GrokVsClaude(t *testing.T) {
	counts := tokens.TokenCounts{Input: 484709, Output: 96317}

	grokRun := NewRuntimeState("nightgauge/nightgauge", 583, "item-1", testRunID())
	grokRun.BeginStage(StageFeaturePlanning)
	grokRun.CompleteStage(0, counts, "sonnet", "grok")
	grokStage := grokRun.CompletedStages[0]
	if grokStage.CostUnstamped {
		t.Fatal("grok/sonnet should resolve to a stamped cost")
	}
	const wantGrok = 0.263044
	if diff := grokStage.CostUSD - wantGrok; diff > 5e-5 || diff < -5e-5 {
		t.Errorf("grok stage CostUSD = %.6f, want ~%.6f (grok-4.6 rates)", grokStage.CostUSD, wantGrok)
	}

	claudeRun := NewRuntimeState("nightgauge/nightgauge", 583, "item-2", testRunID())
	claudeRun.BeginStage(StageFeaturePlanning)
	claudeRun.CompleteStage(0, counts, "sonnet", "claude")
	claudeStage := claudeRun.CompletedStages[0]
	if claudeStage.CostUnstamped {
		t.Fatal("claude/sonnet should resolve to a stamped cost")
	}
	const wantClaude = 2.8989
	if diff := claudeStage.CostUSD - wantClaude; diff > 1e-3 || diff < -1e-3 {
		t.Errorf("claude stage CostUSD = %.6f, want ~%.6f (anthropic sonnet rates, unchanged)", claudeStage.CostUSD, wantClaude)
	}
}

// #585: a stage served by an adapter whose concrete model cannot be resolved
// against its provider must record CostUnstamped, not a fabricated $0 or
// another provider's price.
func TestCompleteStage_UnresolvableAdapterModelIsUnstamped(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 585, "item-1", testRunID())
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "nonexistent-band-xyz", "grok")

	sr := rs.CompletedStages[0]
	if !sr.CostUnstamped {
		t.Error("unresolvable (grok/nonexistent-band-xyz) should be recorded CostUnstamped=true")
	}
	if sr.CostUSD != 0 {
		t.Errorf("unstamped CostUSD placeholder = %v, want 0", sr.CostUSD)
	}
}

// #358: CompleteStage must price every pool it receives with the same
// formula the scheduler's fallback estimate uses, and must record the
// combined-input/cache-subset shape history.go readers depend on
// (`InputTokens - CacheRead` and the cache-hit-rate divide). If either side
// drifts, one stage reports two different costs again. Counts mirror the
// committed real capture (internal/execution/testdata).
func TestCompleteStagePricesAllPoolsConsistently(t *testing.T) {
	counts := tokens.TokenCounts{
		Input:           18,
		Output:          236,
		CacheRead:       29622,
		CacheCreation5m: 3308,
	}
	const model = "claude-haiku-4-5-20251001"

	rs := NewRuntimeState("nightgauge/nightgauge", 358, "item-1", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, counts, model, "")

	sr := rs.CompletedStages[0]
	if want := tokens.CalculateCost(model, counts); sr.CostUSD != want {
		t.Errorf("CostUSD=%v, want CalculateCost's %v — CompleteStage and the fallback estimate diverged",
			sr.CostUSD, want)
	}
	ioOnly := tokens.CalculateCost(model, tokens.TokenCounts{Input: counts.Input, Output: counts.Output})
	if sr.CostUSD <= ioOnly {
		t.Errorf("CostUSD=%v prices no cache pool (input+output alone = %v)", sr.CostUSD, ioOnly)
	}
	if sr.InputTokens != counts.Input+counts.CacheRead {
		t.Errorf("InputTokens=%d, want combined %d (CacheRead is a subset of InputTokens)",
			sr.InputTokens, counts.Input+counts.CacheRead)
	}
	if sr.CacheRead != counts.CacheRead {
		t.Errorf("CacheRead=%d, want %d", sr.CacheRead, counts.CacheRead)
	}
	if sr.CacheCreation != counts.CacheCreation5m+counts.CacheCreation1h {
		t.Errorf("CacheCreation=%d, want %d", sr.CacheCreation, counts.CacheCreation5m+counts.CacheCreation1h)
	}
}

// #390: executor-side token observations must survive the legacy scheduler
// completion signature exactly once. The scheduler can keep calling
// CompleteStageWithCost without cache creation arguments; the stage-keyed
// handoff supplies the omitted pools and is consumed on first completion.
func TestStageTokenCountHandoffIsConsumedOnce(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 390, "item-1", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageTokenCounts(StageFeatureDev, tokens.TokenCounts{
		CacheRead:       29622,
		CacheCreation1h: 3308,
	})
	rs.CompleteStageWithCost(0, 18, 236, 29622, 0.01)

	if got := rs.CompletedStages[0].CacheCreation; got != 3308 {
		t.Fatalf("CacheCreation=%d, want 3308 from executor handoff", got)
	}

	// A new occurrence for the same stage must not inherit the old handoff.
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 1, 2, 0, 0.001)
	if got := rs.CompletedStages[1].CacheCreation; got != 0 {
		t.Errorf("second occurrence CacheCreation=%d, want 0 after one-shot consume", got)
	}

	// Callers that already have only a combined total can pass it directly;
	// the variadic tail keeps legacy scheduler call sites source-compatible.
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStageWithCost(0, 3, 4, 0, 0.002, 500)
	if got := rs.CompletedStages[2].CacheCreation; got != 500 {
		t.Errorf("direct CompleteStageWithCost CacheCreation=%d, want 500", got)
	}
}

// #230: a stage completing twice for the same occurrence (same Stage + the
// same BeginStage-stamped StageStart) must yield exactly one completedStages
// entry and must not double-count tokens/cost. Guards against the duplicate
// pipeline-start entry observed in a dogfood run.
func TestCompleteStageIdempotentPerOccurrence(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")
	// Second complete for the SAME occurrence (no BeginStage between).
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")

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
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 50}, "", "")
	// A real retry: BeginStage stamps a new StageStart. Sleep guarantees the
	// timestamp advances so the occurrence is distinguishable.
	time.Sleep(time.Millisecond)
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 50}, "", "")

	if len(rs.CompletedStages) != 2 {
		t.Fatalf("CompletedStages = %d, want 2 (a genuine retry must append)", len(rs.CompletedStages))
	}
}

func TestConcurrentAccess(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123", testRunID())

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs.BeginStage(StageFeatureDev)
			rs.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 5}, "", "")
			_ = rs.Snapshot()
			_ = rs.IsComplete()
			_ = rs.TotalDuration()
		}()
	}
	wg.Wait()
}

func TestBeginPhase(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 244, "item-1", testRunID())
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.PhaseHistory[0].StartedAt = time.Now().Add(-phaseStartDedupeWindow - time.Second)
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)

	if len(rs.PhaseHistory) != 2 {
		t.Fatalf("PhaseHistory len = %d, want 2 (stale running record must not suppress a real re-run)", len(rs.PhaseHistory))
	}
}

func TestCompletePhase(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", testRunID())
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	// Complete a different name — should not change the existing phase.
	rs.CompletePhase(StageFeatureDev, "quality-review")

	if rs.PhaseHistory[0].Status != "running" {
		t.Errorf("Status = %q, want running (no match)", rs.PhaseHistory[0].Status)
	}
}

func TestSetStageError(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 500, Output: 200}, "", "")
	rs.BeginPhase(StageFeatureDev, "implementation", 3, 14)
	rs.SetStageError(StageFeaturePlanning, "timeout")

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// Verify file exists — under the run-identity-keyed name (ADR-017 D8),
	// composed by the same helper Persist used.
	path := filepath.Join(dir, SnapshotFilename(1899, rs.RunID))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing: %v", err)
	}

	// Load and verify — addressed by run identity, not by issue.
	loaded, err := LoadPersistedState(dir, rs.RunID)
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
	rs := NewRuntimeState("nightgauge/nightgauge", 2008, "item-1", testRunID())
	rs.SetPaused(true)

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if !loaded.Paused {
		t.Error("loaded Paused should be true")
	}
}

func TestRuntimeState_SetPaused_Snapshot(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 2008, "item-1", testRunID())
	rs.SetPaused(true)

	snap := rs.Snapshot()
	if !snap.Paused {
		t.Error("snapshot Paused should be true")
	}
}

func TestRuntimeState_ResumeClears(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 2008, "item-1", testRunID())
	rs.SetPaused(true)
	rs.SetPaused(false)

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.Paused {
		t.Error("loaded Paused should be false after resume")
	}
}

func TestLoadPersistedStateMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadPersistedState(dir, testRunID())
	if err == nil {
		t.Error("expected error for missing file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing snapshot must report fs.ErrNotExist so callers can tell it from a parse error; got %v", err)
	}
}

func TestPersistAtomicity(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 42, "item-42", testRunID())
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	// No .tmp file should remain
	tmpPath := filepath.Join(dir, SnapshotFilename(42, rs.RunID)+".tmp")
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error(".tmp file should not exist after successful persist")
	}
}

func TestPersistJSON(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 100, "item-100", testRunID())
	rs.BeginPhase(StageIssuePickup, "read-issue", 0, 5)
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, SnapshotFilename(100, rs.RunID)))
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
	rs := NewRuntimeState("nightgauge/nightgauge", 1899, "item-1", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 42, "item-42", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 42, "item-42", testRunID())
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
	rs := NewRuntimeState("nightgauge/nightgauge", 100, "item-100", testRunID())
	rs.Title = "Fix login bug"
	rs.SetBranch("fix/100-login-bug")
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, SnapshotFilename(100, rs.RunID)))
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
	rs := NewRuntimeState("nightgauge/nightgauge", 3557, "item-abc", "01966b4c-0000-7000-a000-000000000042")

	snap := rs.Snapshot()
	if snap.RunID != rs.RunID {
		t.Errorf("Snapshot RunID = %q, want %q", snap.RunID, rs.RunID)
	}
}

func TestRuntimeState_RunID_Persisted(t *testing.T) {
	dir := t.TempDir()
	rs := NewRuntimeState("nightgauge/nightgauge", 3557, "item-abc", "01966b4c-0000-7000-a000-000000000042")

	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.RunID != rs.RunID {
		t.Errorf("loaded RunID = %q, want %q", loaded.RunID, rs.RunID)
	}
}

// TestSetWorktree_WritesOnlyTheWorktree pins the one-field contract behind
// SetWorktree (#399): it is called before a child process exists, so it must
// never write PID. Re-implementing it as SetProcess(0, dir) would satisfy the
// worktree assertion and quietly clear a PID a later stage had recorded — and a
// non-zero PID it invented would let a run that never spawned answer the
// liveness ladder's "is your stage child alive?" arm.
func TestSetWorktree_WritesOnlyTheWorktree(t *testing.T) {
	const worktree = "/tmp/ws/.nightgauge/worktrees/nightgauge-issue-399"

	rs := NewRuntimeState("nightgauge/nightgauge", 399, "item-399", testRunID())

	// Populate the neighbourhood the setter must not touch. Enumerated
	// assertions only catch the fields someone thought to enumerate — a
	// re-implementation that wiped Branch or AuthoritativeChangeClass on its way
	// to WorktreeDir would satisfy a "PID is still 0" list and stay green. The
	// comparison below is structural for that reason: every field of the
	// snapshot is in scope without being named here.
	rs.SetBranch("fix/399-worktree-stamp")
	rs.BeginStage(StageFeatureDev)
	rs.SetAuthoritativeChangeClass("code")
	rs.SetStageError(StageFeatureDev, "stage died before spawn")
	rs.RecordStageModel(StageFeatureDev, "opus")
	rs.RecordStageAdapter(StageFeatureDev, "codex")
	rs.SetPrUrl("https://example.invalid/pull/399")

	before := rs.Snapshot()
	rs.SetWorktree(worktree)
	after := rs.Snapshot()

	if after.WorktreeDir != worktree {
		t.Fatalf("WorktreeDir = %q, want %q", after.WorktreeDir, worktree)
	}
	// Neutralise the one field the setter is allowed to write; everything else
	// must be identical. Snapshot reads no clocks and copies only recorded
	// values, so deep equality here is exact, not approximate.
	before.WorktreeDir = after.WorktreeDir
	if !reflect.DeepEqual(before, after) {
		t.Errorf("SetWorktree wrote more than WorktreeDir — it is called before a child exists and must be a "+
			"one-field write (#399).\nbefore: %+v\nafter:  %+v", before, after)
	}
	if pid := rs.StageChildPID(); pid != 0 {
		t.Errorf("PID = %d, want 0 — SetWorktree must not invent a process identity", pid)
	}

	// A stamp arriving after a real spawn (the idempotent re-stamp RunStage
	// performs) must leave that process identity intact.
	rs.SetProcess(4242, worktree)
	rs.SetWorktree(worktree)
	if pid := rs.StageChildPID(); pid != 4242 {
		t.Errorf("PID = %d after a re-stamp, want 4242 — SetWorktree must not clear a recorded child", pid)
	}
}

func TestActualLinesChanged_PreservesMeasuredZeroAcrossSnapshotAndDisk(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 369, "item-369", testRunID())
	if rs.Snapshot().ActualLinesChanged != nil {
		t.Fatal("new runtime has an actual-lines measurement before pr-create")
	}
	rs.SetActualLinesChanged(0)
	snap := rs.Snapshot()
	if snap.ActualLinesChanged == nil || *snap.ActualLinesChanged != 0 {
		t.Fatalf("snapshot ActualLinesChanged = %v, want pointer to measured zero", snap.ActualLinesChanged)
	}

	dir := t.TempDir()
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if loaded.ActualLinesChanged == nil || *loaded.ActualLinesChanged != 0 {
		t.Fatalf("loaded ActualLinesChanged = %v, want pointer to measured zero", loaded.ActualLinesChanged)
	}
}
