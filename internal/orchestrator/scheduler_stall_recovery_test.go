package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// stallStageRunner is a StageRunner test double for adaptive stall-recovery
// (Issue #3005). Per-stage call counts drive the failure pattern: a stage's
// first N invocations return the configured stall error; subsequent
// invocations succeed and write a minimal output context file.
type stallStageRunner struct {
	mu            sync.Mutex
	callCount     map[state.PipelineStage]int
	stallOnCalls  map[state.PipelineStage]int // stage → number of stalls before success
	stallErrText  string
	stalledStages []state.PipelineStage // append-only log of which stages stalled
}

func newStallStageRunner(stallOn map[state.PipelineStage]int, errText string) *stallStageRunner {
	return &stallStageRunner{
		callCount:    make(map[state.PipelineStage]int),
		stallOnCalls: stallOn,
		stallErrText: errText,
	}
}

func (r *stallStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.callCount[params.Stage]++
	currentCall := r.callCount[params.Stage]
	stallsRemaining := r.stallOnCalls[params.Stage]
	r.mu.Unlock()

	if currentCall <= stallsRemaining {
		r.mu.Lock()
		r.stalledStages = append(r.stalledStages, params.Stage)
		r.mu.Unlock()
		return &StageRunResult{ExitCode: 1}, errors.New(r.stallErrText)
	}

	if params.OutputFile != "" {
		if err := os.MkdirAll(filepath.Dir(params.OutputFile), 0755); err == nil {
			payload := map[string]any{
				"schema_version":   "1.0",
				"issue_number":     params.IssueNumber,
				"plan_file":        "plan.md",
				"approach":         "test",
				"files_to_create":  []string{},
				"files_to_modify":  []string{},
				"files_to_read":    []string{},
				"validation_steps": []string{},
				"ok":               true,
			}
			data, _ := json.Marshal(payload)
			_ = os.WriteFile(params.OutputFile, data, 0644)
		}
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

func buildStallTestScheduler(t *testing.T, root string, runner StageRunner) *Scheduler {
	t.Helper()
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
	s := &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(DefaultRetryConfig()),
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
	}
	return s
}

func enableAdaptiveStallRecovery(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("pipeline:\n  adaptive_stall_recovery: true\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestStallRecovery_FirstStallTriggersRewind verifies the happy path:
// feature-dev stalls once, the scheduler synthesizes a feedback signal,
// rewinds to feature-planning, and succeeds on the retry.
func TestStallRecovery_FirstStallTriggersRewind(t *testing.T) {
	root := t.TempDir()
	enableAdaptiveStallRecovery(t, root)

	runner := newStallStageRunner(map[state.PipelineStage]int{
		state.StageFeatureDev: 1, // stall on first attempt only
	}, "feature-dev stall kill threshold reached after 4800s")

	s := buildStallTestScheduler(t, root, runner)
	item := types.BoardItem{Number: 7001, Repo: "nightgauge/test", ID: "item-7001"}
	s.runPipeline(context.Background(), item)

	// feedback-{N}.json must have been written by the synthetic-signal path.
	feedbackPath := filepath.Join(root, ".nightgauge", "pipeline", "feedback-7001.json")
	data, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("expected synthetic feedback file at %s: %v", feedbackPath, err)
	}
	var ctx FeedbackContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("parse feedback: %v", err)
	}
	if len(ctx.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(ctx.Signals))
	}
	sig := ctx.Signals[0]
	if sig.Severity != "blocking" {
		t.Errorf("severity = %s, want blocking", sig.Severity)
	}
	if sig.BacktrackTargetStage != string(state.StageFeaturePlanning) {
		t.Errorf("backtrack_target = %s, want feature-planning", sig.BacktrackTargetStage)
	}

	// RetryEngine must have recorded exactly one backtrack.
	if got := s.retryEngine.BacktrackCount(); got != 1 {
		t.Errorf("BacktrackCount = %d, want 1", got)
	}

	// feature-dev must have been called twice (initial stall + retry success).
	if got := runner.callCount[state.StageFeatureDev]; got != 2 {
		t.Errorf("feature-dev call count = %d, want 2 (stall + retry)", got)
	}
	// feature-planning runs at least twice — once initially, once on rewind.
	if got := runner.callCount[state.StageFeaturePlanning]; got < 2 {
		t.Errorf("feature-planning call count = %d, want >= 2 (rewind)", got)
	}
}

// TestStallRecovery_SecondStallIsTerminal verifies that a run which stalls
// on both attempts terminates without a third try, recording stall_kill +
// stall-killed-after-retry on the failed stage.
func TestStallRecovery_SecondStallIsTerminal(t *testing.T) {
	root := t.TempDir()
	enableAdaptiveStallRecovery(t, root)

	runner := newStallStageRunner(map[state.PipelineStage]int{
		state.StageFeatureDev: 5, // never recovers
	}, "feature-dev stall kill threshold reached")

	s := buildStallTestScheduler(t, root, runner)
	// Disable model escalation so it doesn't add extra retry attempts on top
	// of the stall-recovery path.
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          2,
		MaxEscalationsPerStage: 0,
		OscillationDetection:   true,
	})

	item := types.BoardItem{Number: 7002, Repo: "nightgauge/test", ID: "item-7002"}
	s.runPipeline(context.Background(), item)

	// Exactly two feature-dev attempts: initial + post-rewind.
	if got := runner.callCount[state.StageFeatureDev]; got != 2 {
		t.Errorf("feature-dev call count = %d, want 2 (initial + retry, no third)", got)
	}

	// Read the V2/V3 history record and assert the failure category is set.
	records := readDailyJSONLRecords(t, root)
	if len(records) == 0 {
		t.Fatal("no run record written")
	}
	rec := records[len(records)-1]
	if rec.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind != TerminalKindStallKill {
		t.Errorf("terminal_failure_kind = %q, want stall_kill", rec.TerminalFailureKind)
	}
	devDetail, ok := rec.Stages[string(state.StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev stage detail missing from record")
	}
	if devDetail.FailureCategory != StallKilledAfterRetryCategory {
		t.Errorf("feature-dev failure_category = %q, want %q",
			devDetail.FailureCategory, StallKilledAfterRetryCategory)
	}
}

// TestStallRecovery_DisabledFlagDoesNotRetry verifies that explicitly
// disabling the flag in YAML opts out of recovery: the stall-kill
// terminates the run immediately without writing feedback-{N}.json.
//
// #3020 — default flipped to true; this test now writes an explicit
// `adaptive_stall_recovery: false` to assert the opt-out path.
func TestStallRecovery_DisabledFlagDoesNotRetry(t *testing.T) {
	root := t.TempDir()
	// Explicit opt-out: the default is now true, so we must write
	// `adaptive_stall_recovery: false` to disable.
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("pipeline:\n  adaptive_stall_recovery: false\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runner := newStallStageRunner(map[state.PipelineStage]int{
		state.StageFeatureDev: 5,
	}, "stalled and killed after 4800s")

	s := buildStallTestScheduler(t, root, runner)
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          2,
		MaxEscalationsPerStage: 0,
	})

	item := types.BoardItem{Number: 7003, Repo: "nightgauge/test", ID: "item-7003"}
	s.runPipeline(context.Background(), item)

	// feature-dev called exactly once — flag off, no retry.
	if got := runner.callCount[state.StageFeatureDev]; got != 1 {
		t.Errorf("feature-dev call count = %d, want 1 (no retry when flag off)", got)
	}

	// No synthetic feedback file should exist.
	feedbackPath := filepath.Join(root, ".nightgauge", "pipeline", "feedback-7003.json")
	if _, err := os.Stat(feedbackPath); err == nil {
		t.Errorf("expected no feedback file when flag disabled, but %s exists", feedbackPath)
	}

	// No backtrack should have been recorded.
	if got := s.retryEngine.BacktrackCount(); got != 0 {
		t.Errorf("BacktrackCount = %d, want 0 (flag off)", got)
	}
}

// TestStallRecovery_CostCapKillIsNeverRetried verifies cost-cap precedence:
// a kill marked with the cost-cap error string is terminal even when
// adaptive stall-recovery is enabled.
func TestStallRecovery_CostCapKillIsNeverRetried(t *testing.T) {
	root := t.TempDir()
	enableAdaptiveStallRecovery(t, root)

	// Error contains BOTH cost-cap and stall-kill markers (defensive case).
	runner := newStallStageRunner(map[state.PipelineStage]int{
		state.StageFeatureDev: 5,
	}, "[cost-cap-exceeded] feature-dev exceeded $5.00 cap (also matched stall kill threshold)")

	s := buildStallTestScheduler(t, root, runner)
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          2,
		MaxEscalationsPerStage: 0,
	})

	item := types.BoardItem{Number: 7004, Repo: "nightgauge/test", ID: "item-7004"}
	s.runPipeline(context.Background(), item)

	if got := runner.callCount[state.StageFeatureDev]; got != 1 {
		t.Errorf("feature-dev call count = %d, want 1 (cost-cap never retried)", got)
	}
	feedbackPath := filepath.Join(root, ".nightgauge", "pipeline", "feedback-7004.json")
	if _, err := os.Stat(feedbackPath); err == nil {
		t.Errorf("expected no feedback file for cost-cap kill, but %s exists", feedbackPath)
	}
}

// TestStallRecovery_StallInNonRewindableStageIsTerminal verifies that a
// stall in pr-create (whose backtrack_target_stage is not feature-planning)
// does NOT trigger a retry — the heuristic skips the rewind branch.
func TestStallRecovery_StallInNonRewindableStageIsTerminal(t *testing.T) {
	root := t.TempDir()
	enableAdaptiveStallRecovery(t, root)

	runner := newStallStageRunner(map[state.PipelineStage]int{
		state.StagePRCreate: 5,
	}, "pr-create heartbeat stall")

	s := buildStallTestScheduler(t, root, runner)
	s.retryEngine = NewRetryEngine(RetryConfig{
		MaxBacktracks:          2,
		MaxEscalationsPerStage: 0,
	})

	item := types.BoardItem{Number: 7005, Repo: "nightgauge/test", ID: "item-7005"}
	s.runPipeline(context.Background(), item)

	if got := runner.callCount[state.StagePRCreate]; got != 1 {
		t.Errorf("pr-create call count = %d, want 1 (non-rewindable stall is terminal)", got)
	}
	feedbackPath := filepath.Join(root, ".nightgauge", "pipeline", "feedback-7005.json")
	if _, err := os.Stat(feedbackPath); err == nil {
		t.Errorf("expected no feedback file for pr-create stall, but %s exists", feedbackPath)
	}
}
