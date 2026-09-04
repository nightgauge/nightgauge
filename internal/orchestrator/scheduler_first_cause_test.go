package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// firstCauseStageRunner exits 0 WITHOUT writing the stage's output context —
// the shape that reaches #2870's post-condition check — and reports whatever
// output tail the test hands it.
//
// This is the #878 shape exactly: the stage's real failure (a `git push` with
// no credentials) happened inside the stage and was logged; the stage still
// ended its turn cleanly, so the only thing the scheduler observes directly is
// "the output context is missing".
type firstCauseStageRunner struct {
	mu     sync.Mutex
	tail   string
	models []string
	rt     *state.RuntimeState
}

func (r *firstCauseStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.models = append(r.models, params.Model)
	if r.rt == nil {
		r.rt = params.Runtime
	}
	r.mu.Unlock()
	// Deliberately does NOT write params.OutputFile.
	return &StageRunResult{ExitCode: 0, LastOutputLines: r.tail}, nil
}

func (r *firstCauseStageRunner) dispatches() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.models))
	copy(out, r.models)
	return out
}

// authFailureTail is a trimmed capture of the observed #878 run's stage output:
// the push failure, then the ordinary trailing chatter that follows it.
const authFailureTail = `Running pre-submission validation...
All checks passed.
$ git push -u origin fix/878-example
error: failed to push some refs: invalid auth method
The stage could not publish the branch.
`

func newFirstCauseScheduler(root string, runner StageRunner) *Scheduler {
	s := newRefusalScheduler(root, runner)
	// The DEFAULT retry config, not a hand-rolled one: escalation needs a model
	// ladder, and a config without one makes every "did not escalate" assertion
	// below pass for the wrong reason.
	cfg := DefaultRetryConfig()
	cfg.MaxBacktracks = 0
	cfg.MaxEscalationsPerStage = 1
	s.retryEngine = NewRetryEngine(cfg)
	return s
}

// TestAuthFailureDoesNotEscalateModel is the #878 regression.
//
// The observed run failed on a credential-less `git push`, and the pipeline
// answered by escalating haiku → sonnet and re-dispatching an identical
// 67,610-character prompt, which failed at the same line 44 seconds later.
// Escalation is for CAPABILITY shortfalls; no model can supply a credential the
// machine does not have. The assertion is on DISPATCH COUNT, not on a log line:
// the cost of this bug is a second full stage dispatch, so that is the thing
// that must not happen.
func TestAuthFailureDoesNotEscalateModel(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: authFailureTail}
	s := newFirstCauseScheduler(root, runner)
	s.runPipeline(context.Background(), types.BoardItem{Number: 878, Repo: "nightgauge/nightgauge", ID: "item-878"})

	got := runner.dispatches()
	if len(got) != 1 {
		t.Fatalf("stage dispatched %d time(s) (models %v), want exactly 1 — a permission failure is not "+
			"a capability shortfall, and the second dispatch re-sends the whole prompt to fail at the "+
			"identical line", len(got), got)
	}
}

// TestCapabilityFailureStillEscalates is the discriminator for the test above.
//
// Identical setup, identical post-condition failure, and the ONLY difference is
// that the stage's output tail names no credential problem. Escalation must
// still happen here — otherwise the assertion above would pass on a build that
// had simply removed escalation, and would prove nothing about the gate.
func TestCapabilityFailureStillEscalates(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: "the stage ran out of ideas and stopped writing\n"}
	s := newFirstCauseScheduler(root, runner)
	s.runPipeline(context.Background(), types.BoardItem{Number: 879, Repo: "nightgauge/nightgauge", ID: "item-879"})

	got := runner.dispatches()
	if len(got) != 2 {
		t.Fatalf("stage dispatched %d time(s) (models %v), want 2 — a capability-shaped failure must "+
			"still get its escalation retry; the #878 gate is scoped to permission failures only",
			len(got), got)
	}
	if got[0] == got[1] {
		t.Errorf("both dispatches used model %q — the retry was not an escalation", got[0])
	}
}

// TestPostConditionFailureRecordsTheFirstCause is the #878 attribution
// regression (the same defect class as #875).
//
// The post-condition check can only ever observe that the output context is
// absent; it cannot observe why. When the stage's own output already named a
// cause, that cause is the run's terminal reason and the missing context is its
// consequence. The observed run recorded "issue context file missing" for a
// `git push` that had failed with `invalid auth method` immediately above,
// which sends whoever reads the record at the wrong problem — and the record is
// what docs/OUTCOME_RECORDING.md and the retro path consume.
func TestPostConditionFailureRecordsTheFirstCause(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: authFailureTail}
	s := newFirstCauseScheduler(root, runner)
	s.runPipeline(context.Background(), types.BoardItem{Number: 880, Repo: "nightgauge/nightgauge", ID: "item-880"})

	if runner.rt == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState")
	}
	snap := runner.rt.Snapshot()
	reason := snap.StageErrors[string(snap.Stage)]
	if reason == "" {
		t.Fatalf("no stage error recorded for stage %q", snap.Stage)
	}
	if !strings.Contains(reason, "invalid auth method") {
		t.Errorf("recorded reason = %q\nwant it to NAME the push failure that actually stopped the "+
			"stage; the missing output context is the symptom, not the cause", reason)
	}
	// The symptom is retained, not dropped: "which post-condition tripped" is
	// still the fastest way to see where in the stage the run died.
	if !strings.Contains(reason, "did not write expected output context") {
		t.Errorf("recorded reason = %q\nwant the post-condition symptom retained as trailing context", reason)
	}
	if strings.HasPrefix(reason, "stage ") && !strings.Contains(reason[:40], "auth") {
		t.Errorf("recorded reason still LEADS with the symptom: %q", reason)
	}
}

// TestPostConditionFailureRecordsTheFirstCauseKind is the second half of the
// same attribution defect (#878), and the half the record actually keys on.
//
// The composed reason names the push failure; the KIND is what the V2 run
// record books and what recovery routing and the retro path consume. This site
// hardcoded validation_error for every missing-output failure, so a
// credential-less push was booked as a stage that wrote a malformed context —
// pointing the corpus at the pipeline's output contract instead of at the
// machine's credentials.
func TestPostConditionFailureRecordsTheFirstCauseKind(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: authFailureTail}
	s := newFirstCauseScheduler(root, runner)
	s.runPipeline(context.Background(), types.BoardItem{Number: 881, Repo: "nightgauge/nightgauge", ID: "item-881"})

	records := readDailyJSONLRecords(t, root)
	if len(records) == 0 {
		t.Fatal("no V2 run record written")
	}
	got := records[len(records)-1].TerminalFailureKind
	if got != TerminalKindGitTransportAuthFailed {
		t.Errorf("terminal_failure_kind = %q, want %q — the run died on a credential-less "+
			"`git push`; the missing output context is the symptom the post-condition could "+
			"observe, not what stopped the run", got, TerminalKindGitTransportAuthFailed)
	}
}

// TestPostConditionFailureWithoutAFirstCauseStillRecordsValidationError is the
// discriminator for the test above: the same post-condition, the same missing
// output, and an output tail that names no credential problem. Without this
// row, the assertion above would also pass on a build that had simply renamed
// validation_error.
func TestPostConditionFailureWithoutAFirstCauseStillRecordsValidationError(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: "the stage ran out of ideas and stopped writing\n"}
	s := newFirstCauseScheduler(root, runner)
	s.runPipeline(context.Background(), types.BoardItem{Number: 882, Repo: "nightgauge/nightgauge", ID: "item-882"})

	records := readDailyJSONLRecords(t, root)
	if len(records) == 0 {
		t.Fatal("no V2 run record written")
	}
	got := records[len(records)-1].TerminalFailureKind
	if got != TerminalKindValidationError {
		t.Errorf("terminal_failure_kind = %q, want %q — a missing output context with no "+
			"upstream cause in the tail is still exactly what validation_error means",
			got, TerminalKindValidationError)
	}
}

// schedulerObservedAuthFailure is the line the observed #878 run put in the
// DAEMON LOG and nowhere else: `ensureEpicBranchForItem`'s push, performed by
// the scheduler on the stage's behalf after the subagent had already returned.
const schedulerObservedAuthFailure = "epic branch auto-create: push epic branch epic/900-example: " +
	"push branch epic/900-example: invalid auth method"

// cleanStageTail is a stage output tail that names no failure at all — the
// second half of the observed run's shape. The subagent did not fail; it just
// never wrote its context file.
const cleanStageTail = `Reading the issue and the linked epic...
Drafted the pickup summary.
`

// TestSchedulerObservedPushFailureReachesTheRunRecord is the #878 reproduction
// the other tests in this file do NOT exercise.
//
// Every assertion above hands the auth text to the fake stage runner as
// StageRunResult.LastOutputLines, i.e. it assumes the SUBAGENT printed it. In
// the run the issue actually reports, the subagent printed nothing of the kind:
// the push was `ensureEpicBranchForItem`'s, it is non-blocking, and it only
// ever reached a log.Printf. So the first-cause scan found an empty tail, the
// escalation gate saw no permission evidence and escalated haiku -> sonnet, and
// the record booked the post-condition symptom.
//
// The premise under test is therefore the ROUTING, not the classification: a
// failure the scheduler observes on a stage's behalf has to land in that
// stage's evidence. The stage tail here deliberately names no credential
// problem, so the only way either assertion can pass is through that routing.
func TestSchedulerObservedPushFailureReachesTheRunRecord(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: cleanStageTail}
	s := newFirstCauseScheduler(root, runner)
	s.epicBranchEnsurer = func(context.Context, string, types.BoardItem) string {
		return schedulerObservedAuthFailure
	}
	s.runPipeline(context.Background(), types.BoardItem{
		Number: 883, Repo: "nightgauge/nightgauge", ID: "item-883", ParentNumber: 900,
	})

	if got := runner.dispatches(); len(got) != 1 {
		t.Errorf("stage dispatched %d time(s) (models %v), want exactly 1 — the scheduler's own "+
			"push had already failed on credentials; escalating re-sends the whole prompt to fail "+
			"at the identical line", len(got), got)
	}

	if runner.rt == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState")
	}
	snap := runner.rt.Snapshot()
	if reason := snap.StageErrors[string(snap.Stage)]; !strings.Contains(reason, "invalid auth method") {
		t.Errorf("recorded reason = %q\nwant it to NAME the push failure the scheduler logged; the "+
			"missing output context is the symptom", reason)
	}

	records := readDailyJSONLRecords(t, root)
	if len(records) == 0 {
		t.Fatal("no V2 run record written")
	}
	got := records[len(records)-1].TerminalFailureKind
	if got != TerminalKindGitTransportAuthFailed {
		t.Errorf("terminal_failure_kind = %q, want %q — the cause the daemon logged must be the "+
			"cause the record books", got, TerminalKindGitTransportAuthFailed)
	}
}

// TestSchedulerObservedEpicBranchSuccessRecordsNothing is the discriminator: an
// epic-branch ensure that SUCCEEDS must add nothing to the stage's evidence, so
// the run above cannot be passing because the seam is wired unconditionally.
func TestSchedulerObservedEpicBranchSuccessRecordsNothing(t *testing.T) {
	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := &firstCauseStageRunner{tail: cleanStageTail}
	s := newFirstCauseScheduler(root, runner)
	called := false
	s.epicBranchEnsurer = func(context.Context, string, types.BoardItem) string {
		called = true
		return ""
	}
	s.runPipeline(context.Background(), types.BoardItem{
		Number: 884, Repo: "nightgauge/nightgauge", ID: "item-884", ParentNumber: 900,
	})

	if !called {
		t.Fatal("epic-branch ensure never ran — the sub-issue path did not reach it, so the " +
			"test above proves nothing about routing")
	}
	records := readDailyJSONLRecords(t, root)
	if len(records) == 0 {
		t.Fatal("no V2 run record written")
	}
	if got := records[len(records)-1].TerminalFailureKind; got != TerminalKindValidationError {
		t.Errorf("terminal_failure_kind = %q, want %q — nothing failed on the credential path, so "+
			"the missing output context is the whole story", got, TerminalKindValidationError)
	}
}
