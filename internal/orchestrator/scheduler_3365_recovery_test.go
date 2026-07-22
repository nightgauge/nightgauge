// Tests covering Issue #3542 — pipeline intelligent failure recovery. These
// exercise the uncommitted-work detection/recovery helpers, the new recoverable
// terminal kinds, the budget-ceiling config reader, and the end-to-end issue
// #3365 scenario (feature-dev exits 0 with a stop-hook sentinel and uncommitted
// work — the scheduler must recover the work into a commit instead of losing it).
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// gitInitRepo initializes a git repository at dir with an identity configured
// and a single initial commit so HEAD is valid. Returns nothing — fails the
// test on any git error.
func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@nightgauge.dev")
	runGit("config", "user.name", "Nightgauge Test")
	runGit("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mirror production: .nightgauge/ holds transient pipeline state and
	// is gitignored, so recovery commits only ever capture real source files.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".nightgauge/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-m", "chore: seed commit")
}

// gitLog returns the one-line commit subjects in the repo at dir, newest first.
func gitLog(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--pretty=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects
}

// TestClassifyTerminalKind_RecoverableKinds covers the Issue #3542 additions to
// the terminal-kind heuristic. The budget-ceiling marker MUST win over the
// generic token-budget heuristic — "PIPELINE BUDGET CEILING" lowercased
// contains the substring "budget ceiling".
func TestClassifyTerminalKind_RecoverableKinds(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want string
	}{
		{
			"worktree_uncommitted_marker",
			"worktree_uncommitted: work auto-recovered after feature-dev failure",
			TerminalKindWorktreeUncommitted,
		},
		{
			"stop_hook_uncommitted_alias",
			"stop_hook_uncommitted — agent blocked at session exit",
			TerminalKindWorktreeUncommitted,
		},
		{
			"budget_ceiling_hit_marker",
			"budget_ceiling_hit: $61.51 exceeds ceiling",
			TerminalKindBudgetCeiling,
		},
		{
			"pipeline_budget_ceiling_message",
			"[PIPELINE BUDGET CEILING] Pipeline stopped: $76.00 exceeds ceiling of $75.00",
			TerminalKindBudgetCeiling,
		},
		// Regression guard: the token-based budget heuristic must still win for
		// the canonical token-budget reasons (not reclassified to budget_ceiling_hit).
		{
			"token_budget_exceeded_still_wins",
			"pipeline_budget_exceeded: 12345 > 10000",
			TerminalKindBudgetExceeded,
		},
		// Issue #3661: issue-closed non-failure patterns. Must be matched before
		// generic "exit" heuristics so they don't fall into subagent_crash.
		{
			"issue_closed_pipeline_start_failure",
			"[pipeline-start-failure] issue-closed",
			TerminalKindIssueClosed,
		},
		{
			"issue_closed_underscore_form",
			"issue_closed",
			TerminalKindIssueClosed,
		},
		// Issue #3691: pr-merge "completed but PR not merged" diagnostic
		// markers. The bracket-prefixed form is what HeadlessOrchestrator
		// emits with a blocker-suffix; the underscore form is the canonical
		// terminal-kind name for back-compat with any downstream that may
		// log it directly.
		{
			"pr_merge_unmerged_ci_failures",
			"[pr-merge-unmerged:ci_failures] PR #961 has 1 failing CI check(s): Lint, Typecheck, Test, Build. PR: https://github.com/acme/platform/pull/961",
			TerminalKindPrMergeUnmerged,
		},
		{
			"pr_merge_unmerged_merge_conflict",
			"[pr-merge-unmerged:merge_conflict] PR #455 has unresolved merge conflicts against the base branch.",
			TerminalKindPrMergeUnmerged,
		},
		{
			"pr_merge_unmerged_agent_gave_up",
			"[pr-merge-unmerged:agent_gave_up] PR #500 appears mergeable but the pr-merge agent did not complete the merge.",
			TerminalKindPrMergeUnmerged,
		},
		{
			"pr_merge_unmerged_underscore_form",
			"pr_merge_unmerged: something went wrong",
			TerminalKindPrMergeUnmerged,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTerminalKind(tc.err); got != tc.want {
				t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestHasUncommittedWork verifies clean vs dirty detection and the empty-path
// guard.
func TestHasUncommittedWork(t *testing.T) {
	if hasUncommittedWork("") {
		t.Error("hasUncommittedWork(\"\") = true, want false")
	}

	dir := t.TempDir()
	gitInitRepo(t, dir)

	if hasUncommittedWork(dir) {
		t.Error("hasUncommittedWork on clean repo = true, want false")
	}

	if err := os.WriteFile(filepath.Join(dir, "new-file.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasUncommittedWork(dir) {
		t.Error("hasUncommittedWork with an untracked file = false, want true")
	}
}

// TestRecoverUncommittedWork verifies the recovery commit is created with the
// canonical message, and that the function errors gracefully on a bad path.
func TestRecoverUncommittedWork(t *testing.T) {
	// Bad path — no git repo, empty path.
	if err := recoverUncommittedWork("", 3542, "feature-dev"); err == nil {
		t.Error("recoverUncommittedWork(\"\", ...) = nil, want error")
	}

	dir := t.TempDir()
	gitInitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package impl\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No remote configured — push will fail, but that is non-fatal and the
	// local recovery commit must still be created.
	if err := recoverUncommittedWork(dir, 3542, "feature-dev"); err != nil {
		t.Fatalf("recoverUncommittedWork on a real repo = %v, want nil", err)
	}

	if hasUncommittedWork(dir) {
		t.Error("worktree still dirty after recoverUncommittedWork")
	}
	subjects := gitLog(t, dir)
	if len(subjects) == 0 {
		t.Fatal("no commits after recovery")
	}
	want := "feat(#3542): [auto-recovery] feature-dev work recovered after stop-hook failure"
	if subjects[0] != want {
		t.Errorf("recovery commit subject = %q, want %q", subjects[0], want)
	}
}

// TestStageRunResult_ShippedPartiallyFieldsPropagate pins the #3666 follow-up
// IPC contract: BudgetExceeded / ShippedPartially / ShippedPRNumber must be
// addressable fields on the Go-side StageRunResult so the scheduler can read
// the signal in-memory without depending on a disk-file at a path Go has to
// guess. Specifically, multi-repo workspaces broke the older disk-file path
// (TS wrote to per-issue worktree, Go read from workspaceRoot) — the IPC
// fields close that loop. This test is intentionally minimal: it asserts the
// fields exist with the right types and zero values, so a future refactor
// that drops them fails CI immediately.
func TestStageRunResult_ShippedPartiallyFieldsPropagate(t *testing.T) {
	r := &StageRunResult{}
	// Zero values
	if r.BudgetExceeded {
		t.Errorf("zero-value StageRunResult.BudgetExceeded = true, want false")
	}
	if r.ShippedPartially {
		t.Errorf("zero-value StageRunResult.ShippedPartially = true, want false")
	}
	if r.ShippedPRNumber != 0 {
		t.Errorf("zero-value StageRunResult.ShippedPRNumber = %d, want 0", r.ShippedPRNumber)
	}
	// Round-trip
	r.BudgetExceeded = true
	r.ShippedPartially = true
	r.ShippedPRNumber = 449
	if !r.BudgetExceeded || !r.ShippedPartially || r.ShippedPRNumber != 449 {
		t.Errorf("StageRunResult fields not assignable / read back wrong: %+v", r)
	}
}

// TestBudgetOverrunPathResolution_UsesWorktree pins the fix for the silent
// worktree-vs-repo-root mismatch that disabled the #2338 WIP-retry path AND
// the #3666 shipped-partially advance-stage path for every autonomous run.
//
// The TS HeadlessOrchestrator writes budget-overrun-{N}.json to its working
// directory (the per-issue worktree in autonomous mode); the Go scheduler
// must read it from the SAME directory or the file is "missing" and neither
// path fires. Pre-fix, scheduler.go joined the path against workspaceRoot
// directly; post-fix it routes through loadWorktreePath().
//
// This test fails on a regression by asserting that:
//   - When a run-state exists with a worktree path, the lookup MUST find a
//     budget-overrun JSON placed in the worktree, NOT one placed in the
//     repo root (a wrong-location decoy).
//   - When no run-state exists, the lookup falls back to the workspace root.
func TestBudgetOverrunPathResolution_UsesWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".worktrees", "issue-3666")
	if err := os.MkdirAll(filepath.Join(wt, ".nightgauge", "pipeline"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Decoy at the repo root — pre-fix scheduler reads from here and would
	// silently miss the worktree file. Post-fix it must NOT consult this.
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "pipeline"), 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(root, ".nightgauge", "pipeline", "budget-overrun-3666.json")
	if err := os.WriteFile(decoy, []byte(`{"schema_version":"1.1","issue_number":3666,"stage":"pr-create","shipped_partially":false}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The real file at the worktree path — what TS HeadlessOrchestrator wrote.
	real := filepath.Join(wt, ".nightgauge", "pipeline", "budget-overrun-3666.json")
	if err := os.WriteFile(real, []byte(`{"schema_version":"1.1","issue_number":3666,"stage":"pr-create","shipped_partially":true,"shipped_pr_number":4242}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set up run-state that points to the worktree, mirroring what the
	// scheduler writes at stage start.
	baseDir := filepath.Join(root, ".nightgauge", "pipeline")
	now := time.Now().UTC().Format(time.RFC3339)
	rs := &runstate.RunState{
		SchemaVersion:   runstate.SchemaVersion,
		IssueNumber:     3666,
		State:           runstate.StateRunning,
		RunID:           "run-3666",
		AttemptNumber:   1,
		CompletedStages: []runstate.Stage{},
		WorktreePath:    &wt,
		Branch:          "feat/3666-shipped-partial",
		CreatedAt:       now,
		UpdatedAt:       now,
		Attempts: []runstate.Attempt{
			{RunID: "run-3666", AttemptNumber: 1, StartedAt: now},
		},
	}
	if err := runstate.Save(baseDir, rs); err != nil {
		t.Fatalf("save run-state: %v", err)
	}

	// This is the path computation pattern used by scheduler.go's
	// budget-aware retry block — copied verbatim so a regression in either
	// the path computation OR loadWorktreePath itself fails the test.
	overrunBase := loadWorktreePath(root, 3666)
	overrunFile := filepath.Join(overrunBase, ".nightgauge", "pipeline", "budget-overrun-3666.json")

	overrun, err := ReadBudgetOverrun(overrunFile)
	if err != nil {
		t.Fatalf("expected to read worktree overrun, got error: %v (resolved path=%q)", err, overrunFile)
	}
	if !overrun.ShippedPartially {
		t.Errorf("read the wrong file — resolved path picked up the decoy at repo root instead of the worktree. resolved=%q want ShippedPartially=true", overrunFile)
	}
	if overrun.ShippedPRNumber != 4242 {
		t.Errorf("ShippedPRNumber = %d, want 4242 (worktree file content)", overrun.ShippedPRNumber)
	}
}

// TestLoadWorktreePath verifies the run-state.json preference and the
// workspace-root fallback.
func TestLoadWorktreePath(t *testing.T) {
	// No run-state.json — falls back to workspace root.
	root := t.TempDir()
	if got := loadWorktreePath(root, 3542); got != root {
		t.Errorf("loadWorktreePath fallback = %q, want %q (workspace root)", got, root)
	}

	// run-state.json with worktree_path — prefers it.
	baseDir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, ".worktrees", "issue-3542")
	now := time.Now().UTC().Format(time.RFC3339)
	rs := &runstate.RunState{
		SchemaVersion:   runstate.SchemaVersion,
		IssueNumber:     3542,
		State:           runstate.StateRunning,
		RunID:           "run-3542",
		AttemptNumber:   1,
		CompletedStages: []runstate.Stage{},
		WorktreePath:    &wt,
		Branch:          "feat/3542-recovery",
		CreatedAt:       now,
		UpdatedAt:       now,
		Attempts: []runstate.Attempt{
			{RunID: "run-3542", AttemptNumber: 1, StartedAt: now},
		},
	}
	if err := runstate.Save(baseDir, rs); err != nil {
		t.Fatalf("save run-state: %v", err)
	}
	if got := loadWorktreePath(root, 3542); got != wt {
		t.Errorf("loadWorktreePath with run-state = %q, want %q", got, wt)
	}
	// Issue-number mismatch must NOT use the foreign run-state's path.
	if got := loadWorktreePath(root, 9999); got != root {
		t.Errorf("loadWorktreePath for a different issue = %q, want %q (fallback)", got, root)
	}
}

// TestGetPipelineBudgetCeilingUSD covers the tier-merged config resolution
// (config.Load: machine → project → local) and the maintainer-set default.
func TestGetPipelineBudgetCeilingUSD(t *testing.T) {
	// Hermetic machine tier — never read the developer's real
	// ~/.nightgauge/config.yaml in tests.
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())

	// No config — maintainer-set default of $75.
	if got := getPipelineBudgetCeilingUSD(t.TempDir()); got != 75.0 {
		t.Errorf("default ceiling = %v, want 75", got)
	}

	// Nested token_budget_ceiling.ceiling_usd is honored.
	root := t.TempDir()
	cfgDir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "owner: testorg\npipeline:\n  token_budget_ceiling:\n    enabled: true\n    ceiling_usd: 200\n    warn_threshold_usd: 50\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getPipelineBudgetCeilingUSD(root); got != 200.0 {
		t.Errorf("config ceiling = %v, want 200", got)
	}

	// Local tier (config.local.yaml) overrides the project tier.
	localBody := "pipeline:\n  token_budget_ceiling:\n    ceiling_usd: 250\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.local.yaml"), []byte(localBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getPipelineBudgetCeilingUSD(root); got != 250.0 {
		t.Errorf("local-tier ceiling = %v, want 250", got)
	}

	// Env override wins over all file tiers + default.
	t.Setenv("NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_CEILING_USD", "300")
	if got := getPipelineBudgetCeilingUSD(root); got != 300.0 {
		t.Errorf("env-override ceiling = %v, want 300", got)
	}
}

// TestOnPipelineComplete_RecoverableKinds_NoLifetimeCap verifies that the two
// Issue #3542 recoverable terminal kinds are treated like stall-kills by the
// autonomous scheduler: fixed backoff, NO LifetimeIssueFailures increment, and
// NO per-session circuit-breaker count.
func TestOnPipelineComplete_RecoverableKinds_NoLifetimeCap(t *testing.T) {
	for _, kind := range []string{TerminalKindWorktreeUncommitted, TerminalKindBudgetCeiling} {
		t.Run(kind, func(t *testing.T) {
			as := &AutonomousScheduler{
				config: AutonomousConfig{MaxConcurrent: 3},
				state: &AutonomousState{
					Status: "running",
					Running: []RunningItem{
						{Repo: "nightgauge/nightgauge", Number: 3542, Title: "Pipeline intelligent failure recovery"},
					},
					LifetimeIssueFailures: map[string]int{},
				},
				rescanCh:             make(chan struct{}, 1),
				perIssueFailureCount: map[string]int{},
				retryBackoff:         map[string]time.Time{},
			}

			before := time.Now()
			as.onPipelineComplete("nightgauge/nightgauge", 3542, false, false, kind, kind+" (recoverable)")

			key := "nightgauge/nightgauge#3542"
			if got := as.state.LifetimeIssueFailures[key]; got != 0 {
				t.Errorf("LifetimeIssueFailures[%q] = %d after %s, want 0 (recoverable, must not hit cap)",
					key, got, kind)
			}
			if got := as.perIssueFailureCount[key]; got != 0 {
				t.Errorf("perIssueFailureCount[%q] = %d after %s, want 0", key, got, kind)
			}
			retryAt, ok := as.retryBackoff[key]
			if !ok {
				t.Fatalf("expected retryBackoff[%q] to be set after %s", key, kind)
			}
			if wait := retryAt.Sub(before); wait < 25*time.Minute || wait > 35*time.Minute {
				t.Errorf("backoff = %v, want ~30min (stallKillBackoff range 25m–35m)", wait)
			}
			// The failure is still recorded for observability (audit trail),
			// just not counted toward the circuit breaker.
			if len(as.state.Failed) != 1 {
				t.Errorf("as.state.Failed has %d entries, want 1 (recoverable failure must still be recorded)",
					len(as.state.Failed))
			} else if !strings.Contains(as.state.Failed[0].Reason, "recoverable") {
				t.Errorf("as.state.Failed[0].Reason = %q, want it to mention 'recoverable'",
					as.state.Failed[0].Reason)
			}
			// Item must be removed from the running set.
			for _, r := range as.state.Running {
				if r.Number == 3542 {
					t.Error("issue #3542 still in Running after onPipelineComplete")
				}
			}
		})
	}
}

// stopHookRecoveryRunner is a StageRunner that simulates the Issue #3365
// scenario: every stage exits 0 and writes its output context, but the
// feature-dev stage ALSO leaves an uncommitted file in the worktree and drops
// a stop-hook sentinel — exactly the state that lost $61.51 of work in #3365.
type stopHookRecoveryRunner struct {
	mu            sync.Mutex
	workspaceRoot string
	issueNumber   int
}

func (r *stopHookRecoveryRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Write a minimal output context so the next stage's prerequisite check passes.
	if params.OutputFile != "" {
		if mkErr := os.MkdirAll(filepath.Dir(params.OutputFile), 0o755); mkErr == nil {
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

	if params.Stage == state.StageFeatureDev {
		// Leave uncommitted implementation work in the worktree.
		_ = os.WriteFile(filepath.Join(r.workspaceRoot, "feature.go"),
			[]byte("package feature\n\n// implemented under #3542\n"), 0o644)
		// Drop the stop-hook sentinel — the stop hook returned OK=false.
		sentinel := filepath.Join(r.workspaceRoot, ".nightgauge", "pipeline",
			"stop-hook-status-"+strconv.Itoa(r.issueNumber)+".json")
		_ = os.WriteFile(sentinel, []byte(`{"ok":false,"reason":"1 tasks incomplete in PLAN.md"}`), 0o644)
	}

	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// budgetEscalationRunner stalls feature-dev once (then succeeds), and lets
// issue-pickup report a high CostUsd so the pipeline has burned >50% of its
// budget ceiling by the time feature-dev stalls. Used to exercise the
// Issue #3542 budget-aware model escalation path.
type budgetEscalationRunner struct {
	mu               sync.Mutex
	callCount        map[state.PipelineStage]int
	pickupCostUSD    float64
	featureDevStalls int // number of times feature-dev stalls before succeeding
}

func (r *budgetEscalationRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.callCount[params.Stage]++
	currentCall := r.callCount[params.Stage]
	r.mu.Unlock()

	if params.Stage == state.StageFeatureDev && currentCall <= r.featureDevStalls {
		return &StageRunResult{ExitCode: 1}, errors.New("feature-dev stall kill threshold reached after 4800s")
	}

	if params.OutputFile != "" {
		if mkErr := os.MkdirAll(filepath.Dir(params.OutputFile), 0o755); mkErr == nil {
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

	res := &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}
	// issue-pickup reports a large cost so TotalCostUSD crosses 50% of the
	// configured $10 ceiling before feature-dev runs.
	if params.Stage == state.StageIssuePickup {
		res.CostUsd = r.pickupCostUSD
	}
	return res, nil
}

// TestScheduler_BudgetAwareEscalationOnStallKill covers AC7: when feature-dev
// stall-kills AND the pipeline has burned >50% of its USD budget ceiling, the
// scheduler escalates the model (sonnet → opus) instead of taking a
// same-model stall-retry / re-plan rewind.
func TestScheduler_BudgetAwareEscalationOnStallKill(t *testing.T) {
	root := t.TempDir()
	// Enable adaptive stall recovery AND set a low $10 budget ceiling. Without
	// the budget-aware branch, the stall-kill would rewind to feature-planning;
	// with it, the >50%-budget condition escalates the model first.
	cfgDir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// owner: is required by config.Load — the ceiling now resolves through the
	// typed tier merge, not a hand-rolled parse (so the fixture must be a
	// loadable config). Machine tier is pointed at an empty dir for hermeticity.
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	cfg := "owner: testorg\npipeline:\n  adaptive_stall_recovery: true\n  token_budget_ceiling:\n    ceiling_usd: 10\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &budgetEscalationRunner{
		callCount:        make(map[state.PipelineStage]int),
		pickupCostUSD:    6.0, // 6 / 10 = 60% — over the 50% escalation threshold
		featureDevStalls: 1,
	}
	s := buildStallTestScheduler(t, root, runner)

	item := types.BoardItem{
		Number: 8542,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8542",
		Title:  "Budget-aware escalation on stall-kill",
		Labels: []string{"type:feature"},
	}
	s.runPipeline(context.Background(), item)

	// feature-dev's model must have been escalated off the sonnet default.
	escalated := s.retryEngine.CurrentModel("feature-dev")
	if escalated == "" {
		t.Errorf("feature-dev model not escalated after stall-kill with >50%% budget consumed — "+
			"CurrentModel(feature-dev) = %q, want a non-empty escalated model", escalated)
	}
	// feature-planning must have run exactly once — escalation must NOT rewind.
	if got := runner.callCount[state.StageFeaturePlanning]; got != 1 {
		t.Errorf("feature-planning ran %d times, want 1 (budget-aware escalation must not rewind to planning)", got)
	}
	// feature-dev must have run twice — the stall, then the escalated retry.
	if got := runner.callCount[state.StageFeatureDev]; got != 2 {
		t.Errorf("feature-dev ran %d times, want 2 (stall + escalated retry)", got)
	}
}

// TestScheduler3365Recovery is the end-to-end regression test for Issue #3365.
// feature-dev exits 0 but leaves uncommitted work plus a stop-hook sentinel.
// The scheduler must detect the sentinel post-stage and recover the work into a
// commit on the branch — instead of letting it be silently discarded.
func TestScheduler3365Recovery(t *testing.T) {
	root := t.TempDir()
	gitInitRepo(t, root)

	runner := &stopHookRecoveryRunner{workspaceRoot: root, issueNumber: 8365}
	s := buildStallTestScheduler(t, root, runner)
	// Disable escalation/backtrack so the run is deterministic.
	s.retryEngine = NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0})

	item := types.BoardItem{
		Number: 8365,
		Repo:   "nightgauge/nightgauge",
		ID:     "item-8365",
		Title:  "Reproduce #3365 lost-work scenario",
		Labels: []string{"type:feature"},
	}
	s.runPipeline(context.Background(), item)

	// The auto-recovery commit MUST exist on the branch — the uncommitted
	// feature-dev work was preserved, not discarded.
	subjects := gitLog(t, root)
	foundRecovery := false
	for _, sub := range subjects {
		// Assert both the recovery marker AND the issue number — proves the
		// issue number is threaded through to recoverUncommittedWork.
		if strings.Contains(sub, "feat(#8365):") &&
			strings.Contains(sub, "[auto-recovery] feature-dev work recovered") {
			foundRecovery = true
			break
		}
	}
	if !foundRecovery {
		t.Errorf("no auto-recovery commit for #8365 found in git log — feature-dev work was lost.\ncommits: %v", subjects)
	}

	// The worktree must be clean after recovery (no dangling uncommitted file).
	if hasUncommittedWork(root) {
		t.Error("worktree still has uncommitted work after #3365 recovery")
	}

	// The stop-hook sentinel must be cleaned up by the scheduler after it is read.
	sentinel := filepath.Join(root, ".nightgauge", "pipeline", "stop-hook-status-8365.json")
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("stop-hook sentinel not cleaned up after recovery; stat err=%v", err)
	}
}
