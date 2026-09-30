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
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// readRunStateFile decodes the persisted run-state.json directly, so the
// assertions are on the file other processes read, not on in-memory values.
func readRunStateFile(t *testing.T, root string) map[string]any {
	t.Helper()
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(runstate.Path(dir))
	if err != nil {
		t.Fatalf("run-state.json not written: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunLifecycle_FreshRunTracksStagesAndCompletes(t *testing.T) {
	root := layouttest.Repo(t)
	lc, from := beginRunLifecycle(root, 42, "")
	if from != "" {
		t.Fatalf("fresh run resumeFrom = %q, want empty", from)
	}
	m := readRunStateFile(t, root)
	if m["state"] != "running" || m["issue_number"] != float64(42) || m["branch"] != "issue-42" {
		t.Fatalf("after begin: %v", m)
	}

	lc.stage(state.StageIssuePickup, "")
	lc.stage(state.StageFeaturePlanning, "fix/42-x")
	m = readRunStateFile(t, root)
	if m["current_stage"] != "feature-planning" || m["resume_from_stage"] != "feature-planning" {
		t.Fatalf("stage not tracked: %v", m)
	}
	if m["branch"] != "fix/42-x" {
		t.Fatalf("branch not refreshed: %v", m)
	}

	// A stage outside the run-state order leaves the record untouched.
	lc.stage(state.StageSpikeMaterialize, "")
	if m = readRunStateFile(t, root); m["current_stage"] != "feature-planning" {
		t.Fatalf("spike-materialize changed current_stage: %v", m)
	}

	lc.finish(context.Background(), true)
	m = readRunStateFile(t, root)
	if m["state"] != "completed" {
		t.Fatalf("state = %v, want completed", m["state"])
	}
	if _, ok := m["current_stage"]; ok {
		t.Fatalf("current_stage survived completion: %v", m)
	}
}

func TestRunLifecycle_CancelPausesAndReinvocationResumes(t *testing.T) {
	root := layouttest.Repo(t)
	lc, _ := beginRunLifecycle(root, 7, "fix/7")
	lc.stage(state.StageIssuePickup, "")
	lc.stage(state.StageFeatureValidate, "")

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("terminated"))
	lc.finish(ctx, false)

	m := readRunStateFile(t, root)
	if m["state"] != "paused" || m["resume_from_stage"] != "feature-validate" {
		t.Fatalf("after cancel: %v", m)
	}
	if m["reason"] != "interrupted: terminated" {
		t.Fatalf("reason = %v", m["reason"])
	}

	lc2, from := beginRunLifecycle(root, 7, "")
	if from != runstate.StageFeatureValid {
		t.Fatalf("resumeFrom = %q, want feature-validate", from)
	}
	m = readRunStateFile(t, root)
	if m["state"] != "running" || m["attempt_number"] != float64(2) || m["branch"] != "fix/7" {
		t.Fatalf("after resume: %v", m)
	}
	stages := []state.PipelineStage{state.StageIssuePickup, state.StageFeaturePlanning,
		state.StageFeatureDev, state.StageFeatureValidate, state.StagePRCreate}
	if i := resumeStageIndex(stages, from); i != 3 {
		t.Fatalf("resumeStageIndex = %d, want 3", i)
	}
	lc2.finish(context.Background(), false)
	if m = readRunStateFile(t, root); m["state"] != "aborted" {
		t.Fatalf("failed run state = %v, want aborted", m["state"])
	}
}

func TestRunLifecycle_HardKilledRecordIsResumed(t *testing.T) {
	root := layouttest.Repo(t)
	dir, _ := layout.PipelineStateDir(root)
	if _, err := runstate.MarkRunning(dir, runstate.MarkRunningOptions{IssueNumber: 9, Branch: "fix/9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runstate.MarkStageStarted(dir, runstate.StageFeatureDev, ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a writer that died without recording a stop.
	rs, _ := runstate.Load(dir)
	dead := 1 << 30
	rs.Attempts[0].PID = &dead
	if err := runstate.Save(dir, rs); err != nil {
		t.Fatal(err)
	}

	_, from := beginRunLifecycle(root, 9, "")
	if from != runstate.StageFeatureDev {
		t.Fatalf("resumeFrom = %q, want feature-dev", from)
	}
}

func TestRunLifecycle_LiveRunOfAnotherIssueIsNotClobbered(t *testing.T) {
	root := layouttest.Repo(t)
	lc1, _ := beginRunLifecycle(root, 1, "")
	lc2, _ := beginRunLifecycle(root, 2, "")
	lc2.stage(state.StageFeatureDev, "")
	lc2.finish(context.Background(), true)
	m := readRunStateFile(t, root)
	if m["issue_number"] != float64(1) || m["state"] != "running" {
		t.Fatalf("issue #2 overwrote #1's record: %v", m)
	}
	lc1.finish(context.Background(), true)
}

// cancellingRunner records every stage it is asked to run, snapshots the
// persisted run-state at each one, and cancels the run when it reaches
// cancelAt — the SIGTERM path, which reaches runPipeline as a cancelled
// context.
type cancellingRunner struct {
	mu       sync.Mutex
	root     string
	cancelAt state.PipelineStage
	cancel   context.CancelFunc
	stages   []state.PipelineStage
	seen     map[state.PipelineStage]map[string]any
}

func (r *cancellingRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.stages = append(r.stages, params.Stage)
	if dir, err := layout.PipelineStateDir(r.root); err == nil {
		if data, err := os.ReadFile(runstate.Path(dir)); err == nil {
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			r.seen[params.Stage] = m
		}
	}
	r.mu.Unlock()
	if params.Stage == r.cancelAt && r.cancel != nil {
		r.cancel()
		return nil, ctx.Err()
	}
	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0755)
		payload := map[string]any{
			"schema_version": "1.0",
			"issue_number":   params.IssueNumber,
			"branch":         "fix/1964-test",
			"plan_file":      "plan.md",
			"approach":       "test",
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, OutputTokens: 5}, nil
}

func newLifecycleTestScheduler(root string, runner StageRunner) *Scheduler {
	return &Scheduler{
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(root, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
}

// TestScheduler_RunState_PausedOnCancelAndResumedOnReinvocation is the #1964
// acceptance test: a scheduler run writes run-state.json while it executes,
// a cancellation mid-feature-dev leaves it paused at feature-dev, and the next
// invocation re-enters at feature-dev instead of issue-pickup.
func TestScheduler_RunState_PausedOnCancelAndResumedOnReinvocation(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := gitWorkspace(t)
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "fixture")

	item := types.BoardItem{Number: 1964, Repo: "nightgauge/test", ID: "item-1964"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &cancellingRunner{root: root, cancelAt: state.StageFeatureDev, cancel: cancel,
		seen: map[state.PipelineStage]map[string]any{}}
	newLifecycleTestScheduler(root, first).runPipeline(ctx, item)

	if len(first.stages) == 0 || first.stages[0] != state.StageIssuePickup {
		t.Fatalf("first run stages = %v, want to start at issue-pickup", first.stages)
	}
	live := first.seen[state.StageFeatureDev]
	if live == nil || live["state"] != "running" || live["current_stage"] != "feature-dev" {
		t.Fatalf("run-state while feature-dev executed = %v, want running at feature-dev", live)
	}
	m := readRunStateFile(t, root)
	if m["state"] != "paused" || m["resume_from_stage"] != "feature-dev" {
		t.Fatalf("after cancellation run-state = %v, want paused at feature-dev", m)
	}

	second := &cancellingRunner{root: root, seen: map[state.PipelineStage]map[string]any{}}
	newLifecycleTestScheduler(root, second).runPipeline(context.Background(), item)
	if len(second.stages) == 0 || second.stages[0] != state.StageFeatureDev {
		t.Fatalf("re-invocation stages = %v, want to re-enter at feature-dev", second.stages)
	}
	if got := second.seen[state.StageFeatureDev]; got["attempt_number"] != float64(2) || got["state"] != "running" {
		t.Fatalf("resumed run-state = %v, want running attempt 2", got)
	}
}
