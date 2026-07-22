package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// passingGate is a StageGate that always passes (KindOK). Used to neutralize
// the pr-merge post-condition gate so a phantom-success end-to-end test
// isolates the verifyPRMerged checkpoint, which runs AFTER the gate.
type passingGate struct{ name string }

func (g passingGate) Name() string { return g.name }
func (g passingGate) Verify(_ context.Context, _ int, _ string) gates.GateResult {
	return gates.GateResult{GateName: g.name, Passed: true, Reason: "ok", Kind: gates.KindOK}
}

// TestScheduler_PRMerge_PhantomSuccess_FailsPipeline is the #4070 end-to-end
// guard: the deterministic runner self-reports PathMerged, but the canonical
// post-stage verifyPRMerged checkpoint observes a non-MERGED PR on GitHub. The
// pipeline must fail closed — the pr-merge stage records a named-blocker error
// and the linked issue is NOT closed — rather than write outcome=complete on an
// unmerged PR.
func TestScheduler_PRMerge_PhantomSuccess_FailsPipeline(t *testing.T) {
	root := t.TempDir()

	for _, dir := range []string{
		"nightgauge-issue-pickup",
		"nightgauge-feature-planning",
		"nightgauge-feature-dev",
		"nightgauge-feature-validate",
		"nightgauge-pr-create",
		"nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}

	// Pre-write the pr-create context so loadPrUrl populates runtime.PrUrl —
	// the post-stage verifyPRMerged checkpoint only runs when a PR URL is known.
	pcDir := filepath.Join(root, ".nightgauge", "pipeline", "issue-9100")
	if err := os.MkdirAll(pcDir, 0755); err != nil {
		t.Fatalf("mkdir pr-create context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pcDir, "pr-create-context.json"),
		[]byte(`{"pr_url":"https://github.com/nightgauge/test/pull/1234"}`), 0644); err != nil {
		t.Fatalf("write pr-create context: %v", err)
	}

	// GitHub returns the PR as still OPEN + CONFLICTING — the merge never landed.
	srv := prBlockerServer(t, "OPEN", "CONFLICTING", "DIRTY", "")
	defer srv.Close()

	runner := newSuccessStageRunner()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path: pmstages.PathMerged, PRNumber: 1234, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged,
	}}

	issueSvc := newMockIssueSvc()
	// Linked issue is still OPEN (sub-issue never closed because PR didn't merge).
	issueSvc.addIssue("nightgauge", "test", 9100, &types.Issue{Number: 9100, State: "OPEN"})

	s := &Scheduler{
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 1, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       issueSvc,
		client:         gh.NewClientWithURL("test-token", srv.URL),
		execMgr:        execution.NewManager(root, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prMergeRunner:  det,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
	// Neutralize the gate so verifyPRMerged (which runs after it) is the sole
	// failing checkpoint under test.
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StagePRMerge: passingGate{name: "pr-merge"},
	})

	item := types.BoardItem{Number: 9100, Repo: "nightgauge/test", ID: "item-9100"}
	s.runPipeline(context.Background(), item)

	// The run must be recorded as FAILED (not success) — the deterministic
	// runner's PathMerged was overruled by verifyPRMerged observing CONFLICTING.
	records := readDailyJSONLRecords(t, root)
	var rec *state.V2RunRecord
	for i := range records {
		if records[i].IssueNumber == item.Number {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("no run record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}
	if rec.Outcome != "failed" {
		t.Errorf("rec.Outcome = %q, want failed (PR was CONFLICTING, not MERGED) — fail-closed gate did not engage", rec.Outcome)
	}
	// The pr-merge stage detail must carry the named blocker so #4073's
	// stuck-epic detector can read the precise reason.
	if stage, ok := rec.Stages[string(state.StagePRMerge)]; ok {
		if stage.Error != "" &&
			!strings.Contains(stage.Error, pmstages.ReasonNotMergeable) {
			t.Errorf("pr-merge stage error = %q, want it to name %q", stage.Error, pmstages.ReasonNotMergeable)
		}
	}
}

// fakePRMergeRunner is a controllable PRMergeRunner for scheduler tests.
type fakePRMergeRunner struct {
	result      pmstages.PRMergeResult
	err         error
	callCount   int
	lastWorkdir string // workdir passed to the most recent Run — asserts #275
}

func (f *fakePRMergeRunner) Run(_ context.Context, _ int, _, workdir string) (pmstages.PRMergeResult, error) {
	f.callCount++
	f.lastWorkdir = workdir
	return f.result, f.err
}

// fakeStageRunnerCounter is a minimal StageRunner that records calls per
// stage. It always returns a successful zero-token result.
type fakeStageRunnerCounter struct {
	callsByStage map[state.PipelineStage]int
}

func newFakeStageRunner() *fakeStageRunnerCounter {
	return &fakeStageRunnerCounter{callsByStage: make(map[state.PipelineStage]int)}
}

func (f *fakeStageRunnerCounter) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	f.callsByStage[params.Stage]++
	return &StageRunResult{ExitCode: 0}, nil
}

// newSchedulerForDeterministicTest builds a minimal Scheduler suitable for
// exercising tryDeterministicPRMerge without a GitHub client or filesystem
// dependencies.
func newSchedulerForDeterministicTest() *Scheduler {
	return &Scheduler{
		retryEngine:  NewRetryEngine(DefaultRetryConfig()),
		budgetEngine: NewBudgetEnforcer(DefaultBudgetConfig()),
	}
}

// TestScheduler_PRMerge_DeterministicSkipsLLM asserts the LLM stage runner is
// NOT invoked for pr-merge when the deterministic runner reports `merged`.
func TestScheduler_PRMerge_DeterministicSkipsLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path:     pmstages.PathMerged,
		PRNumber: 99,
		PRState:  "MERGED",
		Reason:   pmstages.ReasonAlreadyMerged,
	}}
	s.WithPRMergeRunner(det)

	llm := newFakeStageRunner()
	s.WithStageRunner(llm)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp")
	if !merged {
		t.Fatalf("tryDeterministicPRMerge returned false, want true")
	}

	// The fake LLM stage runner must not have been called for pr-merge.
	if llm.callsByStage[state.StagePRMerge] != 0 {
		t.Errorf("LLM stage runner called %d times for pr-merge, want 0",
			llm.callsByStage[state.StagePRMerge])
	}

	// Runtime must record execution_path = "deterministic" for pr-merge.
	if got := rs.StageExecutionPath(state.StagePRMerge); got != "deterministic" {
		t.Errorf("StageExecutionPath(pr-merge) = %q, want %q", got, "deterministic")
	}

	// Deterministic runner must have been called exactly once.
	if det.callCount != 1 {
		t.Errorf("PRMergeRunner.Run call count = %d, want 1", det.callCount)
	}
}

// TestScheduler_PRMerge_PuntInvokesLLM asserts that on punt, the deterministic
// path returns false (so the caller invokes the LLM stage runner) and records
// execution_path = "llm".
func TestScheduler_PRMerge_PuntInvokesLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path:     pmstages.PathPunt,
		PRNumber: 99,
		PRState:  "OPEN",
		Reason:   "not-mergeable: CONFLICTING",
	}}
	s.WithPRMergeRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp")
	if merged {
		t.Fatalf("tryDeterministicPRMerge returned true on punt, want false (so LLM runs)")
	}

	// Runtime must record execution_path = "llm" so telemetry attributes the
	// stage to the LLM path that's about to run.
	if got := rs.StageExecutionPath(state.StagePRMerge); got != "llm" {
		t.Errorf("StageExecutionPath(pr-merge) = %q, want %q", got, "llm")
	}
	// And it must record WHY the deterministic path punted (Issue #297) — the
	// machine-readable reason threads into the V3 history record's punt_reason so
	// the decision is observable without session-log archaeology.
	if got := rs.StagePuntReason(state.StagePRMerge); got != "not-mergeable: CONFLICTING" {
		t.Errorf("StagePuntReason(pr-merge) = %q, want %q", got, "not-mergeable: CONFLICTING")
	}
}

// TestScheduler_PRMerge_RateLimitedDefersNoLLM asserts that a rate-limited punt
// from the deterministic runner does NOT fall through to the LLM path — it
// signals a deferral (third return value true) and records NO execution_path
// (neither path produced a result). Issue #3976: re-shelling `gh pr merge` via
// the LLM skill into the same exhausted bucket would burn tokens and risk
// leaving the issue stuck "In review".
func TestScheduler_PRMerge_RateLimitedDefersNoLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path:     pmstages.PathPunt,
		PRNumber: 99,
		PRState:  "OPEN",
		Reason:   pmstages.ReasonRateLimited,
	}}
	s.WithPRMergeRunner(det)

	llm := newFakeStageRunner()
	s.WithStageRunner(llm)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	merged, _, rateLimited := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp")
	if merged {
		t.Fatalf("tryDeterministicPRMerge returned merged=true on rate-limit, want false")
	}
	if !rateLimited {
		t.Fatalf("tryDeterministicPRMerge returned rateLimited=false on a rate-limit punt, want true")
	}

	// The LLM path must NOT have been recorded — the caller defers instead.
	if got := rs.StageExecutionPath(state.StagePRMerge); got != "" {
		t.Errorf("StageExecutionPath(pr-merge) = %q on rate-limit defer, want \"\" (no path ran)", got)
	}
	// The deterministic runner ran exactly once (no LLM, no retry here).
	if det.callCount != 1 {
		t.Errorf("PRMergeRunner.Run call count = %d, want 1", det.callCount)
	}
}

// TestScheduler_PRMerge_DeterministicErrorFallsThroughToLLM asserts that an
// unexpected error from the deterministic runner is treated as a punt — the
// LLM path runs.
func TestScheduler_PRMerge_DeterministicErrorFallsThroughToLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{err: errors.New("unexpected gh failure")}
	s.WithPRMergeRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp")
	if merged {
		t.Fatalf("tryDeterministicPRMerge returned true on error, want false")
	}
	if got := rs.StageExecutionPath(state.StagePRMerge); got != "llm" {
		t.Errorf("StageExecutionPath(pr-merge) = %q, want %q", got, "llm")
	}
}

// TestScheduler_PRMerge_DeterministicPath_CostZero asserts that the
// deterministic path adds zero tokens and zero cost to the runtime — the
// caller-side downstream logic uses CompleteStage with exitCode=0 and the
// scheduler's StageRunResult{ExitCode: 0} replacement carries no token data.
//
// We verify that runtime.TotalCostUSD remains zero when only the deterministic
// path was exercised (no CompleteStage call yet, but the deterministic hook
// itself touches no token state).
func TestScheduler_PRMerge_DeterministicPath_CostZero(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path:     pmstages.PathMerged,
		PRNumber: 99,
		PRState:  "MERGED",
		Reason:   pmstages.ReasonAlreadyMerged,
	}}
	s.WithPRMergeRunner(det)
	llm := newFakeStageRunner()
	s.WithStageRunner(llm)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	beforeCost := rs.TotalCostUSD
	beforeIn := rs.InputTokens
	beforeOut := rs.OutputTokens

	merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp")
	if !merged {
		t.Fatalf("expected deterministic merged=true")
	}

	// Simulate the scheduler's downstream CompleteStage call for the
	// deterministic happy path: zero tokens, zero cost.
	rs.CompleteStage(0, 0, 0, "")

	if rs.TotalCostUSD != beforeCost {
		t.Errorf("TotalCostUSD changed: before=%v after=%v", beforeCost, rs.TotalCostUSD)
	}
	if rs.InputTokens != beforeIn || rs.OutputTokens != beforeOut {
		t.Errorf("token counts changed: in %d→%d out %d→%d",
			beforeIn, rs.InputTokens, beforeOut, rs.OutputTokens)
	}
}

// TestScheduler_PRMerge_NonPRMergeStage_NoOp asserts that calling the
// deterministic hook for any other stage is a no-op — even when a runner is
// registered.
func TestScheduler_PRMerge_NonPRMergeStage_NoOp(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{Path: pmstages.PathMerged}}
	s.WithPRMergeRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StageFeatureDev)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	if merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StageFeatureDev, rs, item, "/tmp"); merged {
		t.Errorf("tryDeterministicPRMerge returned true for non-pr-merge stage")
	}
	if det.callCount != 0 {
		t.Errorf("PRMergeRunner.Run was called for non-pr-merge stage (count=%d)", det.callCount)
	}
	if got := rs.StageExecutionPath(state.StageFeatureDev); got != "" {
		t.Errorf("execution_path leaked onto unrelated stage: %q", got)
	}
}

// TestScheduler_PRMerge_NilRunner_NoOp asserts that with no runner registered,
// the hook is a no-op (returns false, no panic).
func TestScheduler_PRMerge_NilRunner_NoOp(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	// no WithPRMergeRunner — prMergeRunner is nil

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp")
	if merged {
		t.Errorf("nil runner should produce merged=false")
	}
	if got := rs.StageExecutionPath(state.StagePRMerge); got != "" {
		t.Errorf("execution_path should not be set when runner is nil, got %q", got)
	}
}
