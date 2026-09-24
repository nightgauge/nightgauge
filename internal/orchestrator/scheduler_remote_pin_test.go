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
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// pinRecordingRunner succeeds every stage, writing a minimal output context
// so the next stage's prerequisite check passes, and records the dispatch
// envelope each attempt received. capStage, when set, is refused once with a
// usage-cap marker so the scheduler's cap recovery runs mid-run.
type pinRecordingRunner struct {
	mu       sync.Mutex
	calls    []StageRunParams
	runtime  *state.RuntimeState
	capStage state.PipelineStage
	capped   bool
}

func (r *pinRecordingRunner) RunStage(_ context.Context, p StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, p)
	if r.runtime == nil {
		r.runtime = p.Runtime
	}
	capNow := p.Stage == r.capStage && !r.capped
	if capNow {
		r.capped = true
	}
	r.mu.Unlock()
	if capNow {
		return &StageRunResult{ExitCode: 1, ErrorText: capRejectionMarker, ServedModel: p.Model}, nil
	}
	if p.OutputFile != "" {
		if err := os.MkdirAll(filepath.Dir(p.OutputFile), 0o755); err == nil {
			data, _ := json.Marshal(map[string]any{
				"schema_version": "1.0", "issue_number": p.IssueNumber, "plan_file": "plan.md",
				"approach": "test", "files_to_create": []string{}, "files_to_modify": []string{},
				"files_to_read": []string{}, "validation_steps": []string{}, "ok": true,
			})
			_ = os.WriteFile(p.OutputFile, data, 0o644)
		}
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 10, OutputTokens: 10}, nil
}

func (r *pinRecordingRunner) dispatches() []StageRunParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StageRunParams(nil), r.calls...)
}

const pinnedModel = "lmstudio/qwen/qwen3.8-27b"

// newPinScheduler is the budget-terminate harness (IPC shape: execMgr holds
// no adapter, so every dispatch goes through stageRunner) with one queued
// item carrying the given pin.
func newPinScheduler(t *testing.T, root string, runner StageRunner, adapter, model string) (*Scheduler, types.BoardItem) {
	t.Helper()
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}
	s := &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(root, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: root,
		budgetEngine:  NewBudgetEnforcer(BudgetConfig{}),
	}
	item := types.BoardItem{Number: 1656, Repo: "nightgauge/nightgauge", ID: "item-1656"}
	s.queue = []QueueItem{{
		Repo: item.Repo, IssueNumber: item.Number, Status: "processing",
		RequestedAdapter: adapter, RequestedModel: model,
	}}
	return s, item
}

func dispatchedStages(calls []StageRunParams) map[state.PipelineStage]bool {
	seen := map[state.PipelineStage]bool{}
	for _, c := range calls {
		seen[c.Stage] = true
	}
	return seen
}

// Every stage of a pinned run dispatches on the requested adapter and model,
// marked as requested, and the run's record carries the requested pair.
func TestRemotePinDispatchesEveryStageOnTheRequestedPair(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := t.TempDir()
	runner := &pinRecordingRunner{}
	s, item := newPinScheduler(t, root, runner, "opencode", pinnedModel)
	s.runPipeline(context.Background(), item)

	calls := runner.dispatches()
	stages := dispatchedStages(calls)
	for _, st := range []state.PipelineStage{
		state.StageIssuePickup, state.StageFeaturePlanning, state.StageFeatureDev,
		state.StageFeatureValidate, state.StagePRCreate, state.StagePRMerge,
	} {
		if !stages[st] {
			t.Errorf("stage %s was never dispatched", st)
		}
	}
	for _, c := range calls {
		if c.AdapterPin != "opencode" || !c.AdapterPinRequested || c.Model != pinnedModel {
			t.Errorf("stage %s dispatched adapterPin=%q requested=%v model=%q, want opencode/true/%s",
				c.Stage, c.AdapterPin, c.AdapterPinRequested, c.Model, pinnedModel)
		}
	}
	if a, m := runner.runtime.RequestedPin(); a != "opencode" || m != pinnedModel {
		t.Errorf("run record's requested pair = %q %q", a, m)
	}
}

// Without a pin nothing changes: no dispatch names an adapter, none is
// marked requested, and the record names no request.
func TestNoRemotePinDispatchesAsBefore(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := t.TempDir()
	runner := &pinRecordingRunner{}
	s, item := newPinScheduler(t, root, runner, "", "")
	s.runPipeline(context.Background(), item)

	calls := runner.dispatches()
	if len(calls) == 0 {
		t.Fatal("no stage dispatched")
	}
	for _, c := range calls {
		if c.AdapterPin != "" || c.AdapterPinRequested || c.Model == pinnedModel {
			t.Errorf("stage %s dispatched adapterPin=%q requested=%v model=%q on an unpinned run",
				c.Stage, c.AdapterPin, c.AdapterPinRequested, c.Model)
		}
	}
	if a, m := runner.runtime.RequestedPin(); a != "" || m != "" {
		t.Errorf("an unpinned run recorded a request %q %q", a, m)
	}
}

// A usage cap mid-run hops to the next provider in the chain. The hop is
// recorded as a hop, with its reason, and the requested pair stays the
// original: the record shows what was asked for next to what served.
func TestRemotePinCapHopIsRecordedAndTheRequestStands(t *testing.T) {
	stubReconcileGhUnreachable(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"),
		[]byte("owner: nightgauge\npipeline:\n  adapter_fallback_chain:\n    - claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &pinRecordingRunner{capStage: state.StageFeaturePlanning}
	s, item := newPinScheduler(t, root, runner, "opencode", pinnedModel)
	s.SetCapAdapterUsable(alwaysUsable)
	s.SetCapCandidateModel(func(string) string { return "" })
	s.runPipeline(context.Background(), item)

	calls := runner.dispatches()
	var afterHop []StageRunParams
	hopped := false
	for _, c := range calls {
		if hopped {
			afterHop = append(afterHop, c)
		}
		if c.Stage == state.StageFeaturePlanning && !hopped {
			if c.AdapterPin != "opencode" || !c.AdapterPinRequested {
				t.Fatalf("the capped attempt was not on the requested pin: %+v", c)
			}
			hopped = true
		}
	}
	if len(afterHop) == 0 {
		t.Fatal("nothing dispatched after the cap")
	}
	for _, c := range afterHop {
		if c.AdapterPin != "claude" || c.AdapterPinRequested || c.Model == pinnedModel {
			t.Errorf("after the hop, stage %s dispatched adapterPin=%q requested=%v model=%q; want the claude hop, not the request",
				c.Stage, c.AdapterPin, c.AdapterPinRequested, c.Model)
		}
	}

	rt := runner.runtime
	if a, m := rt.RequestedPin(); a != "opencode" || m != pinnedModel {
		t.Errorf("the hop rewrote the requested pair: %q %q", a, m)
	}
	if got := rt.Snapshot().StageAdapters[string(state.StageFeaturePlanning)]; got != "claude" {
		t.Errorf("served adapter for the capped stage = %q, want the hop's claude", got)
	}

	path, err := trace.FilePath(root, rt.RunID)
	if err != nil {
		t.Fatalf("trace path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var hop string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"adapter_fallback_chain"`) {
			hop = line
		}
	}
	if hop == "" || !strings.Contains(hop, "claude") {
		t.Fatalf("no hop with its reason in the trace:\n%s", raw)
	}
}
