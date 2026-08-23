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
	"github.com/nightgauge/nightgauge/internal/state"
)

// daemonWorkspace returns a temp workspace whose DAEMON SOCKET PATH fits.
// t.TempDir() embeds the test name, and these names run well past the
// sockaddr_un sun_path limit (104 bytes on macOS, 108 on Linux) once
// ".nightgauge/daemon.sock" is appended — which surfaces only as a listener
// that never comes up, not as an obvious error.
func daemonWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ngd")
	if err != nil {
		t.Fatalf("temp workspace: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if n := len(ipc.DaemonSocketPath(dir)); n > 100 {
		t.Skipf("socket path is %d bytes, past the sun_path limit — TMPDIR is too deep for this test", n)
	}
	return dir
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

	sock := ipc.DaemonSocketPath(workspace)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	listenErr := make(chan error, 1)
	go func() { listenErr <- srv.ListenSocket(ctx, sock) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-listenErr:
			// Report the real reason rather than a bare timeout.
			t.Fatalf("the test daemon's listener exited: %v", err)
		default:
		}
		if c, err := ipc.DialClient(context.Background(), sock, 100*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the test daemon never came up on its socket")
}

// seedRun drives one run into existence on the daemon — a live registry entry
// and a snapshot on disk — and returns its run id.
func seedRun(t *testing.T, workspace string, issue int) string {
	t.Helper()
	runID := "01a02f24-498e-7364-bb8a-c96fa3739900"

	c, err := ipc.DialClient(context.Background(), ipc.DaemonSocketPath(workspace), time.Second)
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

// TestRecordGateResult_RoutesThroughTheDaemonWhenOneIsReachable is AC1: with a
// server up, the result arrives over IPC and is persisted by the single writer.
func TestRecordGateResult_RoutesThroughTheDaemonWhenOneIsReachable(t *testing.T) {
	workspace := daemonWorkspace(t)
	startGateRecordDaemon(t, workspace)
	const issue = 5501
	runID := seedRun(t, workspace, issue)

	recordGateResult(context.Background(), workspace, issue, "pr-create", runID, state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})

	stateDir := filepath.Join(workspace, ".nightgauge", "pipeline")
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
	recordGateResult(context.Background(), workspace, issue, "pr-create", "", state.StageGateResult{
		GateName: "pr-create", Passed: true, Timestamp: "2026-08-23T00:00:00Z",
	})

	rs, err := state.LoadPersistedState(filepath.Join(workspace, ".nightgauge", "pipeline"), runID)
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
	workspace := t.TempDir()
	const issue = 5503
	runID := "01a02f24-498e-7364-bb8a-c96fa3739901"
	stateDir := filepath.Join(workspace, ".nightgauge", "pipeline")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// A snapshot must already exist: load-or-skip means the direct path never
	// CREATES one, which is the rule that stops it resurrecting a sealed run.
	rs := state.NewRuntimeState("acme/platform", issue, "item-5503", runID)
	if err := rs.Persist(stateDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// No daemon on this workspace's socket.
	if _, err := ipc.DialClient(context.Background(), ipc.DaemonSocketPath(workspace), 100*time.Millisecond); err == nil {
		t.Fatal("a daemon is reachable; this test cannot exercise the fallback")
	}

	recordGateResult(context.Background(), workspace, issue, "pr-create", runID, state.StageGateResult{
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
	stateDir := filepath.Join(workspace, ".nightgauge", "pipeline")

	// Close the run: its terminal claim seals and removes the snapshot, and the
	// id lands in closedRuns.
	c, err := ipc.DialClient(context.Background(), ipc.DaemonSocketPath(workspace), time.Second)
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

	recordGateResult(context.Background(), workspace, issue, "pr-create", runID, state.StageGateResult{
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
