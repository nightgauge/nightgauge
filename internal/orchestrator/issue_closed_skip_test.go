package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/state"
)

// #3081: a stop on an already-closed issue is a skip. A closed issue the board
// still lists as Ready (the graph has not caught up with the close) dispatches
// at most once, and the stop is in no failure total.

func closedReadyGraph(state string) *depgraph.Graph {
	return &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#81": {Repo: "O/app", Number: 81, State: state, BoardStatus: "Ready"},
	}}
}

func TestIssueClosedStop_DispatchesAtMostOnceAcrossCycles(t *testing.T) {
	t.Parallel()

	as := &AutonomousScheduler{
		trustGateOverride:    allWritersGate(),
		config:               AutonomousConfig{MaxConcurrent: 3},
		state:                &AutonomousState{Status: "running", LifetimeIssueFailures: map[string]int{}},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]retryPlan{},
	}
	stale := closedReadyGraph("OPEN") // the board still says Ready and open

	dispatches := 0
	for cycle := 1; cycle <= 3; cycle++ {
		if !isCandidate(as.prioritize(context.Background(), stale), "O/app", 81) {
			continue
		}
		dispatches++
		as.mu.Lock()
		as.state.Running = append(as.state.Running, RunningItem{Repo: "O/app", Number: 81})
		as.mu.Unlock()
		as.onPipelineComplete("O/app", 81, false, false, TerminalKindIssueClosed,
			"[pipeline-start-failure] issue-closed", false)
		as.drainBackground()
	}
	if dispatches != 1 {
		t.Fatalf("closed Ready issue dispatched %d times across three cycles; want 1", dispatches)
	}
	for _, f := range as.state.Failed {
		if f.Repo == "O/app" && f.Number == 81 {
			t.Fatalf("issue_closed stop was booked as a failed item: %+v", f)
		}
	}

	// Once the graph shows the close, the hold is released; a later OPEN is a
	// reopen and the issue is a candidate again.
	as.prioritize(context.Background(), closedReadyGraph("CLOSED"))
	if len(as.state.ClosedStops) != 0 {
		t.Fatalf("hold not released once the graph showed the close: %v", as.state.ClosedStops)
	}
	if !isCandidate(as.prioritize(context.Background(), stale), "O/app", 81) {
		t.Fatal("reopened issue is not a candidate")
	}

	// A node that leaves the graph (closed issues drop off the board) also
	// shows the close.
	as.mu.Lock()
	as.recordClosedStopLocked("O/app#81", "t")
	as.mu.Unlock()
	as.prioritize(context.Background(), &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#82": {Repo: "O/app", Number: 82, State: "OPEN", BoardStatus: "Ready"},
	}})
	if len(as.state.ClosedStops) != 0 {
		t.Fatalf("hold not released once the node left the graph: %v", as.state.ClosedStops)
	}
}

func TestIssueClosedStop_NotCountedAsFailedRun(t *testing.T) {
	t.Parallel()

	// The run record books it as a cancelled skip.
	rec := state.V2RunRecord{Outcome: "failed", TerminalFailureKind: TerminalKindIssueClosed}
	ApplyNonFailureOutcome(&rec)
	if rec.Outcome != "cancelled" || rec.OutcomeType != OutcomeTypeIssueClosed {
		t.Fatalf("issue_closed record booked outcome=%q outcome_type=%q; want cancelled/issue_closed",
			rec.Outcome, rec.OutcomeType)
	}
	if rec.TerminalFailureKind != TerminalKindIssueClosed {
		t.Fatal("the terminal kind must be retained")
	}

	// The exit-records tally leaves it out.
	skip := diagnostics.StageExitRecord{Repo: "O/app", Issue: 81, Stage: diagnostics.StagePreDispatch,
		TerminalKind: TerminalKindIssueClosed, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	fail := skip
	fail.TerminalKind = TerminalKindSubagentCrash
	if !skip.IsSkip() || fail.IsSkip() {
		t.Fatalf("IsSkip: issue_closed=%v subagent_crash=%v; want true/false", skip.IsSkip(), fail.IsSkip())
	}
	stats := diagnostics.ComputeStats([]diagnostics.StageExitRecord{skip, fail})
	if len(stats) != 1 || stats[0].N != 1 || stats[0].OkN != 0 {
		t.Fatalf("stats counted the skip: %+v", stats)
	}

	// A partial no-op is out of the failure total too, on its gate kind alone.
	// The same terminal kind without it, and a gate no-op, are failures (#3487).
	noOp, verdict, gateNoOp := skip, skip, skip
	noOp.TerminalKind, noOp.GateKind = TerminalKindNotPipelineActionable, diagnostics.GateKindPartialNoOp
	verdict.TerminalKind, verdict.GateKind = TerminalKindNotPipelineActionable, "fail"
	gateNoOp.TerminalKind, gateNoOp.GateKind = TerminalKindPrematureTurnEnd, "no_op"
	if !noOp.IsSkip() || verdict.IsSkip() || gateNoOp.IsSkip() {
		t.Fatalf("IsSkip: no-op=%v verdict=%v gate no-op=%v; want true/false/false", noOp.IsSkip(), verdict.IsSkip(), gateNoOp.IsSkip())
	}
	if stats := diagnostics.ComputeStats([]diagnostics.StageExitRecord{noOp, fail, gateNoOp}); len(stats) != 1 || stats[0].N != 2 {
		t.Fatalf("failure total with a no-op = %+v, want the two failures", stats)
	}

	// The two packages' notion of a skip kind agree.
	for _, kind := range []string{TerminalKindIssueClosed, TerminalKindSubagentCrash, TerminalKindStallKill} {
		r := diagnostics.StageExitRecord{TerminalKind: kind}
		if r.IsSkip() && !IsNonFailureTerminalKind(kind) {
			t.Fatalf("diagnostics treats %s as a skip but orchestrator books it as a failure", kind)
		}
	}
	if !IsNonFailureTerminalKind(TerminalKindIssueClosed) {
		t.Fatal("issue_closed must be a non-failure terminal kind")
	}
}
