package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// uuidV7Pattern is the canonical lowercase UUIDv7 shape ADR-017 fixes as the
// run-identity format: version nibble 7, variant nibble in [89ab].
var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// runIDCapturingRunner records every StageRunParams the scheduler dispatches
// and returns a minimal successful result with a well-formed output context, so
// the pipeline advances far enough to be interesting.
type runIDCapturingRunner struct {
	mu    sync.Mutex
	calls []StageRunParams
}

func (r *runIDCapturingRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	r.mu.Unlock()

	if params.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(params.OutputFile), 0o755)
		payload := map[string]any{
			"schema_version":     "1.0",
			"issue_number":       params.IssueNumber,
			"ok":                 true,
			"validation_status":  "passed",
			"build_verification": map[string]any{"ran": true, "status": "passed"},
			"tests_status":       map[string]any{"passed": 1, "failed": 0},
		}
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(params.OutputFile, data, 0o644)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, OutputTokens: 5}, nil
}

func (r *runIDCapturingRunner) captured() []StageRunParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StageRunParams(nil), r.calls...)
}

// newRunIdentityTestScheduler builds a scheduler that can drive runPipeline
// end-to-end against a fake stage runner, rooted at a real git workspace.
func newRunIdentityTestScheduler(t *testing.T, root string, runner StageRunner) *Scheduler {
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
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "fixture skills")

	return &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
	}
}

// TestRunPipeline_DispatchCarriesTheMintedRunID pins the single production line
// that populates a dispatch's run identity — `RunID: runtime.RunID` at
// runPipeline's StageRunParams construction (ADR-017 step 0b).
//
// Without this, that line is deletable with every suite green: the adapter and
// buildRunOptions tests assert the mapping GIVEN a RunID, and the IpcStageRunner
// test asserts the refusal given none. Nothing observed the one place the value
// comes from. A dispatch built without it would carry "" and be refused at the
// boundary — the whole pipeline dead, and no test red.
//
// The assertion is deliberately an equality against the runtime's own id rather
// than "non-empty": non-empty would pass if someone minted a SECOND id at the
// dispatch site, which is the identity-splitting this ADR exists to prevent.
func TestRunPipeline_DispatchCarriesTheMintedRunID(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	item := types.BoardItem{Number: 370, Repo: "nightgauge/nightgauge", ID: "item-370"}
	s.runPipeline(context.Background(), item)

	calls := runner.captured()
	if len(calls) == 0 {
		t.Fatal("no stage was dispatched; the fixture is wrong, not the assertion")
	}

	for _, params := range calls {
		if params.Runtime == nil {
			t.Fatalf("stage %s dispatched with a nil Runtime — non-optional by contract", params.Stage)
		}
		if params.RunID == "" {
			t.Errorf("stage %s dispatched with an empty RunID; IpcStageRunner refuses this at the boundary",
				params.Stage)
			continue
		}
		if params.RunID != params.Runtime.RunID {
			t.Errorf("stage %s: RunID = %q but Runtime.RunID = %q — the dispatch must carry the run's own "+
				"identity, never a second one", params.Stage, params.RunID, params.Runtime.RunID)
		}
		if !uuidV7Pattern.MatchString(params.RunID) {
			t.Errorf("stage %s: RunID = %q is not a canonical lowercase UUIDv7", params.Stage, params.RunID)
		}
	}
}

// TestRunPipeline_MintFailureIsBookedThroughTheTerminalFunnel is the MF-1
// regression, and it is about WHERE the failure returns from, not whether it
// fails.
//
// An abort above runPipeline's terminal defer looks correct in isolation and is
// silently catastrophic in autonomous mode: the Running entry stamped before
// dispatch is only ever removed by onPipelineComplete, so returning early leaks
// a MaxConcurrent slot forever, pins the issue behind the runningSet filter, and
// emits no pipeline_done, no history record and no outcome. Only the repoRunning
// defer fires. Every OTHER pre-dispatch fatal — the license and identity
// preflights — returns below that defer, and the run-identity refusal must join
// them.
//
// So: force the mint to fail, then assert on the funnel's observable effects
// rather than on the return itself.
func TestRunPipeline_MintFailureIsBookedThroughTheTerminalFunnel(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	original := newRunID
	newRunID = func() (string, error) { return "", fmt.Errorf("simulated CSPRNG failure") }
	t.Cleanup(func() { newRunID = original })

	var (
		completeCalled  bool
		completeSuccess bool
		completeSnap    *state.RuntimeState
	)
	s.OnPipelineComplete(func(_ string, _ int, snap *state.RuntimeState, success bool) {
		completeCalled = true
		completeSuccess = success
		completeSnap = snap
	})

	item := types.BoardItem{Number: 370, Repo: "nightgauge/nightgauge", ID: "item-370"}
	s.runPipeline(context.Background(), item)

	// 1. Nothing was dispatched — the refusal is pre-dispatch, so it costs no
	//    tokens and spawns no child process.
	if calls := runner.captured(); len(calls) != 0 {
		t.Errorf("%d stage(s) dispatched despite an unmintable run identity; the first is %s",
			len(calls), calls[0].Stage)
	}

	// 2. The terminal funnel ran. This is the assertion the whole fix exists
	//    for: onPipelineComplete is what releases the autonomous concurrency
	//    slot, so a refusal that skips it leaks that slot for the process's
	//    lifetime.
	if !completeCalled {
		t.Fatal("onPipelineComplete never fired — the run returned ABOVE the terminal defer, " +
			"which leaks the autonomous MaxConcurrent slot and books nothing")
	}
	if completeSuccess {
		t.Error("onPipelineComplete reported success=true for a run that never dispatched a stage")
	}

	// 3. The failure names itself, at the same place the license and identity
	//    preflights record theirs, so the reason survives into the record.
	if completeSnap == nil {
		t.Fatal("onPipelineComplete received a nil snapshot")
	}
	if got := completeSnap.StageErrors["pipeline-start"]; got == "" {
		t.Error("no pipeline-start stage error recorded; the license and identity preflights both " +
			"SetStageError on their blocked path and this refusal must too")
	}

	// 4. The run's identity really is absent — the preflight fired on the
	//    condition it claims to, not on some incidental failure downstream.
	if completeSnap.RunID != "" {
		t.Errorf("RunID = %q, want empty — the test's forced mint failure did not take effect",
			completeSnap.RunID)
	}
}
