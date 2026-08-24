package ipc

// #377 / ADR-017 R-1 — the runtime snapshot's single authoritative writer.
//
// Five writers in three processes share Persist's whole-file last-write-wins
// contract. Decision 5 closed the cross-process half as far as a foreign
// process can — `nightgauge gate verify --record` does load-or-skip, refuses a
// terminal snapshot, and writes through PersistExisting — and named what it
// could NOT close: the rename race in the window between that load and that
// write. A second process cannot participate in a latch it cannot see.
//
// pipeline.recordStageGateResult removes the gate CLI from the writer set
// whenever a server is alive, which is what makes the residual go away rather
// than narrower.

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// gateRecordServer builds a server rooted at a temp workspace with the pipeline
// methods registered.
func gateRecordServer(t *testing.T) (*Server, string) {
	t.Helper()
	workspaceRoot := t.TempDir()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
		workspaceRoot:  workspaceRoot,
	}
	s.registerMethods()
	return s, workspaceRoot
}

func gateResultJSON(t *testing.T, issue int, runID, stage, gateName string, passed bool) json.RawMessage {
	t.Helper()
	p := PipelineRecordStageGateResultParams{
		Repo:        "acme/platform",
		IssueNumber: issue,
		Stage:       stage,
		RunID:       runID,
		Result: state.StageGateResult{
			GateName:  gateName,
			Passed:    passed,
			Reason:    "recorded through the single writer",
			Timestamp: "2026-08-23T00:00:00Z",
			Kind:      "ok",
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

// TestRecordStageGateResult_ServerPersistsTheResult is AC1's server half: the
// result arrives over IPC and is persisted by the single writer, onto the run
// the caller addressed BY IDENTITY rather than by an issue-number pick.
func TestRecordStageGateResult_ServerPersistsTheResult(t *testing.T) {
	s, workspaceRoot := gateRecordServer(t)
	const issue = 4712
	runID := newTestRunID()

	// The run exists and is live — one ordinary transition puts it in the
	// registry and on disk.
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4712,"stage":"pr-create","status":"complete","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("seed transition: %v", err)
	}

	if _, err := s.methods["pipeline.recordStageGateResult"](context.Background(),
		gateResultJSON(t, issue, runID, "pr-create", "pr-create", true)); err != nil {
		t.Fatalf("recordStageGateResult: %v", err)
	}

	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
	rs, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load the run's own snapshot: %v", err)
	}
	got := rs.StageGateResultsFor(state.PipelineStage("pr-create"))
	if len(got) != 1 {
		t.Fatalf("expected 1 gate result on pr-create, got %d", len(got))
	}
	if got[0].GateName != "pr-create" || !got[0].Passed {
		t.Errorf("gate result did not round-trip: %+v", got[0])
	}
}

// TestRecordStageGateResult_RefusesAClosedRun pins the property the direct
// path can only approximate: the refusal consults the LIVE registry, not a
// file that may be mid-seal. A run whose terminal claim has already latched is
// refused here, so nothing can append a gate result to a booked run.
func TestRecordStageGateResult_RefusesAClosedRun(t *testing.T) {
	s, _ := gateRecordServer(t)
	const issue = 4713
	runID := newTestRunID()

	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4713,"stage":"pr-merge","status":"complete","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	if _, err := s.methods["pipeline.notifyComplete"](context.Background(),
		json.RawMessage(`{"repo":"acme/platform","issueNumber":4713,"success":true,"totalDurationMs":10,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	_, err := s.methods["pipeline.recordStageGateResult"](context.Background(),
		gateResultJSON(t, issue, runID, "pr-merge", "pr-merge", true))
	if err == nil {
		t.Fatal("a closed run accepted a gate record — the terminal latch does not cover this verb")
	}
	if !strings.Contains(err.Error(), codeRunClosed) {
		t.Errorf("refusal is %q, want the machine-readable %s", err.Error(), codeRunClosed)
	}
}

// TestRecordStageGateResult_RequiresARunIdentity pins that this verb is a
// run message like every other: it resolves by identity, so an unaddressed
// call is refused rather than guessed at. The guess is precisely what the
// direct path has to do.
func TestRecordStageGateResult_RequiresARunIdentity(t *testing.T) {
	s, _ := gateRecordServer(t)
	_, err := s.methods["pipeline.recordStageGateResult"](context.Background(),
		gateResultJSON(t, 4714, "", "pr-create", "pr-create", true))
	if err == nil {
		t.Fatal("a call with no run identity was accepted")
	}
	if !strings.Contains(err.Error(), codeRunIDRequired) {
		t.Errorf("refusal is %q, want %s", err.Error(), codeRunIDRequired)
	}
}

// TestGateRecord_TheRenameRaceLosesAFieldOnTheDirectPath is AC3, and it is
// written to FAIL if the race is ever closed some other way rather than to
// assert a bug is permanent: it demonstrates the loss the direct path admits,
// and the sibling test below shows the same interleaving losing nothing when
// the result goes through the server.
//
// THE RACE, DETERMINISTICALLY ORDERED. Concurrency is not needed to show it —
// the defect is last-write-wins over a WHOLE FILE, so the loss is a function
// of ordering alone:
//
//  1. the gate CLI loads the snapshot into its own process;
//  2. the server writes a field onto the same run (an ordinary transition);
//  3. the gate CLI appends its verdict to its now-STALE copy and writes.
//
// Step 3's whole-file write silently reverts step 2. Two writers, one run,
// field loss — exactly what the issue describes.
func TestGateRecord_TheRenameRaceLosesAFieldOnTheDirectPath(t *testing.T) {
	s, workspaceRoot := gateRecordServer(t)
	const issue = 4715
	runID := newTestRunID()
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")

	// "running" then "complete": CompleteStageWithCost books onto the stage
	// BeginStage opened, so a complete with no running before it banks nothing
	// and the fixture would prove nothing.
	for _, params := range []string{
		`{"repo":"acme/platform","issueNumber":4715,"stage":"feature-dev","status":"running","runId":"` + runID + `"}`,
		`{"repo":"acme/platform","issueNumber":4715,"stage":"feature-dev","status":"complete","inputTokens":100,"outputTokens":10,"costUsd":0.25,"runId":"` + runID + `"}`,
	} {
		if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(params)); err != nil {
			t.Fatalf("seed transition: %v", err)
		}
	}

	// STEP 1 — the foreign process loads. This is exactly what
	// AppendStageGateResultToDisk does before it writes.
	stale, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("gate CLI load: %v", err)
	}

	// STEP 2 — the server books more spend on the same run, and persists.
	for _, params := range []string{
		`{"repo":"acme/platform","issueNumber":4715,"stage":"feature-validate","status":"running","runId":"` + runID + `"}`,
		`{"repo":"acme/platform","issueNumber":4715,"stage":"feature-validate","status":"complete","inputTokens":900,"outputTokens":90,"costUsd":1.50,"runId":"` + runID + `"}`,
	} {
		if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(params)); err != nil {
			t.Fatalf("second transition: %v", err)
		}
	}
	afterServer, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load after the server's write: %v", err)
	}
	serverCost := afterServer.TotalCostUSD
	if serverCost <= stale.TotalCostUSD {
		t.Fatalf("the server's write booked no additional spend (%v -> %v); the fixture proves nothing",
			stale.TotalCostUSD, serverCost)
	}

	// STEP 3 — the foreign process appends to its stale copy and writes the
	// whole file back.
	stale.AppendStageGateResult(state.PipelineStage("feature-dev"), state.StageGateResult{
		GateName: "feature-dev", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})
	if err := stale.PersistExisting(stateDir); err != nil {
		t.Fatalf("gate CLI write: %v", err)
	}

	after, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load after the gate CLI's write: %v", err)
	}
	if after.TotalCostUSD == serverCost {
		t.Fatalf("the direct path no longer loses the server's field (cost still %v) — if the "+
			"whole-file contract was fixed elsewhere, this test should be REPLACED, not deleted, "+
			"and #377's routing re-justified", serverCost)
	}
	t.Logf("field loss reproduced: the server booked %v, the gate CLI's stale whole-file write reverted it to %v",
		serverCost, after.TotalCostUSD)

	// And the gate result did land — the loss is silent, which is what makes it
	// dangerous: the write "succeeded".
	if len(after.StageGateResultsFor(state.PipelineStage("feature-dev"))) != 1 {
		t.Error("the gate result itself was not written; the reproduction is not the one described")
	}
}

// TestGateRecord_RoutingThroughTheServerLosesNothing is AC3's other half: the
// SAME interleaving, with step 3 going through pipeline.recordStageGateResult
// instead of a direct write. The gate result lands AND the server's field
// survives, because there is only ever one writer.
func TestGateRecord_RoutingThroughTheServerLosesNothing(t *testing.T) {
	s, workspaceRoot := gateRecordServer(t)
	const issue = 4716
	runID := newTestRunID()
	stateDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline")

	// "running" then "complete": CompleteStageWithCost books onto the stage
	// BeginStage opened, so a complete with no running before it banks nothing
	// and the fixture would prove nothing.
	for _, params := range []string{
		`{"repo":"acme/platform","issueNumber":4716,"stage":"feature-dev","status":"running","runId":"` + runID + `"}`,
		`{"repo":"acme/platform","issueNumber":4716,"stage":"feature-dev","status":"complete","inputTokens":100,"outputTokens":10,"costUsd":0.25,"runId":"` + runID + `"}`,
	} {
		if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(params)); err != nil {
			t.Fatalf("seed transition: %v", err)
		}
	}

	// STEP 1 — the gate CLI would load here. Under the fix it does not: it
	// holds no copy of the file at all, which is the property being pinned.
	// The load is kept only to capture the pre-race cost.
	before, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// STEP 2 — the server books more spend.
	for _, params := range []string{
		`{"repo":"acme/platform","issueNumber":4716,"stage":"feature-validate","status":"running","runId":"` + runID + `"}`,
		`{"repo":"acme/platform","issueNumber":4716,"stage":"feature-validate","status":"complete","inputTokens":900,"outputTokens":90,"costUsd":1.50,"runId":"` + runID + `"}`,
	} {
		if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), json.RawMessage(params)); err != nil {
			t.Fatalf("second transition: %v", err)
		}
	}
	afterServer, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load after the server's write: %v", err)
	}
	serverCost := afterServer.TotalCostUSD
	if serverCost <= before.TotalCostUSD {
		t.Fatalf("the fixture booked no additional spend; it proves nothing")
	}

	// STEP 3 — through the single writer.
	if _, err := s.methods["pipeline.recordStageGateResult"](context.Background(),
		gateResultJSON(t, issue, runID, "feature-dev", "feature-dev", true)); err != nil {
		t.Fatalf("recordStageGateResult: %v", err)
	}

	after, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if after.TotalCostUSD != serverCost {
		t.Errorf("the server's field was LOST through its own verb: cost = %v, want %v",
			after.TotalCostUSD, serverCost)
	}
	if len(after.StageGateResultsFor(state.PipelineStage("feature-dev"))) != 1 {
		t.Error("the gate result did not land through the IPC route")
	}
}
