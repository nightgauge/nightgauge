package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestStageBudgetStopIsBudgetExceededAndNeverRetried is the scheduler half of
// #1652. A Go-executor stage stopped at a stage budget ends its stderr with the
// manager's stamped reason; projected through the same cliFailureText the
// runner uses, it classifies as budget_exceeded, and the stage is not retried:
// the retry engine records no escalation and the stage is dispatched once. A
// stronger model re-dispatched under the same budget would only spend it again.
func TestStageBudgetStopIsBudgetExceededAndNeverRetried(t *testing.T) {
	for _, dimension := range []string{execution.StageBudgetTurns, execution.StageBudgetWallClock, execution.StageBudgetTokens} {
		t.Run(dimension, func(t *testing.T) {
			root := t.TempDir()
			notice := execution.StageBudgetNotice(adapters.StageBudgetBreach{Dimension: dimension, Observed: 5, Limit: 5})
			errText, lastOutput := cliFailureText("", "some earlier stderr chatter\n"+notice+"\n")
			if kind := ClassifyTerminalKind(errText); kind != TerminalKindBudgetExceeded {
				t.Fatalf("the stamped reason %q classifies as %q, want %q", errText, kind, TerminalKindBudgetExceeded)
			}
			runner := &cliFailureStageRunner{
				failStage:       state.StagePRCreate,
				errText:         errText,
				lastOutputLines: lastOutput,
			}
			s := buildStallTestScheduler(t, root, runner)
			// Escalation ENABLED, so "it did not escalate" means something.
			s.retryEngine = NewRetryEngine(RetryConfig{
				MaxBacktracks:          0,
				MaxEscalationsPerStage: 1,
				ModelLadder:            []string{"haiku", "sonnet", "opus"},
			})
			item := types.BoardItem{
				Number: 1652,
				Repo:   "nightgauge/nightgauge",
				ID:     "item-1652",
				Title:  "stage budget stop must not be retried",
				Labels: []string{"type:feature", "component:go-binary"},
			}
			s.runPipeline(context.Background(), item)

			var rec *state.V2RunRecord
			for _, r := range readDailyJSONLRecords(t, root) {
				if r.IssueNumber == item.Number {
					r := r
					rec = &r
					break
				}
			}
			if rec == nil {
				t.Fatalf("no record for issue #%d", item.Number)
			}
			if rec.TerminalFailureKind != TerminalKindBudgetExceeded {
				t.Errorf("terminal_failure_kind = %q, want %q", rec.TerminalFailureKind, TerminalKindBudgetExceeded)
			}
			if got := s.retryEngine.CurrentModel(string(state.StagePRCreate)); got != "" {
				t.Errorf("pr-create escalated to %q: a stage stopped at its budget must not be retried", got)
			}
			if runner.failStageCalls != 1 {
				t.Errorf("pr-create dispatched %d times, want 1: the retry engine retried a budget stop", runner.failStageCalls)
			}
		})
	}
}

// TestStageBudgetsReachTheExecutor pins the scheduler wiring: the workspace's
// pipeline.stage_budgets is read through the tier merge and carried onto the
// execution.StageOptions the manager enforces.
func TestStageBudgetsReachTheExecutor(t *testing.T) {
	// Keep the machine tier out of the merge.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(`
schema_version: "2"
owner: nightgauge
pipeline:
  stage_budgets:
    feature-dev:
      max_turns: 120
      max_wall_clock: 90m
`), 0o644); err != nil {
		t.Fatal(err)
	}
	budgets := pipelineStageBudgets(root)
	want := map[string]config.StageBudget{"feature-dev": {MaxTurns: 120, MaxWallClock: config.StageBudgetDuration(90 * time.Minute)}}
	if !reflect.DeepEqual(budgets, want) {
		t.Fatalf("pipelineStageBudgets = %+v, want %+v", budgets, want)
	}
	opts := stageOptionsFromParams(StageRunParams{Stage: state.StageFeatureDev, StageBudgets: budgets})
	if !reflect.DeepEqual(opts.StageBudgets, want) {
		t.Errorf("StageOptions.StageBudgets = %+v, want the workspace's %+v", opts.StageBudgets, want)
	}
}
