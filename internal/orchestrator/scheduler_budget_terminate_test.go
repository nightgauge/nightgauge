package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// budgetCapturingStageRunner is a StageRunner test double that (a) succeeds
// every stage it is asked to run, writing a minimal output context file so
// the pipeline's prerequisite check on the following stage is satisfied, and
// (b) captures the *state.RuntimeState the scheduler threads through
// StageRunParams the first time a stage dispatches. Capturing this pointer
// is the only way the test can inspect the runtime after runPipeline
// returns: the scheduler unregisters the run from activeRuntimes via a
// defer right after registration (#370 / ADR-017 run-identity keying), so a
// post-return lookup by issue number would find nothing.
type budgetCapturingStageRunner struct {
	mu      sync.Mutex
	runtime *state.RuntimeState
	calls   map[state.PipelineStage]int
}

func newBudgetCapturingStageRunner() *budgetCapturingStageRunner {
	return &budgetCapturingStageRunner{calls: make(map[state.PipelineStage]int)}
}

func (r *budgetCapturingStageRunner) count(stage state.PipelineStage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[stage]
}

func (r *budgetCapturingStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls[params.Stage]++
	if r.runtime == nil {
		r.runtime = params.Runtime
	}
	r.mu.Unlock()

	if params.OutputFile != "" {
		if err := os.MkdirAll(filepath.Dir(params.OutputFile), 0o755); err == nil {
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
			_ = os.WriteFile(params.OutputFile, data, 0o644)
		}
	}
	// Each stage reports enough tokens (150) that a pipeline ceiling of 1
	// token is blown through after the FIRST stage completes — the second
	// stage's pre-dispatch CheckPipelineBudget call is guaranteed to see
	// ShouldTerminate on the very next iteration, deterministically, with no
	// dependence on real budget arithmetic beyond "more than a ceiling of 1".
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// TestBudgetCeilingTerminate_SnapshotStageMatchesRefusedStage pins #444: the
// budget-ceiling terminate path must BeginStage(stage) before it
// SetStageError(stage, ...), so the runtime snapshot's current Stage names
// the SAME stage the refusal is keyed under. Every downstream reader that
// derives "what went wrong" from the current stage — internal/ipc/server.go's
// terminal-notify errMsg derivation, scheduler_exit_record.go's fallback, the
// outcome-recording site in scheduler.go, and autonomous.go's reporting —
// performs exactly the read this test asserts on:
// snap.StageErrors[string(snap.Stage)].
//
// Pre-fix, the budget check ran before BeginStage advanced the runtime past
// issue-pickup, so snap.Stage stayed "issue-pickup" while the refusal was
// filed under "feature-planning" — the reader-shaped read below returned ""
// instead of the budget reason. See runtime_state.go's BeginStage: it only
// sets Stage/StageStart (no dispatch/history side effect absent a paired
// CompleteStage, which the terminal budget path never calls) — the same
// no-side-effect shape the branch-fork preflight precedent
// (scheduler.go's BeginStage-then-SetStageError block) already relies on.
func TestBudgetCeilingTerminate_SnapshotStageMatchesRefusedStage(t *testing.T) {
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

	runner := newBudgetCapturingStageRunner()
	s := &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
		// A hard, effectively-zero pipeline ceiling: issue-pickup's 150
		// reported tokens blow through it on the very first RecordStageTokens
		// call, so CheckPipelineBudget's pre-dispatch check for the SECOND
		// stage (feature-planning) is guaranteed to return ShouldTerminate.
		budgetEngine: NewBudgetEnforcer(BudgetConfig{
			PipelineCeilingTokens: 1,
			GracePercent:          50,
			Mode:                  "hard",
		}),
	}

	item := types.BoardItem{Number: 444, Repo: "nightgauge/nightgauge", ID: "item-444"}
	s.runPipeline(context.Background(), item)

	// Sanity: issue-pickup ran (it is what accrues the tokens that blow the
	// ceiling); feature-planning must NEVER have dispatched — the whole point
	// of a pipeline-budget terminate is that it refuses BEFORE spending a
	// single token on the next stage.
	if got := runner.count(state.StageIssuePickup); got != 1 {
		t.Fatalf("issue-pickup ran %d times, want 1", got)
	}
	if got := runner.count(state.StageFeaturePlanning); got != 0 {
		t.Fatalf("feature-planning was dispatched %d time(s) — budget-ceiling terminate must refuse before dispatch", got)
	}

	if runner.runtime == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState — issue-pickup did not dispatch")
	}
	snap := runner.runtime.Snapshot()

	// The defect: pre-fix, snap.Stage still names "issue-pickup" (the last
	// stage BeginStage actually advanced to) even though the refusal was
	// filed under "feature-planning".
	if snap.Stage != state.StageFeaturePlanning {
		t.Fatalf("snap.Stage = %q, want %q (the refused stage) — a snap.Stage-keyed reader "+
			"resolves the wrong stage entirely when this drifts from the SetStageError key",
			snap.Stage, state.StageFeaturePlanning)
	}

	// The reader-shaped access every affected caller performs: index
	// StageErrors by the CURRENT stage, not by whichever stage happens to be
	// known to be refused.
	gotReason, ok := snap.StageErrors[string(snap.Stage)]
	if !ok || gotReason == "" {
		t.Fatalf("snap.StageErrors[string(snap.Stage)] = (%q, ok=%v), want the budget refusal reason — "+
			"this is exactly the read internal/ipc/server.go, scheduler_exit_record.go, the outcome-recording "+
			"site in scheduler.go, and autonomous.go all perform, and it must not come back empty",
			gotReason, ok)
	}
	const wantReason = "pipeline_budget_exceeded"
	if gotReason != wantReason {
		t.Errorf("snap.StageErrors[string(snap.Stage)] = %q, want %q", gotReason, wantReason)
	}
}
