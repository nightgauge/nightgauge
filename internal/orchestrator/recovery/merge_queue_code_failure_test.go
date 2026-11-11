package recovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

const codeFailureIssue = 3193

func writeQueueFailure(t *testing.T, ws, check, step, excerpt string) {
	t.Helper()
	dir := layouttest.MkPipelineDir(t, ws)
	data, _ := json.Marshal(pmstages.MergeQueueFailureRecord{
		IssueNumber: codeFailureIssue, PRNumber: 3194, Class: string(pmstages.MergeGroupCode),
		Check: check, JobID: 12, RunID: 1, Step: step, LogExcerpt: excerpt,
	})
	if err := os.WriteFile(filepath.Join(dir, pmstages.MergeQueueFailureFile(codeFailureIssue)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func codeFailure(ws, check, step string) StageFailure {
	return StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
		IssueNumber: codeFailureIssue, PRNumber: 3194, RunID: "run-1",
		StageError: "pr-merge refused: " + pmstages.MergeQueueCodeFailureReason(pmstages.MergeGroupVerdict{
			Class: pmstages.MergeGroupCode, Job: check, Step: step}),
	}
}

func TestMergeQueueCodeFailure_Matches(t *testing.T) {
	a := NewMergeQueueCodeFailure()
	if !a.Matches(codeFailure("/ws", "Go", "Test")) {
		t.Fatal("a marked merge-group code failure did not match")
	}
	conflict := StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/ws", IssueNumber: 1,
		StageError: "pr-merge refused: " + pmstages.ReasonMergeQueueFailed + ": " + pmstages.MergeQueueConflictReason("main", 2, "DIRTY")}
	plain := StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/ws", IssueNumber: 1,
		StageError: "pr-merge refused: merge-queue-failed: merge queue: PR #2 was removed from the queue; failed merge-group checks: Go"}
	infra := StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/ws", IssueNumber: 1,
		StageError: "pr-merge refused: " + pmstages.MergeQueueInfraExhaustedReason(pmstages.MergeGroupVerdict{Job: "E2E", Step: "Install"}, 2)}
	wrongStage := codeFailure("/ws", "Go", "Test")
	wrongStage.Stage = state.StagePRCreate
	for name, f := range map[string]StageFailure{"conflict": conflict, "plain removal": plain, "infra": infra, "wrong stage": wrongStage} {
		if a.Matches(f) {
			t.Errorf("%s matched the code-failure recovery", name)
		}
	}
	if NewConflictRecoveryLoop(2).Matches(codeFailure("/ws", "Go", "Test")) {
		t.Error("conflict recovery matched a code failure")
	}
}

func TestMergeQueueCodeFailure_RewindsOncePerCheck(t *testing.T) {
	ws := layouttest.Repo(t)
	a := NewMergeQueueCodeFailure()

	writeQueueFailure(t, ws, "Go", "Test", "--- FAIL: TestSkipAccounting")
	res := a.Execute(context.Background(), codeFailure(ws, "Go", "Test"))
	if res.Recovered || res.FollowUp != FollowUpStageCanResume || res.BacktrackTargetStage != string(state.StageFeatureDev) {
		t.Fatalf("first removal: got %+v, want a rewind to feature-dev", res)
	}
	fb := readFeedbackSignals(t, ws, codeFailureIssue)
	if len(fb.Signals) != 1 {
		t.Fatalf("signals = %+v", fb.Signals)
	}
	sig := fb.Signals[0]
	if sig.BacktrackTargetStage != "feature-dev" || sig.Severity != "warning" ||
		!strings.Contains(strings.Join(sig.Evidence, "\n"), "--- FAIL: TestSkipAccounting") {
		t.Errorf("signal = %+v, want a feature-dev warning carrying the log excerpt", sig)
	}

	// A different check still gets its one rewind.
	writeQueueFailure(t, ws, "Playwright E2E", "Test (Playwright)", "1 failed")
	if res := a.Execute(context.Background(), codeFailure(ws, "Playwright E2E", "Test (Playwright)")); res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("a different check did not rewind: %+v", res)
	}

	// The same check again fails the run.
	writeQueueFailure(t, ws, "Go", "Test", "--- FAIL: TestSkipAccounting")
	res = a.Execute(context.Background(), codeFailure(ws, "Go", "Test"))
	if res.Recovered || res.FollowUp != FollowUpHumanTriageRequired || res.BacktrackTargetStage != "" ||
		!strings.Contains(res.Reason, "already sent back to feature-dev once") {
		t.Fatalf("second removal for the same check: got %+v, want a failure", res)
	}
	if n := len(readFeedbackSignals(t, ws, codeFailureIssue).Signals); n != 2 {
		t.Errorf("the refused rewind still wrote a signal (%d signals)", n)
	}
}

// The ledger is keyed by run and by job plus failed step (#3198 operator
// steer): a different failing step of the same job, or a later run of the
// same issue, is not refused.
func TestMergeQueueCodeFailure_LedgerKeyedByRunAndStep(t *testing.T) {
	ws := layouttest.Repo(t)
	a := NewMergeQueueCodeFailure()
	rewinds := func(run, check, step string) bool {
		t.Helper()
		writeQueueFailure(t, ws, check, step, "")
		f := codeFailure(ws, check, step)
		f.RunID = run
		return a.Execute(context.Background(), f).FollowUp == FollowUpStageCanResume
	}
	if !rewinds("run-1", "Go build & test", "Test") {
		t.Fatal("first removal did not rewind")
	}
	if !rewinds("run-1", "Go build & test", "Vet") {
		t.Error("a different failed step of the same job was refused")
	}
	if rewinds("run-1", "Go build & test", "Test") {
		t.Error("the same job and step in the same run rewound twice")
	}
	if !rewinds("run-2", "Go build & test", "Test") {
		t.Error("a later run of the issue was refused by the previous run's ledger")
	}
	if rewinds("run-2", "Go build & test", "Test") {
		t.Error("the same job and step rewound twice in the later run")
	}
	if !rewinds("", "Go build & test", "Test") || !rewinds("", "Go build & test", "Test") {
		// A failure with no run cannot be bounded per run; its ledger never
		// matches, so it is never refused by a stale one.
		t.Error("a failure with no run id was refused by a ledger")
	}
}

func TestClearMergeQueueFailureState(t *testing.T) {
	ws := layouttest.Repo(t)
	writeQueueFailure(t, ws, "Go", "Test", "x")
	if res := NewMergeQueueCodeFailure().Execute(context.Background(), codeFailure(ws, "Go", "Test")); res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("got %+v", res)
	}
	dir := layouttest.PipelineDir(t, ws)
	for _, name := range []string{mergeQueueCodeRewindsFile(codeFailureIssue), pmstages.MergeQueueFailureFile(codeFailureIssue)} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("precondition: %s not written: %v", name, err)
		}
	}
	if err := ClearMergeQueueFailureState(ws, codeFailureIssue); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{mergeQueueCodeRewindsFile(codeFailureIssue), pmstages.MergeQueueFailureFile(codeFailureIssue)} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the clear: %v", name, err)
		}
	}
	if err := ClearMergeQueueFailureState(ws, codeFailureIssue); err != nil {
		t.Errorf("clearing absent files: %v", err)
	}
}

func TestMergeQueueCodeFailure_KeepsExistingSignals(t *testing.T) {
	ws := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, ws)
	prior := `{"schema_version":"1.1","issue_number":3193,"signals":[{"signal_type":"PLAN_REVISION_NEEDED","emitted_by_stage":"feature-validate","rationale":"r","evidence":["e"],"severity":"blocking"}]}`
	if err := os.WriteFile(filepath.Join(dir, "feedback-3193.json"), []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQueueFailure(t, ws, "Go", "Test", "")
	if res := NewMergeQueueCodeFailure().Execute(context.Background(), codeFailure(ws, "Go", "Test")); res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("got %+v", res)
	}
	fb := readFeedbackSignals(t, ws, codeFailureIssue)
	if len(fb.Signals) != 2 || fb.Signals[0].SignalType != "PLAN_REVISION_NEEDED" || fb.Signals[1].SignalType != mergeQueueCodeFailureSignal {
		t.Errorf("signals = %+v", fb.Signals)
	}
}

func TestMergeQueueCodeFailure_MissingRecordFails(t *testing.T) {
	ws := layouttest.Repo(t)
	res := NewMergeQueueCodeFailure().Execute(context.Background(), codeFailure(ws, "Go", "Test"))
	if res.FollowUp != FollowUpHumanTriageRequired || res.BacktrackTargetStage != "" {
		t.Fatalf("got %+v, want a failure with no rewind", res)
	}
}

func TestDefaultRegistry_RoutesQueueCodeFailure(t *testing.T) {
	ws := layouttest.Repo(t)
	reg := Default(ws, nil, nil)
	f := codeFailure(ws, "Go", "Test")
	for _, a := range reg.Actions() {
		if a.Matches(f) {
			if a.Name() != "merge-queue-code-failure" {
				t.Fatalf("first matching action is %s, want merge-queue-code-failure", a.Name())
			}
			return
		}
	}
	t.Fatal("no action matched a merge-group code failure")
}

// An exhausted infrastructure removal has spent pr-merge's re-enqueue bound.
// No recovery may re-run the merge runner and start a second round (#3198).
func TestDefaultRegistry_InfraExhaustedIsNotRecovered(t *testing.T) {
	ws := layouttest.Repo(t)
	reg := Default(ws, nil, nil)
	v := pmstages.MergeGroupVerdict{Class: pmstages.MergeGroupInfra, Job: "Playwright webview tests",
		Step: "Install Playwright OS dependencies (cache hit)"}
	for name, f := range map[string]StageFailure{
		"by reason": {Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
			IssueNumber: codeFailureIssue, PRNumber: 3194,
			StageError: "pr-merge refused: " + pmstages.MergeQueueInfraExhaustedReason(v, 2)},
		"by terminal kind": {Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
			IssueNumber: codeFailureIssue, PRNumber: 3194, TerminalKind: "merge_queue_infra_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			if res, ok := reg.TryRecover(context.Background(), f, 0); ok {
				t.Fatalf("recovery %s ran on an exhausted infrastructure removal: %+v", res.Action, res)
			}
		})
	}
}
