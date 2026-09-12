package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestScheduler_PRMerge_RefusedNeverFallsThroughToLLM_1675 pins the gate half
// of the steering guard: a refusal is not a punt. A punt hands pr-merge to the
// LLM skill, which can merge the very head the runner refused; the refusal
// must fail the stage instead and name the classified reason.
func TestScheduler_PRMerge_RefusedNeverFallsThroughToLLM_1675(t *testing.T) {
	s := newSchedulerForDeterministicTest()
	reason := pmstages.ReasonGeneratedSteering + ": PR head abc carries generated Nightgauge steering in AGENTS.md"
	s.WithPRMergeRunner(&fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path: pmstages.PathRefused, PRNumber: 7, PRState: "OPEN", Reason: reason,
	}})
	rs := state.NewRuntimeState("owner/repo", 42, "item-id", testRunID())
	rs.BeginStage(state.StagePRMerge)

	merged, _, rateLimited, refusal := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rs,
		types.BoardItem{Number: 42, Repo: "owner/repo"}, "/tmp")
	if merged || rateLimited {
		t.Fatalf("merged=%v rateLimited=%v, want a refusal only", merged, rateLimited)
	}
	if refusal == nil || !strings.Contains(refusal.Error(), reason) {
		t.Fatalf("refusal = %v, want it to carry %q", refusal, reason)
	}
	if got := rs.StageExecutionPath(state.StagePRMerge); got == "llm" {
		t.Errorf("a refusal must not be recorded as an LLM fall-through")
	}
}

// TestScheduler_PRMerge_RefusalFailsRunWithoutLLM_1675 drives the whole
// pipeline: the pr-merge LLM skill must never run after a refusal, and the
// run fails carrying the reason.
func TestScheduler_PRMerge_RefusalFailsRunWithoutLLM_1675(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}
	pcDir := filepath.Join(root, ".nightgauge", "pipeline", "issue-9101")
	if err := os.MkdirAll(pcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pcDir, "pr-create-context.json"),
		[]byte(`{"pr_url":"https://github.com/nightgauge/test/pull/1235"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newSuccessStageRunner()
	det := &fakePRMergeRunner{result: pmstages.PRMergeResult{
		Path: pmstages.PathRefused, PRNumber: 1235, PRState: "OPEN",
		Reason: pmstages.ReasonGeneratedSteering + ": no local worktree",
	}}
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
		prMergeRunner:  det,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
	item := types.BoardItem{Number: 9101, Repo: "nightgauge/test", ID: "item-9101"}
	s.runPipeline(context.Background(), item)

	if det.callCount == 0 {
		t.Fatal("precondition: the pipeline never reached pr-merge")
	}
	runner.mu.Lock()
	llmMerges := runner.callCount[state.StagePRMerge]
	runner.mu.Unlock()
	if llmMerges != 0 {
		t.Fatalf("the pr-merge LLM skill ran %d time(s) after a refusal — it could merge the refused head", llmMerges)
	}
	var rec *state.V2RunRecord
	records := readDailyJSONLRecords(t, root)
	for i := range records {
		if records[i].IssueNumber == item.Number {
			rec = &records[i]
		}
	}
	if rec == nil {
		t.Fatalf("no run record for #%d", item.Number)
	}
	if rec.Outcome == "success" {
		t.Fatalf("a refused merge was recorded as success")
	}
	st, ok := rec.Stages[string(state.StagePRMerge)]
	if !ok || !strings.Contains(st.Error, pmstages.ReasonGeneratedSteering) {
		t.Errorf("pr-merge stage error = %q (recorded=%v), want it to name %q", st.Error, ok, pmstages.ReasonGeneratedSteering)
	}
}
