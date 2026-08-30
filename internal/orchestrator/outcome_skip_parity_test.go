// Skip parity between the corpus's two writers (#304).
//
// The learning corpus is one undiscriminated file read by consumers that cannot
// tell which path produced a row, so a terminal state that records NOTHING on
// one writer and a row on the other puts two meanings in `success` and
// `costUsd`. The extension writer skips a #305 blocked-dependency deferral and a
// #3296 network_unavailable failure (internal/ipc/outcome_record.go); the
// scheduler skipped only the second, and the docs claimed both.
//
// What the missing skip did to the loop verdicts: a deferral is a failed run at
// ~$0 that ran no AI stage. Five of them in the recent half of a 20-run corpus
// flip cost-optimization to `closing` ("cost per run decreasing") and
// reliability to `degrading` ("failure rate increasing") — both strings are
// exported per-loop by the continuous-improvement skill and drive proposal
// generation.
package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// terminalSkipScheduler builds a scheduler that WILL record outcomes, whose
// first stage always fails with errText. Backtracks and escalations are
// disabled so the run reaches its terminal defer on the first failure.
func terminalSkipScheduler(t *testing.T, root, errText string) *Scheduler {
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
	return &Scheduler{
		repoRunning:  make(map[string]int),
		mergeLocks:   make(map[string]*sync.Mutex),
		retryEngine:  NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine: NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:  NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:     newMockIssueSvc(),
		execMgr:      execution.NewManager(root, nil),
		stageRunner: newStallStageRunner(
			map[state.PipelineStage]int{state.StageIssuePickup: 99}, errText),
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		recordOutcomes: true,
	}
}

func runTerminalSkipPipeline(t *testing.T, issue int, errText string) (root string) {
	t.Helper()
	root = t.TempDir()
	s := terminalSkipScheduler(t, root, errText)
	s.runPipeline(context.Background(), types.BoardItem{
		Number: issue,
		Repo:   "acme/widget",
		ID:     "item-id",
		Title:  "terminal skip parity",
		Labels: []string{"type:feature"},
	})
	return root
}

func corpusRowCount(t *testing.T, root string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".nightgauge", "pipeline", "history", "outcomes.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read outcomes.jsonl: %v", err)
	}
	n := 0
	for _, line := range splitNonEmptyLines(string(data)) {
		if line != "" {
			n++
		}
	}
	return n
}

func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if line := s[start:i]; line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if line := s[start:]; line != "" {
		out = append(out, line)
	}
	return out
}

// A blocked-dependency deferral records NOTHING on the autonomous path, exactly
// as it already did on the extension path.
func TestRunPipeline_BlockedDependencyDeferralRecordsNoOutcome(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := runTerminalSkipPipeline(t, 305,
		"[blocked-dependency] issue #305 dispatched while blockedBy #300 is still open — deferring")

	if got := corpusRowCount(t, root); got != 0 {
		t.Errorf("corpus has %d rows after a blocked-dependency deferral, want 0 — a deferral is not a failure and ran no AI stage, so booking it success:false at ~$0 credits the cost loop and blames the reliability loop for a run that never executed",
			got)
	}
}

// The network_unavailable skip (#3296) stays.
func TestRunPipeline_NetworkUnavailableRecordsNoOutcome(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := runTerminalSkipPipeline(t, 3296, ErrNetworkUnavailable.Error())

	if got := corpusRowCount(t, root); got != 0 {
		t.Errorf("corpus has %d rows after a network_unavailable failure, want 0", got)
	}
}

// CONTROL: an ordinary failure still records. Without this the two tests above
// would pass just as well against a writer that was switched off entirely.
func TestRunPipeline_OrdinaryFailureStillRecordsAnOutcome(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := runTerminalSkipPipeline(t, 999, "stage exited 1: the model produced no usable output")

	if got := corpusRowCount(t, root); got != 1 {
		t.Errorf("corpus has %d rows after an ordinary failure, want 1 — the skips must be scoped to the two non-signal terminal kinds, not disable recording",
			got)
	}
}
