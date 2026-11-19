package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Issue #1848: an operator-initiated stop must be recorded as a distinct
// non-fault kind, must not charge the lifetime cap, must survive a pr-merge
// hand-back interrupted by an admission stop, and no run may be booked failed
// without naming its stage and reason.

// TestCancelForOperatorStop_CancelsMatchingStagesWithCause pins the IPC entry
// point: only the named run's stage context is cancelled, and its cause is the
// operator stop — the structural fact every downstream reader keys on.
func TestCancelForOperatorStop_CancelsMatchingStagesWithCause(t *testing.T) {
	t.Parallel()

	s := newSchedulerForDeterministicTest()
	stopCtx, stopCancel := context.WithCancelCause(context.Background())
	otherCtx, otherCancel := context.WithCancelCause(context.Background())
	defer stopCancel(nil)
	defer otherCancel(nil)
	s.registerActiveStage("run-a", "owner/repo", 1848, stopCancel)
	s.registerActiveStage("run-b", "owner/repo", 7, otherCancel)

	got := s.CancelForOperatorStop("owner/repo", 1848)
	if len(got) != 1 || got[0] != 1848 {
		t.Fatalf("cancelled = %v, want [1848]", got)
	}
	if !IsOperatorStop(stopCtx) {
		t.Errorf("stopped stage cause = %v, want ErrOperatorStop", context.Cause(stopCtx))
	}
	if otherCtx.Err() != nil {
		t.Errorf("an unrelated issue's stage was cancelled: %v", context.Cause(otherCtx))
	}
	if got := s.CancelForOperatorStop("other/repo", 7); len(got) != 0 {
		t.Errorf("a different repository's number matched: %v", got)
	}

	// Stop All: no issue named.
	if got := s.CancelForOperatorStop("", 0); len(got) != 2 {
		t.Errorf("Stop All cancelled %v, want both runs", got)
	}
	if !IsOperatorStop(otherCtx) {
		t.Errorf("Stop All left cause %v, want ErrOperatorStop", context.Cause(otherCtx))
	}
}

// TestIsOperatorStop_OnlyTheOperatorCause guards the predicate against a
// widening that would hide real faults behind the stop exemption.
func TestIsOperatorStop_OnlyTheOperatorCause(t *testing.T) {
	t.Parallel()

	if IsOperatorStop(context.Background()) {
		t.Error("a live context reads as stopped")
	}
	plain, cancel := context.WithCancel(context.Background())
	cancel()
	if IsOperatorStop(plain) {
		t.Error("a plain cancel reads as an operator stop")
	}
	net, netCancel := context.WithCancelCause(context.Background())
	netCancel(ErrNetworkUnavailable)
	if IsOperatorStop(net) {
		t.Error("a network-outage cancel reads as an operator stop")
	}
}

// TestOperatorStop_RuntimeVerdictReachesTheCapExemption is the AC3 regression
// on the path the incident took: the scheduler records the stop as a
// structured verdict on the runtime, the autonomous completion callback
// resolves the kind from that verdict (not from the SIGTERM'd process's text),
// and the issue's lifetime failure count stays empty with no Stop pending.
//
// Serial: sets the environment (via newAutonomousForCascadeTest).
func TestOperatorStop_RuntimeVerdictReachesTheCapExemption(t *testing.T) {
	rs := state.NewRuntimeState("octocat/acme-app", 1848, "item-id", testRunID())
	rs.BeginStage(state.StageFeatureDev)
	rs.SetStageOperatorStoppedError(state.StageFeatureDev, "exit 143")
	snap := rs.Snapshot()

	// The kind the autonomous completion callback hands onPipelineComplete.
	kind, _, _ := completionTerminalKind(rs, false)
	if kind != TerminalKindOperatorStop {
		t.Fatalf("completion kind = %q, want %q", kind, TerminalKindOperatorStop)
	}
	_ = snap

	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	addRunning(as, "octocat/acme-app", 1848, "issue")
	as.onPipelineComplete("octocat/acme-app", 1848, false, false, kind, "exit 143", false)
	as.drainBackground()
	if len(as.state.LifetimeIssueFailures) != 0 {
		t.Errorf("LifetimeIssueFailures = %v, want empty — the operator stopped this run",
			as.state.LifetimeIssueFailures)
	}

	// The same text without the verdict is not a stop: the flag, not the
	// text, is what classifies.
	plain := state.NewRuntimeState("octocat/acme-app", 1849, "item-id", testRunID())
	plain.BeginStage(state.StageFeatureDev)
	plain.SetStageError(state.StageFeatureDev, "exit 143")
	if k, _, _ := completionTerminalKind(plain, false); k == TerminalKindOperatorStop {
		t.Errorf("a stage error without the verdict resolved to %q", k)
	}

	// A snapshot is a deep copy: mutating it never reaches the live state.
	snap.StageOperatorStopped[string(state.StageFeatureDev)] = false
	if !rs.Snapshot().StageOperatorStopped[string(state.StageFeatureDev)] {
		t.Error("mutating a snapshot changed the live operator-stop verdict")
	}

	// A later attempt's own error clears the verdict.
	rs.SetStageError(state.StageFeatureDev, "a real failure")
	if rs.Snapshot().StageOperatorStopped[string(state.StageFeatureDev)] {
		t.Error("SetStageError left a stale operator-stop verdict")
	}

	// So does a successful completion of the stopped stage on resume.
	done := state.NewRuntimeState("octocat/acme-app", 1850, "item-id", testRunID())
	done.BeginStage(state.StageFeatureDev)
	done.SetStageOperatorStoppedError(state.StageFeatureDev, "exit 143")
	done.CompleteStage(0, tokens.TokenCounts{}, "sonnet", "claude")
	if done.Snapshot().StageOperatorStopped[string(state.StageFeatureDev)] {
		t.Error("a successful completion left a stale operator-stop verdict")
	}
}

// operatorStopRunner stands in for a non-IPC stage runner (the CLI shape) that
// knows nothing of stops: on the target stage it asks the scheduler for an
// operator stop the way pipeline.cancelForOperatorStop does, blocks until its
// context ends, and exits 143 with Cancelled=false. Only the stage context's
// cause can tell the scheduler what happened.
type operatorStopRunner struct {
	*runIDCapturingRunner
	s      *Scheduler
	target state.PipelineStage
	hits   int
}

func (r *operatorStopRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	if params.Stage != r.target {
		return r.runIDCapturingRunner.RunStage(ctx, params)
	}
	r.hits++
	r.s.CancelForOperatorStop(params.Repo, params.IssueNumber)
	<-ctx.Done()
	return &StageRunResult{ExitCode: 143}, errors.New("exit status 143")
}

// TestRunPipeline_OperatorStopBooksOperatorStop drives the whole scheduler
// path (#1848 AC1-AC3): a stop delivered to a running stage ends the run
// operator_stop, records the structured verdict the autonomous callback reads,
// and is neither retried nor escalated.
//
// Serial: sets the environment (via isolateRoutingEnv).
func TestRunPipeline_OperatorStopBooksOperatorStop(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	base := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, base)
	runner := &operatorStopRunner{runIDCapturingRunner: base, s: s, target: state.StageFeatureDev}
	s.WithStageRunner(runner)
	reader := &boundaryReader{states: []string{"OPEN"}}
	s.WithIssueStateReader(reader.read)
	// The terminal path reconciles the issue's state with the forge; the issue
	// is open, which is what makes a stopped run resumable.
	stubReconcileGh(t, func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"state":"OPEN"}`), nil
	})
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	success, kind := s.runPipeline(context.Background(), boundaryItem(991848))

	if success || kind != TerminalKindOperatorStop {
		t.Fatalf("runPipeline = (%v, %q), want (false, %q)", success, kind, TerminalKindOperatorStop)
	}
	if runner.hits != 1 {
		t.Errorf("stopped stage dispatched %d times, want 1 — a stop must not be retried or escalated", runner.hits)
	}
	if snap == nil {
		t.Fatal("no completion snapshot")
	}
	if !snap.StageOperatorStopped[string(state.StageFeatureDev)] {
		t.Errorf("StageOperatorStopped = %v, want the stopped stage marked", snap.StageOperatorStopped)
	}
	if got, _, _ := completionTerminalKind(snap, false); got != TerminalKindOperatorStop {
		t.Errorf("autonomous completion kind = %q, want %q", got, TerminalKindOperatorStop)
	}
}

// TestRecordV2History_FailedRecordAlwaysAttributed drives the scheduler's
// run-record writer (#1848 AC5): a failed run with no stage error still names
// its stage and a reason, and an operator-stopped run is written cancelled.
func TestRecordV2History_FailedRecordAlwaysAttributed(t *testing.T) {
	t.Parallel()

	const repo = "nightgauge/nightgauge"
	root := gitWorkspace(t)
	s := &Scheduler{}

	bare := state.NewRuntimeState(repo, 1848, "item-1848", testRunID())
	bare.BeginStage(state.StagePRMerge)
	s.recordV2History(types.BoardItem{Number: 1848, Repo: repo, Title: "t"}, bare.Snapshot(),
		false, root, 3, "standard", "", nil, nil)
	rec := findHistoryRecord(t, root, 1848)
	if rec.Outcome != RunOutcomeFailed || rec.FailureStage != string(state.StagePRMerge) ||
		rec.TerminalFailureDetail == "" {
		t.Errorf("failed record = outcome %q stage %q detail %q, want failed/pr-merge/non-empty",
			rec.Outcome, rec.FailureStage, rec.TerminalFailureDetail)
	}

	stopped := state.NewRuntimeState(repo, 1849, "item-1849", testRunID())
	stopped.BeginStage(state.StagePRMerge)
	stopped.SetStageOperatorStoppedError(state.StagePRMerge, "exit 143")
	s.recordV2History(types.BoardItem{Number: 1849, Repo: repo, Title: "t"}, stopped.Snapshot(),
		false, root, 3, "standard", TerminalKindOperatorStop, nil, nil)
	rec = findHistoryRecord(t, root, 1849)
	if rec.Outcome != "cancelled" || rec.OutcomeType != OutcomeTypeOperatorStopped || rec.FailureStage != "" {
		t.Errorf("stopped record = outcome %q type %q stage %q, want cancelled/%s/empty",
			rec.Outcome, rec.OutcomeType, rec.FailureStage, OutcomeTypeOperatorStopped)
	}
}

// TestPRMergeHandback_AdmissionStopIsInterruptedNotFailed pins AC4: a pushed
// fix handed back while admission is stopped ends Cancelled — no
// auto-fix-exhausted, no extra fix session — and the runtime stays at
// pr-merge so the run resumes into it for the still-open PR.
func TestPRMergeHandback_AdmissionStopIsInterruptedNotFailed(t *testing.T) {
	t.Parallel()

	s, skill, _, rs, root := handbackFixture(t, true, failedCI)
	s.prAutoFixMaxAttempts = 3
	s.SetAdmissionStopped(func() bool { return true })
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	res, err := s.runPRMergeSkillWithFixHandback(context.Background(), StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err != nil {
		t.Fatalf("err = %v, want nil — an interrupted hand-back is not a failure", err)
	}
	if !res.Cancelled {
		t.Errorf("Cancelled = false, want true")
	}
	if strings.Contains(res.ErrorText, "auto-fix-exhausted") {
		t.Errorf("ErrorText = %q, booked as an exhausted auto-fix", res.ErrorText)
	}
	if skill.calls != 1 {
		t.Errorf("skill calls = %d, want 1 — a stopped run must not start another fix session", skill.calls)
	}
	if got := rs.Snapshot().Stage; got != state.StagePRMerge {
		t.Errorf("runtime stage = %q, want %q so the resume re-enters pr-merge", got, state.StagePRMerge)
	}
}

// TestPRMergeHandback_OperatorStopCauseIsInterrupted covers the same hand-back
// when the stage context itself was stopped, and that a network-outage cancel
// is left to its own classification.
func TestPRMergeHandback_OperatorStopCauseIsInterrupted(t *testing.T) {
	t.Parallel()

	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	s, _, _, rs, root := handbackFixture(t, true, failedCI)
	s.prAutoFixMaxAttempts = 3
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrOperatorStop)
	res, err := s.runPRMergeSkillWithFixHandback(ctx, StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err != nil || !res.Cancelled {
		t.Fatalf("operator-stop ctx: Cancelled=%v err=%v, want Cancelled and no error", res.Cancelled, err)
	}

	s2, _, _, rs2, root2 := handbackFixture(t, true, failedCI)
	s2.prAutoFixMaxAttempts = 1
	netCtx, netCancel := context.WithCancelCause(context.Background())
	netCancel(ErrNetworkUnavailable)
	res2, _ := s2.runPRMergeSkillWithFixHandback(netCtx, StageRunParams{Stage: state.StagePRMerge}, rs2, item, root2)
	if res2.Cancelled {
		t.Error("a network-outage cancel was booked as an operator stop")
	}
}

// TestPRMergeHandback_NoStopStillExhausts guards the other direction: without
// a stop the hand-back's budget exhaustion is still a failure.
func TestPRMergeHandback_NoStopStillExhausts(t *testing.T) {
	t.Parallel()

	s, _, _, rs, root := handbackFixture(t, true, failedCI)
	s.prAutoFixMaxAttempts = 1
	s.SetAdmissionStopped(func() bool { return false })
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	res, err := s.runPRMergeSkillWithFixHandback(context.Background(), StageRunParams{Stage: state.StagePRMerge}, rs, item, root)
	if err == nil || !strings.Contains(err.Error(), "auto-fix-exhausted") || res.Cancelled {
		t.Fatalf("Cancelled=%v err=%v, want auto-fix-exhausted failure", res.Cancelled, err)
	}
}

// TestApplyNonFailureOutcome_OperatorStopIsCancelled pins the run-record half
// of AC4: an operator stop books outcome cancelled / operator_stopped and keeps
// its terminal kind, while the pipeline_done success predicate stays narrow.
func TestApplyNonFailureOutcome_OperatorStopIsCancelled(t *testing.T) {
	t.Parallel()

	rec := state.V2RunRecord{Outcome: "failed", TerminalFailureKind: TerminalKindOperatorStop}
	ApplyNonFailureOutcome(&rec)
	EnsureFailureAttribution(&rec, "pr-merge", "")
	if rec.Outcome != "cancelled" || rec.OutcomeType != OutcomeTypeOperatorStopped {
		t.Errorf("outcome = %q/%q, want cancelled/%s", rec.Outcome, rec.OutcomeType, OutcomeTypeOperatorStopped)
	}
	if rec.TerminalFailureKind != TerminalKindOperatorStop {
		t.Errorf("terminal kind = %q, want retained %q", rec.TerminalFailureKind, TerminalKindOperatorStop)
	}
	if rec.FailureStage != "" {
		t.Errorf("a cancelled record was given failure attribution: stage %q", rec.FailureStage)
	}
	if IsNonFailureTerminalKind(TerminalKindOperatorStop) {
		t.Error("operator_stop widened IsNonFailureTerminalKind — an interrupted run would report success")
	}
}

// TestEnsureFailureAttribution pins AC5: a failed record always names both its
// stage and its reason, and any other outcome is left untouched.
func TestEnsureFailureAttribution(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                  string
		rec                   state.V2RunRecord
		stage, reason         string
		wantStage, wantDetail string
	}{
		{"empty everything", state.V2RunRecord{Outcome: "failed"}, "", "",
			UnknownFailureStage, UnclassifiedFailureReason},
		{"runtime stage and error", state.V2RunRecord{Outcome: "failed"}, "pr-merge", "boom",
			"pr-merge", "boom"},
		{"kind but no error", state.V2RunRecord{Outcome: "failed", TerminalFailureKind: TerminalKindStallKill},
			"feature-dev", "", "feature-dev", TerminalKindStallKill},
		{"existing attribution kept", state.V2RunRecord{Outcome: "failed", FailureStage: "pr-create", TerminalFailureDetail: "kept"},
			"pr-merge", "other", "pr-create", "kept"},
		{"succeeded untouched", state.V2RunRecord{Outcome: "complete"}, "pr-merge", "boom", "", ""},
		{"cancelled untouched", state.V2RunRecord{Outcome: "cancelled"}, "pr-merge", "boom", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			EnsureFailureAttribution(&rec, tc.stage, tc.reason)
			if rec.FailureStage != tc.wantStage || rec.TerminalFailureDetail != tc.wantDetail {
				t.Errorf("got stage=%q detail=%q, want %q/%q",
					rec.FailureStage, rec.TerminalFailureDetail, tc.wantStage, tc.wantDetail)
			}
			if rec.Outcome == "failed" && (rec.FailureStage == "" || rec.TerminalFailureDetail == "") {
				t.Error("a failed record is left without stage or reason")
			}
		})
	}
	EnsureFailureAttribution(nil, "x", "y") // nil-safe
}
