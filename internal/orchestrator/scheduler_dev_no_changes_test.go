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
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// honestReportEmptyDiskRunner is the #202 stage runner: every stage exits 0 and
// writes a context file that reports files_changed HONESTLY — because in the
// real incident the subagent genuinely did change five files. It just changed
// them in an agent-isolation worktree, so nothing lands on disk here.
//
// This is what makes #202 different from #74. The stage did not lie and did not
// idle; the pipeline simply asked the wrong witness.
type honestReportEmptyDiskRunner struct {
	mu     sync.Mutex
	stages []state.PipelineStage
}

func (r *honestReportEmptyDiskRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.stages = append(r.stages, params.Stage)
	r.mu.Unlock()

	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0755)
		payload := map[string]any{
			"schema_version": "1.0",
			"issue_number":   params.IssueNumber,
			"plan_file":      "plan.md",
			"approach":       "test",
			"ok":             true,
			// The honest report of work done somewhere else.
			"files_changed": map[string]any{
				"created":  []string{"src/added.go"},
				"modified": []string{"src/changed.go"},
				"deleted":  []string{},
			},
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
			"validation_status":  "passed",
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

func (r *honestReportEmptyDiskRunner) ran(stage state.PipelineStage) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.stages {
		if s == stage {
			return true
		}
	}
	return false
}

// TestScheduler_FeatureDev_NoChangesProduced_FailsBeforeValidate is the #202
// acceptance test.
//
// Pre-fix: feature-dev exited 0 after 31 minutes and $3.16, its gate read the
// dev context's truthful files_changed and passed, and the pipeline advanced.
// feature-validate then spent $0.87 discovering the branch was empty and
// reported it as its OWN validation_failed — $5.33 for zero output, charged to
// the wrong stage, with a lifetime-cap increment against an issue that had done
// nothing wrong.
//
// Post-fix the run must die at feature-dev's own gate, and feature-validate must
// never be invoked at all — that is where the $0.87 goes.
func TestScheduler_FeatureDev_NoChangesProduced_FailsBeforeValidate(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := gitWorkspace(t)

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
	// Commit the fixture skills. An untracked tree would read as "the stage
	// produced work" and the gate under test would never fire.
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "fixture")

	runner := &honestReportEmptyDiskRunner{}

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
	// Only feature-dev gets its REAL gate — the stage under test.
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StageFeatureDev: gates.FeatureDevGate{},
	})

	item := types.BoardItem{Number: 202, Repo: "nightgauge/test", ID: "item-202"}
	s.runPipeline(context.Background(), item)

	// 1. The headline acceptance criterion: the money-burning stage never ran.
	if runner.ran(state.StageFeatureValidate) {
		t.Errorf("feature-validate was invoked — the whole point of the gate is that it is not")
	}
	if !runner.ran(state.StageFeatureDev) {
		t.Fatalf("feature-dev never ran; the fixture is wrong, not the gate")
	}

	// 2. The run is FAILED, not a silent success.
	records := readDailyJSONLRecords(t, root)
	var rec *state.V2RunRecord
	for i := range records {
		if records[i].IssueNumber == item.Number {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("no run record for issue #%d (got %d records)", item.Number, len(records))
	}
	if rec.Outcome != "failed" {
		t.Fatalf("rec.Outcome = %q, want failed", rec.Outcome)
	}

	// 3. The failure is attributed to feature-dev and names the cause, rather
	//    than leaving a downstream stage to infer it.
	stage, ok := rec.Stages[string(state.StageFeatureDev)]
	if !ok {
		t.Fatalf("run record has no feature-dev stage detail")
	}
	if !strings.Contains(stage.Error, "produced none in the stage workspace") {
		t.Errorf("stage.Error does not name the cause: %q", stage.Error)
	}
	if n := len(stage.GateResults); n == 0 {
		t.Errorf("feature-dev stage has no gate results")
	} else if kind := stage.GateResults[n-1].Kind; kind != string(gates.KindNoOp) {
		t.Errorf("gate result kind = %q, want %q", kind, gates.KindNoOp)
	}

	// 4. The terminal kind is the distinct one, not the generic
	//    premature_turn_end the KindNoOp wrapper would otherwise produce, and
	//    emphatically not validation_failed (which is what #202 actually
	//    recorded — feature-validate's honest report of someone else's defect).
	var found bool
	for _, er := range readExitRecords(t, root) {
		if er.Stage != string(state.StageFeatureDev) {
			continue
		}
		found = true
		if er.TerminalKind != TerminalKindDevProducedNoChanges {
			t.Errorf("exit record TerminalKind = %q, want %q", er.TerminalKind, TerminalKindDevProducedNoChanges)
		}
		if er.Success {
			t.Errorf("exit record Success = true, want false")
		}
	}
	if !found {
		t.Errorf("no exit record written for feature-dev")
	}
}

// TestClassifyTerminalKind_DevProducedNoChanges pins the text-classified path.
// The scheduler wraps every KindNoOp reason into a "premature turn end:"
// string, so without the marker check ordering, a text-only consumer (the exit
// record replay, the SDK health classifier) would disagree with the gate about
// what the same failure was.
func TestClassifyTerminalKind_DevProducedNoChanges(t *testing.T) {
	errText := "premature turn end: stage exited 0 with no state change (gate no-op): " +
		"[dev-produced-no-changes] dev reported file changes but produced none in the stage workspace"
	if got := ClassifyTerminalKind(errText); got != TerminalKindDevProducedNoChanges {
		t.Errorf("ClassifyTerminalKind = %q, want %q", got, TerminalKindDevProducedNoChanges)
	}

	// A generic no-op still classifies as premature_turn_end.
	generic := "premature turn end: stage exited 0 with no state change (gate no-op): plan file missing"
	if got := ClassifyTerminalKind(generic); got != TerminalKindPrematureTurnEnd {
		t.Errorf("ClassifyTerminalKind(generic) = %q, want %q", got, TerminalKindPrematureTurnEnd)
	}
}

// TestHasUncommittedWork_IgnoresBookkeeping covers the defect that would have
// neutered #202's gate wherever it mattered most.
//
// The #3542 recovery reclassifies a failure as worktree_uncommitted — a kind
// that means "recovered, not a failure", so it skips the LifetimeIssueFailures
// increment and the board revert. It fires whenever the worktree looks dirty
// and no terminal kind is set. Counting `.nightgauge/**` there meant a JSON
// file the pipeline wrote itself could launder any real defect into a
// non-event, and sweep pipeline state into the user's branch on the way out.
func TestHasUncommittedWork_IgnoresBookkeeping(t *testing.T) {
	repo := gitWorkspace(t)

	if hasUncommittedWork(repo) {
		t.Fatalf("clean repo reported uncommitted work")
	}

	// Pipeline exhaust only — deliberately NOT gitignored, as in a consumer
	// repo (and as `.nightgauge/attention/` already is in this one).
	if err := os.MkdirAll(filepath.Join(repo, ".nightgauge", "pipeline"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nightgauge", "pipeline", "dev-202.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if hasUncommittedWork(repo) {
		t.Errorf("bookkeeping counted as recoverable work — any failure here would be relabeled worktree_uncommitted and stop counting")
	}

	// One real file flips it back on: the #3365 rescue must still work.
	if err := os.WriteFile(filepath.Join(repo, "feature.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !hasUncommittedWork(repo) {
		t.Errorf("real deliverable work not detected — the #3365 recovery is disabled")
	}
}

// gitWorkspace returns a temp dir initialized as a git repo on `main` with one
// commit, so the feature-dev gate's base-ref resolution succeeds.
func gitWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init")
	gitIn(t, dir, "checkout", "-b", "main")
	gitIn(t, dir, "config", "user.email", "sched-test@example.com")
	gitIn(t, dir, "config", "user.name", "Scheduler Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-m", "base")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gittest.Command(dir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
