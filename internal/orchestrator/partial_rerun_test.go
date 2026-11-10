package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// The AC 4 scenario 1 remainder: a missing test file.
const partialRerunCriterion = "An integration test `internal/x/x_integration_test.go` exists"

// partialRerunRunner exits 0 on every stage, writes a context reporting no
// changed files, records each dispatch's stage and prompt, and runs onStage
// inside the dispatch.
type partialRerunRunner struct {
	mu      sync.Mutex
	calls   []StageRunParams
	onStage func(StageRunParams)
}

func (r *partialRerunRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	hook := r.onStage
	r.mu.Unlock()
	if hook != nil {
		hook(params)
	}
	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0o755)
		payload := map[string]any{
			"schema_version":     "1.0",
			"issue_number":       params.IssueNumber,
			"plan_file":          "plan.md",
			"approach":           "test",
			"files_changed":      map[string]any{"created": []string{}, "modified": []string{}, "deleted": []string{}},
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"validation_status":  "passed",
			"created_at":         "2026-11-10T00:00:00Z",
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0o644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, OutputTokens: 5}, nil
}

func (r *partialRerunRunner) prompt(stage state.PipelineStage) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.Stage == stage {
			return c.Prompt, true
		}
	}
	return "", false
}

// recordingCommenter records every comment it is asked to post.
type recordingCommenter struct {
	mu     sync.Mutex
	bodies []string
}

func (c *recordingCommenter) AddComment(_ context.Context, _ string, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
	return nil
}

func (c *recordingCommenter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func pendingRemainder(pr int, criteria ...string) func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Pending: true, PR: pr, Criteria: criteria, CommentAt: &at}, nil
	}
}

// partialRerunScheduler is the #202 fixture: a git workspace with committed
// skill fixtures, every stage's real context written by the runner, and a
// pr-create runner that always punts. gate wires feature-dev's real gate.
func partialRerunScheduler(t *testing.T, runner StageRunner, gate bool) (*Scheduler, string) {
	t.Helper()
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
	s := &Scheduler{
		trustGate:      allWritersGate(),
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
	if gate {
		s.WithStageGates(map[state.PipelineStage]gates.StageGate{state.StageFeatureDev: gates.FeatureDevGate{}})
	}
	return s, root
}

func ranStage(r *partialRerunRunner, stage state.PipelineStage) bool {
	_, ok := r.prompt(stage)
	return ok
}

// AC 4 scenario 1 and AC 1: the remaining criterion is the run's scope. The
// runtime carries it, the planning context names it, and the planning and
// feature-dev prompts say the rest of the issue is already on main.
func TestPartialRerun_RemainderIsTheRunScope(t *testing.T) {
	runner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}

	var planningCtx map[string]any
	runner.onStage = func(p StageRunParams) {
		// planning-{N}.json as feature-dev reads it: after the splice.
		if p.Stage != state.StageFeatureDev {
			return
		}
		path, err := stagecontext.ContextPath(root, p.IssueNumber, "planning")
		if err != nil {
			t.Errorf("planning context path: %v", err)
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read planning context: %v", err)
			return
		}
		_ = json.Unmarshal(data, &planningCtx)
	}
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	s.runPipeline(context.Background(), types.BoardItem{Number: 3160, Repo: "nightgauge/test", ID: "item-3160", NodeID: "I_3160"})

	if snap == nil || snap.PartialRerun == nil || snap.PartialRerun.PR != 3141 ||
		len(snap.PartialRerun.Criteria) != 1 || snap.PartialRerun.Criteria[0] != partialRerunCriterion {
		t.Fatalf("runtime partial-rerun scope = %+v, want PR #3141 with the one remaining criterion", snap)
	}
	pd, ok := planningCtx["partial_delivery"].(map[string]any)
	if !ok {
		t.Fatalf("planning context has no partial_delivery field: %v", planningCtx)
	}
	if crit, _ := pd["criteria"].([]any); len(crit) != 1 || crit[0] != partialRerunCriterion {
		t.Fatalf("partial_delivery.criteria = %v, want [%q]", pd["criteria"], partialRerunCriterion)
	}
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		prompt, ok := runner.prompt(stage)
		if !ok {
			t.Fatalf("%s never ran", stage)
		}
		if !strings.Contains(prompt, partialRerunPromptHeading) || !strings.Contains(prompt, partialRerunCriterion) {
			t.Errorf("%s prompt does not carry the scope block and its criterion", stage)
		}
		if !strings.Contains(prompt, "already on `main`") {
			t.Errorf("%s prompt does not say the rest of the issue is already on main", stage)
		}
	}
}

// An unscoped run's planning and feature-dev prompts carry no scope block, so
// the cache prefix holds.
func TestPartialRerun_UnscopedPromptHasNoBlock(t *testing.T) {
	if got := partialRerunPromptBlock(state.StageFeatureDev, nil); got != "" {
		t.Fatalf("nil scope rendered %q, want \"\"", got)
	}
	scope := &state.PartialRerun{PR: 1, Criteria: []string{"x"}}
	if got := partialRerunPromptBlock(state.StageFeatureValidate, scope); got != "" {
		t.Fatalf("feature-validate got a scope block: %q", got)
	}

	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Criteria: []string{}}, nil
	}
	s.runPipeline(context.Background(), types.BoardItem{Number: 3161, Repo: "nightgauge/test", ID: "item-3161"})
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		if prompt, _ := runner.prompt(stage); strings.Contains(prompt, partialRerunPromptHeading) {
			t.Errorf("unscoped %s prompt carries the partial-rerun block", stage)
		}
	}
}

// AC 4 scenario 3 and AC 2: a partial re-run whose feature-dev leaves the
// branch level ends not_pipeline_actionable before feature-validate, with the
// remainder commented once and the issue left open.
func TestPartialRerun_NoDiffEndsBeforeValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate bool
	}{{"clean exit", false}, {"gate reports dev_produced_no_changes", true}} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &partialRerunRunner{}
			s, _ := partialRerunScheduler(t, runner, tc.gate)
			s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
			commenter := &recordingCommenter{}
			s.noDiffCommenter = commenter
			var ok bool
			s.OnPipelineComplete(func(_ string, _ int, _ *state.RuntimeState, success bool) { ok = success })

			_, terminalKind := s.runPipeline(context.Background(),
				types.BoardItem{Number: 3162, Repo: "nightgauge/test", ID: "item-3162", NodeID: "I_3162"})

			if !ranStage(runner, state.StageFeatureDev) {
				t.Fatal("feature-dev never ran; the fixture is wrong")
			}
			if ranStage(runner, state.StageFeatureValidate) {
				t.Fatal("feature-validate ran on a no-diff partial re-run")
			}
			if ok {
				t.Fatal("run reported success")
			}
			if terminalKind != TerminalKindNotPipelineActionable {
				t.Fatalf("terminal kind = %q, want %q", terminalKind, TerminalKindNotPipelineActionable)
			}
			if n := commenter.count(); n != 1 {
				t.Fatalf("posted %d comment(s), want 1", n)
			}
			if !strings.Contains(commenter.bodies[0], partialRerunCriterion) {
				t.Errorf("comment does not name the remaining criterion: %q", commenter.bodies[0])
			}
		})
	}
}

// The same level branch on an unscoped run is unchanged: with no gate it goes
// on to feature-validate.
func TestPartialRerun_UnscopedNoDiffStillValidates(t *testing.T) {
	runner := &partialRerunRunner{}
	s, _ := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{}, errors.New("forge unreachable")
	}
	commenter := &recordingCommenter{}
	s.noDiffCommenter = commenter
	s.runPipeline(context.Background(), types.BoardItem{Number: 3163, Repo: "nightgauge/test", ID: "item-3163", NodeID: "I_3163"})
	if !ranStage(runner, state.StageFeatureValidate) {
		t.Fatal("an unscoped run stopped before feature-validate")
	}
	if commenter.count() != 0 {
		t.Fatalf("an unscoped run posted %d partial-rerun comment(s)", commenter.count())
	}
}

// A partial re-run whose feature-dev produced a change continues to
// feature-validate.
func TestPartialRerun_WithDiffValidates(t *testing.T) {
	runner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, runner, false)
	s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
	s.noDiffCommenter = &recordingCommenter{}
	runner.onStage = func(p StageRunParams) {
		if p.Stage != state.StageFeatureDev {
			return
		}
		wt := runWorktreeFor(t, root, "issue-3164")
		if wt == "" {
			t.Error("no run worktree to change")
			return
		}
		dir := filepath.Join(wt, "internal", "x")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "x_integration_test.go"), []byte("package x\n"), 0o644)
	}
	s.runPipeline(context.Background(), types.BoardItem{Number: 3164, Repo: "nightgauge/test", ID: "item-3164", NodeID: "I_3164"})
	if !ranStage(runner, state.StageFeatureValidate) {
		t.Fatal("a partial re-run with a diff stopped before feature-validate")
	}
}

// runWorktreeFor finds the run worktree whose path contains marker.
func runWorktreeFor(t *testing.T, root, marker string) string {
	t.Helper()
	out, err := gittest.Command(root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimPrefix(line, "worktree "); p != line && strings.Contains(p, marker) {
			return p
		}
	}
	return ""
}
