package ipc

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestNotifyStageTransition_RetrySuccessClearsTheStageError drives #407 through
// the PRODUCTION attach path: the real `pipeline.notifyStageTransition` handler
// that NewServer registers, invoked with the real
// PipelineNotifyStageTransitionParams wire struct, on a real Server whose
// snapshot lands on a real disk.
//
// This is the extension/HeadlessOrchestrator dispatch path — the one that emits
// "failed" and then, after the scheduler retries, "complete" for the SAME
// stage. Before #407 the run's snapshot carried that stage in BOTH
// completedStages and stageErrors from then on, and every TS applier
// (PipelineStateService x2, PipelineSlotsTracker) applies stageErrors AFTER
// completedStages — so the recovered stage rendered "failed" for the rest of
// the run and dragged the run's outcome badge down with it.
//
// Both the EMITTED stateChanged envelope (what the live UI consumes) and the
// PERSISTED snapshot (what crash reconciliation and the CLI-fallback resolver
// consume) are asserted, because they are two different readers of the same
// contract.
//
// RED-FIRST: run against the pre-#407 internal/state/runtime_state.go via
// `go test -overlay`, this fails at "the recovered stage is still in
// stageErrors".
func TestNotifyStageTransition_RetrySuccessClearsTheStageError(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	// The emitted-event sink. NewServer writes to os.Stdout; the same-package
	// swap is how the other transition tests capture the wire.
	var wire bytes.Buffer
	s.writer = &wire

	const (
		repo  = "acme/widgets"
		issue = 407
		stage = "feature-validate"
	)
	runID := newTestRunID()

	send := func(status, errMsg string, exitCost float64) {
		t.Helper()
		mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
			Repo:            repo,
			IssueNumber:     issue,
			Stage:           stage,
			Status:          status,
			Error:           errMsg,
			RunID:           runID,
			Model:           "claude-sonnet-4-6",
			Adapter:         "claude",
			InputTokens:     9000,
			OutputTokens:    2500,
			CacheReadTokens: 3000,
			CostUsd:         exitCost,
		})
	}

	// Attempt 1: the stage runs and fails.
	send("running", "", 0)
	send("failed", "exit 1: 2 tests failed", 0.19)

	// The failure must be visible while it IS the latest attempt — otherwise
	// this test could pass by never recording anything at all.
	if got := latestEmittedState(t, &wire).StageErrors[stage]; got != "exit 1: 2 tests failed" {
		t.Fatalf("precondition: a failed transition must record the error; stageErrors[%s] = %q", stage, got)
	}

	// Attempt 2: the scheduler retries the same stage and it succeeds.
	send("running", "", 0)
	send("complete", "", 0.24)

	emitted := latestEmittedState(t, &wire)
	if msg, ok := emitted.StageErrors[stage]; ok {
		t.Errorf("the emitted stateChanged snapshot still carries stageErrors[%s] = %q "+
			"after the stage completed on retry — the TS appliers apply stageErrors "+
			"after completedStages, so the UI renders this stage failed", stage, msg)
	}
	if !hasCompletedStage(emitted, stage) {
		t.Errorf("the emitted snapshot does not list %s in completedStages: %+v", stage, emitted.CompletedStages)
	}

	persisted, err := state.LoadPersistedState(filepath.Join(root, ".nightgauge", "pipeline"), runID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if msg, ok := persisted.StageErrors[stage]; ok {
		t.Errorf("the persisted crash snapshot still carries stageErrors[%s] = %q after recovery", stage, msg)
	}
	if !hasCompletedStage(persisted, stage) {
		t.Errorf("the persisted snapshot does not list %s in completedStages: %+v", stage, persisted.CompletedStages)
	}
}

// TestNotifyStageTransition_TerminalFailureKeepsTheStageError is the other
// direction of the same contract, on the same production path: a stage that
// fails and is NEVER re-run keeps its entry. The clear site is completion and
// only completion, so nothing about #407 can quietly turn a terminal failure
// green.
func TestNotifyStageTransition_TerminalFailureKeepsTheStageError(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	var wire bytes.Buffer
	s.writer = &wire

	const (
		repo  = "acme/widgets"
		issue = 4071
	)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "failed",
		Error: "exit 1: build failed", RunID: runID,
		InputTokens: 4000, OutputTokens: 900, CostUsd: 0.12,
	})
	// A LATER stage completing must not launder the earlier stage's failure.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "complete", RunID: runID,
		InputTokens: 1000, OutputTokens: 200, CostUsd: 0.02,
	})

	emitted := latestEmittedState(t, &wire)
	if got := emitted.StageErrors["feature-dev"]; got != "exit 1: build failed" {
		t.Errorf("stageErrors[feature-dev] = %q after a different stage completed, want the failure preserved", got)
	}
}

// --- helpers ---------------------------------------------------------------

// latestEmittedState decodes the most recent pipeline.stateChanged envelope the
// server wrote to the wire. Reading the EVENT rather than the in-memory runtime
// is deliberate: the event's `state` payload is the exact JSON the TS appliers
// receive, so a field that never survives marshalling cannot pass this test.
func latestEmittedState(t *testing.T, wire *bytes.Buffer) *state.RuntimeState {
	t.Helper()
	var last *state.RuntimeState
	dec := json.NewDecoder(bytes.NewReader(wire.Bytes()))
	for dec.More() {
		var evt struct {
			Event string `json:"event"`
			Data  struct {
				State *state.RuntimeState `json:"state"`
			} `json:"data"`
		}
		if err := dec.Decode(&evt); err != nil {
			t.Fatalf("decode emitted wire line: %v", err)
		}
		if evt.Event == "pipeline.stateChanged" && evt.Data.State != nil {
			last = evt.Data.State
		}
	}
	if last == nil {
		t.Fatalf("no pipeline.stateChanged event was emitted; wire = %s", wire.String())
	}
	return last
}

func hasCompletedStage(rs *state.RuntimeState, stage string) bool {
	for _, sr := range rs.CompletedStages {
		if string(sr.Stage) == stage {
			return true
		}
	}
	return false
}
