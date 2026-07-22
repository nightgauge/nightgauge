package orchestrator

import (
	"context"
	"errors"
	"testing"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// fakePRCreateRunner is a controllable PRCreateRunner for scheduler tests.
type fakePRCreateRunner struct {
	result      pmstages.PRCreateResult
	err         error
	callCount   int
	lastWorkdir string // workdir passed to the most recent Run — asserts #275
}

func (f *fakePRCreateRunner) Run(_ context.Context, _ int, _, workdir string) (pmstages.PRCreateResult, error) {
	f.callCount++
	f.lastWorkdir = workdir
	return f.result, f.err
}

// TestScheduler_PRCreate_DeterministicSkipsLLM (AC #2) — when the
// deterministic runner reports `created`, the LLM stage runner must not be
// invoked for pr-create.
func TestScheduler_PRCreate_DeterministicSkipsLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{
		Path:     pmstages.CreatePathCreated,
		PRNumber: 99,
		PRURL:    "https://github.com/owner/repo/pull/99",
		Reason:   pmstages.ReasonRichContext,
	}}
	s.WithPRCreateRunner(det)

	llm := newFakeStageRunner()
	s.WithStageRunner(llm)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if !created {
		t.Fatalf("tryDeterministicPRCreate returned false, want true")
	}

	if llm.callsByStage[state.StagePRCreate] != 0 {
		t.Errorf("LLM stage runner called %d times for pr-create, want 0",
			llm.callsByStage[state.StagePRCreate])
	}

	if got := rs.StageExecutionPath(state.StagePRCreate); got != "deterministic" {
		t.Errorf("StageExecutionPath(pr-create) = %q, want %q", got, "deterministic")
	}

	if rs.PrUrl != "https://github.com/owner/repo/pull/99" {
		t.Errorf("PrUrl = %q, want PR URL captured from deterministic result", rs.PrUrl)
	}

	if det.callCount != 1 {
		t.Errorf("PRCreateRunner.Run call count = %d, want 1", det.callCount)
	}
}

// TestScheduler_PRCreate_PuntInvokesLLM (AC #3) — on punt, the deterministic
// path returns false so the caller invokes the LLM stage runner; runtime
// records execution_path = "llm".
func TestScheduler_PRCreate_PuntInvokesLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{
		Path:   pmstages.CreatePathPunt,
		Reason: pmstages.ReasonValidationNotPassed,
	}}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if created {
		t.Fatalf("tryDeterministicPRCreate returned true on punt, want false (so LLM runs)")
	}

	if got := rs.StageExecutionPath(state.StagePRCreate); got != "llm" {
		t.Errorf("StageExecutionPath(pr-create) = %q, want %q", got, "llm")
	}
	// The deterministic punt reason must be recorded for history/telemetry
	// observability (Issue #297).
	if got := rs.StagePuntReason(state.StagePRCreate); got != pmstages.ReasonValidationNotPassed {
		t.Errorf("StagePuntReason(pr-create) = %q, want %q", got, pmstages.ReasonValidationNotPassed)
	}
}

// TestScheduler_PRCreate_RateLimitedDefersNoLLM — a punt whose reason carries a
// GitHub rate-limit signal must defer (second return value true) and NOT run the
// LLM path or record an execution_path. Issue #3976.
func TestScheduler_PRCreate_RateLimitedDefersNoLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{
		Path: pmstages.CreatePathPunt,
		// pr-create has no dedicated rate-limit reason; it wraps the in-process
		// client error inside create-call-failed. The substring matcher keys on
		// the embedded "API rate limit exceeded" signal.
		Reason: pmstages.ReasonCreateFailed + ": API rate limit exceeded for installation",
	}}
	s.WithPRCreateRunner(det)

	llm := newFakeStageRunner()
	s.WithStageRunner(llm)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	created, rateLimited := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if created {
		t.Fatalf("tryDeterministicPRCreate returned created=true on rate-limit, want false")
	}
	if !rateLimited {
		t.Fatalf("tryDeterministicPRCreate returned rateLimited=false on a rate-limit punt, want true")
	}
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "" {
		t.Errorf("StageExecutionPath(pr-create) = %q on rate-limit defer, want \"\" (no path ran)", got)
	}
}

// TestScheduler_PRCreate_NonRateLimitPuntStillInvokesLLM — a punt whose reason
// is NOT rate-limit related must keep the existing behaviour: defer flag false,
// execution_path = "llm". Guards against the rate-limit matcher being too broad.
func TestScheduler_PRCreate_NonRateLimitPuntStillInvokesLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{
		Path:   pmstages.CreatePathPunt,
		Reason: pmstages.ReasonValidationNotPassed,
	}}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	created, rateLimited := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if created || rateLimited {
		t.Fatalf("non-rate-limit punt: got created=%v rateLimited=%v, want false/false", created, rateLimited)
	}
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "llm" {
		t.Errorf("StageExecutionPath(pr-create) = %q, want llm", got)
	}
}

// TestReasonIndicatesRateLimit_RoutesToGitHubQuotaLow locks in the contract that
// ties the two #3976 halves together: every reason the matcher accepts, when
// wrapped in the stage error the scheduler synthesizes, must classify to
// TerminalKindGitHubQuotaLow so it routes to the #3896 environmental recovery
// path (global cooldown, board→Ready, no lifetime-cap penalty).
func TestReasonIndicatesRateLimit_RoutesToGitHubQuotaLow(t *testing.T) {
	rateLimitReasons := []string{
		pmstages.ReasonRateLimited,
		"create-call-failed: API rate limit exceeded",
		"create-call-failed: You have exceeded a secondary rate limit",
		"push-failed: HTTP 429 Too Many Requests",
		"github rate limit gated by SharedRateLimitTracker",
		"abuse detection mechanism triggered",
	}
	for _, reason := range rateLimitReasons {
		if !ReasonIndicatesRateLimit(reason) {
			t.Errorf("ReasonIndicatesRateLimit(%q) = false, want true", reason)
			continue
		}
		// The scheduler wraps the deferral in this exact marker form.
		stageErr := "github-quota-low: pr-create deterministic path rate-limited; deferring [#3976]"
		if got := ClassifyTerminalKind(stageErr); got != TerminalKindGitHubQuotaLow {
			t.Errorf("ClassifyTerminalKind(defer marker for %q) = %q, want %q",
				reason, got, TerminalKindGitHubQuotaLow)
		}
	}

	nonRateLimit := []string{
		pmstages.ReasonValidationNotPassed,
		"not-mergeable: CONFLICTING",
		"create-call-failed: repository not found",
		"",
	}
	for _, reason := range nonRateLimit {
		if ReasonIndicatesRateLimit(reason) {
			t.Errorf("ReasonIndicatesRateLimit(%q) = true, want false", reason)
		}
	}
}

// TestScheduler_PRCreate_DeterministicErrorFallsThroughToLLM — an unexpected
// error from the runner is treated as a punt; LLM path runs.
func TestScheduler_PRCreate_DeterministicErrorFallsThroughToLLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{err: errors.New("unexpected gh failure")}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if created {
		t.Fatalf("tryDeterministicPRCreate returned true on error, want false")
	}
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "llm" {
		t.Errorf("StageExecutionPath(pr-create) = %q, want %q", got, "llm")
	}
}

// TestScheduler_PRCreate_RecordsExecutionPath_Deterministic (AC #5).
func TestScheduler_PRCreate_RecordsExecutionPath_Deterministic(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{Path: pmstages.CreatePathCreated, PRNumber: 1, PRURL: "u"}}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	_, _ = s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "deterministic" {
		t.Errorf("execution_path = %q, want deterministic", got)
	}
}

// TestScheduler_PRCreate_RecordsExecutionPath_LLM (AC #5).
func TestScheduler_PRCreate_RecordsExecutionPath_LLM(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonNoChanges}}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	_, _ = s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "llm" {
		t.Errorf("execution_path = %q, want llm", got)
	}
}

// TestScheduler_PRCreate_DeterministicPath_CostZero (AC #7 cost arm).
// Mirrors TestScheduler_PRMerge_DeterministicPath_CostZero — the deterministic
// hook itself touches no token state.
func TestScheduler_PRCreate_DeterministicPath_CostZero(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{Path: pmstages.CreatePathCreated, PRNumber: 1, PRURL: "u"}}
	s.WithPRCreateRunner(det)
	llm := newFakeStageRunner()
	s.WithStageRunner(llm)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	beforeCost := rs.TotalCostUSD
	beforeIn := rs.InputTokens
	beforeOut := rs.OutputTokens

	created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if !created {
		t.Fatalf("expected deterministic created=true")
	}

	rs.CompleteStage(0, 0, 0, "")

	if rs.TotalCostUSD != beforeCost {
		t.Errorf("TotalCostUSD changed: before=%v after=%v", beforeCost, rs.TotalCostUSD)
	}
	if rs.InputTokens != beforeIn || rs.OutputTokens != beforeOut {
		t.Errorf("token counts changed: in %d→%d out %d→%d",
			beforeIn, rs.InputTokens, beforeOut, rs.OutputTokens)
	}
}

// TestScheduler_PRCreate_NonPRCreateStage_NoOp — the hook is a no-op for any
// stage other than pr-create, even with a runner registered.
func TestScheduler_PRCreate_NonPRCreateStage_NoOp(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{Path: pmstages.CreatePathCreated}}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StageFeatureDev)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	if created, _ := s.tryDeterministicPRCreate(context.Background(), state.StageFeatureDev, rs, item, "/tmp"); created {
		t.Errorf("tryDeterministicPRCreate returned true for non-pr-create stage")
	}
	if det.callCount != 0 {
		t.Errorf("PRCreateRunner.Run was called for non-pr-create stage (count=%d)", det.callCount)
	}
	if got := rs.StageExecutionPath(state.StageFeatureDev); got != "" {
		t.Errorf("execution_path leaked onto unrelated stage: %q", got)
	}
}

// TestScheduler_PRCreate_NilRunner_NoOp — no runner → no-op (no panic, no
// execution_path recorded).
func TestScheduler_PRCreate_NilRunner_NoOp(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	// no WithPRCreateRunner — prCreateRunner is nil

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp")
	if created {
		t.Errorf("nil runner should produce created=false")
	}
	if got := rs.StageExecutionPath(state.StagePRCreate); got != "" {
		t.Errorf("execution_path should not be set when runner is nil, got %q", got)
	}
}

// TestScheduler_PRCreate_ReadsContextFromWorktree — regression for #275. On a
// worktree-isolated run the dev/validate/issue context the runner projects lives
// ONLY in the worktree's `.nightgauge/pipeline/`, never in the canonical root.
// The scheduler MUST hand the runner the worktree path (via stageWorkspace), not
// the bare workspaceRoot — otherwise every worktree-mode run punts
// missing-dev-context and burns the expensive LLM fallback (bowlsheet 0-for-N).
func TestScheduler_PRCreate_ReadsContextFromWorktree(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{
		Path: pmstages.CreatePathCreated, PRNumber: 7, PRURL: "u",
	}}
	s.WithPRCreateRunner(det)

	const worktree = "/tmp/repo/.worktrees/issue-42"
	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.SetProcess(0, worktree) // populates runtime.WorktreeDir, as a real run does
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	// workspaceRoot is the canonical repo root — deliberately different from the
	// worktree so a regression (passing workspaceRoot) is caught.
	if _, _ = s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp/repo"); det.lastWorkdir != worktree {
		t.Fatalf("PRCreateRunner.Run workdir = %q, want worktree %q (#275: must read context from the worktree, not workspaceRoot)",
			det.lastWorkdir, worktree)
	}
}

// TestScheduler_PRCreate_NoWorktreeUsesWorkspaceRoot — in-place runs (no
// worktree, e.g. VSCode/headless) must keep passing workspaceRoot unchanged, so
// the #275 fix is byte-identical for the non-worktree majority.
func TestScheduler_PRCreate_NoWorktreeUsesWorkspaceRoot(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRCreateRunner{result: pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonNoChanges}}
	s.WithPRCreateRunner(det)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id") // WorktreeDir == ""
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	if _, _ = s.tryDeterministicPRCreate(context.Background(), state.StagePRCreate, rs, item, "/tmp/repo"); det.lastWorkdir != "/tmp/repo" {
		t.Fatalf("PRCreateRunner.Run workdir = %q, want workspaceRoot %q when no worktree is set", det.lastWorkdir, "/tmp/repo")
	}
}

// TestScheduler_PRMerge_ReadsContextFromWorktree — regression for #275. pr-merge
// reads pr-{N}.json (and runs `gh`) and has the identical worktree-blindness bug:
// pr-create writes pr-{N}.json into the worktree, so the runner must be handed
// the worktree path or it punts missing-pr-context on every worktree-mode run.
func TestScheduler_PRMerge_ReadsContextFromWorktree(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{Path: pmstages.PathMerged, PRNumber: 7, PRState: "MERGED"}}
	s.WithPRMergeRunner(det)

	const worktree = "/tmp/repo/.worktrees/issue-42"
	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.SetProcess(0, worktree)
	rs.BeginStage(state.StagePRMerge)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	if merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs, item, "/tmp/repo"); !merged || det.lastWorkdir != worktree {
		t.Fatalf("PRMergeRunner.Run workdir = %q, want worktree %q (#275)", det.lastWorkdir, worktree)
	}
}

// TestScheduler_PRCreate_OrthogonalToPRMerge — both deterministic hooks must
// be guarded on stage so they don't fire for the wrong stage.
func TestScheduler_PRCreate_OrthogonalToPRMerge(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	prCreate := &fakePRCreateRunner{result: pmstages.PRCreateResult{Path: pmstages.CreatePathCreated}}
	prMerge := &fakePRMergeRunner{result: pmstages.PRMergeResult{Path: pmstages.PathMerged}}
	s.WithPRCreateRunner(prCreate)
	s.WithPRMergeRunner(prMerge)

	rs := state.NewRuntimeState("owner/repo", 42, "item-id")
	rs.BeginStage(state.StagePRCreate)
	item := types.BoardItem{Number: 42, Repo: "owner/repo"}

	// Call pr-merge hook for pr-create stage — must NOT fire.
	if merged, _, _ := s.tryDeterministicPRMerge(context.Background(), state.StagePRCreate, rs, item, "/tmp"); merged {
		t.Errorf("pr-merge hook fired for pr-create stage")
	}
	if prMerge.callCount != 0 {
		t.Errorf("pr-merge runner ran for pr-create stage (calls=%d)", prMerge.callCount)
	}

	// Call pr-create hook for pr-merge stage — must NOT fire.
	if created, _ := s.tryDeterministicPRCreate(context.Background(), state.StagePRMerge, rs, item, "/tmp"); created {
		t.Errorf("pr-create hook fired for pr-merge stage")
	}
	if prCreate.callCount != 0 {
		t.Errorf("pr-create runner ran for pr-merge stage (calls=%d)", prCreate.callCount)
	}
}
