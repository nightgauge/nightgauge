package main

// #377 — `gate verify --record` routing: the IPC server is the single
// authoritative writer of the runtime snapshot whenever it is alive, and the
// direct file write is reserved for the no-server path.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// daemonWorkspace returns a temp workspace for a test daemon: a git
// repository, because the run snapshots live in its clone's pipeline state
// directory (ADR-024 § 7). Its socket is placed in the runtime directory
// TestMain isolates (ADR-024 § 10), so the workspace path's length no longer
// matters.
func daemonWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ngd")
	if err != nil {
		t.Fatalf("temp workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return layouttest.Init(t, dir)
}

// startGateRecordDaemon brings up a real socket-listening server rooted at
// workspace and returns once the socket accepts a connection. A real socket is
// used rather than a fake because the behaviour under test IS the dial: what
// recordGateResult does depends entirely on whether one succeeds.
func startGateRecordDaemon(t *testing.T, workspace string) {
	t.Helper()
	srv := ipc.NewServer(nil, ipc.WithWorkspaceRoot(workspace))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	sock, err := ipc.DaemonSocketPath(workspace)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	// BindSocket is the readiness signal (#1158): once it returns the kernel
	// is accepting on sock, so the first DialClient below cannot be refused.
	ln, err := srv.BindSocket(sock)
	if err != nil {
		t.Fatalf("bind the test daemon's socket: %v", err)
	}
	go func() { _ = srv.ServeSocket(ctx, ln, sock) }()
}

// seedRun drives one run into existence on the daemon — a live registry entry
// and a snapshot on disk — and returns its run id.
func seedRun(t *testing.T, workspace string, issue int) string {
	t.Helper()
	runID := "01a02f24-498e-7364-bb8a-c96fa3739900"

	c, err := ipc.DialDaemon(context.Background(), workspace, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	params := map[string]any{
		"repo": "acme/platform", "issueNumber": issue, "stage": "pr-create",
		"status": "running", "runId": runID,
	}
	if err := c.Call(context.Background(), "pipeline.notifyStageTransition", params, nil); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	return runID
}

// daemonGateResults reads the run's gate results for stage from the daemon's
// live runtime (pipeline.getState), not from the snapshot file: the durable
// record is built from that runtime, so a result only a file holds is lost.
func daemonGateResults(t *testing.T, workspace string, issue int, stage string) int {
	t.Helper()
	c, err := ipc.DialDaemon(context.Background(), workspace, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	var got struct {
		StageGateResults map[string][]state.StageGateResult `json:"stageGateResults"`
	}
	params := ipc.PipelineGetStateParams{Owner: "acme", Repo: "platform", IssueNumber: issue}
	if err := c.Call(context.Background(), "pipeline.getState", params, &got); err != nil {
		t.Fatalf("getState: %v", err)
	}
	return len(got.StageGateResults[stage])
}

// TestRecordGateResult_RoutesThroughTheDaemonWhenOneIsReachable is AC1: with a
// server up, the result arrives over IPC and is persisted by the single writer.
func TestRecordGateResult_RoutesThroughTheDaemonWhenOneIsReachable(t *testing.T) {
	workspace := daemonWorkspace(t)
	startGateRecordDaemon(t, workspace)
	const issue = 5501
	runID := seedRun(t, workspace, issue)

	recordGateResult(context.Background(), workspace, "", "", issue, "pr-create", runID, state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})

	stateDir := layouttest.PipelineDir(t, workspace)
	rs, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load the run's snapshot: %v", err)
	}
	got := rs.StageGateResultsFor(state.PipelineStage("pr-create"))
	if len(got) != 1 {
		t.Fatalf("the gate result did not reach the snapshot through IPC (found %d)", len(got))
	}
	if !got[0].Passed || got[0].GateName != "pr-create" {
		t.Errorf("gate result did not round-trip through IPC: %+v", got[0])
	}
}

// TestRecordGateResult_TakesTheRunIDFromTheStageEnvironment pins that the
// common case needs no flag: every adapter exports NIGHTGAUGE_RUN_ID into the
// stage environment, and the gate CLI is spawned as a stage subprocess.
func TestRecordGateResult_TakesTheRunIDFromTheStageEnvironment(t *testing.T) {
	workspace := daemonWorkspace(t)
	startGateRecordDaemon(t, workspace)
	const issue = 5502
	runID := seedRun(t, workspace, issue)

	t.Setenv(adapters.RunIDEnvVar, runID)

	// No run id passed — it must come from the environment.
	recordGateResult(context.Background(), workspace, "", "", issue, "pr-create", "", state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})

	rs, err := state.LoadPersistedState(layouttest.PipelineDir(t, workspace), runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rs.StageGateResultsFor(state.PipelineStage("pr-create"))) != 1 {
		t.Error("the environment-supplied run id did not address the daemon")
	}
}

// TestRecordGateResult_FallsBackToTheDirectWriteWithNoDaemon is AC2. The
// fallback is not a degraded mode: with no daemon there is no second writer, so
// the direct path is racing nothing and keeps every one of ADR-017 Decision 5's
// three rules.
func TestRecordGateResult_FallsBackToTheDirectWriteWithNoDaemon(t *testing.T) {
	workspace := layouttest.Repo(t)
	const issue = 5503
	runID := "01a02f24-498e-7364-bb8a-c96fa3739901"
	stateDir := layouttest.MkPipelineDir(t, workspace)

	// A snapshot must already exist: load-or-skip means the direct path never
	// CREATES one, which is the rule that stops it resurrecting a sealed run.
	rs := state.NewRuntimeState("acme/platform", issue, "item-5503", runID)
	if err := rs.Persist(stateDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// No daemon on this workspace's socket.
	if _, err := ipc.DialDaemon(context.Background(), workspace, 100*time.Millisecond); err == nil {
		t.Fatal("a daemon is reachable; this test cannot exercise the fallback")
	}

	recordGateResult(context.Background(), workspace, "", "", issue, "pr-create", runID, state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})

	back, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := back.StageGateResultsFor(state.PipelineStage("pr-create"))
	if len(got) != 1 {
		t.Fatalf("the direct-write fallback did not record the gate result (found %d)", len(got))
	}
	if !got[0].Passed {
		t.Errorf("gate result did not round-trip on the direct path: %+v", got[0])
	}
}

// TestRecordGateResult_DoesNotWriteDirectlyWhenTheDaemonRefuses is the sharp
// half of AC1. A refusal is the single authoritative writer saying this record
// does not belong on that run; falling back to the file would reintroduce
// exactly the second writer the routing exists to remove.
func TestRecordGateResult_DoesNotWriteDirectlyWhenTheDaemonRefuses(t *testing.T) {
	workspace := daemonWorkspace(t)
	startGateRecordDaemon(t, workspace)
	const issue = 5504
	runID := seedRun(t, workspace, issue)
	stateDir := layouttest.PipelineDir(t, workspace)

	// Close the run: its terminal claim seals and removes the snapshot, and the
	// id lands in closedRuns.
	c, err := ipc.DialDaemon(context.Background(), workspace, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	done := map[string]any{"repo": "acme/platform", "issueNumber": issue, "success": true, "totalDurationMs": 5, "runId": runID}
	if err := c.Call(context.Background(), "pipeline.notifyComplete", done, nil); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	// Re-create a snapshot the way a stray writer would, so a direct write
	// COULD succeed if the fallback were taken. If the routing is right,
	// nothing appends to it.
	revived := state.NewRuntimeState("acme/platform", issue, "item-5504", runID)
	if err := revived.Persist(stateDir); err != nil {
		t.Fatalf("seed a stray snapshot: %v", err)
	}

	recordGateResult(context.Background(), workspace, "", "", issue, "pr-create", runID, state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})

	back, err := state.LoadPersistedState(stateDir, runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := len(back.StageGateResultsFor(state.PipelineStage("pr-create"))); n != 0 {
		t.Errorf("the CLI wrote the file after the daemon REFUSED the record (%d result(s)) — "+
			"the fallback must not second-guess the single writer", n)
	}
}

// TestGateVerify_RunIDFlagIsWired pins the flag exists and documents its
// default, so the exact-addressing path cannot be dropped silently.
func TestGateVerify_RunIDFlagIsWired(t *testing.T) {
	cmd := gateVerifyCmd()
	f := cmd.Flags().Lookup("run-id")
	if f == nil {
		t.Fatal("gate verify has no --run-id flag; the IPC route cannot address a run without one")
	}
	if f.DefValue != "" {
		t.Errorf("--run-id default = %q, want empty (it falls back to $NIGHTGAUGE_RUN_ID at call time)", f.DefValue)
	}
	// Keep the marshalled params in step with the server's expectations.
	if _, err := json.Marshal(ipc.PipelineRecordStageGateResultParams{}); err != nil {
		t.Fatalf("params do not marshal: %v", err)
	}
}

// TestRecordGateResult_FilesUnderTheRecordRootWhenWorkdirIsAWorktree is the
// #1054 regression. The extension runs gates with `--workdir <worktree>`,
// because that is where the gate reads issue-N.json / dev-N.json from. But the
// daemon listens only at the repo root, and only the repo root holds the run
// snapshot.
//
// Before --record-root existed, this function used `workspace` for both: the
// dial at <worktree>/.nightgauge/daemon.sock always failed, and the direct
// write then targeted the worktree's own pipeline directory, which held stage
// context but no runtime-{issue}-{runID}.json — so the append took its
// load-or-skip branch and wrote nothing. Every gate on every worktree run
// recorded nowhere, which is why the end-of-run audit reported
// [gate-not-invoked] for stages whose gates had demonstrably passed.
//
// Per-clone state is shared by every root of a clone now (ADR-024 § 7), so the
// direct write would find the snapshot; it is still the second writer, and
// the daemon's live runtime would never see the result. Passing "" for
// recordRoot here takes that path and fails the daemon-side assertion.
func TestRecordGateResult_FilesUnderTheRecordRootWhenWorkdirIsAWorktree(t *testing.T) {
	repo := daemonWorkspace(t)
	startGateRecordDaemon(t, repo)
	const issue = 5505
	runID := seedRun(t, repo, issue)

	// The worktree the gate reads its inputs from. Its stage context lives in
	// the clone's pipeline state directory, never in the working tree.
	worktree := filepath.Join(repo, ".worktrees", "issue-5505")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	recordGateResult(context.Background(), worktree, repo, repo, issue, "pr-create", runID, state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-28T00:00:00Z",
	})

	// The record must land on the run snapshot at the REPO root.
	stateDir := layouttest.PipelineDir(t, repo)
	rs, loadErr := state.LoadPersistedState(stateDir, runID)
	if loadErr != nil {
		t.Fatalf("load the run's snapshot at the repo root: %v", loadErr)
	}
	if got := rs.StageGateResultsFor(state.PipelineStage("pr-create")); len(got) != 1 {
		t.Fatalf("gate results recorded on the run = %d, want 1 — the record did not reach the authoritative snapshot", len(got))
	}
	if n := daemonGateResults(t, repo, issue, "pr-create"); n != 1 {
		t.Fatalf("gate results on the daemon's live run = %d, want 1 — the record bypassed the daemon", n)
	}

	// And nothing may be written into the worktree.
	if _, err := os.Stat(filepath.Join(worktree, ".nightgauge")); !os.IsNotExist(err) {
		t.Errorf("per-clone state was written into the worktree (stat err = %v)", err)
	}
}

// TestRecordGateResult_ReachesTheDaemonWhenItServesADifferentRoot is the #1054
// round-two regression, and the shape the first fix missed.
//
// In a multi-repo workspace `nightgauge serve` is started with ONE workspace
// root while runs execute in sibling repos. The daemon's socket lives at the
// SERVE root; the run's snapshot lives at the run's repo root. Round one pointed
// the dial at the run's repo root, which has no socket, so the dial still failed
// on every run.
//
// The fallback file write cannot cover for it: the durable record is built by
// the daemon from its in-memory RuntimeState (claimTerminal snapshots
// entry.rs), so a result written to a file by this process never reaches the
// record. Only the IPC path lands a gate result on the run.
//
// Passing daemonRoot == repo (the round-one behaviour) reproduces the failure.
func TestRecordGateResult_ReachesTheDaemonWhenItServesADifferentRoot(t *testing.T) {
	serveRoot := daemonWorkspace(t)
	startGateRecordDaemon(t, serveRoot)
	const issue = 5506
	runID := seedRun(t, serveRoot, issue)

	// A sibling repo: the run's own root, with a pipeline dir but NO socket.
	repo := daemonWorkspace(t)
	layouttest.MkPipelineDir(t, repo)
	// And the worktree the gate reads its inputs from.
	worktree := filepath.Join(repo, ".worktrees", "issue-5506")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	if p, _ := ipc.DaemonSocketPath(repo); func() bool { _, err := os.Stat(p); return err == nil }() {
		t.Fatal("fixture is wrong: the sibling repo must not have a daemon socket")
	}

	recordGateResult(context.Background(), worktree, repo, serveRoot, issue, "pr-create", runID,
		state.StageGateResult{GateName: "pr-create", Passed: true, Timestamp: "2026-08-28T00:00:00Z"})

	// The daemon owns the snapshot; the result must be on it.
	rs, loadErr := state.LoadPersistedState(layouttest.PipelineDir(t, serveRoot), runID)
	if loadErr != nil {
		t.Fatalf("load the run's snapshot at the serve root: %v", loadErr)
	}
	if got := rs.StageGateResultsFor(state.PipelineStage("pr-create")); len(got) != 1 {
		t.Fatalf("gate results on the run = %d, want 1 — the record did not reach the daemon", len(got))
	}
}
