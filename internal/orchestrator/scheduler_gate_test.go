package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// stubGate is a minimal StageGate implementation for tests.
type stubGate struct {
	name   string
	passed bool
	reason string
	calls  int
}

func (s *stubGate) Name() string { return s.name }

func (s *stubGate) Verify(_ context.Context, _ int, _ string) gates.GateResult {
	s.calls++
	return gates.GateResult{
		GateName: s.name,
		Passed:   s.passed,
		Reason:   s.reason,
	}
}

// TestScheduler_WithStageGates_Override confirms the test seam works as
// advertised — WithStageGates replaces the registry, and nil restores the
// default. Issue #3266.
func TestScheduler_WithStageGates_Override(t *testing.T) {
	s := &Scheduler{stageGates: gates.Default()}

	stub := &stubGate{name: "issue-pickup", passed: false, reason: "rejected"}
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StageIssuePickup: stub,
	})
	if _, ok := s.stageGates[state.StageIssuePickup]; !ok {
		t.Fatalf("stub gate missing from registry after WithStageGates")
	}
	if _, ok := s.stageGates[state.StagePRMerge]; ok {
		t.Errorf("expected non-overridden stages to be absent from custom registry")
	}

	// nil restores the default 6-gate registry.
	s.WithStageGates(nil)
	if len(s.stageGates) < 6 {
		t.Errorf("WithStageGates(nil) did not restore defaults; size=%d", len(s.stageGates))
	}
}

// TestScheduler_GateFailure_ConvertsToStageFailure simulates the relevant
// slice of the stage loop: a successful skill exit followed by a gate that
// rejects the post-state. The gate rejection must:
//   - Append a StageGateResult on the runtime state
//   - Convert the success into an error matching `stage gate failed: ...`
//
// This mirrors the scheduler's integration logic without spinning up the
// full runPipeline plumbing.
func TestScheduler_GateFailure_ConvertsToStageFailure(t *testing.T) {
	stage := state.StageFeatureDev
	rs := state.NewRuntimeState("o/r", 42, "item-1")
	rs.BeginStage(stage)
	rs.CompleteStage(0, 100, 200, "claude-sonnet-4-6")

	gate := &stubGate{
		name:   "feature-dev",
		passed: false,
		reason: "dev context records zero file changes",
	}
	registry := map[state.PipelineStage]gates.StageGate{stage: gate}

	// Replay the post-RunStage gate hook from scheduler.runPipeline.
	var stageErr error
	exitCode := 0
	if g, ok := registry[stage]; ok {
		gr := g.Verify(context.Background(), 42, t.TempDir())
		rs.AppendStageGateResult(stage, gr.ToStageGateResult())
		if !gr.Passed {
			stageErr = errors.New("stage gate failed: " + gr.Reason)
			exitCode = 2
		}
	}

	if stageErr == nil {
		t.Fatalf("expected gate failure to produce stage error")
	}
	if exitCode == 0 {
		t.Errorf("exitCode should be non-zero on gate failure")
	}
	if got := rs.StageGateResultsFor(stage); len(got) != 1 || got[0].Passed {
		t.Errorf("StageGateResults not populated correctly: %#v", got)
	}
	if gate.calls != 1 {
		t.Errorf("expected gate called once, got %d", gate.calls)
	}
}

// TestScheduler_TerminalFailure_ReconciledByGate covers #3835 WS1: a terminal
// stage reports failure (non-zero exit) but its post-condition gate confirms the
// work actually landed (PR merged). The reconciliation must clear the error so
// the operator is not paged and autonomous is not paused on completed work.
func TestScheduler_TerminalFailure_ReconciledByGate(t *testing.T) {
	stage := state.StagePRMerge
	rs := state.NewRuntimeState("o/r", 3806, "item-1")
	rs.BeginStage(stage)
	rs.CompleteStage(0, 100, 200, "claude-opus-4-8")

	// Skill reported failure (e.g. hit a 429 right after merging the PR).
	stageErr := errors.New("API Error: 429")
	exitCode := 1
	gate := &stubGate{name: "pr-merge", passed: true, reason: "PR is MERGED"}
	registry := map[state.PipelineStage]gates.StageGate{stage: gate}

	// Replay the #3835 reconciliation hook from scheduler.runPipeline.
	gateRan := false
	if (stageErr != nil || exitCode != 0) && !gateRan && isTerminalStage(stage) {
		if g, ok := registry[stage]; ok && g != nil {
			recon := g.Verify(context.Background(), 3806, t.TempDir())
			gateRan = true
			rs.AppendStageGateResult(stage, recon.ToStageGateResult())
			if recon.Passed {
				stageErr = nil
				exitCode = 0
			}
		}
	}

	if stageErr != nil || exitCode != 0 {
		t.Errorf("terminal failure with passing gate should reconcile to success; err=%v exit=%d", stageErr, exitCode)
	}
	if gate.calls != 1 {
		t.Errorf("expected reconciliation gate called once, got %d", gate.calls)
	}
	if got := rs.StageGateResultsFor(stage); len(got) != 1 || !got[0].Passed {
		t.Errorf("reconciliation gate result not recorded as passed: %#v", got)
	}
}

// TestScheduler_TerminalFailure_GateAlsoFails confirms a genuine terminal
// failure (gate also fails — the PR was never merged) is NOT reconciled.
func TestScheduler_TerminalFailure_GateAlsoFails(t *testing.T) {
	stage := state.StagePRMerge
	rs := state.NewRuntimeState("o/r", 477, "item-1")
	rs.BeginStage(stage)
	rs.CompleteStage(0, 100, 200, "claude-opus-4-8")

	stageErr := errors.New("merge failed")
	exitCode := 1
	gate := &stubGate{name: "pr-merge", passed: false, reason: "PR #477 is not MERGED (state=OPEN)"}
	registry := map[state.PipelineStage]gates.StageGate{stage: gate}

	gateRan := false
	if (stageErr != nil || exitCode != 0) && !gateRan && isTerminalStage(stage) {
		if g, ok := registry[stage]; ok && g != nil {
			recon := g.Verify(context.Background(), 477, t.TempDir())
			gateRan = true
			rs.AppendStageGateResult(stage, recon.ToStageGateResult())
			if recon.Passed {
				stageErr = nil
				exitCode = 0
			}
		}
	}

	if stageErr == nil || exitCode == 0 {
		t.Errorf("genuine terminal failure must NOT be reconciled when gate fails")
	}
}

// TestScheduler_NonTerminalFailure_NotReconciled confirms terminal-stage
// classification: feature-* stages are non-terminal (so the #3835 terminal
// post-condition-gate block never runs for them) while pr-create / pr-merge are
// terminal.
//
// Note (#3873): non-terminal stages are no longer *blanket* non-reconciled — a
// non-terminal failure on an already-resolved issue (closed / branch PR landed)
// now reconciles via the separate `reconcileIssueResolved` forge check (see
// scheduler_nonterminal_reconcile_test.go). This test asserts only the
// terminal-stage membership that gates the #3835 block; the fail-closed
// genuinely-open path is pinned by
// TestScheduler_NonTerminalFailure_GenuinelyOpen_NotReconciled.
func TestScheduler_NonTerminalFailure_NotReconciled(t *testing.T) {
	if isTerminalStage(state.StageFeatureValidate) {
		t.Errorf("feature-validate must not be treated as a terminal stage")
	}
	if isTerminalStage(state.StageFeatureDev) {
		t.Errorf("feature-dev must not be treated as a terminal stage")
	}
	if !isTerminalStage(state.StagePRCreate) || !isTerminalStage(state.StagePRMerge) {
		t.Errorf("pr-create and pr-merge must be terminal stages")
	}
}

// TestScheduler_GatePass_DoesNotErrorOut covers the happy path: the gate
// reports passed=true, the runtime records the result, and no synthetic
// error is created.
func TestScheduler_GatePass_DoesNotErrorOut(t *testing.T) {
	stage := state.StageIssuePickup
	rs := state.NewRuntimeState("o/r", 42, "item-1")
	rs.BeginStage(stage)
	rs.CompleteStage(0, 100, 200, "claude-sonnet-4-6")

	gate := &stubGate{name: "issue-pickup", passed: true, reason: "ok"}
	registry := map[state.PipelineStage]gates.StageGate{stage: gate}

	var stageErr error
	if g, ok := registry[stage]; ok {
		gr := g.Verify(context.Background(), 42, t.TempDir())
		rs.AppendStageGateResult(stage, gr.ToStageGateResult())
		if !gr.Passed {
			stageErr = fmt.Errorf("stage gate failed: %s", gr.Reason)
		}
	}

	if stageErr != nil {
		t.Errorf("expected no error on gate pass; got %v", stageErr)
	}
	got := rs.StageGateResultsFor(stage)
	if len(got) != 1 || !got[0].Passed {
		t.Errorf("StageGateResults not recorded as passed: %#v", got)
	}
}
