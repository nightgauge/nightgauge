package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// uuidV7Pattern is the canonical lowercase UUIDv7 shape ADR-017 fixes as the
// run-identity format. It points at the SHARED constant rather than restating
// the expression: the wire validation, the snapshot filename composer and the
// snapshot discovery regex are all built from that one constant (ADR-017
// Decision 1), and a test carrying its own copy would keep passing while the
// production shape drifted underneath it.
var uuidV7Pattern = runstate.IdentityRegexp

// runIDCapturingRunner records every StageRunParams the scheduler dispatches
// and returns a minimal successful result with a well-formed output context, so
// the pipeline advances far enough to be interesting.
type runIDCapturingRunner struct {
	mu    sync.Mutex
	calls []StageRunParams
	// onStage, when set, runs inside each dispatch — the only place a test can
	// observe on-disk state a run tears down when it completes (the sidecar,
	// the mid-run snapshot).
	onStage func()
}

func (r *runIDCapturingRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	hook := r.onStage
	r.mu.Unlock()
	if hook != nil {
		hook()
	}

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

// TestRunPipeline_IgnoresANonIdentityRemoteRunID_AndMintsLocally is the SEED
// half of the ADR-017 Decision 1 security fix.
//
// `remoteRunId` reaches the scheduler from `queue.add` over the local IPC
// socket, which ADR-015 documents as UNAUTHENTICATED, and step 1 made the
// identity a FILENAME COMPONENT. Seeding it verbatim therefore had two
// consequences, one hostile and one benign:
//
//   - `../../../victim/OWNED` composed a path outside stateDir, which Persist
//     wrote and (once wired) SealAndRemove deleted;
//   - a merely non-canonical id — a platform-assigned UUIDv4, a ULID — wrote a
//     real snapshot under a name the discovery regex cannot match, so the run
//     was invisible to orphan reconciliation, the gate seam, getState and the
//     wave orchestrator, and nothing ever removed the file.
//
// The state layer refuses both at the persist sink. This test pins the other
// half: local resolution NEVER PRODUCES A NON-IDENTITY, so a non-compliant
// platform falls back to a fresh local mint (loudly) instead of a run whose
// every snapshot is a phantom. A spec-compliant v7 remote id still correlates —
// pinned by the sibling case below.
func TestRunPipeline_IgnoresANonIdentityRemoteRunID_AndMintsLocally(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote string
	}{
		{"path traversal", "../../../victim/OWNED"},
		{"uuid v4", "3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{"ulid", "run_01H8XGJWBWBAQ4ZZY1N1V9PJ0M"},
		{"uppercase canonical", "019FE6F3-FCFE-7B6F-8A7C-BE0F444B6610"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitWorkspace(t)
			runner := &runIDCapturingRunner{}
			s := newRunIdentityTestScheduler(t, root, runner)

			const issue = 371
			item := types.BoardItem{Number: issue, Repo: "nightgauge/nightgauge", ID: "item-371"}
			// The queue is where queueItemRemoteRunID reads from — this is the
			// real production seam, not a stubbed accessor.
			s.queue = []QueueItem{{
				Repo: item.Repo, IssueNumber: issue, RemoteRunID: tc.remote, Status: "ready",
			}}

			// The snapshot only exists mid-run (the terminal tail removes it),
			// so capture the directory from inside a dispatch.
			var midRun []string
			runner.onStage = func() {
				if midRun != nil {
					return
				}
				found, err := state.FindPersistedStatesForIssue(
					filepath.Join(root, ".nightgauge", "pipeline"), issue)
				if err != nil {
					return
				}
				for _, rs := range found {
					midRun = append(midRun, rs.RunID)
				}
			}

			s.runPipeline(context.Background(), item)

			calls := runner.captured()
			if len(calls) == 0 {
				t.Fatal("the run was refused outright; a non-identity remote id must fall back to a local mint, not block the run")
			}
			got := calls[0].RunID
			if got == tc.remote {
				t.Fatalf("the scheduler adopted the remote id %q verbatim — it is interpolated into the snapshot filename", got)
			}
			if !uuidV7Pattern.MatchString(got) {
				t.Fatalf("RunID = %q is not a canonical lowercase UUIDv7; local resolution must never produce a non-identity", got)
			}

			// And the run is DISCOVERABLE, which is the property a phantom id
			// silently destroyed.
			if len(midRun) != 1 || midRun[0] != got {
				t.Errorf("mid-run discoverable snapshots = %v, want exactly the dispatched identity %q", midRun, got)
			}
		})
	}
}

// TestRunPipeline_AcceptsACanonicalRemoteRunID keeps the fix honest: validation
// must not become "ignore the platform". A spec-compliant UUIDv7 from the wire
// is still adopted, so platform correlation survives for compliant callers.
// (A NON-compliant one loses correlation until ADR-017 Decision 2 threads
// `remoteRunId` as its own attribute rather than as the identity.)
func TestRunPipeline_AcceptsACanonicalRemoteRunID(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	remote, err := runstate.NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	item := types.BoardItem{Number: 372, Repo: "nightgauge/nightgauge", ID: "item-372"}
	s.queue = []QueueItem{{
		Repo: item.Repo, IssueNumber: 372, RemoteRunID: remote, Status: "ready",
	}}

	s.runPipeline(context.Background(), item)

	calls := runner.captured()
	if len(calls) == 0 {
		t.Fatal("no stage dispatched")
	}
	if calls[0].RunID != remote {
		t.Errorf("RunID = %q, want the platform's canonical %q — validation must not discard a compliant remote id",
			calls[0].RunID, remote)
	}
}

// TestRunPipeline_PreflightRefusesANonIdentity_AndBooksThroughTheFunnel pins the
// LAST line of defence.
//
// Local resolution can no longer produce a non-identity, so this branch should
// be unreachable — which is exactly why it must exist and be pinned. If any
// future path ever seeds one, the run must die LOUDLY at the preflight (booked,
// board reset, concurrency slot released) rather than run to completion while
// every Persist is silently refused at the state sink. The forced mint here
// stands in for that future path.
func TestRunPipeline_PreflightRefusesANonIdentity_AndBooksThroughTheFunnel(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	original := newRunID
	newRunID = func() (string, error) { return "not-a-run-identity", nil }
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

	item := types.BoardItem{Number: 373, Repo: "nightgauge/nightgauge", ID: "item-373"}
	s.runPipeline(context.Background(), item)

	if calls := runner.captured(); len(calls) != 0 {
		t.Errorf("%d stage(s) dispatched with a non-identity run id; the first is %s", len(calls), calls[0].Stage)
	}
	if !completeCalled {
		t.Fatal("onPipelineComplete never fired — the refusal returned ABOVE the terminal defer and leaks the concurrency slot")
	}
	if completeSuccess {
		t.Error("onPipelineComplete reported success=true for a run that never dispatched a stage")
	}
	if completeSnap == nil {
		t.Fatal("onPipelineComplete received a nil snapshot")
	}
	reason := completeSnap.StageErrors["pipeline-start"]
	if reason == "" {
		t.Fatal("no pipeline-start stage error recorded")
	}
	// The reason must distinguish "no id" from "an id that is not an identity" —
	// they are different operator problems.
	if !strings.Contains(reason, "not a canonical run identity") {
		t.Errorf("refusal reason = %q, want it to name the non-canonical id case", reason)
	}
	// And nothing was written under the bad name.
	entries, _ := os.ReadDir(filepath.Join(root, ".nightgauge", "pipeline"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "runtime-") {
			t.Errorf("a refused run left a snapshot behind: %s", e.Name())
		}
	}
}
