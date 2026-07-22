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
	"github.com/nightgauge/nightgauge/internal/orchestrator/recovery"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// successStageRunner is a StageRunner test double that always reports success
// and writes a minimal JSON output context for stages that have one. It
// records per-stage call counts so tests can assert which stages re-ran on
// recovery vs. ran for the first time.
type successStageRunner struct {
	mu        sync.Mutex
	callCount map[state.PipelineStage]int
}

func newSuccessStageRunner() *successStageRunner {
	return &successStageRunner{callCount: make(map[state.PipelineStage]int)}
}

func (r *successStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.callCount[params.Stage]++
	r.mu.Unlock()

	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0755)
		payload := map[string]any{
			"schema_version":     "1.0",
			"issue_number":       params.IssueNumber,
			"plan_file":          "plan.md",
			"approach":           "test",
			"files_to_create":    []string{},
			"files_to_modify":    []string{},
			"files_to_read":      []string{},
			"validation_steps":   []string{},
			"ok":                 true,
			"pr_number":          1234,
			"pr_url":             "https://github.com/nightgauge/test/pull/1234",
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
		_ = os.WriteFile(params.OutputFile, data, 0644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// flipFlopGate is a StageGate stub that fails on its first call (KindNoOp,
// "PR is not MERGED (state=OPEN)") and passes on subsequent calls. Models
// the synthetic pr-merge "skill exited 0 but PR is still OPEN" case for
// the FailureRecovery integration test.
type flipFlopGate struct {
	mu    sync.Mutex
	calls int
	name  string
}

func (g *flipFlopGate) Name() string { return g.name }
func (g *flipFlopGate) Verify(_ context.Context, _ int, _ string) gates.GateResult {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()
	if n == 1 {
		return gates.GateResult{
			GateName: g.name,
			Passed:   false,
			Reason:   "PR #1234 is not MERGED (state=OPEN)",
			Evidence: []string{"pr=1234", "state=OPEN"},
			Kind:     gates.KindNoOp,
		}
	}
	return gates.GateResult{
		GateName: g.name,
		Passed:   true,
		Reason:   "PR is MERGED",
		Evidence: []string{"pr=1234"},
		Kind:     gates.KindOK,
	}
}

// flipFlopRunner is a PRMergeRunner test double: punts on its first call
// (so the deterministic-first hook falls through to the LLM skill path)
// and reports merged on every subsequent call (so the recovery action's
// re-run succeeds).
type flipFlopRunner struct {
	mu    sync.Mutex
	calls int
}

func (f *flipFlopRunner) Run(_ context.Context, _ int, _, _ string) (pmstages.PRMergeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 1234, PRState: "OPEN", Reason: pmstages.ReasonNotMergeable + ": UNKNOWN"}, nil
	}
	return pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 1234, PRState: "MERGED", Reason: pmstages.ReasonCleanMerged}, nil
}

// alwaysPuntPRCreateRunner makes the deterministic-first pr-create hook
// punt unconditionally so the LLM skill path runs (the success runner then
// writes pr-{N}.json from the synthetic payload).
type alwaysPuntPRCreateRunner struct{}

func (alwaysPuntPRCreateRunner) Run(_ context.Context, _ int, _, _ string) (pmstages.PRCreateResult, error) {
	return pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonClientUnavailable}, nil
}

// TestRecoveryRegistry_PRMergeSelfHeal exercises the integration end-to-end:
// pr-merge stage runs, gate fails KindNoOp, the FailureRecovery registry
// fires SkillExitedWithoutMerging, and the run completes with one recovery
// attempt recorded on the runtime.
func TestRecoveryRegistry_PRMergeSelfHeal(t *testing.T) {
	root := t.TempDir()

	// Pre-write pr-{N}.json so loadPRNumberForRecovery and the gate context
	// path agree on the PR number.
	prDir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(prDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prPayload := map[string]any{
		"issue_number": 9001,
		"pr_number":    1234,
		"pr_url":       "https://github.com/nightgauge/test/pull/1234",
	}
	prData, _ := json.Marshal(prPayload)
	if err := os.WriteFile(filepath.Join(prDir, "pr-9001.json"), prData, 0644); err != nil {
		t.Fatalf("write pr context: %v", err)
	}

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

	runner := newSuccessStageRunner()
	prMergeRunner := &flipFlopRunner{}

	s := &Scheduler{
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 1, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(root, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prMergeRunner:  prMergeRunner,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}

	// Stub the pr-merge gate so it doesn't shell out to gh; only pr-merge
	// needs special handling — every other stage gets the noop gate.
	stageGates := map[state.PipelineStage]gates.StageGate{
		state.StagePRMerge: &flipFlopGate{name: "pr-merge"},
	}
	s.WithStageGates(stageGates)

	// Wire a registry containing only SkillExitedWithoutMerging — the action
	// the synthetic pr-merge KindNoOp signal triggers.
	reg := recovery.New(3, recovery.NewSkillExitedWithoutMerging(prMergeRunner))
	s.WithRecoveryRegistry(reg)

	// verifyPRMerged is a Scheduler method that gh-checks the PR after
	// pr-merge succeeds; in tests we have no live PR, so leave PrUrl empty
	// — the post-stage check skips gracefully.
	item := types.BoardItem{Number: 9001, Repo: "nightgauge/test", ID: "item-9001"}
	s.runPipeline(context.Background(), item)

	// Recovery attempt must have been recorded on the pr-merge stage.
	rs, err := state.LoadPersistedState(filepath.Join(root, ".nightgauge", "pipeline"), item.Number)
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	attempts := rs.StageRecoveryAttemptsFor(state.StagePRMerge)
	if len(attempts) != 1 {
		t.Fatalf("expected 1 recovery attempt on pr-merge, got %d (%+v)", len(attempts), attempts)
	}
	if attempts[0].Action != "skill-exited-without-merging" {
		t.Errorf("attempt action = %q, want skill-exited-without-merging", attempts[0].Action)
	}
	if !attempts[0].Recovered {
		t.Errorf("expected Recovered=true; got reason=%q", attempts[0].Reason)
	}

	// flipFlopRunner: 1 punt (deterministic-first) + 1 merged (recovery) = 2 calls.
	if prMergeRunner.calls < 2 {
		t.Errorf("flipFlopRunner.calls = %d, want >= 2 (det-first punt + recovery merge)", prMergeRunner.calls)
	}
}

// TestRecoveryRegistry_HonoursPerRunCap forces three recovery attempts in
// a single run and asserts the registry stops at the configured cap.
// Constructed by injecting an action that always matches but never recovers,
// so the failure branch reruns escalation/backtrack each time.
//
// This test is intentionally narrower than the self-heal one: it exercises
// the cap arithmetic without depending on the full pipeline path. We call
// TryRecover directly with monotonically increasing attemptsSoFar.
func TestRecoveryRegistry_HonoursPerRunCap(t *testing.T) {
	matchedCount := 0
	a := &alwaysMatchAction{
		onExecute: func() recovery.RecoveryResult {
			matchedCount++
			return recovery.RecoveryResult{Action: "always-match", Recovered: false, Reason: "declined"}
		},
	}
	reg := recovery.New(2, a)

	for i := 0; i < 5; i++ {
		_, _ = reg.TryRecover(context.Background(), recovery.StageFailure{}, i)
	}
	if matchedCount != 2 {
		t.Errorf("matched %d times, want 2 (cap)", matchedCount)
	}
}

type alwaysMatchAction struct {
	onExecute func() recovery.RecoveryResult
}

func (a *alwaysMatchAction) Name() string                         { return "always-match" }
func (a *alwaysMatchAction) Description() string                  { return "always matches; configurable Execute" }
func (a *alwaysMatchAction) Matches(_ recovery.StageFailure) bool { return true }
func (a *alwaysMatchAction) Execute(_ context.Context, _ recovery.StageFailure) recovery.RecoveryResult {
	if a.onExecute == nil {
		return recovery.RecoveryResult{Action: a.Name(), Recovered: true}
	}
	return a.onExecute()
}

// TestFormatConflictExhaustion covers the terminal-reason builder for an
// exhausted conflict-recovery loop: it must name the conflicting files parsed
// from the recovery action's "conflicting_file=" evidence, and degrade
// gracefully when none are present (#4072 review).
func TestFormatConflictExhaustion(t *testing.T) {
	got := formatConflictExhaustion([]string{
		"pr=1234", "branch=feat/x", "conflicting_file=lib/a.dart", "conflicting_file=lib/b.dart",
	})
	if !strings.Contains(got, "lib/a.dart") || !strings.Contains(got, "lib/b.dart") {
		t.Errorf("expected both files named, got %q", got)
	}
	if !strings.Contains(got, "conflict recovery exhausted") {
		t.Errorf("expected exhaustion prefix, got %q", got)
	}

	// No conflicting_file= evidence → generic (no panic, no trailing junk).
	bare := formatConflictExhaustion([]string{"pr=1234", "branch=feat/x"})
	if !strings.Contains(bare, "conflict recovery exhausted") || strings.Contains(bare, " in ") {
		t.Errorf("expected generic exhaustion reason without file list, got %q", bare)
	}

	// Nil evidence must not panic.
	_ = formatConflictExhaustion(nil)
}

// conflictThenMergeGate fails the pr-merge gate with a CONFLICT KindNoOp on its
// first `failTimes` calls (so the conflict-recovery loop fires), then passes.
type conflictThenMergeGate struct {
	mu        sync.Mutex
	calls     int
	failTimes int
}

func (g *conflictThenMergeGate) Name() string { return "pr-merge" }
func (g *conflictThenMergeGate) Verify(_ context.Context, _ int, _ string) gates.GateResult {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()
	if n <= g.failTimes {
		return gates.GateResult{
			GateName: "pr-merge", Passed: false,
			Reason:   "PR #1234 is not MERGED (state=OPEN) — rebase conflict",
			Evidence: []string{"pr=1234", "state=OPEN", "conflict"},
			Kind:     gates.KindNoOp,
		}
	}
	return gates.GateResult{GateName: "pr-merge", Passed: true, Reason: "PR is MERGED", Evidence: []string{"pr=1234"}, Kind: gates.KindOK}
}

// alwaysPuntPRMergeRunner makes the deterministic-first pr-merge hook punt so the
// gate is the sole decider of MERGED state.
type alwaysPuntPRMergeRunner struct{}

func (alwaysPuntPRMergeRunner) Run(_ context.Context, _ int, _, _ string) (pmstages.PRMergeResult, error) {
	return pmstages.PRMergeResult{Path: pmstages.PathPunt, PRNumber: 1234, PRState: "OPEN", Reason: pmstages.ReasonNotMergeable + ": CONFLICTING"}, nil
}

// TestRecoveryRegistry_ConflictRecoveryRewindsTwice locks the #4072 review fix:
// the conflict-recovery loop must re-dispatch feature-dev for EACH conflict
// failure, NOT be capped at one by the generic RetryEngine oscillation /
// MaxBacktracks guard. With MaxBacktracks=1, the OLD code would block the 2nd
// rewind and fail the pipeline; the self-bounded BacktrackTargetStage path must
// bypass that guard so feature-dev re-runs both times and the run lands.
func TestRecoveryRegistry_ConflictRecoveryRewindsTwice(t *testing.T) {
	root := t.TempDir()
	prDir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(prDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prData, _ := json.Marshal(map[string]any{"issue_number": 9002, "pr_number": 1234, "pr_url": "https://github.com/nightgauge/test/pull/1234"})
	if err := os.WriteFile(filepath.Join(prDir, "pr-9002.json"), prData, 0644); err != nil {
		t.Fatalf("write pr context: %v", err)
	}
	// The conflict-recovery action reads conflict-context-{N}.json to build the
	// dev-redispatch context. Pre-write it (the pr-merge skill writes it in prod).
	ccData, _ := json.Marshal(map[string]any{
		"schema_version": "1.0", "issue_number": 9002, "pr_number": 1234,
		"branch": "feat/9002-x", "base_ref": "main",
		"conflicting_files": []map[string]string{{"path": "lib/page.dart", "ours": "a", "theirs": "b"}},
	})
	if err := os.WriteFile(filepath.Join(prDir, "conflict-context-9002.json"), ccData, 0644); err != nil {
		t.Fatalf("write conflict context: %v", err)
	}

	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning",
		"nightgauge-feature-dev", "nightgauge-feature-validate",
		"nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}

	runner := newSuccessStageRunner()
	s := &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(RetryConfig{MaxBacktracks: 1, MaxEscalationsPerStage: 0}), // 1 → generic guard would block the 2nd rewind
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
		prMergeRunner: alwaysPuntPRMergeRunner{},
	}
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StagePRMerge: &conflictThenMergeGate{failTimes: 2},
	})
	// Registry with ONLY the conflict-recovery loop. Action bound 5 so the
	// on-disk per-failure escalation never fires (we're testing the rewind). The
	// GLOBAL per-run cap is deliberately 1 — below the loop's needs — to prove the
	// conflict loop is cap-exempt (#4072 review): without the exemption the cap
	// would block the 2nd rewind and feature-dev would run only twice.
	s.WithRecoveryRegistry(recovery.New(1, recovery.NewConflictRecoveryLoop(5)))

	item := types.BoardItem{Number: 9002, Repo: "nightgauge/test", ID: "item-9002"}
	s.runPipeline(context.Background(), item)

	// feature-dev must have run the initial time PLUS once per conflict rewind
	// (2) = 3. With the oscillation guard NOT bypassed it would be capped at 2.
	if got := runner.callCount[state.StageFeatureDev]; got < 3 {
		t.Errorf("feature-dev ran %d times, want >= 3 (initial + 2 conflict rewinds); the oscillation guard was not bypassed", got)
	}
}

// TestRecoveryRegistry_ConflictExhaustionNamesFiles locks the #4072 round-2 fix:
// when the conflict bound is exhausted (gate NEVER passes), the terminal stage
// error must name the conflicting files — and survive a model-escalation retry
// (MaxEscalationsPerStage:1) that re-runs pr-merge before the loop gives up.
func TestRecoveryRegistry_ConflictExhaustionNamesFiles(t *testing.T) {
	root := t.TempDir()
	prDir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(prDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prData, _ := json.Marshal(map[string]any{"issue_number": 9003, "pr_number": 1234, "pr_url": "https://github.com/nightgauge/test/pull/1234"})
	if err := os.WriteFile(filepath.Join(prDir, "pr-9003.json"), prData, 0644); err != nil {
		t.Fatalf("write pr context: %v", err)
	}
	ccData, _ := json.Marshal(map[string]any{
		"schema_version": "1.0", "issue_number": 9003, "pr_number": 1234,
		"branch": "feat/9003-x", "base_ref": "main",
		"conflicting_files": []map[string]string{{"path": "lib/page.dart", "ours": "a", "theirs": "b"}},
	})
	if err := os.WriteFile(filepath.Join(prDir, "conflict-context-9003.json"), ccData, 0644); err != nil {
		t.Fatalf("write conflict context: %v", err)
	}
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning",
		"nightgauge-feature-dev", "nightgauge-feature-validate",
		"nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}

	runner := newSuccessStageRunner()
	s := &Scheduler{
		repoRunning:  make(map[string]int),
		mergeLocks:   make(map[string]*sync.Mutex),
		retryEngine:  NewRetryEngine(RetryConfig{MaxBacktracks: 1, MaxEscalationsPerStage: 1, ModelLadder: []string{"haiku", "sonnet", "opus"}, MaxConflictRedispatch: 2}),
		budgetEngine: NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:  NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:     newMockIssueSvc(),
		execMgr:      execution.NewManager(root, nil),
		stageRunner:  runner, budgetRetries: make(map[string]int),
		workspaceRoot: root,
		prMergeRunner: alwaysPuntPRMergeRunner{},
	}
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StagePRMerge: &conflictThenMergeGate{failTimes: 999}, // never passes
	})
	s.WithRecoveryRegistry(recovery.New(1, recovery.NewConflictRecoveryLoop(5)))

	var lastErr string
	var sawComplete, succeeded bool
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, success bool) {
		sawComplete = true
		succeeded = success
		lastErr = rt.StageErrors[string(state.StagePRMerge)]
	})

	s.runPipeline(context.Background(), types.BoardItem{Number: 9003, Repo: "nightgauge/test", ID: "item-9003"})

	if !sawComplete {
		t.Fatal("pipeline did not complete")
	}
	if succeeded {
		t.Error("pipeline must fail when the conflict bound is exhausted")
	}
	if !strings.Contains(lastErr, "conflict recovery exhausted") {
		t.Errorf("terminal pr-merge error must name conflict exhaustion, got %q", lastErr)
	}
	if !strings.Contains(lastErr, "lib/page.dart") {
		t.Errorf("terminal pr-merge error must name the conflicting file, got %q", lastErr)
	}
}
