package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// boundaryReader records the stage each read happens before (the last
// dispatched stage + 1) and answers from states, one per call.
type boundaryReader struct {
	mu     sync.Mutex
	states []string
	err    error
	calls  int
}

func (b *boundaryReader) read(context.Context, string, string, int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return "", b.err
	}
	i := b.calls - 1
	if i >= len(b.states) {
		i = len(b.states) - 1
	}
	return b.states[i], nil
}

func boundaryItem(n int) types.BoardItem {
	return types.BoardItem{Number: n, Repo: "nightgauge/nightgauge",
		ID: "item-boundary", Title: "update CONTRIBUTING.md", Labels: []string{"type:docs"}, Size: "S"}
}

func boundaryDispatched(r *runIDCapturingRunner) []state.PipelineStage {
	var out []state.PipelineStage
	for _, p := range r.captured() {
		out = append(out, p.Stage)
	}
	return out
}

// Open at pickup, closed before the next stage: the run ends issue_closed
// before that stage is dispatched, and the stage error classifies as the
// non-failure kind the autonomous booking keys on.
func TestRunPipeline_IssueClosedAfterPickup_EndsIssueClosed(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	reader := &boundaryReader{states: []string{"CLOSED"}}
	s.WithIssueStateReader(reader.read)
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	success, kind := s.runPipeline(context.Background(), boundaryItem(993151))

	if success || kind != TerminalKindIssueClosed {
		t.Fatalf("runPipeline = (%v, %q), want (false, %q)", success, kind, TerminalKindIssueClosed)
	}
	for _, st := range boundaryDispatched(runner) {
		if st != state.StageIssuePickup {
			t.Fatalf("stage %s dispatched after the issue closed; dispatched %v", st, boundaryDispatched(runner))
		}
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want 1 (the first post-pickup boundary)", reader.calls)
	}
	if snap == nil {
		t.Fatal("no completion snapshot")
	}
	errMsg := snap.StageErrors[string(snap.Stage)]
	if !strings.HasPrefix(errMsg, "issue_closed: #993151 was closed before stage ") {
		t.Fatalf("stage error = %q", errMsg)
	}
	if got := ResolveTerminalKind(false, "", false, errMsg); got != TerminalKindIssueClosed {
		t.Fatalf("stage error classifies %q, want %q", got, TerminalKindIssueClosed)
	}
}

// An issue that stays open runs every stage, one read per pre-merge
// boundary, and none for issue-pickup.
func TestRunPipeline_IssueStaysOpen_EveryStageRuns(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	reader := &boundaryReader{states: []string{"OPEN"}}
	s.WithIssueStateReader(reader.read)

	_, kind := s.runPipeline(context.Background(), boundaryItem(993152))

	if kind == TerminalKindIssueClosed {
		t.Fatalf("an open issue ended %q", kind)
	}
	nonPickup := 0
	for _, st := range boundaryDispatched(runner) {
		if st != state.StageIssuePickup {
			nonPickup++
		}
	}
	if nonPickup == 0 {
		t.Fatalf("no post-pickup stage dispatched: %v", boundaryDispatched(runner))
	}
	if reader.calls < nonPickup {
		t.Fatalf("reader calls = %d, want at least one per dispatched post-pickup stage (%d)", reader.calls, nonPickup)
	}
}

// A failed read fails open: the stages still run.
func TestRunPipeline_IssueStateReadError_FailsOpen(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	reader := &boundaryReader{err: errors.New("forge unreachable")}
	s.WithIssueStateReader(reader.read)

	_, kind := s.runPipeline(context.Background(), boundaryItem(993153))

	if kind == TerminalKindIssueClosed {
		t.Fatalf("a failed read ended the run %q", kind)
	}
	if reader.calls == 0 {
		t.Fatal("reader never called")
	}
	for _, st := range boundaryDispatched(runner) {
		if st != state.StageIssuePickup {
			return
		}
	}
	t.Fatalf("no post-pickup stage dispatched after a failed read: %v", boundaryDispatched(runner))
}

func TestIssueStateBoundaryApplies(t *testing.T) {
	rt := state.NewRuntimeState("o/r", 1, "item", "run")
	if issueStateBoundaryApplies(state.StageIssuePickup, rt) {
		t.Error("boundary must not apply to issue-pickup")
	}
	if !issueStateBoundaryApplies(state.StageFeatureDev, rt) {
		t.Error("boundary must apply to feature-dev before merge")
	}
	rt.SetMergeOutcome("abc123", "2026-11-13T00:00:00Z")
	if issueStateBoundaryApplies(state.StageFollowupMaterialize, rt) {
		t.Error("boundary must not apply once the run has merged")
	}
}

// A post-merge stage is never ended issue_closed, and makes no read.
func TestIssueClosedAtBoundary_NoReadAfterMerge(t *testing.T) {
	s := &Scheduler{}
	reader := &boundaryReader{states: []string{"CLOSED"}}
	s.WithIssueStateReader(reader.read)
	rt := state.NewRuntimeState("o/r", 1, "item", "run")
	rt.SetMergeOutcome("abc123", "2026-11-13T00:00:00Z")
	if closed, _ := s.issueClosedAtBoundary(context.Background(), boundaryItem(1), state.StageFollowupMaterialize, rt); closed {
		t.Error("post-merge boundary reported closed")
	}
	if closed, _ := s.issueClosedAtBoundary(context.Background(), boundaryItem(1), state.StageIssuePickup, nil); closed {
		t.Error("issue-pickup boundary reported closed")
	}
	if reader.calls != 0 {
		t.Errorf("reader calls = %d, want 0", reader.calls)
	}
}

type issueClosedMergeRunner struct{}

func (issueClosedMergeRunner) Run(context.Context, int, string, string) (pmstages.PRMergeResult, error) {
	return pmstages.PRMergeResult{Path: pmstages.PathRefused, PRNumber: 9, Reason: pmstages.ReasonIssueClosed}, nil
}

// The pr-merge refusal for a closed issue maps to the sentinel the stage loop
// ends the run on, and its text classifies as issue_closed.
func TestTryDeterministicPRMerge_IssueClosedRefusal(t *testing.T) {
	s := &Scheduler{}
	s.WithPRMergeRunner(issueClosedMergeRunner{})
	rt := state.NewRuntimeState("o/r", 7, "item", "run")
	merged, _, rateLimited, refusal := s.tryDeterministicPRMerge(context.Background(), state.StagePRMerge, rt,
		types.BoardItem{Number: 7, Repo: "o/r"}, t.TempDir())
	if merged || rateLimited || refusal == nil {
		t.Fatalf("got merged=%v rateLimited=%v refusal=%v", merged, rateLimited, refusal)
	}
	if !errors.Is(refusal, errMergeRefusedIssueClosed) {
		t.Fatalf("refusal %v does not wrap errMergeRefusedIssueClosed", refusal)
	}
	if got := ClassifyTerminalKind(refusal.Error()); got != TerminalKindIssueClosed {
		t.Fatalf("%q classifies %q, want %q", refusal.Error(), got, TerminalKindIssueClosed)
	}
}
