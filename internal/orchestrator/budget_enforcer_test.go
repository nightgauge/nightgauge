package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBudgetEnforcer_RecordAndCheck(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 10000,
		PerStageCeilings:      map[string]int{"feature-dev": 5000},
		GracePercent:          50,
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)

	// Record some tokens
	be.RecordStageTokens("issue-pickup", 100, 200)
	be.RecordStageTokens("feature-dev", 2000, 1500)

	if be.TotalUsed() != 3800 {
		t.Errorf("expected 3800 total, got %d", be.TotalUsed())
	}
	if be.StageUsed("feature-dev") != 3500 {
		t.Errorf("expected 3500 for feature-dev, got %d", be.StageUsed("feature-dev"))
	}

	// Pipeline budget should be fine
	pd := be.CheckPipelineBudget()
	if pd.ShouldWarn || pd.ShouldTerminate {
		t.Error("expected no warning or termination under ceiling")
	}

	// Stage budget should be fine
	sd := be.CheckStageBudget("feature-dev")
	if sd.ShouldWarn || sd.ShouldTerminate {
		t.Error("expected no warning for feature-dev under ceiling")
	}
}

func TestBudgetEnforcer_HardMode_Terminates(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 1000,
		GracePercent:          50, // effective limit = 1500
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)

	// Exceed effective limit (1500)
	be.RecordStageTokens("feature-dev", 1000, 600)

	pd := be.CheckPipelineBudget()
	if !pd.ShouldTerminate {
		t.Error("expected termination when over effective limit")
	}
	if pd.Reason != "pipeline_budget_exceeded" {
		t.Errorf("expected pipeline_budget_exceeded, got %s", pd.Reason)
	}
}

func TestBudgetEnforcer_HardMode_WarnsBeforeTerminate(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 1000,
		GracePercent:          50, // effective limit = 1500
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)

	// Over base (1000) but under effective (1500)
	be.RecordStageTokens("feature-dev", 700, 500)

	pd := be.CheckPipelineBudget()
	if pd.ShouldTerminate {
		t.Error("expected no termination yet")
	}
	if !pd.ShouldWarn {
		t.Error("expected warning when over base but under effective")
	}
}

func TestBudgetEnforcer_SoftMode_NeverTerminates(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 100,
		GracePercent:          50,
		Mode:                  "soft",
	}
	be := NewBudgetEnforcer(cfg)

	be.RecordStageTokens("feature-dev", 5000, 5000)

	pd := be.CheckPipelineBudget()
	if pd.ShouldTerminate {
		t.Error("soft mode should never terminate")
	}
	if !pd.ShouldWarn {
		t.Error("soft mode should warn when over ceiling")
	}
}

func TestBudgetEnforcer_PerStageCeiling(t *testing.T) {
	cfg := BudgetConfig{
		PerStageCeilings: map[string]int{"feature-dev": 1000},
		GracePercent:     50, // effective = 1500
		Mode:             "hard",
	}
	be := NewBudgetEnforcer(cfg)

	be.RecordStageTokens("feature-dev", 1000, 600) // 1600 > 1500

	sd := be.CheckStageBudget("feature-dev")
	if !sd.ShouldTerminate {
		t.Error("expected stage budget termination")
	}
	if sd.Reason != "stage_budget_exceeded" {
		t.Errorf("expected stage_budget_exceeded, got %s", sd.Reason)
	}
}

func TestBudgetEnforcer_NoCeiling(t *testing.T) {
	be := NewBudgetEnforcer(DefaultBudgetConfig())

	be.RecordStageTokens("feature-dev", 999999, 999999)

	pd := be.CheckPipelineBudget()
	if pd.ShouldWarn || pd.ShouldTerminate {
		t.Error("expected no action with no ceiling configured")
	}

	sd := be.CheckStageBudget("feature-dev")
	if sd.ShouldWarn || sd.ShouldTerminate {
		t.Error("expected no action with no stage ceiling configured")
	}
}

func TestBudgetEnforcer_Reset(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 10000,
		GracePercent:          50,
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)

	be.RecordStageTokens("feature-dev", 5000, 3000)
	if be.TotalUsed() != 8000 {
		t.Errorf("expected 8000, got %d", be.TotalUsed())
	}

	be.Reset()

	if be.TotalUsed() != 0 {
		t.Errorf("expected 0 after reset, got %d", be.TotalUsed())
	}
	if be.StageUsed("feature-dev") != 0 {
		t.Errorf("expected 0 for feature-dev after reset")
	}
}

// Issue #2338 - BudgetOverrunContext tests

func TestReadBudgetOverrun_ValidFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "budget-overrun-472.json")
	content := `{
		"schema_version": "1.0",
		"issue_number": 472,
		"stage": "feature-dev",
		"estimated_budget_usd": 4.0,
		"actual_cost_usd": 13.34,
		"effective_limit_usd": 12.0,
		"overrun_ratio": 3.335,
		"wip_committed": true,
		"wip_branch": "feat/team-analytics",
		"timestamp": "2026-03-21T15:54:07.161Z"
	}`
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := ReadBudgetOverrun(fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.IssueNumber != 472 {
		t.Errorf("expected issue 472, got %d", ctx.IssueNumber)
	}
	if ctx.Stage != "feature-dev" {
		t.Errorf("expected feature-dev, got %s", ctx.Stage)
	}
	if ctx.OverrunRatio != 3.335 {
		t.Errorf("expected 3.335 overrun ratio, got %f", ctx.OverrunRatio)
	}
	if !ctx.WIPCommitted {
		t.Error("expected WIPCommitted to be true")
	}
	if ctx.WIPBranch != "feat/team-analytics" {
		t.Errorf("expected feat/team-analytics, got %s", ctx.WIPBranch)
	}
}

func TestReadBudgetOverrun_FileNotFound(t *testing.T) {
	_, err := ReadBudgetOverrun("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// Issue #3666 — shipped_partially / shipped_pr_number routing.
//
// When the budget enforcer kills a stage AFTER its work product already
// shipped (the pr-create case: PR opened, then cost cap fired), the v1.1
// overrun envelope carries `shipped_partially: true` plus the PR number.
// Older v1.0 files lacking those fields must still parse and default to
// the pre-#3666 behavior (no shipping, retry path unchanged).
func TestReadBudgetOverrun_ShippedPartially_V11(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "budget-overrun-215.json")
	content := `{
		"schema_version": "1.1",
		"issue_number": 215,
		"stage": "pr-create",
		"estimated_budget_usd": 0.60,
		"actual_cost_usd": 1.21,
		"effective_limit_usd": 0.90,
		"overrun_ratio": 2.02,
		"wip_committed": true,
		"wip_branch": "feat/215-push-notifications",
		"shipped_partially": true,
		"shipped_pr_number": 222,
		"timestamp": "2026-05-18T14:04:55.776Z"
	}`
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := ReadBudgetOverrun(fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ctx.ShippedPartially {
		t.Error("expected ShippedPartially=true for v1.1 envelope")
	}
	if ctx.ShippedPRNumber != 222 {
		t.Errorf("expected ShippedPRNumber=222, got %d", ctx.ShippedPRNumber)
	}
	if ctx.SchemaVersion != "1.1" {
		t.Errorf("expected schema_version=1.1, got %s", ctx.SchemaVersion)
	}
}

func TestReadBudgetOverrun_V10BackCompat_NoShippedFields(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "budget-overrun-100.json")
	// v1.0 shape — no shipped_partially / shipped_pr_number fields. Readers
	// must tolerate this and default both to their zero values.
	content := `{
		"schema_version": "1.0",
		"issue_number": 100,
		"stage": "feature-dev",
		"estimated_budget_usd": 4.0,
		"actual_cost_usd": 13.34,
		"effective_limit_usd": 12.0,
		"overrun_ratio": 3.335,
		"wip_committed": true,
		"wip_branch": "feat/legacy",
		"timestamp": "2026-03-21T15:54:07.161Z"
	}`
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := ReadBudgetOverrun(fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.ShippedPartially {
		t.Error("v1.0 envelope must default ShippedPartially=false")
	}
	if ctx.ShippedPRNumber != 0 {
		t.Errorf("v1.0 envelope must default ShippedPRNumber=0, got %d", ctx.ShippedPRNumber)
	}
	// Pre-existing fields still readable.
	if !ctx.WIPCommitted || ctx.OverrunRatio != 3.335 {
		t.Error("v1.0 envelope misparsed pre-existing fields")
	}
}

// TestBudgetEnforcer_ConcurrentAccess exercises the mutex protection added in
// Issue #3198. The test must pass under `go test -race`; any regression that
// drops a mutex will be caught by the detector. Assertions are minimal — the
// race detector and the absence of a "concurrent map writes" panic are the
// real signal here.
func TestBudgetEnforcer_ConcurrentAccess(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 100000,
		PerStageCeilings:      map[string]int{"feature-dev": 50000},
		GracePercent:          50,
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)

	const goroutines = 50
	const iterations = 100
	stages := []string{"issue-pickup", "feature-dev", "feature-validate", "pr-create"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				stage := stages[(id+j)%len(stages)]
				switch (id + j) % 4 {
				case 0:
					be.RecordStageTokens(stage, 10, 20)
				case 1:
					_ = be.TotalUsed()
					_ = be.StageUsed(stage)
				case 2:
					_ = be.CheckPipelineBudget()
					_ = be.CheckStageBudget(stage)
				case 3:
					if j%50 == 0 {
						be.Reset()
					} else {
						be.RecordStageTokens(stage, 5, 5)
					}
				}
			}
		}(i)
	}
	wg.Wait()

	// Final state should be internally consistent (non-negative).
	if be.TotalUsed() < 0 {
		t.Errorf("TotalUsed should not be negative, got %d", be.TotalUsed())
	}
	for _, s := range stages {
		if be.StageUsed(s) < 0 {
			t.Errorf("StageUsed(%q) should not be negative, got %d", s, be.StageUsed(s))
		}
	}
}

// Issue #3217 — BudgetEnforcer mode-aware behavior.

// TestBudgetEnforcer_MaximumModeObserveOnly verifies that when
// PerformanceMode == "maximum" AND DisableBudgetCeiling is true, the
// enforcer emits warn-style decisions (Reason suffixed `_observe_only`)
// instead of terminating the run, even under "hard" mode.
func TestBudgetEnforcer_MaximumModeObserveOnly(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 1000,
		PerStageCeilings:      map[string]int{"feature-dev": 500},
		GracePercent:          50, // pipeline effective limit = 1500
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)
	be.SetPerformanceMode("maximum", true)

	// Exceed both pipeline (1500) and stage (750) effective limits.
	be.RecordStageTokens("feature-dev", 1200, 800) // 2000 > 1500 pipeline; 2000 > 750 stage

	pd := be.CheckPipelineBudget()
	if pd.ShouldTerminate {
		t.Error("maximum + disableCeiling should suppress termination, got ShouldTerminate=true")
	}
	if !pd.ShouldWarn {
		t.Error("expected warn-style decision under maximum observe-only path")
	}
	if pd.Reason != "pipeline_budget_exceeded_observe_only" {
		t.Errorf("expected Reason 'pipeline_budget_exceeded_observe_only', got %q", pd.Reason)
	}
	if pd.PerformanceMode != "maximum" {
		t.Errorf("expected PerformanceMode='maximum', got %q", pd.PerformanceMode)
	}

	sd := be.CheckStageBudget("feature-dev")
	if sd.ShouldTerminate {
		t.Error("maximum + disableCeiling should suppress stage termination")
	}
	if !sd.ShouldWarn {
		t.Error("expected stage warn-style decision under maximum observe-only path")
	}
	if sd.Reason != "stage_budget_exceeded_observe_only" {
		t.Errorf("expected Reason 'stage_budget_exceeded_observe_only', got %q", sd.Reason)
	}
	if sd.PerformanceMode != "maximum" {
		t.Errorf("expected PerformanceMode='maximum', got %q", sd.PerformanceMode)
	}
}

// TestBudgetEnforcer_DecisionCarriesMode asserts every decision shape
// (terminate, warn, no-op) populates BudgetDecision.PerformanceMode so
// downstream log lines / metrics can attribute the result to a mode.
func TestBudgetEnforcer_DecisionCarriesMode(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 1000,
		PerStageCeilings:      map[string]int{"feature-dev": 500},
		GracePercent:          50,
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)
	be.SetPerformanceMode("elevated", false)

	// No-op: under both ceilings.
	pd := be.CheckPipelineBudget()
	if pd.PerformanceMode != "elevated" {
		t.Errorf("no-op decision: expected PerformanceMode='elevated', got %q", pd.PerformanceMode)
	}

	// Warn: over base, under effective.
	be.RecordStageTokens("feature-dev", 700, 500) // 1200 > 1000 base, < 1500 effective
	pd = be.CheckPipelineBudget()
	if !pd.ShouldWarn {
		t.Fatalf("expected ShouldWarn=true after over-base usage")
	}
	if pd.PerformanceMode != "elevated" {
		t.Errorf("warn decision: expected PerformanceMode='elevated', got %q", pd.PerformanceMode)
	}

	// Terminate: over effective.
	be.RecordStageTokens("feature-dev", 400, 0) // total = 1600 > 1500 effective
	pd = be.CheckPipelineBudget()
	if !pd.ShouldTerminate {
		t.Fatalf("expected ShouldTerminate=true after over-effective usage")
	}
	if pd.PerformanceMode != "elevated" {
		t.Errorf("terminate decision: expected PerformanceMode='elevated', got %q", pd.PerformanceMode)
	}

	// No-stage-ceiling no-op also carries mode.
	noStage := be.CheckStageBudget("nonexistent-stage")
	if noStage.PerformanceMode != "elevated" {
		t.Errorf("no-stage-ceiling decision: expected PerformanceMode='elevated', got %q", noStage.PerformanceMode)
	}
}

// TestBudgetEnforcer_NonMaximumIgnoresDisableCeiling proves the gate is
// mode-AND-flag: setting DisableBudgetCeiling=true under PerformanceMode !=
// "maximum" must NOT switch to observe-only — hard-mode termination still
// fires.
func TestBudgetEnforcer_NonMaximumIgnoresDisableCeiling(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 1000,
		GracePercent:          50,
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)
	// Wrong combination: elevated mode but flag accidentally set true.
	be.SetPerformanceMode("elevated", true)

	be.RecordStageTokens("feature-dev", 1000, 600) // 1600 > 1500 effective

	pd := be.CheckPipelineBudget()
	if !pd.ShouldTerminate {
		t.Error("elevated + disableCeiling=true should still terminate (gate is mode-AND-flag)")
	}
	if pd.Reason != "pipeline_budget_exceeded" {
		t.Errorf("expected Reason 'pipeline_budget_exceeded', got %q", pd.Reason)
	}
	if pd.PerformanceMode != "elevated" {
		t.Errorf("expected PerformanceMode='elevated', got %q", pd.PerformanceMode)
	}
}

// TestBudgetEnforcer_SetPerformanceMode_Concurrent exercises the mutex
// protection on SetPerformanceMode under concurrent RecordStageTokens. The
// race detector and the absence of inconsistent mode reads are the signal.
func TestBudgetEnforcer_SetPerformanceMode_Concurrent(t *testing.T) {
	cfg := BudgetConfig{
		PipelineCeilingTokens: 100000,
		PerStageCeilings:      map[string]int{"feature-dev": 50000},
		GracePercent:          50,
		Mode:                  "hard",
	}
	be := NewBudgetEnforcer(cfg)

	const goroutines = 32
	const iterations = 200
	modes := []struct {
		name    string
		disable bool
	}{
		{"efficiency", false},
		{"elevated", false},
		{"maximum", true},
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				switch (id + j) % 4 {
				case 0:
					be.RecordStageTokens("feature-dev", 5, 5)
				case 1:
					m := modes[(id+j)%len(modes)]
					be.SetPerformanceMode(m.name, m.disable)
				case 2:
					_ = be.CheckPipelineBudget()
				case 3:
					_ = be.CheckStageBudget("feature-dev")
				}
			}
		}(i)
	}
	wg.Wait()

	// Final state: any mode the loop set is acceptable; the assertion is
	// that no goroutine panicked or saw torn writes (race detector enforces
	// this). Read the final decision to ensure the enforcer is still usable.
	final := be.CheckPipelineBudget()
	switch final.PerformanceMode {
	case "efficiency", "elevated", "maximum":
		// expected
	default:
		t.Errorf("unexpected final PerformanceMode %q (want efficiency/elevated/maximum)", final.PerformanceMode)
	}
}

func TestReadBudgetOverrun_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "budget-overrun-99.json")
	if err := os.WriteFile(fp, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadBudgetOverrun(fp)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestBudgetEnforcer_ConcurrentRecord documents the contract that
// BudgetEnforcer is safe for concurrent use by parallel-wave subagents
// (see issue #3198). Without locking, the stageUsed map races and the
// program panics with "concurrent map writes". Run with `-race` to detect
// regressions; the totalUsed assertion also catches lost updates if a
// future change replaces the mutex with a non-atomic write.
func TestBudgetEnforcer_ConcurrentRecord(t *testing.T) {
	be := NewBudgetEnforcer(DefaultBudgetConfig())

	const goroutines = 32
	const callsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(stage string) {
			defer wg.Done()
			for i := 0; i < callsPerGoroutine; i++ {
				be.RecordStageTokens(stage, 1, 1)
				_ = be.TotalUsed()
				_ = be.StageUsed(stage)
				_ = be.CheckPipelineBudget()
			}
		}("stage-shared")
	}
	wg.Wait()

	wantTotal := goroutines * callsPerGoroutine * 2
	if got := be.TotalUsed(); got != wantTotal {
		t.Errorf("TotalUsed: got %d, want %d (lost updates indicate a sync bug)", got, wantTotal)
	}
	if got := be.StageUsed("stage-shared"); got != wantTotal {
		t.Errorf("StageUsed: got %d, want %d", got, wantTotal)
	}
}
