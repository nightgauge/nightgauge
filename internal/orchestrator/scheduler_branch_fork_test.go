package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// branchAwareStageRunner is successStageRunner plus the one field that matters
// for #163: issue-pickup records the feature branch in its context, which is
// how every later stage learns which branch to compare against origin.
type branchAwareStageRunner struct {
	mu     sync.Mutex
	calls  map[state.PipelineStage]int
	branch string
}

func newBranchAwareStageRunner(branch string) *branchAwareStageRunner {
	return &branchAwareStageRunner{calls: make(map[state.PipelineStage]int), branch: branch}
}

func (r *branchAwareStageRunner) count(stage state.PipelineStage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[stage]
}

func (r *branchAwareStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls[params.Stage]++
	r.mu.Unlock()

	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0o755)
		payload := map[string]any{
			"schema_version":     "1.0",
			"issue_number":       params.IssueNumber,
			"branch":             r.branch,
			"plan_file":          "plan.md",
			"approach":           "test",
			"files_to_create":    []string{},
			"files_to_modify":    []string{},
			"files_to_read":      []string{},
			"validation_steps":   []string{},
			"ok":                 true,
			"validation_status":  "passed",
			"build_verification": map[string]any{"status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"build":              map[string]any{"passed": true},
			"unit_tests":         map[string]any{"passed": true},
			"integration_tests":  map[string]any{"passed": false},
			"manual_checklist":   []any{},
			"dead_code_warnings": []any{},
			"files_changed":      map[string]any{"created": []string{}, "modified": []string{}, "deleted": []string{}},
			"quality_checks":     map[string]any{"code_standards": "passed", "security_review": "passed", "type_check": "passed", "dead_code_scan": "passed"},
			"errorCategory":      "",
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0o644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// TestScheduler_BranchForkPreflight_BlocksBeforeSpendingTokens is the #163
// reproduction at the scheduler level, in the shape that was observed: a prior
// run was killed mid-push having already pushed, so origin's branch head is a
// commit this worktree has never seen, and the worktree's branch sits at the
// base.
//
// The assertion that matters is NEGATIVE — feature-planning, feature-dev and
// feature-validate must never be dispatched. Pre-fix all three ran to
// completion, regenerating an entire implementation (~$25) whose only possible
// ending was the push rejection that was already guaranteed before the first
// token was spent. Detecting the fork "eventually" is not the fix; detecting it
// before the spend is.
func TestScheduler_BranchForkPreflight_BlocksBeforeSpendingTokens(t *testing.T) {
	f := newForkFixture(t)
	root := f.work
	const branch = "fix/163-orphaned-push"

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

	// The orphan: a killed run's commit, pushed and then abandoned on origin.
	other := f.clone()
	gittest.Run(t, other, "checkout", "-b", branch)
	orphan := commitFile(t, other, "impl.go", "// implementation A\n", "feat: implementation A")
	gittest.Run(t, other, "push", "origin", branch)

	// This run's branch, created from the base exactly as issue-pickup makes it.
	gittest.Run(t, root, "branch", branch)

	runner := newBranchAwareStageRunner(branch)
	s := &Scheduler{
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

	item := types.BoardItem{Number: 163, Repo: "nightgauge/nightgauge", ID: "item-163"}
	s.runPipeline(context.Background(), item)

	// 1. issue-pickup ran (it is the stage that resolves the branch); every
	//    stage after it was blocked at the pre-flight.
	if got := runner.count(state.StageIssuePickup); got != 1 {
		t.Fatalf("issue-pickup ran %d times, want 1", got)
	}
	for _, stage := range []state.PipelineStage{
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	} {
		if got := runner.count(stage); got != 0 {
			t.Errorf("%s was dispatched %d time(s) onto a branch that cannot push — the whole point is that it is not", stage, got)
		}
	}

	// 2. The run is recorded FAILED and names the fork, not a generic crash.
	records := readDailyJSONLRecords(t, root)
	var rec *state.V2RunRecord
	for i := range records {
		if records[i].IssueNumber == item.Number {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("no run record for #%d (got %d records)", item.Number, len(records))
	}
	if rec.Outcome != "failed" {
		t.Errorf("rec.Outcome = %q, want failed", rec.Outcome)
	}
	if rec.TerminalFailureKind != TerminalKindBranchForked {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindBranchForked)
	}

	// 3. The failure names both SHAs. A fork is only actionable if the operator
	//    can see what diverged.
	stageDetail, ok := rec.Stages[string(state.StageFeaturePlanning)]
	if !ok {
		t.Fatalf("no feature-planning stage detail on the run record")
	}
	if !strings.Contains(stageDetail.Error, "[branch-forked]") {
		t.Errorf("stage error = %q, want the [branch-forked] marker", stageDetail.Error)
	}
	if !strings.Contains(stageDetail.Error, orphan[:8]) {
		t.Errorf("stage error = %q, want it to name the orphaned remote head %s", stageDetail.Error, orphan[:8])
	}

	// 4. The orphan is NOT this run's own push (this run pushed nothing), so the
	//    post-run reclamation must have left it standing rather than deleting a
	//    commit whose provenance it cannot prove.
	if out := gittest.Run(t, root, "ls-remote", "--heads", "origin", "refs/heads/"+branch); !strings.Contains(out, orphan) {
		t.Errorf("origin/%s should still carry %s after a declined reclamation; ls-remote = %q", branch, orphan, out)
	}
}

// TestScheduler_BranchForkPreflight_DoesNotBlockAHealthyRun is the false-positive
// guard. The pre-flight runs before every post-pickup stage, so a bug in it
// would not degrade the pipeline — it would stop it entirely. A branch whose
// remote copy the run itself pushed must sail through all six stages.
func TestScheduler_BranchForkPreflight_DoesNotBlockAHealthyRun(t *testing.T) {
	f := newForkFixture(t)
	root := f.work
	const branch = "feat/164-healthy"

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

	// The run's own branch, pushed to origin — the ordinary post-validate state.
	gittest.Run(t, root, "checkout", "-b", branch)
	commitFile(t, root, "impl.go", "// work\n", "feat: work")
	gittest.Run(t, root, "push", "origin", branch)

	runner := newBranchAwareStageRunner(branch)
	s := &Scheduler{
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

	item := types.BoardItem{Number: 164, Repo: "nightgauge/nightgauge", ID: "item-164"}
	s.runPipeline(context.Background(), item)

	for _, stage := range []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
	} {
		if got := runner.count(stage); got == 0 {
			t.Errorf("%s never ran — the fork pre-flight blocked a healthy branch", stage)
		}
	}
}

// TestClassifyTerminalKind_BranchForked covers both routes into the kind: the
// pre-flight's marker, and a fork that first surfaces at push time as git's own
// rejection wording. Pre-fix the latter fell through to subagent_crash — every
// retry then looked like a fresh process death rather than the same
// unrecoverable fork, which is why the loop was never recognised as a loop.
func TestClassifyTerminalKind_BranchForked(t *testing.T) {
	forked := []string{
		"[branch-forked] origin/fix/163-x is at abc12345, which is NOT an ancestor of the local tip def67890",
		"PUSH REJECTED: non-fast-forward.",
		`hint: Updates were rejected because the remote contains work that you do not have locally. PUSH REJECTED — fetch first`,
		"branch_forked",
	}
	for _, text := range forked {
		if got := ClassifyTerminalKind(text); got != TerminalKindBranchForked {
			t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", text, got, TerminalKindBranchForked)
		}
	}

	// A push rejection carries "exit 1" from SetStageError; branch_forked must
	// win over the generic subagent-crash fallback that substring would hit.
	withExit := "feature-validate exit 1: PUSH REJECTED: non-fast-forward."
	if got := ClassifyTerminalKind(withExit); got != TerminalKindBranchForked {
		t.Errorf("ClassifyTerminalKind(%q) = %q, want %q — the crash fallback must not win", withExit, got, TerminalKindBranchForked)
	}

	// And it must not steal from neighbouring kinds.
	notForked := map[string]string{
		"API Error: Overloaded":                              TerminalKindApiOverloaded,
		"[validation-failed] quality gates failed":           TerminalKindValidationFailed,
		"[no-changes-produced] zero commits ahead of main":   TerminalKindNoChangesProduced,
		"subagent crash: exit 137, killed by signal SIGKILL": TerminalKindSubagentCrash,
	}
	for text, want := range notForked {
		if got := ClassifyTerminalKind(text); got != want {
			t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", text, got, want)
		}
	}
}
