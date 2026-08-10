package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// The ADR-017 step-4 regression suite (Testing Strategy, `internal/ipc/` rows).
// The two red probes that drove the implementation live in
// server_run_identity_step4_test.go; these cover the rest of the step's
// normative surface. Each test's doc comment names the ADR failure ids it
// covers.

// --- helpers ---------------------------------------------------------------

// callHandler invokes a registered handler with a production param struct
// marshalled to the wire form. Wire payloads are never hand-authored JSON: a
// hand-written shape can drift from what the product sends and turn a real
// regression into a green test.
func callRunVerb(t *testing.T, s *Server, method string, params interface{}) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %T: %v", params, err)
	}
	h, ok := s.methods[method]
	if !ok {
		t.Fatalf("%s is not registered", method)
	}
	_, callErr := h(context.Background(), raw)
	return callErr
}

func mustCall(t *testing.T, s *Server, method string, params interface{}) {
	t.Helper()
	if err := callRunVerb(t, s, method, params); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// wantRefusal asserts a JSON-RPC error carrying the machine-readable code.
func wantRefusal(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("call was ACCEPTED; want a JSON-RPC error %s", code)
	}
	if !strings.Contains(err.Error(), code) {
		t.Fatalf("error %q does not carry the machine-readable code %q", err.Error(), code)
	}
}

// fakeSchedulerRuns stands in for the Go scheduler's registry (ADR-017
// Decision 11). The IPC server sees the scheduler through this narrow read
// surface, so the resolution rule's scheduler arm is exercised without
// standing up a real scheduler and its GitHub client.
type fakeSchedulerRuns struct {
	mu           sync.Mutex
	runs         map[int]*state.RuntimeState
	phaseStarts  []string
	phaseDone    []string
	gateRefusals int
}

func newFakeSchedulerRuns() *fakeSchedulerRuns {
	return &fakeSchedulerRuns{runs: map[int]*state.RuntimeState{}}
}

func (f *fakeSchedulerRuns) register(issue int, rt *state.RuntimeState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[issue] = rt
}

func (f *fakeSchedulerRuns) LookupRunByID(runID string) *state.RuntimeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rt := range f.runs {
		if rt != nil && rt.RunID == runID {
			return rt
		}
	}
	return nil
}

func (f *fakeSchedulerRuns) IsRunLive(runID string) bool { return f.LookupRunByID(runID) != nil }

func (f *fakeSchedulerRuns) RecordPhaseStartForRun(runID string, issueNumber int, stage, name string, index, total int) {
	f.mu.Lock()
	rt := f.runs[issueNumber]
	f.mu.Unlock()
	if rt == nil {
		return
	}
	if rt.RunID != runID {
		f.mu.Lock()
		f.gateRefusals++
		f.mu.Unlock()
		return
	}
	rt.BeginPhase(state.PipelineStage(stage), name, index, total)
	f.mu.Lock()
	f.phaseStarts = append(f.phaseStarts, name)
	f.mu.Unlock()
}

func (f *fakeSchedulerRuns) RecordPhaseCompleteForRun(runID string, issueNumber int, stage, name string) {
	f.mu.Lock()
	rt := f.runs[issueNumber]
	f.mu.Unlock()
	if rt == nil {
		return
	}
	if rt.RunID != runID {
		f.mu.Lock()
		f.gateRefusals++
		f.mu.Unlock()
		return
	}
	rt.CompletePhase(state.PipelineStage(stage), name)
	f.mu.Lock()
	f.phaseDone = append(f.phaseDone, name)
	f.mu.Unlock()
}

func (f *fakeSchedulerRuns) RunIDForIssue(issueNumber int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rt := f.runs[issueNumber]; rt != nil {
		return rt.RunID
	}
	return ""
}

// snapshotFingerprint is dirFingerprint restricted to runtime snapshots, for
// assertions about which repo's SNAPSHOTS a call touched (the history dir is a
// separate, legitimately-written artifact).
func snapshotFingerprint(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "<absent>"
		}
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, ok := state.ParseSnapshotFilename(e.Name()); !ok {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), readErr)
		}
		parts = append(parts, fmt.Sprintf("%s:%x", e.Name(), len(data)))
	}
	return strings.Join(parts, " ")
}

// --- Decision 5: the terminal claim ----------------------------------------

// TestRunIdentity_TerminalDeleteIsIdentityChecked covers ADR-017 F5 and C7.
//
// A successor run of the same issue must survive its predecessor's terminal
// claim: the compare-and-delete keys on the CLAIMED entry and the removal is
// composed from the claimed snapshot's own identity, so neither can take the
// successor's registry entry or its file. The bare-issue delete this replaces
// could and did.
func TestRunIdentity_TerminalDeleteIsIdentityChecked(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")

	const (
		repo  = "acme/platform"
		issue = 370
	)
	zombie, successor := newTestRunID(), newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: zombie,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: successor,
	})

	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: zombie,
	})

	s.runtimesMu.Lock()
	_, zombieLives := s.activeRuntimes[zombie]
	_, successorLives := s.activeRuntimes[successor]
	s.runtimesMu.Unlock()
	if zombieLives {
		t.Error("the claimed entry must be compare-and-deleted")
	}
	if !successorLives {
		t.Error("the successor's registry entry did not survive another run's terminal claim (F5)")
	}
	if _, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(issue, successor))); err != nil {
		t.Errorf("the successor's snapshot did not survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(issue, zombie))); !os.IsNotExist(err) {
		t.Errorf("the claimed run's own snapshot survived its seal; stat = %v", err)
	}
}

// TestRunIdentity_TerminalSnapshotIsNeverRehydrated covers the R-4
// interleaving: a terminal-marked snapshot left on disk (the removal failed, or
// the process died between the write and the remove) makes a later call
// run_closed, NOT an adoption. Rehydrating it would produce a record strictly
// richer by one stage, which the history layer accepts as an upgrade —
// replacing the correct authoritative entry with a zombie's outcome.
func TestRunIdentity_TerminalSnapshotIsNeverRehydrated(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")

	const (
		repo  = "acme/platform"
		issue = 4102
	)
	runID := newTestRunID()

	seeded := state.NewRuntimeState(repo, issue, "", runID)
	seeded.BeginStage(state.StageFeatureDev)
	seeded.MarkTerminal("complete")
	if err := seeded.Persist(stateDir); err != nil {
		t.Fatalf("seed terminal snapshot: %v", err)
	}
	before := dirFingerprint(t, stateDir)

	err := callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	})
	wantRefusal(t, err, codeRunClosed)

	if after := dirFingerprint(t, stateDir); after != before {
		t.Errorf("a refused call rewrote the terminal snapshot:\n before: %s\n  after: %s", before, after)
	}
	s.runtimesMu.Lock()
	entries := len(s.activeRuntimes)
	repopulated := s.closedRuns.hasLocked(runID)
	s.runtimesMu.Unlock()
	if entries != 0 {
		t.Errorf("a terminal snapshot was adopted into %d registry entr(ies)", entries)
	}
	if !repopulated {
		t.Error("adoption meeting a terminal snapshot must re-populate closedRuns — its only refill path")
	}
}

// TestRunIdentity_TerminalRemovalUsesSnapshotRepo pins Decision 4's fix #3: the
// seal's directory is derived from the CLAIMED SNAPSHOT'S own Repo, never from
// the call's repo param. A notifyComplete whose repo disagrees with the run's
// persisted repo could otherwise leave the real file behind while deleting
// nothing.
func TestRunIdentity_TerminalRemovalUsesSnapshotRepo(t *testing.T) {
	launch := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launch))
	s.RegisterRepo("acme", "alpha", rootA)
	s.RegisterRepo("acme", "beta", rootB)

	const issue = 815
	runID := newTestRunID()
	dirA := filepath.Join(rootA, ".nightgauge", "pipeline")
	dirB := filepath.Join(rootB, ".nightgauge", "pipeline")

	// The run belongs to repo A and persists there.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "acme/alpha", IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	if _, err := os.Stat(filepath.Join(dirA, state.SnapshotFilename(issue, runID))); err != nil {
		t.Fatalf("precondition: the run must have persisted into repo A: %v", err)
	}
	// A decoy in repo B under the same name, to prove the seal cannot reach it.
	decoy := state.NewRuntimeState("acme/beta", issue, "", newTestRunID())
	if err := decoy.Persist(dirB); err != nil {
		t.Fatalf("seed repo B decoy: %v", err)
	}
	decoyBefore := snapshotFingerprint(t, dirB)

	// The terminal event names the WRONG repo.
	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: "acme/beta", IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: runID,
	})

	if _, err := os.Stat(filepath.Join(dirA, state.SnapshotFilename(issue, runID))); !os.IsNotExist(err) {
		t.Errorf("the run's real snapshot (repo A) survived its terminal claim; stat = %v", err)
	}
	if after := snapshotFingerprint(t, dirB); after != decoyBefore {
		t.Errorf("the seal touched the WRONG repo's snapshots:\n before: %s\n  after: %s", decoyBefore, after)
	}
	// The RECORD follows the same authority: the run's own repo, not the
	// caller's parameter, so a run's state is never split across two repos.
	if records := readHistoryRecords(t, rootA); len(records) != 1 {
		t.Errorf("the run record landed outside repo A: %d record(s) in A", len(records))
	}
}

// TestRunIdentity_ExecutionPathReplayIsInsideTheClaim pins claim step 1b: the
// #309 replay is the LAST mutation the run accepts and it runs INSIDE the
// claim, so the latch does not refuse it. Under a sequence that latched first,
// every extension-path history record would silently lose execution_path and
// punt_reason with no test noticing.
//
// The end-to-end record assertion ALONE does not discriminate: a replay placed
// BEFORE the claim and outside the lock — the shape Decision 5 explicitly
// rejects — produces exactly the same record on the happy path. The two
// white-box parts below are what tell the shapes apart:
//
//   - the claim's OWN returned snapshot already carries the replay AND the
//     durable latch, so the two are one critical section rather than a mutation
//     followed by a later re-read;
//   - a claim that LOSES step 1a's re-check replays NOTHING. That is the
//     discriminator: with the replay before the claim, the loser would have
//     mutated the run — a foreign, already-closed run — before discovering it
//     had no claim to make.
func TestRunIdentity_ExecutionPathReplayIsInsideTheClaim(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	const (
		repo  = "acme/platform"
		issue = 309
	)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "complete", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: runID,
		StageExecutionPaths: map[string]string{"pr-create": "llm"},
		StagePuntReasons:    map[string]string{"pr-create": "missing-validate-context"},
	})

	records := readHistoryRecords(t, root)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	stage, ok := records[0].Stages["pr-create"]
	if !ok {
		t.Fatalf("pr-create missing from the record; stages=%v", records[0].Stages)
	}
	if stage.ExecutionPath != "llm" {
		t.Errorf("execution_path = %q, want llm — the latch refused the replay", stage.ExecutionPath)
	}
	if stage.PuntReason != "missing-validate-context" {
		t.Errorf("punt_reason = %q, want missing-validate-context", stage.PuntReason)
	}

	// --- The claim itself, white-box ---------------------------------------
	s2 := NewServer(nil, WithWorkspaceRoot(t.TempDir()))
	runID2 := newTestRunID()
	mustCall(t, s2, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID2,
	})
	res, err := s2.resolveRun("pipeline.notifyComplete", verbTerminal, runID2, repo, issue)
	if err != nil || res.entry == nil {
		t.Fatalf("resolve for the claim: %v (entry=%v)", err, res.entry)
	}
	live := res.entry.rs
	complete := func(state.PipelineStage) string { return "complete" }

	// A claim that loses step 1a's re-check — the entry was latched in the
	// unlocked window between resolve and claim, which is precisely the race
	// the re-check exists for.
	s2.runtimesMu.Lock()
	res.entry.terminal = true
	s2.runtimesMu.Unlock()

	_, _, loserErr := s2.runTerminalClaim("pipeline.notifyComplete", res, runID2, repo, issue,
		map[string]string{"pr-create": "deterministic"},
		map[string]string{"pr-create": "loser-should-never-land"}, complete)
	wantRefusal(t, loserErr, codeRunClosed)
	if got := live.StageExecutionPath(state.StagePRCreate); got != "" {
		t.Errorf("a REFUSED claim replayed onto the run: execution_path = %q, want empty — the replay ran outside the claim", got)
	}
	if got := live.StagePuntReason(state.StagePRCreate); got != "" {
		t.Errorf("a REFUSED claim replayed onto the run: punt_reason = %q, want empty", got)
	}
	if live.IsTerminal() {
		t.Error("a refused claim latched the DURABLE half of the terminal latch")
	}

	// The winner: the snapshot the claim RETURNS already carries the replay and
	// the latch — one critical section, not a mutation plus a later read.
	s2.runtimesMu.Lock()
	res.entry.terminal = false
	s2.runtimesMu.Unlock()

	_, snap, err := s2.runTerminalClaim("pipeline.notifyComplete", res, runID2, repo, issue,
		map[string]string{"pr-create": "llm"},
		map[string]string{"pr-create": "missing-validate-context"}, complete)
	if err != nil {
		t.Fatalf("the winning claim was refused: %v", err)
	}
	if got := snap.StageExecutionPaths["pr-create"]; got != "llm" {
		t.Errorf("the CLAIM'S OWN snapshot has execution_path %q, want llm — step 1b did not run inside the claim", got)
	}
	if got := snap.StagePuntReasons["pr-create"]; got != "missing-validate-context" {
		t.Errorf("the CLAIM'S OWN snapshot has punt_reason %q, want missing-validate-context", got)
	}
	if !snap.Terminal {
		t.Error("the claim's snapshot is not terminal — 1c did not run before 1d in the same hold")
	}
}

// TestRunIdentity_InFlightPersistCannotResurrect covers F27.
//
// After the claim returns there is NO non-terminal snapshot for the run, and a
// Persist that lands afterwards writes nothing and returns ErrRunSealed. The
// hole this closes: a transition's unlocked Persist re-creating the snapshot
// with terminal:false and the full history, which the reconciler then
// double-terminals and which adoption rehydrates after any restart.
func TestRunIdentity_InFlightPersistCannotResurrect(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 927
	)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	s.runtimesMu.Lock()
	live := s.activeRuntimes[runID].rs
	s.runtimesMu.Unlock()

	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: runID,
	})

	// The in-flight Persist lands after the seal.
	if err := live.Persist(stateDir); err == nil {
		t.Error("a post-seal Persist wrote a snapshot; it must return ErrRunSealed without writing")
	} else if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("post-seal Persist error = %v, want ErrRunSealed", err)
	}
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	for _, rs := range found {
		if !rs.Terminal {
			t.Errorf("a NON-TERMINAL snapshot for run %s exists after the claim returned — the resurrection R-4 depends on", rs.RunID)
		}
	}

	// And the run's own later transition is refused rather than served.
	wantRefusal(t, callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	}), codeRunClosed)

	// --- The same claim with a Persist genuinely HELD MID-FLIGHT across it. -
	//
	// The sequential leg above proves the post-seal refusal; this one proves
	// there is NO INTERLEAVING that leaves a resurrected file. A writer spins on
	// the run's own Persist while the terminal claim runs, so its calls land
	// before the latch (non-terminal, then removed by the seal), between the
	// latch and the seal (terminal:true, then removed), or after the seal
	// (ErrRunSealed, nothing written). Only an implementation that let the write
	// escape rs.mu — marshal under the lock, write outside it — can drop a stale
	// non-terminal byte slice after the removal (F27).
	raceRoot := t.TempDir()
	rs := NewServer(nil, WithWorkspaceRoot(raceRoot))
	raceDir := filepath.Join(raceRoot, ".nightgauge", "pipeline")
	raceID := newTestRunID()

	mustCall(t, rs, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: raceID,
	})
	rs.runtimesMu.Lock()
	inflight := rs.activeRuntimes[raceID].rs
	rs.runtimesMu.Unlock()

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		// Bounded as well as signalled: a wedged claim must fail this test on
		// its own assertions rather than spin a core forever.
		for i := 0; i < 100000; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = inflight.Persist(raceDir)
		}
	}()

	mustCall(t, rs, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1000, RunID: raceID,
	})
	close(stop)
	writer.Wait()

	raced, err := state.FindPersistedStatesForIssue(raceDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	for _, snap := range raced {
		if !snap.Terminal {
			t.Errorf("a NON-TERMINAL snapshot for run %s survived a claim raced by its own Persist — the resurrection R-4 depends on (F27)",
				snap.RunID)
		}
	}
	if err := inflight.Persist(raceDir); !errors.Is(err, state.ErrRunSealed) {
		t.Errorf("post-seal Persist error = %v, want ErrRunSealed", err)
	}
}

// TestRunIdentity_ClosedRunIsRefusedOnEveryRunProgressMethod: table-driven over
// the four run-progress verbs. Each returns run_closed as a JSON-RPC ERROR —
// never a success payload with a status field, which is how the earlier
// design's rejections became invisible — and mutates nothing.
func TestRunIdentity_ClosedRunIsRefusedOnEveryRunProgressMethod(t *testing.T) {
	const (
		repo  = "acme/platform"
		issue = 553
	)
	cases := []struct {
		method string
		params func(runID string) interface{}
	}{
		{"pipeline.notifyStageTransition", func(r string) interface{} {
			return PipelineNotifyStageTransitionParams{Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: r}
		}},
		{"pipeline.notifyStageProgress", func(r string) interface{} {
			return PipelineNotifyStageProgressParams{Repo: repo, IssueNumber: issue, Stage: "pr-create", RunID: r}
		}},
		{"pipeline.notifyPhaseTransition", func(r string) interface{} {
			return PipelineNotifyPhaseTransitionParams{Repo: repo, IssueNumber: issue, Stage: "pr-create", Name: "p", EventType: "start", RunID: r}
		}},
		{"pipeline.notifyComplete", func(r string) interface{} {
			return PipelineNotifyCompleteParams{Repo: repo, IssueNumber: issue, Success: true, RunID: r}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			root := t.TempDir()
			s := NewServer(nil, WithWorkspaceRoot(root))
			stateDir := filepath.Join(root, ".nightgauge", "pipeline")
			runID := newTestRunID()

			mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
				Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
			})
			mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
				Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: runID,
			})
			before := dirFingerprint(t, stateDir)

			wantRefusal(t, callRunVerb(t, s, tc.method, tc.params(runID)), codeRunClosed)

			if after := dirFingerprint(t, stateDir); after != before {
				t.Errorf("a refused %s touched the state dir:\n before: %s\n  after: %s", tc.method, before, after)
			}
			s.runtimesMu.Lock()
			n := len(s.activeRuntimes)
			s.runtimesMu.Unlock()
			if n != 0 {
				t.Errorf("a refused %s installed %d registry entr(ies)", tc.method, n)
			}
		})
	}
}

// TestRunIdentity_ClosedRunsEvictionFallsBackToTheDurableMarker pins Decision
// 4's late-duplicate table: with the in-memory ring forced past its cap, a late
// duplicate whose TERMINAL SNAPSHOT SURVIVED is still run_closed — because the
// durable marker, not the ring, is the authority.
func TestRunIdentity_ClosedRunsEvictionFallsBackToTheDurableMarker(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 1024
	)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	// The seal's remove fails (a permissions change, a crash between write and
	// remove) — modelled by re-creating the terminal file the seal wrote.
	s.runtimesMu.Lock()
	live := s.activeRuntimes[runID].rs
	s.runtimesMu.Unlock()
	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: runID,
	})
	survivor := live.Snapshot()
	if !survivor.Terminal {
		t.Fatal("precondition: the claim must stamp the durable terminal marker")
	}
	data, err := json.Marshal(survivor)
	if err != nil {
		t.Fatalf("marshal survivor: %v", err)
	}
	if err := state.AtomicWriteFile(filepath.Join(stateDir, state.SnapshotFilename(issue, runID)), data, 0644); err != nil {
		t.Fatalf("re-create the surviving terminal snapshot: %v", err)
	}

	// Evict the id from the ring by overflowing it.
	s.runtimesMu.Lock()
	for i := 0; i < closedRunsCap+1; i++ {
		s.closedRuns.addLocked(fmt.Sprintf("0190%04x-0000-7000-8000-%012d", i&0xffff, i))
	}
	evicted := !s.closedRuns.hasLocked(runID)
	s.runtimesMu.Unlock()
	if !evicted {
		t.Fatal("precondition: the ring must have evicted the id at its cap")
	}

	wantRefusal(t, callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	}), codeRunClosed)

	// --- The other row of the table: evicted AND the snapshot is gone. ------
	//
	// This is the only case eviction can actually degrade, and Decision 4 says
	// so explicitly: with no ring entry and no durable marker the late duplicate
	// ADOPTS EMPTY and is served. That cannot produce a wrong record or a
	// reopened run, because the skeleton it builds is dropped by the history
	// layer's richer-upgrade-only rule (and its learning row by the corpus
	// dedup) — it costs one spurious pipeline_done and nothing else.
	// A fresh root so the history assertions below see this run's records only:
	// the history directory is the dedup coordinator's key.
	lateRoot := t.TempDir()
	late := NewServer(nil, WithWorkspaceRoot(lateRoot))
	lateDir := filepath.Join(lateRoot, ".nightgauge", "pipeline")
	gone := newTestRunID()

	mustCall(t, late, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: gone,
	})
	mustCall(t, late, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "complete", RunID: gone,
		InputTokens: 100, OutputTokens: 20, CostUsd: 0.5,
	})
	mustCall(t, late, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: gone,
		StagesRun: []string{"feature-dev"},
	})
	authoritative := readHistoryRecords(t, lateRoot)
	if len(authoritative) != 1 || len(authoritative[0].Stages) == 0 {
		t.Fatalf("precondition: the real run must have written exactly one NON-EMPTY record; got %d", len(authoritative))
	}
	if _, err := os.Stat(filepath.Join(lateDir, state.SnapshotFilename(issue, gone))); !os.IsNotExist(err) {
		t.Fatalf("precondition: the seal must have removed the snapshot; stat = %v", err)
	}

	late.runtimesMu.Lock()
	for i := 0; i < closedRunsCap+1; i++ {
		late.closedRuns.addLocked(fmt.Sprintf("0191%04x-0000-7000-8000-%012d", i&0xffff, i))
	}
	stillRinged := late.closedRuns.hasLocked(gone)
	late.runtimesMu.Unlock()
	if stillRinged {
		t.Fatal("precondition: the ring must have evicted the id at its cap")
	}

	// Served, not refused — there is nothing left to refuse it with.
	mustCall(t, late, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: gone,
	})
	mustCall(t, late, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: gone,
	})

	// …and the damage is bounded to that: the authoritative record still stands
	// alone, un-buried by the late duplicate's skeleton.
	after := readHistoryRecords(t, lateRoot)
	if len(after) != 1 {
		t.Errorf("history holds %d records for one run id, want 1 — the late duplicate's skeleton was not dropped by the richer-upgrade rule", len(after))
	}
	if len(after) == 1 && len(after[0].Stages) != len(authoritative[0].Stages) {
		t.Errorf("the authoritative record was replaced by a leaner one: stages %d, want %d",
			len(after[0].Stages), len(authoritative[0].Stages))
	}
	// The late duplicate leaves no live entry and no resurrected snapshot: its
	// own claim sealed the empty runtime it adopted.
	late.runtimesMu.Lock()
	leftovers := len(late.activeRuntimes)
	late.runtimesMu.Unlock()
	if leftovers != 0 {
		t.Errorf("the late duplicate left %d registry entr(ies) behind", leftovers)
	}
	if found, ferr := state.FindPersistedStatesForIssue(lateDir, issue); ferr != nil || len(found) != 0 {
		t.Errorf("the late duplicate resurrected %d snapshot(s) (err=%v)", len(found), ferr)
	}
}

// --- Decision 6: the derived issue index -----------------------------------

// TestRunIdentity_CrossRepoSameIssueNumberDoNotCollide covers F8. Repo A #42
// and repo B #42, no force-clear involved, keep separate runtimes, separate
// snapshots and separate index entries — because the identity is globally
// unique on its own and repo is a component of the INDEX key, not of the
// identity.
func TestRunIdentity_CrossRepoSameIssueNumberDoNotCollide(t *testing.T) {
	launch := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(launch))
	s.RegisterRepo("acme", "alpha", rootA)
	s.RegisterRepo("acme", "beta", rootB)

	const issue = 42
	runA, runB := newTestRunID(), newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "acme/alpha", IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runA,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "acme/beta", IssueNumber: issue, Stage: "feature-dev", Status: "complete", CostUsd: 9.5, RunID: runB,
	})

	s.runtimesMu.Lock()
	entries := len(s.activeRuntimes)
	s.runtimesMu.Unlock()
	if entries != 2 {
		t.Fatalf("two repos' #42 collapsed into %d registry entr(ies)", entries)
	}

	currentA, othersA := s.currentRunForIssue("acme/alpha", issue)
	currentB, othersB := s.currentRunForIssue("acme/beta", issue)
	if currentA == nil || currentA.rs.RunID != runA {
		t.Errorf("repo A's index answered with %v, want run %s", currentA, runA)
	}
	if currentB == nil || currentB.rs.RunID != runB {
		t.Errorf("repo B's index answered with %v, want run %s", currentB, runB)
	}
	if len(othersA) != 0 || len(othersB) != 0 {
		t.Errorf("an issue-addressed lookup crossed repos: othersA=%v othersB=%v", othersA, othersB)
	}
	if _, err := os.Stat(filepath.Join(rootA, ".nightgauge", "pipeline", state.SnapshotFilename(issue, runA))); err != nil {
		t.Errorf("repo A's snapshot missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootB, ".nightgauge", "pipeline", state.SnapshotFilename(issue, runB))); err != nil {
		t.Errorf("repo B's snapshot missing: %v", err)
	}
	if currentA.rs.TotalCostUSD != 0 {
		t.Errorf("repo B's spend leaked onto repo A's run: %v", currentA.rs.TotalCostUSD)
	}
}

// TestRunIdentity_IssueIndexRanksOnLastSeen pins the ranking correction in
// Decision 6: "current" is the newest LEASE, not the oldest entry. Ranking on
// FirstSeen is not self-correcting — a wedged run that adopts before a live one
// stays current for the rest of the live run's life (F12 through the index).
func TestRunIdentity_IssueIndexRanksOnLastSeen(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	const (
		repo  = "acme/platform"
		issue = 612
	)
	wedged, live := newTestRunID(), newTestRunID()

	// The wedged run adopts FIRST.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: wedged,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: live,
	})
	// …and the wedged one is silent from here on while the live one keeps
	// reporting, so the newest lease belongs to the live run.
	mustCall(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", RunID: live,
	})

	current, others := s.currentRunForIssue(repo, issue)
	if current == nil || current.rs.RunID != live {
		t.Fatalf("index chose %v as current; want the run holding the newest lease (%s)", current, live)
	}
	if len(others) != 1 || others[0] != wedged {
		t.Errorf("others = %v, want exactly the wedged run %s", others, wedged)
	}
}

// TestRunIdentity_GetStateResolvesThroughTheIndexAndNamesTheRun pins Decision
// 6's UX disambiguation: the response carries the resolved runId and, when the
// issue has more than one run, the other run ids — so a caller can tell it is
// looking at one of several.
func TestRunIdentity_GetStateResolvesThroughTheIndexAndNamesTheRun(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	s.RegisterRepo("acme", "platform", root)
	const issue = 733
	older, newer := newTestRunID(), newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "acme/platform", IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: older,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "acme/platform", IssueNumber: issue, Stage: "feature-planning", Status: "running", RunID: newer,
	})

	h := s.methods["pipeline.getState"]
	raw, err := json.Marshal(PipelineGetStateParams{Owner: "acme", Repo: "platform", IssueNumber: issue})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("getState: %v", err)
	}
	got, ok := result.(*PipelineGetStateResult)
	if !ok {
		t.Fatalf("getState result type = %T", result)
	}
	if got.RunID != newer {
		t.Errorf("resolved runId = %q, want the current run %q", got.RunID, newer)
	}
	if len(got.OtherRunIDs) != 1 || got.OtherRunIDs[0] != older {
		t.Errorf("otherRunIds = %v, want [%s]", got.OtherRunIDs, older)
	}
	// And the response is still a superset of the snapshot the caller used to
	// get: the embedded fields are at the same place on the wire.
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, key := range []string{`"issueNumber":733`, `"stage":"feature-planning"`, `"runId":"` + newer + `"`} {
		if !strings.Contains(string(wire), key) {
			t.Errorf("getState wire JSON is missing %s: %s", key, wire)
		}
	}
}

// TestRunIdentity_GetStateAnswersNothingRatherThanWrongly pins the accepted
// consequence of Decision 6: an issue-addressed read may now return NOTHING
// where it used to return a dead run's snapshot indefinitely (F12). No answer
// is better than a confidently wrong one, and it is an EMPTY RESPONSE, not an
// error — getState is lookup class.
func TestRunIdentity_GetStateAnswersNothingRatherThanWrongly(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	s.RegisterRepo("acme", "platform", root)

	h := s.methods["pipeline.getState"]
	raw, err := json.Marshal(PipelineGetStateParams{Owner: "acme", Repo: "platform", IssueNumber: 999})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("getState for an unknown issue must not error: %v", err)
	}
	if result != nil {
		t.Errorf("getState = %#v, want nil for an issue with no run", result)
	}
}

// --- Decision 11: two registries -------------------------------------------

// TestRunIdentity_SchedulerRunIsServedNotAdopted covers the PipelineBridge path
// and Decision 11's step 3. A run-progress call carrying a LIVE SCHEDULER run's
// id creates NO entry in the IPC registry, records onto the scheduler's own
// runtime, and never becomes "current" in the derived index.
//
// Without that arm the scheduler's RunID would fall into adoption and
// manufacture a second in-memory entry for a run the scheduler already owns: it
// would hold a lease, never be terminal-claimed (the scheduler path never calls
// notifyComplete), leak for the life of the server, and win the index.
func TestRunIdentity_SchedulerRunIsServedNotAdopted(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	fake := newFakeSchedulerRuns()
	s.schedulerRuns = fake

	const (
		repo  = "acme/platform"
		issue = 611
	)
	schedRun := state.NewRuntimeState(repo, issue, "item-1", newTestRunID())
	fake.register(issue, schedRun)

	mustCall(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", InputTokens: 10, RunID: schedRun.RunID,
	})
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
		Index: 1, Total: 3, EventType: "start", RunID: schedRun.RunID,
	})

	s.runtimesMu.Lock()
	n := len(s.activeRuntimes)
	s.runtimesMu.Unlock()
	if n != 0 {
		t.Errorf("a scheduler-owned run was ADOPTED into the IPC registry (%d entr(ies))", n)
	}
	if current, _ := s.currentRunForIssue(repo, issue); current != nil {
		t.Errorf("a scheduler-owned run became 'current' in the IPC issue index: %v", current.rs.RunID)
	}
	if len(schedRun.PhaseHistory) != 1 || schedRun.PhaseHistory[0].Name != "implementation" {
		t.Errorf("the phase did not reach the SCHEDULER's runtime: %+v", schedRun.PhaseHistory)
	}
}

// TestRunIdentity_PhaseTransitionSchedulerArmIsIdentityGated covers the second
// arm of F10: a phase event whose runId does not match the scheduler's
// registered runtime for that issue records NOTHING in that runtime's
// PhaseHistory.
func TestRunIdentity_PhaseTransitionSchedulerArmIsIdentityGated(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	fake := newFakeSchedulerRuns()
	s.schedulerRuns = fake

	const (
		repo  = "acme/platform"
		issue = 610
	)
	schedRun := state.NewRuntimeState(repo, issue, "item-1", newTestRunID())
	fake.register(issue, schedRun)

	// A DIFFERENT run of the same issue reports a phase over IPC.
	foreign := newTestRunID()
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
		Index: 1, Total: 3, EventType: "start", RunID: foreign,
	})

	if len(schedRun.PhaseHistory) != 0 {
		t.Errorf("a foreign run wrote into the scheduler runtime's PhaseHistory: %+v", schedRun.PhaseHistory)
	}
	// It landed on its OWN adopted runtime instead.
	s.runtimesMu.Lock()
	entry := s.activeRuntimes[foreign]
	s.runtimesMu.Unlock()
	if entry == nil {
		t.Fatal("the foreign run's phase was dropped entirely; it must land on its own adopted runtime")
	}
	if len(entry.rs.PhaseHistory) != 1 {
		t.Errorf("the foreign run's own PhaseHistory = %+v, want one entry", entry.rs.PhaseHistory)
	}
}

// TestRunIdentity_TerminalVerbAgainstSchedulerRunIsRefused covers F29 and C4.
//
// notifyComplete and setPaused carrying a LIVE SCHEDULER run's id each return
// run_wrong_owner, write no V2 record and emit no pipeline_done: the scheduler
// books that run's record itself, and serving a terminal verb from a registry
// with no latch, no lease and no compare-and-delete target would write a SECOND
// authoritative record under one run id.
//
// The abandonRun leg of this test arrives with ADR-017 step 6, when the verb
// exists.
func TestRunIdentity_TerminalVerbAgainstSchedulerRunIsRefused(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	fake := newFakeSchedulerRuns()
	s.schedulerRuns = fake

	const (
		repo  = "acme/platform"
		issue = 629
	)
	schedRun := state.NewRuntimeState(repo, issue, "item-1", newTestRunID())
	schedRun.BeginStage(state.StageFeatureDev)
	fake.register(issue, schedRun)

	wantRefusal(t, callRunVerb(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: schedRun.RunID,
	}), codeRunWrongOwner)

	wantRefusal(t, callRunVerb(t, s, "pipeline.setPaused", PipelineSetPausedParams{
		IssueNumber: issue, Repo: repo, Paused: true, RunID: schedRun.RunID,
	}), codeRunWrongOwner)

	if schedRun.Terminal {
		t.Error("an IPC terminal verb latched a scheduler-owned run")
	}
	if schedRun.Paused {
		t.Error("an IPC administrative verb paused a scheduler-owned run")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, ".nightgauge", "pipeline")); len(entries) != 0 {
		t.Errorf("a refused terminal verb wrote %d file(s) into the state dir", len(entries))
	}
	if _, err := os.Stat(filepath.Join(root, ".nightgauge", "pipeline", "history")); !os.IsNotExist(err) {
		t.Errorf("a refused terminal verb wrote a history record; stat = %v", err)
	}
}

// --- Decision 3: setPaused's two arms --------------------------------------

// TestRunIdentity_SetPausedNeverInventsARuntime covers F9 across all three
// dispositions: a CLOSED id errors run_closed; an UNKNOWN id with no snapshot
// errors run_not_found and writes no file; and (in the sibling test in
// server_runtime_persist_test.go) an unknown id WITH a snapshot adopts it
// through the singleflight with its lease left at zero.
func TestRunIdentity_SetPausedNeverInventsARuntime(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 918
	)

	// 1. Unknown id, nothing on disk.
	wantRefusal(t, callRunVerb(t, s, "pipeline.setPaused", PipelineSetPausedParams{
		IssueNumber: issue, Repo: repo, Paused: true, RunID: newTestRunID(),
	}), codeRunNotFound)
	if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
		t.Errorf("a refused setPaused wrote %d file(s)", len(entries))
	}

	// 2. A closed run.
	closed := newTestRunID()
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: closed,
	})
	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: closed,
	})
	wantRefusal(t, callRunVerb(t, s, "pipeline.setPaused", PipelineSetPausedParams{
		IssueNumber: issue, Repo: repo, Paused: true, RunID: closed,
	}), codeRunClosed)

	// 3. Corroboration: a live run of ANOTHER issue is not this issue's run.
	other := newTestRunID()
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: 4242, Stage: "feature-dev", Status: "running", RunID: other,
	})
	wantRefusal(t, callRunVerb(t, s, "pipeline.setPaused", PipelineSetPausedParams{
		IssueNumber: issue, Repo: repo, Paused: true, RunID: other,
	}), codeRunNotFound)
	s.runtimesMu.Lock()
	victim := s.activeRuntimes[other]
	s.runtimesMu.Unlock()
	if victim == nil || victim.rs.Paused {
		t.Error("a mis-addressed setPaused paused a run it did not name")
	}
}

// TestRunIdentity_SetPausedGlobalArmTouchesNothing pins the operator-wide arm
// (issueNumber 0), which exists for this verb only.
//
// MattermostCommandDispatcher's /pause and /resume send pipelineSetPaused(0, …)
// naming no run, because an operator-wide pause is a DIFFERENT TRANSACTION from
// pausing one run. It is accepted without an identity and touches nothing:
// no registry, no runtime, no disk, no event. (Retarget tracked in #423.)
func TestRunIdentity_SetPausedGlobalArmTouchesNothing(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")

	mustCall(t, s, "pipeline.setPaused", PipelineSetPausedParams{IssueNumber: 0, Paused: true})
	mustCall(t, s, "pipeline.setPaused", PipelineSetPausedParams{IssueNumber: 0, Paused: false})

	s.runtimesMu.Lock()
	n := len(s.activeRuntimes)
	s.runtimesMu.Unlock()
	if n != 0 {
		t.Errorf("the global pause arm installed %d registry entr(ies)", n)
	}
	if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
		t.Errorf("the global pause arm wrote %d file(s)", len(entries))
	}
}

// --- Decision 4: the singleflight ------------------------------------------

// TestRunIdentity_ConcurrentAdoptionYieldsOneRuntime covers F30 and C14. Run
// under -race.
//
// N goroutines calling run-progress verbs for ONE unknown id concurrently must
// produce exactly one *RuntimeState, one registry entry and one snapshot
// carrying every goroutine's contribution. Without the per-id singleflight both
// callers miss, both load, both construct, one wins the map and the loser keeps
// mutating an orphan whose Persist targets the SAME filename — same-run field
// loss inside one process.
func TestRunIdentity_ConcurrentAdoptionYieldsOneRuntime(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 3030
		n     = 24
	)
	runID := newTestRunID()

	// A snapshot on disk, so the flight really performs I/O.
	seeded := state.NewRuntimeState(repo, issue, "", runID)
	seeded.BeginStage(state.StageFeatureDev)
	if err := seeded.Persist(stateDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_ = callRunVerb(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
				Repo: repo, IssueNumber: issue, Stage: "feature-dev",
				Name: fmt.Sprintf("phase-%02d", i), Index: i, Total: n,
				EventType: "start", RunID: runID,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	s.runtimesMu.Lock()
	entries := len(s.activeRuntimes)
	entry := s.activeRuntimes[runID]
	flights := len(s.adopting)
	s.runtimesMu.Unlock()
	if entries != 1 || entry == nil {
		t.Fatalf("concurrent adoption produced %d registry entr(ies) for one identity", entries)
	}
	if flights != 0 {
		t.Errorf("%d adoption flight(s) were left behind; every exit path must close and delete its flight", flights)
	}
	snap := entry.rs.Snapshot()
	if snap.Stage != state.StageFeatureDev {
		t.Errorf("adoption did not rehydrate the snapshot: Stage = %q", snap.Stage)
	}
	if len(snap.PhaseHistory) != n {
		t.Errorf("PhaseHistory has %d entries, want %d — a goroutine's work landed on a second *RuntimeState (F30)",
			len(snap.PhaseHistory), n)
	}
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil || len(found) != 1 {
		t.Fatalf("one identity must leave one snapshot; found %d / %v", len(found), err)
	}
}

// TestRunIdentity_AdministrativeResolutionInstallsAnEntryWithoutVouching covers
// F33 and the F9 pin it must not re-create. Run under -race.
//
// An administrative verb (setPaused today; pipeline.abandonRun joins in ADR-017
// step 6) for an id with a SNAPSHOT and no entry installs exactly ONE entry
// through the same singleflight run-progress adoption uses — because adopting a
// snapshot already on disk is not "inventing a run": the snapshot IS the
// evidence. What it must NOT do is vouch for the run. Its lease stays at the
// ZERO time, so an administrative touch can never make a run the operator has
// given up on look alive.
//
// F33 is the interleaving underneath: the administrative caller and the run
// itself must share ONE *RuntimeState. A detached read-modify-write — load the
// snapshot, stamp it, write it back — races the run's own Persist and silently
// drops whichever side wrote first. Sharing the live object makes rs.mu
// serialise the two.
func TestRunIdentity_AdministrativeResolutionInstallsAnEntryWithoutVouching(t *testing.T) {
	const (
		repo  = "acme/platform"
		issue = 3333
	)

	// --- The resolution itself: one entry, installed, not vouched for. -----
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	runID := newTestRunID()

	seeded := state.NewRuntimeState(repo, issue, "", runID)
	seeded.BeginStage(state.StageFeatureDev)
	if err := seeded.Persist(stateDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	mustCall(t, s, "pipeline.setPaused", PipelineSetPausedParams{
		IssueNumber: issue, Repo: repo, Paused: true, RunID: runID,
	})

	s.runtimesMu.Lock()
	entry := s.activeRuntimes[runID]
	installed := len(s.activeRuntimes)
	flights := len(s.adopting)
	s.runtimesMu.Unlock()
	if entry == nil {
		t.Fatal("the administrative resolution installed NO entry — the pause had nothing to serialise against (F33)")
	}
	if installed != 1 {
		t.Errorf("administrative adoption installed %d entr(ies) for one identity", installed)
	}
	if flights != 0 {
		t.Errorf("%d adoption flight(s) were left behind", flights)
	}
	if !entry.lastSeen.IsZero() {
		t.Errorf("lastSeen = %v, want the ZERO time: an administrative verb may install a run's state and may never make it look alive (the F9 pin)",
			entry.lastSeen)
	}
	if entry.rs.Stage != state.StageFeatureDev {
		t.Errorf("the adopted runtime lost the snapshot's history: Stage = %q", entry.rs.Stage)
	}
	// The zero lease is the fact the #44 reconciler's lease arm consumes to
	// answer "this run is NOT vouched for" — its skipRun leg lands with ADR-017
	// step 5's liveness ladder, which is out of step 4's scope. What step 4 owes
	// that ladder is exactly this: an administratively-installed entry that
	// carries no lease stamp to mistake for liveness.

	// --- F33 under concurrency: one runtime, and no stage is lost. ---------
	raceRoot := t.TempDir()
	rs := NewServer(nil, WithWorkspaceRoot(raceRoot))
	raceDir := filepath.Join(raceRoot, ".nightgauge", "pipeline")
	raceID := newTestRunID()

	raceSeed := state.NewRuntimeState(repo, issue, "", raceID)
	if err := raceSeed.Persist(raceDir); err != nil {
		t.Fatalf("seed race snapshot: %v", err)
	}

	const (
		stages  = 12
		pausers = 8
	)
	var wg sync.WaitGroup
	start := make(chan struct{})

	// The run books stages from ONE goroutine, so running→complete stays a
	// coherent pair; the administrative callers race it from eight others.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < stages; i++ {
			stage := fmt.Sprintf("stage-%02d", i)
			if err := callRunVerb(t, rs, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
				Repo: repo, IssueNumber: issue, Stage: stage, Status: "running", RunID: raceID,
			}); err != nil {
				t.Errorf("running transition %s was refused: %v", stage, err)
				return
			}
			if err := callRunVerb(t, rs, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
				Repo: repo, IssueNumber: issue, Stage: stage, Status: "complete", RunID: raceID,
			}); err != nil {
				t.Errorf("complete transition %s was refused: %v", stage, err)
				return
			}
		}
	}()
	for i := 0; i < pausers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := callRunVerb(t, rs, "pipeline.setPaused", PipelineSetPausedParams{
				IssueNumber: issue, Repo: repo, Paused: i%2 == 0, RunID: raceID,
			}); err != nil {
				t.Errorf("concurrent setPaused was refused: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	rs.runtimesMu.Lock()
	raceEntry := rs.activeRuntimes[raceID]
	raceEntries := len(rs.activeRuntimes)
	raceFlights := len(rs.adopting)
	rs.runtimesMu.Unlock()
	if raceEntries != 1 || raceEntry == nil {
		t.Fatalf("an administrative verb racing its run produced %d registry entr(ies) for one identity", raceEntries)
	}
	if raceFlights != 0 {
		t.Errorf("%d adoption flight(s) were stranded", raceFlights)
	}
	snap := raceEntry.rs.Snapshot()
	if len(snap.CompletedStages) != stages {
		t.Errorf("CompletedStages = %d, want %d — a stage the run booked was lost to a concurrent administrative write (F33)",
			len(snap.CompletedStages), stages)
	}
	// One identity, one file: a detached administrative copy would have
	// persisted its own view of the run under the same name.
	found, err := state.FindPersistedStatesForIssue(raceDir, issue)
	if err != nil || len(found) != 1 {
		t.Fatalf("one identity must leave one snapshot; found %d / %v", len(found), err)
	}
	if found[0].RunID != raceID {
		t.Errorf("snapshot RunID = %q, want %q", found[0].RunID, raceID)
	}
}

// TestRunIdentity_UnreadableSnapshotIsRefusedNotAdoptedEmpty pins Decision 4's
// third load outcome: a snapshot that EXISTS but cannot be read is a refusal,
// not an empty adoption. Adopting empty would install a runtime that believes
// the run has no history and would let its next Persist overwrite the
// unreadable-but-present file with a thinner one.
func TestRunIdentity_UnreadableSnapshotIsRefusedNotAdoptedEmpty(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	const (
		repo  = "acme/platform"
		issue = 4404
	)
	runID := newTestRunID()

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corrupt := filepath.Join(stateDir, state.SnapshotFilename(issue, runID))
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}
	before := dirFingerprint(t, stateDir)

	err := callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	if err == nil {
		t.Fatal("an unreadable-but-present snapshot must be refused, not adopted empty")
	}
	if after := dirFingerprint(t, stateDir); after != before {
		t.Errorf("the refused call rewrote the unreadable file:\n before: %s\n  after: %s", before, after)
	}
	s.runtimesMu.Lock()
	n := len(s.activeRuntimes)
	s.runtimesMu.Unlock()
	if n != 0 {
		t.Errorf("a refused adoption installed %d registry entr(ies)", n)
	}
}

// --- Decision 5: the sequence executes without wedging ---------------------

// TestRunIdentity_ClaimSequenceIsDeadlockFree covers F36 and C16.
//
// The whole of Decision 5 executed literally, under concurrency, WITH A
// WATCHDOG that fails the test rather than hanging CI: N concurrent
// notifyCompletes, run-progress calls and derived-index scans over one repo.
// A literal transcription of the refuted sequence — which contained the
// singleflight inside the claim's critical section — wedges on the first call,
// because resolveOrAdopt takes runtimesMu up to three times itself.
//
// THE RECONCILER PASSES JOINED IT IN ADR-017 STEP 5, as that step's own note
// here promised. They are the new lock-order participant: a pass takes
// runtimesMu in arm 1 and in each of the reaping's two LOCKED phases, RELEASES
// it for the unlocked phase between them (the scheduler consult and the
// ProcessAlive syscall), and removes files under neither lock while the
// transition handlers persist into the same directory.
//
// The reaping's later phases only run when phase 1 found a candidate, so ONE
// back-dated entry is seeded below: without it the reaper returns at
// `len(candidates) == 0` and this test exercises exactly one of its three
// phases while claiming all of them.
func TestRunIdentity_ClaimSequenceIsDeadlockFree(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))
	const (
		repo    = "acme/platform"
		runners = 12
	)

	// The reaper's candidate: no lease, no scheduler, no live process. It is
	// reaped by the first pass, so the "no leftovers" assertion below still
	// means every RUNNER's entry was claimed away.
	staleRunID := newTestRunID()
	installRegistryEntry(t, s, state.NewRuntimeState(repo, 7999, "", staleRunID), time.Now().Add(-2*livenessWindow))

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < runners; i++ {
			issue := 7000 + i
			runID := newTestRunID()
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = callRunVerb(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
					Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
				})
				_ = callRunVerb(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
					Repo: repo, IssueNumber: issue, Stage: "feature-dev", RunID: runID,
				})
				// Two concurrent terminal claims for the same run: exactly one
				// wins the latch, the other is refused run_closed.
				var inner sync.WaitGroup
				for c := 0; c < 2; c++ {
					inner.Add(1)
					go func() {
						defer inner.Done()
						_ = callRunVerb(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
							Repo: repo, IssueNumber: issue, Success: true, TotalDurationMs: 1, RunID: runID,
						})
					}()
				}
				inner.Wait()
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					s.currentRunForIssue(repo, issue)
					// The ladder's arm 1, on a run that is concurrently being
					// adopted, leased and terminal-claimed.
					s.runLeaseIsFresh(runID, time.Now())
				}
			}()
		}
		// The reconciler passes, walking the same directory the runners are
		// persisting into while they hold the registry. Each pass takes
		// runtimesMu in arm 1 and in both locked reaping phases, and RELEASES it
		// across the unlocked one — the lock order this test exists to prove
		// acyclic. The seeded stale entry is what carries the first pass past
		// phase 1 into the other two.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				s.reconcileOrphanedRuns()
			}
		}()
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the claim sequence WEDGED — failing rather than hanging CI (F36)")
	}

	s.runtimesMu.Lock()
	leftovers := len(s.activeRuntimes)
	flights := len(s.adopting)
	s.runtimesMu.Unlock()
	if leftovers != 0 {
		t.Errorf("%d registry entr(ies) survived their terminal claims", leftovers)
	}
	if flights != 0 {
		t.Errorf("%d adoption flight(s) were stranded", flights)
	}
	// Exactly one authoritative record per run — a doubled claim must not
	// double-book (C4).
	records := readHistoryRecords(t, root)
	if len(records) != runners {
		t.Errorf("history holds %d records, want exactly one per run (%d)", len(records), runners)
	}
}
