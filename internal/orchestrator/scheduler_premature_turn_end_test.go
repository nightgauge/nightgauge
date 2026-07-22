package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestScheduler_FeaturePlanning_PrematureTurnEnd_FailsPipeline is the #74
// reproduction: feature-planning exits 0 and writes a planning context that
// PROMISES a plan (`plan_file: "plan.md"`), but the plan file itself was never
// written — the agent ended its turn on a promise. The real
// FeaturePlanningGate must report KindNoOp, the scheduler must convert the
// clean exit into a `premature turn end:` failure, and the run must be
// recorded FAILED with the premature_turn_end terminal kind in the exit
// record — never as a success.
//
// This is the epic #71 failure mode made observable: pre-#74, this exact run
// shape (clean exit, output narrating intent, no state change) tripped
// neither the idle-stall ticker nor the exit-code check and was recorded as
// a success.
func TestScheduler_FeaturePlanning_PrematureTurnEnd_FailsPipeline(t *testing.T) {
	root := t.TempDir()

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

	// successStageRunner writes each stage's context file with
	// plan_file: "plan.md" — but never writes plan.md itself. Exit 0 with a
	// dangling promise is precisely the shape under test.
	runner := newSuccessStageRunner()

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
	// Only feature-planning gets its REAL gate — the stage under test. Other
	// stages run ungated so the pipeline reaches feature-planning untouched.
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StageFeaturePlanning: gates.FeaturePlanningGate{},
	})

	item := types.BoardItem{Number: 7400, Repo: "nightgauge/test", ID: "item-7400"}
	s.runPipeline(context.Background(), item)

	// 1. The run record must be FAILED — the clean exit was overruled.
	records := readDailyJSONLRecords(t, root)
	var rec *state.V2RunRecord
	for i := range records {
		if records[i].IssueNumber == item.Number {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		t.Fatalf("no run record for issue #%d in daily JSONL (got %d records)", item.Number, len(records))
	}
	if rec.Outcome != "failed" {
		t.Fatalf("rec.Outcome = %q, want failed — a promise-ending turn was recorded as success", rec.Outcome)
	}

	// 2. The feature-planning stage detail carries the #74 stamp and the
	//    structural no-op gate result.
	stage, ok := rec.Stages[string(state.StageFeaturePlanning)]
	if !ok {
		t.Fatalf("run record has no feature-planning stage detail")
	}
	if !strings.Contains(stage.Error, "premature turn end") {
		t.Errorf("stage.Error = %q, want it to carry the `premature turn end` stamp", stage.Error)
	}
	if n := len(stage.GateResults); n == 0 {
		t.Errorf("feature-planning stage has no gate results")
	} else if kind := stage.GateResults[n-1].Kind; kind != string(gates.KindNoOp) {
		t.Errorf("gate result kind = %q, want %q", kind, gates.KindNoOp)
	}

	// 3. The exit record classifies the terminal kind.
	var found bool
	for _, er := range readExitRecords(t, root) {
		if er.Stage == string(state.StageFeaturePlanning) {
			found = true
			if er.TerminalKind != TerminalKindPrematureTurnEnd {
				t.Errorf("exit record TerminalKind = %q, want %q", er.TerminalKind, TerminalKindPrematureTurnEnd)
			}
			if er.Success {
				t.Errorf("exit record Success = true, want false")
			}
		}
	}
	if !found {
		t.Errorf("no exit record written for feature-planning")
	}
}
