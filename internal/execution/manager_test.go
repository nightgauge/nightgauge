package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

func TestHostBinaryPath(t *testing.T) {
	t.Run("returns the resolved executable", func(t *testing.T) {
		if got := hostBinaryPath(func() (string, error) { return "/opt/ib/nightgauge", nil }); got != "/opt/ib/nightgauge" {
			t.Fatalf("got %q, want /opt/ib/nightgauge", got)
		}
	})
	t.Run("empty when resolution errors", func(t *testing.T) {
		if got := hostBinaryPath(func() (string, error) { return "/ignored", context.DeadlineExceeded }); got != "" {
			t.Fatalf("expected empty on error, got %q", got)
		}
	})
}

func TestUpsertEnvVar(t *testing.T) {
	t.Run("appends when key absent", func(t *testing.T) {
		got := upsertEnvVar([]string{"PATH=/bin", "FOO=1"}, "NIGHTGAUGE_BIN", "/opt/ib")
		want := []string{"PATH=/bin", "FOO=1", "NIGHTGAUGE_BIN=/opt/ib"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("replaces an inherited value so the new one is authoritative", func(t *testing.T) {
		got := upsertEnvVar([]string{"NIGHTGAUGE_BIN=/stale", "PATH=/bin"}, "NIGHTGAUGE_BIN", "/opt/ib")
		// Exactly one NIGHTGAUGE_BIN, with the new value.
		count, val := 0, ""
		for _, kv := range got {
			if strings.HasPrefix(kv, "NIGHTGAUGE_BIN=") {
				count++
				val = strings.TrimPrefix(kv, "NIGHTGAUGE_BIN=")
			}
		}
		if count != 1 || val != "/opt/ib" {
			t.Fatalf("expected exactly one NIGHTGAUGE_BIN=/opt/ib, got %v", got)
		}
	})
}

func TestRunStage_NilAdapter_ReturnsErrorWithoutPanic(t *testing.T) {
	// VSCode IPC mode constructs the Manager with a nil adapter — the Scheduler
	// uses IpcStageRunner instead. Direct RunStage callers (e.g. autonomous
	// refinement) must get a clean error rather than a nil-pointer panic.
	m := NewManager(t.TempDir(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := m.RunStage(ctx, StageOptions{
		Repo:        "owner/repo",
		IssueNumber: 1,
		Timeout:     time.Second,
	})

	if err == nil {
		t.Fatal("expected error when adapter is nil, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on nil-adapter error, got %+v", result)
	}
	if !strings.Contains(err.Error(), "adapter") {
		t.Errorf("expected error mentioning 'adapter', got: %v", err)
	}
}

func TestHasAdapter_ReflectsAdapterState(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	if m.HasAdapter() {
		t.Error("HasAdapter() should be false when constructed with nil adapter")
	}
}

func TestCancelWithGrace_NoExecution_ReturnsNoError(t *testing.T) {
	m := NewManager("/tmp", nil)
	graceful, err := m.CancelWithGrace("missing#1", 5*time.Second)
	if err != nil {
		t.Errorf("expected no error for missing key, got %v", err)
	}
	if graceful {
		t.Error("expected graceful=false for missing key")
	}
}

func TestCancelWithGrace_ForceKill_WhenProcessIgnoresSIGTERM(t *testing.T) {
	// Spawn a shell process that ignores SIGTERM so we can exercise the SIGKILL path.
	// The while loop prevents the shell from exec-optimizing the last command,
	// which would discard the trap and cause the process to exit on SIGTERM.
	cmd := exec.Command("sh", "-c", "trap '' TERM; while true; do sleep 1; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}

	// Wait for the shell to fully start and execute "trap '' TERM" before we
	// send SIGTERM. Without this pause there is a startup race: SIGTERM arrives
	// before the trap is set up and the process exits immediately.
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	ex := &Execution{
		Process: cmd.Process,
		Cancel:  cancel,
	}

	m := NewManager("/tmp", nil)
	key := "test/repo#99"
	m.mu.Lock()
	m.running[key] = ex
	m.mu.Unlock()

	// Use a short timeout so the test doesn't wait 30s.
	graceful, err := m.CancelWithGrace(key, 100*time.Millisecond)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if graceful {
		t.Error("expected graceful=false when process ignores SIGTERM and is force-killed")
	}

	// ctx should be cancelled regardless of graceful outcome.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("expected context to be cancelled after CancelWithGrace")
	}

	// Clean up: wait for the process to avoid zombie.
	_ = cmd.Wait()
}

func TestCancelWithGrace_GracefulExit_WhenProcessExitsOnSIGTERM(t *testing.T) {
	// Spawn a process that exits immediately on SIGTERM.
	cmd := exec.Command("sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ex := &Execution{
		Process: cmd.Process,
		Cancel:  cancel,
	}

	m := NewManager("/tmp", nil)
	key := "test/repo#100"
	m.mu.Lock()
	m.running[key] = ex
	m.mu.Unlock()

	// Use a generous timeout; the default sh process responds to SIGTERM quickly.
	graceful, err := m.CancelWithGrace(key, 5*time.Second)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !graceful {
		t.Error("expected graceful=true when process exits before timeout")
	}

	// ctx should be cancelled.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("expected context to be cancelled after graceful exit")
	}

	_ = cmd.Wait()
}

func TestCancelWithGrace_NilProcess_DoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ex := &Execution{
		Process: nil,
		Cancel:  cancel,
	}

	m := NewManager("/tmp", nil)
	key := "test/repo#101"
	m.mu.Lock()
	m.running[key] = ex
	m.mu.Unlock()

	graceful, err := m.CancelWithGrace(key, 1*time.Second)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// No process means nothing to wait for — graceful stays false.
	if graceful {
		t.Error("expected graceful=false when process is nil")
	}

	// Cancel should still be called.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("expected Cancel() to be called even when Process is nil")
	}
}

// TestRunStage_NonAgenticAdapter_RejectedBeforeSpawn guards the #57 agentic
// truth-gate: a chat-completion-only adapter must be rejected with remediation
// before any command is built or spawned.
func TestRunStage_NonAgenticAdapter_RejectedBeforeSpawn(t *testing.T) {
	m := NewManager(t.TempDir(), chatOnlyAdapter{adapters.NewClaudeAdapter()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := m.RunStage(ctx, StageOptions{
		Repo:        "owner/repo",
		IssueNumber: 1,
		Timeout:     time.Second,
	})

	if err == nil {
		t.Fatal("expected error dispatching a non-agentic adapter, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	for _, want := range []string{"chat-completion-only", "NIGHTGAUGE_ADAPTER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
	// The remediation list is generated from the registry (#1670): every
	// agentic adapter, grok and opencode included, must be named.
	agentic := adapters.NewRegistry().AgenticNames()
	for _, want := range []string{"grok", "opencode"} {
		found := false
		for _, n := range agentic {
			found = found || n == want
		}
		if !found {
			t.Errorf("registry AgenticNames() = %v, missing %q", agentic, want)
		}
	}
	for _, name := range agentic {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("expected error to name agentic adapter %q, got: %v", name, err)
		}
	}
}

// TestBuildRunOptions_ThreadsRunIDFromRuntime covers the one place where the
// run identity crosses from the orchestrator layer into the adapter layer
// (ADR-017 step 0). StageOptions already carries the whole RuntimeState, so the
// mapping reads Runtime.RunID directly rather than duplicating a scalar — one
// source of truth per layer. Before #370 the mapping dropped the runtime
// entirely and every spawned stage ran with no way to name its run.
func TestBuildRunOptions_ThreadsRunIDFromRuntime(t *testing.T) {
	rt := state.NewRuntimeState("nightgauge/nightgauge", 370, "", "01890a5d-ac96-774b-bcce-b302099a8057")

	got := buildRunOptions(StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 370,
		Stage:       "feature-dev",
		Runtime:     rt,
	}, "/tmp/worktree")

	if got.RunID != rt.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, rt.RunID)
	}
}

// TestBuildRunOptions_NilRuntimeYieldsEmptyRunID is the refineViaCLI shape
// (autonomous.go's issue-refine dispatch): a legitimate non-pipeline execution
// that carries no run identity at all. It must map to an empty RunID — no
// panic, and no invented id — so the adapters export no NIGHTGAUGE_RUN_ID.
func TestBuildRunOptions_NilRuntimeYieldsEmptyRunID(t *testing.T) {
	got := buildRunOptions(StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 370,
		Stage:       "issue-refine",
		// Runtime intentionally nil
	}, "/tmp/worktree")

	if got.RunID != "" {
		t.Errorf("RunID = %q, want empty for a nil Runtime", got.RunID)
	}
}

// lookupEnv finds key in a composed environment slice, honoring the
// last-entry-wins rule os/exec applies (Cmd.environ dedups keeping the last
// occurrence). Returns the effective value and whether the key is present at
// all — the distinction the whole run-identity contract turns on.
func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	value, found := "", false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value, found = strings.TrimPrefix(kv, prefix), true
		}
	}
	return value, found
}

// TestComposeStageEnv_RunIdentityIsReconciledNotInherited pins the ADR-017
// anti-laundering rule at the only place it is observable: the environment the
// CHILD receives.
//
// nightgauge dispatches nightgauge, so a host environment already carrying
// NIGHTGAUGE_RUN_ID is not hypothetical — a stage subprocess is itself running
// under one, and anything it launches inherits it. An identity-less dispatch
// (autonomous issue-refine; a manual per-stage invocation) that merely "adds
// no run id" would therefore still hand the child a run id: the OUTER run's.
// Every record the child then writes is booked under a run it has nothing to
// do with. Absence has to be produced, not assumed.
//
// The counterpart case pins the other half: when the dispatch DOES have an
// identity, its value must beat the inherited one rather than landing beside it
// with precedence left to chance.
func TestComposeStageEnv_RunIdentityIsReconciledNotInherited(t *testing.T) {
	const outer = "01890a5d-ac96-774b-bcce-b302099a8057"
	const inner = "0189aaaa-bbbb-7ccc-8ddd-eeeeffff0000"

	// A host env that is itself inside a run — the recursive-dogfood shape.
	inherited := []string{"PATH=/usr/bin", adapters.RunIDEnvVar + "=" + outer}

	t.Run("identity-less dispatch strips the inherited id", func(t *testing.T) {
		env := composeStageEnv(inherited, nil, map[string]string{"NIGHTGAUGE_STAGE": "issue-refine"}, "", "")

		if v, ok := lookupEnv(env, adapters.RunIDEnvVar); ok {
			t.Errorf("%s present as %q; a dispatch with no identity must leave the child with none — "+
				"inheriting %q would book its records under the outer run", adapters.RunIDEnvVar, v, outer)
		}
		// The strip is surgical: unrelated inherited entries survive. PATH is
		// asserted by containment, not equality — applyNodeResolution prepends
		// the host's nvm bin dir when there is one (#3863), which is host state.
		if v, ok := lookupEnv(env, "PATH"); !ok || !strings.Contains(v, "/usr/bin") {
			t.Errorf("PATH = %q (present=%v), want the inherited entry preserved", v, ok)
		}
		if v, ok := lookupEnv(env, "NIGHTGAUGE_STAGE"); !ok || v != "issue-refine" {
			t.Errorf("NIGHTGAUGE_STAGE = %q (present=%v), want the adapter's export", v, ok)
		}
	})

	t.Run("identified dispatch overrides the inherited id", func(t *testing.T) {
		adapterEnv := map[string]string{adapters.RunIDEnvVar: inner}
		env := composeStageEnv(inherited, nil, adapterEnv, "", inner)

		if v, ok := lookupEnv(env, adapters.RunIDEnvVar); !ok || v != inner {
			t.Errorf("%s = %q (present=%v), want this dispatch's id %q, not the inherited %q",
				adapters.RunIDEnvVar, v, ok, inner, outer)
		}
		// Belt and braces: no stale entry lingers for a later reader that scans
		// forward instead of taking the last match.
		count := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, adapters.RunIDEnvVar+"=") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s appears %d times in the composed env, want exactly 1", adapters.RunIDEnvVar, count)
		}
	})
}

// TestRunStage_NonZeroExit_ReturnsNilErrorWithStderr pins the contract the #533
// fix rests on, and which nothing pinned before.
//
// A non-zero exit is NOT an error here: RunStage converts *exec.ExitError into
// result.ExitCode and returns err == nil, keeping the process's own output on
// result.Stdout / result.Stderr. Every consumer that wants to know WHY a CLI
// stage failed must therefore read the result, not the error — which is exactly
// what ExecutionManagerRunner failed to do, so the scheduler classified the
// literal string "exit 1: <nil>" as subagent_crash for every CLI-mode failure.
//
// If this test ever starts failing because RunStage returns a non-nil error on
// a non-zero exit, the orchestrator's stageFailureText fallback becomes dead
// code — which is fine, but it should be a deliberate change, not a silent one.
func TestRunStage_NonZeroExit_ReturnsNilErrorWithStderr(t *testing.T) {
	root := t.TempDir()

	const wantStdout = `{"type":"error","message":"unknown model id"}`
	const wantStderr = `Error: Couldn't set model 'grok-build-0.1': Invalid params: "unknown model id".`

	stubDir := t.TempDir()
	outPath := filepath.Join(stubDir, "out.txt")
	errPath := filepath.Join(stubDir, "err.txt")
	if err := os.WriteFile(outPath, []byte(wantStdout+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errPath, []byte(wantStderr+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(stubDir, "stub-grok.sh")
	script := "#!/bin/sh\ncat " + outPath + "\ncat " + errPath + " >&2\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", stub)

	// ensureWorktree returns early when the directory already exists, so the
	// spawn/wait path is reachable without a git repo behind it.
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-533"), 0755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(root, adapters.NewGrokAdapter())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := m.RunStage(ctx, StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 533,
		Stage:       "pr-create",
		Timeout:     30 * time.Second,
	})

	if err != nil {
		t.Fatalf("RunStage returned err = %v; a non-zero exit must surface as "+
			"result.ExitCode with a NIL error (manager.go's *exec.ExitError branch)", err)
	}
	if result == nil {
		t.Fatal("RunStage returned a nil result alongside a nil error")
	}
	if result.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, wantStderr) {
		t.Errorf("result.Stderr = %q, want it to contain the CLI's verbatim reason %q — "+
			"this field is the ONLY place a CLI failure's reason survives", result.Stderr, wantStderr)
	}
	if !strings.Contains(result.Stdout, wantStdout) {
		t.Errorf("result.Stdout = %q, want it to contain %q", result.Stdout, wantStdout)
	}
}

// --- #555: the stage child must be observable on disk while it runs ---------

// pollUntil spins on cond until it holds or the deadline expires. Polling
// rather than a fixed sleep because the fact under test — "the snapshot on disk
// now names a live child" — becomes true at an instant this process does not
// observe directly, and a sleep long enough to be reliable is long enough to be
// slow.
func pollUntil(t *testing.T, why string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, why)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// blockingStageStub writes a stub CLI that reports its own pid and then blocks
// until `release` appears — a stage that is HEALTHY and SILENT, which is the
// shape #555 is about. The pid is published through a temp-file rename so a
// reader can never see a half-written value.
//
// $$ inside the script is the pid of the process Go started: the stub is the
// command exec.Command spawns, so the shell running it IS cmd.Process.
func blockingStageStub(t *testing.T, dir string) (stub, pidFile, release string) {
	t.Helper()
	pidFile = filepath.Join(dir, "child.pid")
	release = filepath.Join(dir, "release")
	stub = filepath.Join(dir, "stub-blocking-stage.sh")
	script := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s' \"$$\" > %[1]s.tmp\nmv %[1]s.tmp %[1]s\nwhile [ ! -f %[2]s ]; do sleep 0.02; done\nexit 0\n",
		pidFile, release)
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return stub, pidFile, release
}

func readPidFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	pid := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil {
		t.Fatalf("parse pid from %q: %v", string(raw), err)
	}
	return pid
}

func snapshotPID(t *testing.T, stateDir string, issue int, runID string) (int, bool) {
	t.Helper()
	snap, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil || snap == nil {
		return 0, false
	}
	return snap.PID, true
}

// TestRunStage_PublishesTheLiveStageChildPidAndRetractsItOnExit is the producer
// half of #555, and the only place the defect is observable at all.
//
// A scheduler-owned run is reconciled by a SEPARATE process (a `nightgauge
// serve` daemon), whose liveness ladder can consult neither the scheduler's
// registry nor its own. Arms 1 and 2 are therefore false by construction, and
// once a single stage runs quietly past runstate.LivenessWindow arm 4 goes false
// too. Only arm 3 — processAlive(snap.PID), read out of the run's snapshot FILE
// — can carry the population, and before this change nothing ever wrote a live
// pid into that file: SetProcess runs after cmd.Start() and only touches memory,
// and the scheduler is blocked in Wait() from that instant until the stage ends.
// The snapshot's pid was the ZERO the stage-start persist wrote (#534), forever.
//
// RED-FIRST: delete the publishStageChild call after SetProcess in
// manager.go and the first poll below times out — the on-disk pid never leaves
// 0, which is precisely the state in which a healthy 30-minute stage is emitted
// as pipeline_done(success=false) and has its snapshot deleted mid-flight.
// Delete the retraction after Wait() instead and the final assertion fails: the
// exited child's pid stays on disk across the whole between-stages gap, where a
// recycled pid answers arm 3 for a run nobody is running.
func TestRunStage_PublishesTheLiveStageChildPidAndRetractsItOnExit(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	stub, pidFile, release := blockingStageStub(t, t.TempDir())
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", stub)

	// ensureWorktree returns early when the directory already exists, so the
	// spawn path is reachable without a git repo behind it.
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-555"), 0755); err != nil {
		t.Fatal(err)
	}

	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	rt := state.NewRuntimeState("nightgauge/nightgauge", 555, "", runID)
	rt.BeginStage(state.StageFeatureDev)
	// The scheduler's stage-start persist (#534): the snapshot exists before the
	// spawn, and the pid it advertises is 0. Persisting here rather than letting
	// the manager create the file is also the contract under test — the manager
	// uses PersistExisting and must never become a snapshot's author.
	if err := rt.Persist(stateDir); err != nil {
		t.Fatalf("stage-start persist: %v", err)
	}
	if pid, ok := snapshotPID(t, stateDir, 555, runID); !ok || pid != 0 {
		t.Fatalf("precondition: on-disk pid = %d (loaded=%v), want 0", pid, ok)
	}

	m := NewManager(root, adapters.NewGrokAdapter())
	done := make(chan error, 1)
	go func() {
		_, runErr := m.RunStage(context.Background(), StageOptions{
			Repo:        "nightgauge/nightgauge",
			IssueNumber: 555,
			Stage:       "feature-dev",
			Runtime:     rt,
			Timeout:     60 * time.Second,
		})
		done <- runErr
		// CLOSED, not merely sent to: the cleanup below receives from this
		// channel too, and on the happy path the assertion body has already
		// taken the one buffered value. A plain send would leave the cleanup
		// blocked for its full timeout on every green run.
		close(done)
	}()
	// Always let the child go, however the assertions land, so a failure cannot
	// leave a blocked process behind for the rest of the package.
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0644)
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
	})

	var onDisk int
	pollUntil(t, "the manager to publish a live stage-child pid into the run's snapshot", 30*time.Second, func() bool {
		pid, ok := snapshotPID(t, stateDir, 555, runID)
		onDisk = pid
		return ok && pid != 0
	})
	pollUntil(t, "the stub stage to report its own pid", 30*time.Second, func() bool {
		_, err := os.Stat(pidFile)
		return err == nil
	})

	if child := readPidFile(t, pidFile); onDisk != child {
		t.Errorf("snapshot pid = %d, want the stage child's own pid %d — arm 3 must name the process doing the work", onDisk, child)
	}
	if !runstate.ProcessAlive(onDisk) {
		t.Errorf("ProcessAlive(%d) is false while the stage is still running — the published pid is not a liveness signal", onDisk)
	}

	// The child exits: the fact stops being true, so the snapshot must stop
	// asserting it.
	if err := os.WriteFile(release, nil, 0644); err != nil {
		t.Fatal(err)
	}
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("RunStage: %v", runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunStage did not return after the stub was released")
	}

	if pid, ok := snapshotPID(t, stateDir, 555, runID); !ok || pid != 0 {
		t.Errorf("on-disk pid after the stage exited = %d (loaded=%v), want 0 — a dead pid left on disk is a "+
			"PID-reuse window spanning the whole between-stages gap (#534)", pid, ok)
	}
}

// TestRunStage_DoesNotCreateASnapshotForARunThatHasNone pins the other half of
// the PersistExisting choice.
//
// The manager is not the snapshot's author. If it created the file, a direct
// RunStage caller whose owner never persisted anything would suddenly leave a
// reconcilable snapshot behind — a run the reconciler would later close and
// report terminal to the platform, for a run that never had a record at all.
// It is also the resurrection guard: a terminal claim that sealed and removed
// the file between the spawn and this write must not have it re-created.
func TestRunStage_DoesNotCreateASnapshotForARunThatHasNone(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	stub, _, release := blockingStageStub(t, t.TempDir())
	// Release before the run so the stub exits immediately — this test is about
	// the file, not the timing.
	if err := os.WriteFile(release, nil, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", stub)
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-556"), 0755); err != nil {
		t.Fatal(err)
	}

	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	rt := state.NewRuntimeState("nightgauge/nightgauge", 556, "", runID)
	rt.BeginStage(state.StageFeatureDev)

	m := NewManager(root, adapters.NewGrokAdapter())
	if _, err := m.RunStage(context.Background(), StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 556,
		Stage:       "feature-dev",
		Runtime:     rt,
		Timeout:     60 * time.Second,
	}); err != nil {
		t.Fatalf("RunStage: %v", err)
	}

	if entries, err := os.ReadDir(stateDir); err == nil && len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the manager wrote %v into a state dir it must never author", names)
	}
}

// sigtermTrapAdapter is a minimal agentic adapters.SkillRunner whose
// BuildCommand spawns a shell that traps SIGTERM, echoes proof to stderr, and
// exits 0 — the graceful-stop shape #564 exists for. No real CLI adapter
// exercises this cheaply: they all shell out to a vendor binary that isn't
// present in CI, so the fake is the only way to pin the trap-and-exit-0 race
// against CancelWithGrace deterministically.
type sigtermTrapAdapter struct{ ready string }

func (sigtermTrapAdapter) Name() string { return "sigterm-trap-fake" }

// The ready file is written only once the trap is installed, so the test can
// wait for it instead of guessing how long sh takes to reach the builtin.
func (a sigtermTrapAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "sh", []string{"-c", `trap "echo received SIGTERM >&2; exit 0" TERM; : > "$0"; sleep 30 & wait`, a.ready}, nil
}

func (sigtermTrapAdapter) UsesStdin() bool { return false }
func (sigtermTrapAdapter) Agentic() bool   { return true }

// TestRunStage_GracefulStopExitZeroIsReportedCancelled is the #564 red test:
// operator cancel goes through Manager.CancelWithGrace, which SIGTERMs the
// stage and waits for it to exit BEFORE cancelling the execution context. A
// CLI that traps SIGTERM and exits 0 — this fake stands in for one — comes
// back from cmd.Wait() as err==nil, ExitCode==0: identical to a healthy
// finish. Without RunResult.Cancelled there is no way for anything downstream
// to tell the two apart, and the stage's own stderr (its explanation for
// dying) is silently dropped.
func TestRunStage_GracefulStopExitZeroIsReportedCancelled(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-564"), 0755); err != nil {
		t.Fatal(err)
	}

	ready := filepath.Join(root, "trap-ready")
	m := NewManager(root, sigtermTrapAdapter{ready: ready})
	key := "nightgauge/nightgauge#564"

	resultCh := make(chan *adapters.RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := m.RunStage(context.Background(), StageOptions{
			Repo:        "nightgauge/nightgauge",
			IssueNumber: 564,
			Stage:       "feature-dev",
			Timeout:     30 * time.Second,
		})
		resultCh <- result
		errCh <- err
	}()

	// Wait for RunStage to register the execution before requesting the stop —
	// CancelWithGrace is a no-op (graceful=false, no error) against a key that
	// isn't in m.running yet, which would make this test race the spawn
	// instead of exercising the cancel path. Same helper and budget as the
	// #555 waits above, for the same class of fact: a just-spawned
	// subprocess publishing something this process does not observe directly.
	pollUntil(t, "RunStage to register its execution", 30*time.Second, func() bool {
		m.mu.Lock()
		_, ok := m.running[key]
		m.mu.Unlock()
		return ok
	})
	// Registration happens right after cmd.Start(), which only forks+execs —
	// the shell itself needs a moment to reach the `trap` builtin, and a
	// SIGTERM that lands before then kills it by signal (ExitCode -1). A fixed
	// 50ms sleep lost that race under -race load (#2171); wait for the file
	// the shell writes after installing the trap instead.
	pollUntil(t, "the fake CLI to install its SIGTERM trap", 30*time.Second, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})

	graceful, err := m.CancelWithGrace(key, 5*time.Second)
	if err != nil {
		t.Fatalf("CancelWithGrace: %v", err)
	}
	if !graceful {
		t.Fatal("expected graceful=true — the fake CLI traps SIGTERM and exits 0 well inside the grace period")
	}

	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatalf("RunStage returned err = %v; a trapped-SIGTERM exit-0 is CLI mode's nil-error shape", err)
	}
	if result == nil {
		t.Fatal("RunStage returned a nil result alongside a nil error")
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 — the fake CLI traps SIGTERM and exits cleanly", result.ExitCode)
	}
	if !result.Cancelled {
		t.Fatal("result.Cancelled = false, want true — CancelWithGrace requested this stop; " +
			"the field does not exist / is never set on today's tree, which is exactly the #564 hole")
	}
	if !strings.Contains(result.Stderr, "received SIGTERM") {
		t.Fatalf("result.Stderr = %q, want it to contain the trap's proof line %q", result.Stderr, "received SIGTERM")
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written. The adapter prints its dispatch warning to os.Stderr, which is
// read at call time, so swapping the variable is enough; nothing in this
// package runs tests in parallel.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	out := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		out <- b
	}()
	defer func() {
		os.Stderr = orig
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return string(<-out)
}

// openCodeFakeVersion is the first line of every fake `opencode`: it answers
// the `opencode --version` the stream parser runs after a stage (#1624) and
// exits, so the invocation is never recorded as the stage's own run.
const openCodeFakeVersion = `[ "$1" = --version ] && { echo 1.18.30; exit 0; }`

// TestOpenCodeDispatchRefusedUntilEnabled is ADR-022's enable gate, observed at
// the only place it matters: whether RunStage spawns the CLI. A fake `opencode`
// first on PATH records every invocation, its argv and its stdin.
//
// With NIGHTGAUGE_EXPERIMENTAL_OPENCODE unset (or set to anything but "1"),
// RunStage must refuse with remediation and the fake must never run. With it
// set, the fake runs exactly once, the prompt arrives on stdin and nowhere on
// argv, and the warning naming the unenforced controls reaches stderr. The
// refusal comes from the adapter's PreDispatch hook, which RunStage calls
// before BuildCommand; without that call the fake would run in every case.
func TestOpenCodeDispatchRefusedUntilEnabled(t *testing.T) {
	isolateOpenCodeHome(t)
	stubDir := t.TempDir()
	invocations := filepath.Join(stubDir, "invocations.log")
	argvFile := filepath.Join(stubDir, "argv.txt")
	stdinFile := filepath.Join(stubDir, "stdin.txt")
	// The version the stream parser reads after the run is answered and not
	// counted: only the stage's own `run` is an invocation here.
	script := fmt.Sprintf("#!/bin/sh\n%s\necho invoked >> %q\nprintf '%%s\\n' \"$@\" > %q\ncat > %q\nexit 0\n",
		openCodeFakeVersion, invocations, argvFile, stdinFile)
	if err := os.WriteFile(filepath.Join(stubDir, "opencode"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	worktree := filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-1612")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	// A real git repository: the project-config tamper gate (#1638 fix
	// round) now fails CLOSED, not open, on a worktree git reports is not
	// one, so a plain directory would be refused before this test's own
	// enable-gate check ever ran.
	gitInitOneCommitWorktree(t, worktree)

	const prompt = "--auto implement the issue; this prompt must arrive on stdin only"
	run := func() (*adapters.RunResult, error, string) {
		var result *adapters.RunResult
		var err error
		stderr := captureStderr(t, func() {
			m := NewManager(root, adapters.NewOpenCodeAdapter())
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err = m.RunStage(ctx, StageOptions{
				Repo:        "nightgauge/nightgauge",
				IssueNumber: 1612,
				Stage:       "feature-dev",
				Model:       "lmstudio/qwen/qwen3.8-27b",
				Prompt:      prompt,
				Timeout:     30 * time.Second,
			})
		})
		return result, err, stderr
	}
	// Each subtest counts only its own invocations.
	invocationCount := func() int {
		raw, err := os.ReadFile(invocations)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "invoked\n")
	}
	const warningHeader = "[opencode] WARNING: experimental adapter enabled by " + adapters.ExperimentalOpenCodeEnvVar + "=1"

	for _, closed := range []string{"", "true"} {
		t.Run(fmt.Sprintf("closed=%q", closed), func(t *testing.T) {
			t.Setenv(adapters.ExperimentalOpenCodeEnvVar, closed)
			_ = os.Remove(invocations)
			result, err, stderr := run()
			if n := invocationCount(); n != 0 {
				t.Errorf("the opencode binary ran %d time(s) with the gate closed; want 0", n)
			}
			if err == nil {
				t.Fatalf("RunStage dispatched opencode with %s=%q; want a refusal", adapters.ExperimentalOpenCodeEnvVar, closed)
			}
			if result != nil {
				t.Errorf("a refused dispatch returned a result: %+v", result)
			}
			for _, want := range []string{"dispatch refused", adapters.ExperimentalOpenCodeEnvVar + "=1", "NIGHTGAUGE_ADAPTER"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q: %v", want, err)
				}
			}
			if strings.Contains(stderr, warningHeader) {
				t.Errorf("a refused dispatch printed the enabled-dispatch warning:\n%s", stderr)
			}
		})
	}

	t.Run("open", func(t *testing.T) {
		t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
		_ = os.Remove(invocations)
		result, err, stderr := run()
		if err != nil {
			t.Fatalf("RunStage refused with %s=1: %v", adapters.ExperimentalOpenCodeEnvVar, err)
		}
		if result == nil || result.ExitCode != 0 {
			t.Fatalf("result = %+v, want exit 0 from the fake", result)
		}
		if n := invocationCount(); n != 1 {
			t.Errorf("the opencode binary ran %d time(s); want exactly 1", n)
		}
		if !strings.Contains(stderr, warningHeader) {
			t.Errorf("stderr lacks the warning line %q:\n%s", warningHeader, stderr)
		}
		for _, control := range []string{"subagent cost", "stage limits", "permission map", "safety plugin"} {
			if !strings.Contains(stderr, "[opencode]   - "+control+": ") {
				t.Errorf("warning does not list the %q control:\n%s", control, stderr)
			}
		}

		gotStdin, err := os.ReadFile(stdinFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotStdin) != prompt {
			t.Errorf("stdin = %q, want the prompt %q", gotStdin, prompt)
		}
		gotArgv, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatal(err)
		}
		wantArgv := strings.Join([]string{
			"run", "--format", "json", "--print-logs", "--log-level", "ERROR",
			"-m", "lmstudio/qwen/qwen3.8-27b", "--dir", worktree,
		}, "\n") + "\n"
		if string(gotArgv) != wantArgv {
			t.Errorf("argv the binary received =\n%s\nwant\n%s", gotArgv, wantArgv)
		}
		if strings.Contains(string(gotArgv), "implement the issue") || strings.Contains(string(gotArgv), "--auto") {
			t.Errorf("prompt bytes reached argv:\n%s", gotArgv)
		}
	})
}

// isolateOpenCodeHome points HOME at a fresh directory and clears every
// variable the opencode run root is resolved from, so a test's run roots,
// ~/.opencode check and machine-tier directory never touch the real home. It
// writes openCodeMachineConfig as the machine-tier config, so a stage on the
// reference LM Studio has the endpoint its per-run config needs.
func isolateOpenCodeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"GH_CONFIG_DIR", "GOCACHE", "NIGHTGAUGE_CONFIG_HOME", "NIGHTGAUGE_STATE_HOME",
	} {
		t.Setenv(k, "")
	}
	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	return home
}

// openCodeMachineConfig is the reference machine's `opencode:` block: one LM
// Studio on loopback, with a 131072-token window loaded.
const openCodeMachineConfig = `opencode:
  provider: lm-studio
  base_url: http://127.0.0.1:1234/v1
  limit:
    context: 131072
    output: 8192
`

// writeOpenCodeMachineConfig writes contents as the machine-tier config at
// the path the environment resolves it to now, and returns the path.
func writeOpenCodeMachineConfig(t *testing.T, contents string) string {
	t.Helper()
	path, err := config.MachineConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// openCodeFake is a fake `opencode` first on PATH. Each run records the
// environment it received, NUL-separated, and the listing of its four XDG
// directories, then runs extra, a shell fragment, and drains stdin.
type openCodeFake struct{ dir string }

func installOpenCodeFake(t *testing.T, extra string) *openCodeFake {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
`+openCodeFakeVersion+`
echo invoked >> %[1]q/invocations.log
env -0 > %[1]q/env.bin
ls -ld "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_CACHE_HOME" "$XDG_STATE_HOME" > %[1]q/dirs.txt 2>&1
%[2]s
cat > /dev/null
exit 0
`, dir, extra)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &openCodeFake{dir: dir}
}

func (f *openCodeFake) invocations(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "invocations.log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "invoked\n")
}

// env returns the last run's environment, and every entry of it, duplicates
// included, as the child saw them.
func (f *openCodeFake) env(t *testing.T) (map[string]string, []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "env.bin"))
	if err != nil {
		t.Fatalf("the fake opencode did not record its environment: %v", err)
	}
	entries := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	env := map[string]string{}
	for _, kv := range entries {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env, entries
}

// openCodeStageOptions is a feature-dev dispatch of model for issue 1612.
func openCodeStageOptions(model string, runtime *state.RuntimeState) StageOptions {
	return StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 1612,
		Stage:       "feature-dev",
		Model:       model,
		Prompt:      "implement the issue",
		Timeout:     30 * time.Second,
		Runtime:     runtime,
	}
}

// openCodeWorkspace is a workspace root whose issue-1612 worktree exists as
// a real, one-commit git repository — every real dispatch's WorktreeDir is
// one by construction (Manager.RunStage's own worktree setup), and the
// OpenCode adapter's project-config tamper gate now fails CLOSED, not open,
// on a worktree git reports is not a repository (#1638 fix round finding
// 9/9), so a fixture that used to be a plain directory would be refused
// before ever reaching the fake opencode binary the test under it means to
// exercise.
func openCodeWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-1612")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitOneCommitWorktree(t, worktree)
	return root
}

// gitInitOneCommitWorktree makes dir a one-commit git repository in place:
// the minimum openCodeProjectConfigTamperCheck needs to pass a clean
// worktree (#1638 fix round: the gate now fails closed on a worktree git
// reports is not a repository, so every OpenCode fixture that used to be a
// plain directory needs this).
func gitInitOneCommitWorktree(t *testing.T, dir string) {
	t.Helper()
	gittest.InitRepo(t, dir, "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "base")
}

// TestOpenCodeAnthropicDispatchNeedsTheAPIKey is ADR-022 § 17's key
// requirement observed where it matters, whether RunStage spawns the CLI.
// With the enable switch set and ANTHROPIC_API_KEY unset, an anthropic/ model
// is refused before spawn and the fake never runs; with the key set, the
// dispatch spawns once, because run isolation leaves OpenCode no stored login
// to use in its place, and the child receives the key.
func TestOpenCodeAnthropicDispatchNeedsTheAPIKey(t *testing.T) {
	isolateOpenCodeHome(t)
	fake := installOpenCodeFake(t, "")
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	root := openCodeWorkspace(t)
	dispatch := func() (string, error) {
		var err error
		stderr := captureStderr(t, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err = NewManager(root, adapters.NewOpenCodeAdapter()).RunStage(ctx, openCodeStageOptions("anthropic/claude-sonnet-5", nil))
		})
		return stderr, err
	}

	t.Setenv("ANTHROPIC_API_KEY", "")
	stderr, err := dispatch()
	if n := fake.invocations(t); n != 0 {
		t.Errorf("the opencode binary ran %d time(s) with ANTHROPIC_API_KEY unset; want 0", n)
	}
	if err == nil {
		t.Fatal("RunStage dispatched anthropic/claude-sonnet-5 through opencode with ANTHROPIC_API_KEY unset")
	}
	for _, want := range []string{"dispatch refused", "ANTHROPIC_API_KEY is not set", "claude-headless"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(stderr, "[opencode] WARNING") {
		t.Errorf("a refused dispatch printed the enabled-dispatch warning:\n%s", stderr)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake-key-for-the-test-1616")
	// An inherited base URL would send the stage and its key to whatever
	// server it names, a proxy serving a subscription included, so it never
	// reaches the child: the endpoint is the catalog's or a config's.
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9/v1")
	if _, err := dispatch(); err != nil {
		t.Fatalf("RunStage refused anthropic/claude-sonnet-5 with ANTHROPIC_API_KEY set: %v", err)
	}
	if n := fake.invocations(t); n != 1 {
		t.Errorf("the opencode binary ran %d time(s) after the keyed dispatch; want exactly 1", n)
	}
	env, _ := fake.env(t)
	if env["ANTHROPIC_API_KEY"] != "sk-ant-fake-key-for-the-test-1616" {
		t.Error("an anthropic/ dispatch did not hand the child its own provider's key")
	}
	if v, ok := env["ANTHROPIC_BASE_URL"]; ok {
		t.Errorf("ANTHROPIC_BASE_URL reached an anthropic/ child (%q); the key could go to any server", v)
	}
}

// TestOpenCodeSpawnWithholdsInheritedOpenCodeVariablesAndForeignKeys is the
// inherited-environment policy (ADR-022 § 8, § 17) at the only place it is
// observable, the environment the child receives. Every inherited OPENCODE_*
// variable is absent, the login-bearing ones included, and no value of one
// reaches the child or anything the run printed. A local model receives none
// of the variables OpenCode's catalog binds to a hosted model service, beyond
// the issue's seven too, and no provider base URL, and the dispatch names on
// stderr the ones it withheld. A cloud platform's credentials arrive whole,
// the catalog's and the companions it does not list alike, so the stage's
// tools keep the identity the operator chose instead of falling back to
// another. The adapter's own OPENCODE_* exports survive the filter, and an
// unrelated inherited variable arrives, so the absences are not an empty dump.
func TestOpenCodeSpawnWithholdsInheritedOpenCodeVariablesAndForeignKeys(t *testing.T) {
	isolateOpenCodeHome(t)
	fake := installOpenCodeFake(t, "")
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	// Fake values in the shapes OpenCode reads, never real credentials.
	const sentinel = "fake-login-sentinel-1616"
	inherited := map[string]string{
		"OPENCODE_AUTH_CONTENT":    `{"anthropic":{"type":"oauth","refresh":"` + sentinel + `"}}`,
		"OPENCODE_CONSOLE_TOKEN":   sentinel + "-console",
		"OPENCODE_DB":              "/tmp/" + sentinel + ".db",
		"OPENCODE_CONFIG_CONTENT":  `{"small_model":"` + sentinel + `/x"}`,
		"OPENCODE_CONFIG":          "/tmp/" + sentinel + ".json",
		"OPENCODE_CONFIG_DIR":      "/tmp/" + sentinel,
		"OPENCODE_MODELS_PATH":     "/tmp/" + sentinel + "-models.json",
		"OPENCODE_SERVER_PASSWORD": sentinel + "-password",
	}
	for k, v := range inherited {
		t.Setenv(k, v)
	}
	keys := []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "XAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY",
		"GROQ_API_KEY", "MISTRAL_API_KEY", "DEEPSEEK_API_KEY",
		"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL",
	}
	for _, k := range keys {
		t.Setenv(k, sentinel+"-"+strings.ToLower(k))
	}
	// Scoped cloud credentials, as a credential helper exports them. Fake
	// values; the stage's tools must receive every one of them.
	platform := map[string]string{
		"AWS_ACCESS_KEY_ID":              "platform-kept-aws-access-key-id",
		"AWS_SECRET_ACCESS_KEY":          "platform-kept-aws-secret-access-key",
		"AWS_SESSION_TOKEN":              "platform-kept-aws-session-token",
		"AWS_REGION":                     "platform-kept-aws-region",
		"GOOGLE_APPLICATION_CREDENTIALS": "/tmp/platform-kept-service-account.json",
		"GOOGLE_CLOUD_PROJECT":           "platform-kept-project",
		"DATABRICKS_HOST":                "platform-kept-databricks-host",
		"DATABRICKS_TOKEN":               "platform-kept-databricks-token",
	}
	for k, v := range platform {
		t.Setenv(k, v)
	}
	// Not in the NIGHTGAUGE_ namespace, which an opencode child inherits only
	// the needed names of (TestOpenCodeSpawnWithholdsNightgaugeSecrets).
	t.Setenv("UNRELATED_TEST_INHERITED", "kept")

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err = NewManager(openCodeWorkspace(t), adapters.NewOpenCodeAdapter()).RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})
	if err != nil {
		t.Fatalf("RunStage refused a local model with %s=1: %v", adapters.ExperimentalOpenCodeEnvVar, err)
	}
	env, entries := fake.env(t)
	if env["UNRELATED_TEST_INHERITED"] != "kept" {
		t.Fatal("an unrelated inherited variable did not reach the child, so its recorded environment proves nothing")
	}
	for k := range inherited {
		if k == "OPENCODE_SERVER_PASSWORD" || k == "OPENCODE_CONFIG_CONTENT" {
			continue // the adapter sets its own; checked below
		}
		if v, ok := env[k]; ok {
			t.Errorf("%s reached the opencode child (%d bytes); no inherited OpenCode variable may", k, len(v))
		}
	}
	if p := env["OPENCODE_SERVER_PASSWORD"]; p == "" || strings.Contains(p, sentinel) {
		t.Error("the child did not get the adapter's own server password in place of the inherited one")
	}
	if c := env["OPENCODE_CONFIG_CONTENT"]; strings.Contains(c, sentinel) || !strings.Contains(c, `"small_model":"lmstudio/qwen/qwen3.8-27b"`) {
		t.Errorf("the child did not get the per-run config in place of the inherited one: %.120q", c)
	}
	if env["OPENCODE_DISABLE_SHARE"] != "1" {
		t.Error("the filter removed the adapter's own OPENCODE_* exports along with the inherited ones")
	}
	for _, k := range keys {
		if _, ok := env[k]; ok {
			t.Errorf("%s reached a local-model child; a local run inherits no hosted model service's credentials and no base URL", k)
		}
	}
	for k, v := range platform {
		if env[k] != v {
			t.Errorf("%s did not reach the child intact; withholding part of a cloud platform's credentials moves the stage's tools to another identity", k)
		}
	}
	var notice []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "withheld from this stage and every tool it runs: ") {
			notice = append(notice, line)
		}
	}
	if len(notice) != 1 {
		t.Errorf("stderr has %d lines naming the withheld variables, want 1:\n%s", len(notice), stderr)
	} else {
		for _, k := range []string{"OPENAI_API_KEY", "GROQ_API_KEY", "ANTHROPIC_BASE_URL"} {
			if !strings.Contains(notice[0], k) {
				t.Errorf("the withheld-variables line does not name %s:\n%s", k, notice[0])
			}
		}
		for k := range platform {
			if strings.Contains(notice[0], k) {
				t.Errorf("the withheld-variables line names %s, which the stage keeps:\n%s", k, notice[0])
			}
		}
	}
	for _, kv := range entries {
		if strings.Contains(kv, sentinel) {
			t.Errorf("an inherited secret's value reached the child's environment in %.40q", kv)
		}
	}
	for name, out := range map[string]string{"stderr": stderr, "result.Stdout": result.Stdout, "result.Stderr": result.Stderr} {
		if strings.Contains(out, sentinel) {
			t.Errorf("an inherited secret's value was written to %s", name)
		}
	}
}

// TestOpenCodeSpawnWithholdsNightgaugeSecrets: a Go-spawned opencode child
// inherits no NIGHTGAUGE_* variable outside adapters.OpenCodeNightgaugeEnvAllow
// (#1657). Operator secrets in the namespace stay out; the names the child and
// its plugin read, inherited or exported, arrive.
func TestOpenCodeSpawnWithholdsNightgaugeSecrets(t *testing.T) {
	isolateOpenCodeHome(t)
	fake := installOpenCodeFake(t, "")
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	secrets := map[string]string{
		"NIGHTGAUGE_JIRA_TOKEN":        "fake-jira-token-1657",
		"NIGHTGAUGE_LM_STUDIO_API_KEY": "fake-lm-studio-key-1657",
		"NIGHTGAUGE_AUDIT_API_KEY":     "fake-audit-key-1657",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	// Needed names only ever inherited, never exported by the Go path.
	t.Setenv("NIGHTGAUGE_EDIT_HOOK_DIAGNOSTICS", "1")
	t.Setenv("NIGHTGAUGE_MODEL", "lmstudio/qwen/qwen3.8-27b")

	var err error
	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err = NewManager(openCodeWorkspace(t), adapters.NewOpenCodeAdapter()).RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil))
	})
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	env, entries := fake.env(t)
	for k, v := range secrets {
		if _, ok := env[k]; ok {
			t.Errorf("%s reached the opencode child", k)
		}
		for _, kv := range entries {
			if strings.Contains(kv, v) {
				t.Errorf("the value of %s reached the child's environment in %.40q", k, kv)
			}
		}
	}
	for _, k := range []string{"NIGHTGAUGE_EDIT_HOOK_DIAGNOSTICS", "NIGHTGAUGE_MODEL", "NIGHTGAUGE_STAGE", "NIGHTGAUGE_ADAPTER", "NIGHTGAUGE_OPENCODE_PLUGIN_NONCE"} {
		if env[k] == "" {
			t.Errorf("%s did not reach the opencode child, which needs it", k)
		}
	}
	for k := range env {
		if strings.HasPrefix(k, "NIGHTGAUGE_") && !strings.HasPrefix(k, "NIGHTGAUGE_OPENCODE_") &&
			!slices.Contains(adapters.OpenCodeNightgaugeEnvAllow, k) && k != "NIGHTGAUGE_CONFIG_HOME" && k != "NIGHTGAUGE_STATE_HOME" {
			t.Errorf("%s reached the opencode child but is neither allowed nor a run variable", k)
		}
	}
}

// TestOpenCodeStageRunsInItsOwnRunRoot: every opencode spawn runs with its
// four XDG directories inside ~/.nightgauge/opencode/runs/<id>/, each a 0700
// directory, and with the tools that move with XDG pinned back to the
// operator's (ADR-022 § 8). A dispatch with no run identity gets a root id of
// its own, never exported as NIGHTGAUGE_RUN_ID, and its root is gone when
// RunStage returns. The stages of an identified run share one root, which
// outlives each stage: the run's end deletes it (§ 22,
// TestManagerCleanupOpenCodeRunRoot).
func TestOpenCodeStageRunsInItsOwnRunRoot(t *testing.T) {
	home := isolateOpenCodeHome(t)
	operatorXDG := filepath.Join(home, "operator-xdg")
	t.Setenv("XDG_CONFIG_HOME", operatorXDG)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "operator-data"))
	writeOpenCodeMachineConfig(t, openCodeMachineConfig) // the machine tier moved with XDG_CONFIG_HOME
	fake := installOpenCodeFake(t, "")
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	runs := filepath.Join(home, ".nightgauge", "opencode", "runs")
	workspace := openCodeWorkspace(t)

	run := func(runtime *state.RuntimeState) map[string]string {
		t.Helper()
		var err error
		captureStderr(t, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", runtime))
		})
		if err != nil {
			t.Fatalf("RunStage: %v", err)
		}
		env, entries := fake.env(t)
		for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "GH_CONFIG_DIR", "NIGHTGAUGE_CONFIG_HOME"} {
			n := 0
			for _, kv := range entries {
				if strings.HasPrefix(kv, k+"=") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s appears %d times in the child's environment, want exactly 1", k, n)
			}
		}
		return env
	}

	env := run(nil)
	rootDir := filepath.Dir(env["XDG_DATA_HOME"])
	if filepath.Dir(rootDir) != runs || !runstate.IsIdentity(filepath.Base(rootDir)) {
		t.Fatalf("XDG_DATA_HOME = %q, want <home>/.nightgauge/opencode/runs/<run id>/data", env["XDG_DATA_HOME"])
	}
	for xdg, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state"} {
		if want := filepath.Join(rootDir, dir); env[xdg] != want {
			t.Errorf("%s = %q, want %q", xdg, env[xdg], want)
		}
	}
	listing, _ := os.ReadFile(filepath.Join(fake.dir, "dirs.txt"))
	if lines := strings.Split(strings.TrimSpace(string(listing)), "\n"); len(lines) != 4 {
		t.Errorf("the child did not find its four XDG directories:\n%s", listing)
	} else {
		for _, line := range lines {
			if !strings.HasPrefix(line, "drwx------") {
				t.Errorf("an XDG directory is not a 0700 directory: %s", line)
			}
		}
	}
	if _, ok := env[adapters.RunIDEnvVar]; ok {
		t.Errorf("a dispatch with no run identity exported %s", adapters.RunIDEnvVar)
	}
	for k, want := range map[string]string{
		"GH_CONFIG_DIR":                       filepath.Join(operatorXDG, "gh"),
		"NIGHTGAUGE_CONFIG_HOME":              filepath.Join(operatorXDG, "nightgauge"),
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS":    "1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if env["GOCACHE"] == "" || strings.HasPrefix(env["GOCACHE"], rootDir) {
		t.Errorf("GOCACHE = %q; want the operator's build cache, outside the run root", env["GOCACHE"])
	}
	if _, ok := env["OPENCODE_DISABLE_CLAUDE_CODE"]; ok {
		t.Error("the blanket OPENCODE_DISABLE_CLAUDE_CODE is set")
	}
	if left, _ := os.ReadDir(runs); len(left) != 0 {
		t.Errorf("the root of a dispatch with no run identity survived RunStage: %d entries in %s", len(left), runs)
	}

	const runID = "01890a5d-ac96-774b-bcce-b302099a8057"
	runtime := state.NewRuntimeState("nightgauge/nightgauge", 1612, "item-1612", runID)
	first := run(runtime)
	second := run(runtime)
	want := filepath.Join(runs, runID, "data")
	if first["XDG_DATA_HOME"] != want || second["XDG_DATA_HOME"] != want {
		t.Errorf("the stages of run %s ran in %q and %q, want both in %q", runID, first["XDG_DATA_HOME"], second["XDG_DATA_HOME"], want)
	}
	if first[adapters.RunIDEnvVar] != runID {
		t.Errorf("%s = %q, want the run's identity", adapters.RunIDEnvVar, first[adapters.RunIDEnvVar])
	}
	if _, err := os.Stat(filepath.Join(runs, runID)); err != nil {
		t.Fatalf("an identified run's root did not outlive its stage: %v", err)
	}
}

// TestOpenCodeZeroContextLimitRefusesBeforeSpawn: LM Studio reports a context
// limit of 0, and OpenCode never compacts a session whose limit is 0, so a
// dispatch to an endpoint whose machine-tier limit.context is 0 is refused
// with an error naming the key, before the CLI is spawned and before the
// run's root is created. With the limit set, the same dispatch spawns once,
// the child's OPENCODE_CONFIG_CONTENT is the per-run config with the stage's
// steps cap, and the endpoint's base URL is in no variable of its environment,
// only in the private file the config refers to.
func TestOpenCodeZeroContextLimitRefusesBeforeSpawn(t *testing.T) {
	home := isolateOpenCodeHome(t)
	fake := installOpenCodeFake(t, "")
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
	workspace := openCodeWorkspace(t)
	const runID = "01890a5d-ac96-774b-bcce-b30209a81625"
	root := filepath.Join(home, ".nightgauge", "opencode", "runs", runID)
	dispatch := func() error {
		var err error
		captureStderr(t, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", state.NewRuntimeState("nightgauge/nightgauge", 1625, "item-1625", runID))
			opts.MaxTurns = 40
			_, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
		})
		return err
	}

	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "context: 131072", "context: 0", 1))
	err := dispatch()
	if err == nil {
		t.Fatal("RunStage dispatched to an endpoint whose context limit is 0")
	}
	for _, want := range []string{"dispatch refused", "opencode.limit.context", "never"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "127.0.0.1:1234") {
		t.Errorf("the refusal quotes the endpoint's base URL: %v", err)
	}
	if n := fake.invocations(t); n != 0 {
		t.Errorf("the opencode binary ran %d time(s) for a refused config; want 0", n)
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Errorf("a refused config created the run's root %s", root)
	}

	writeOpenCodeMachineConfig(t, openCodeMachineConfig)
	if err := dispatch(); err != nil {
		t.Fatalf("RunStage refused the reference endpoint: %v", err)
	}
	if n := fake.invocations(t); n != 1 {
		t.Fatalf("the opencode binary ran %d time(s); want 1", n)
	}
	env, entries := fake.env(t)
	var cfg struct {
		EnabledProviders []string `json:"enabled_providers"`
		Agent            map[string]struct {
			Steps int `json:"steps"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatalf("the child's OPENCODE_CONFIG_CONTENT is not the per-run config: %v", err)
	}
	if len(cfg.EnabledProviders) != 1 || cfg.EnabledProviders[0] != "lmstudio" || cfg.Agent["build"].Steps != 40 {
		t.Errorf("the child's config does not pin the provider and the steps cap: %+v", cfg)
	}
	for _, kv := range entries {
		if strings.Contains(kv, "127.0.0.1:1234") {
			k, _, _ := strings.Cut(kv, "=")
			t.Errorf("%s carries the endpoint's base URL into the child's environment", k)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "nightgauge", "lmstudio.base-url")); err != nil || string(got) != "http://127.0.0.1:1234/v1" {
		t.Errorf("the endpoint's base URL file holds %q (%v)", got, err)
	}
}

// redactionStreamer records everything the manager streams.
type redactionStreamer struct {
	mu  sync.Mutex
	out strings.Builder
}

func (r *redactionStreamer) OnOutput(_ string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.Write(data)
}

func (r *redactionStreamer) OnComplete(adapters.RunResult) {}

// TestOpenCodeCapturedOutputIsRedacted (ADR-022 § 22): a tool that prints its
// environment must not put the secrets Nightgauge handed the child in a log.
// The fake prints the server password, the forge tokens and the dispatched
// provider's API key on stdout, inside a JSON event, and on stderr. None of
// the values may reach the streamed output or the result; each becomes
// [REDACTED:<name>], the JSON event stays valid, and an ordinary line is kept.
// The dispatched provider's key is redacted whichever provider it is: openai,
// and deepseek, which no hand-written list of providers named.
func TestOpenCodeCapturedOutputIsRedacted(t *testing.T) {
	for model, keyVar := range map[string]string{
		"openai/gpt-5.5":         "OPENAI_API_KEY",
		"deepseek/deepseek-chat": "DEEPSEEK_API_KEY",
	} {
		t.Run(keyVar, func(t *testing.T) {
			isolateOpenCodeHome(t)
			fake := installOpenCodeFake(t, `printf '{"type":"text","part":{"text":"pw=%s gh=%s ght=%s gl=%s key=%s%s"}}\n' "$OPENCODE_SERVER_PASSWORD" "$GITHUB_TOKEN" "$GH_TOKEN" "$GITLAB_TOKEN" "$OPENAI_API_KEY" "$DEEPSEEK_API_KEY"
echo "an ordinary line"
echo "ERROR leaked $GITHUB_TOKEN $OPENAI_API_KEY $DEEPSEEK_API_KEY $OPENCODE_SERVER_PASSWORD $GH_TOKEN $GITLAB_TOKEN" >&2`)
			t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")
			t.Setenv("GITHUB_TOKEN", "fake-github-token-for-the-redaction-test-1616")
			t.Setenv("GH_TOKEN", "fake-gh-token-for-the-redaction-test-1616")
			t.Setenv("GITLAB_TOKEN", "fake-gitlab-token-for-the-redaction-test-1616")
			t.Setenv("OPENAI_API_KEY", "sk-proj-fakeOpenAIKeyForTheRedactionTest1616")
			t.Setenv("DEEPSEEK_API_KEY", "sk-fakeDeepSeekKeyForTheRedactionTest1616")

			streamer := &redactionStreamer{}
			opts := openCodeStageOptions(model, nil)
			opts.Streamer = streamer
			var result *adapters.RunResult
			var err error
			captureStderr(t, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				result, err = NewManager(openCodeWorkspace(t), adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
			})
			if err != nil {
				t.Fatal(err)
			}
			env, _ := fake.env(t)
			secrets := map[string]string{}
			for _, name := range []string{"OPENCODE_SERVER_PASSWORD", "GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", keyVar} {
				if env[name] == "" {
					t.Fatalf("%s did not reach the child, so its output proves nothing", name)
				}
				secrets[name] = env[name]
			}
			outputs := map[string]string{"streamed": streamer.out.String(), "result.Stdout": result.Stdout, "result.Stderr": result.Stderr}
			for where, out := range outputs {
				for name, value := range secrets {
					if strings.Contains(out, value) {
						t.Errorf("%s holds the value of %s", where, name)
					}
					if !strings.Contains(out, "[REDACTED:"+name+"]") {
						t.Errorf("%s does not show where %s was redacted:\n%s", where, name, out)
					}
				}
			}
			if !strings.Contains(result.Stdout, "an ordinary line") {
				t.Error("redaction removed an ordinary line")
			}
			if event, _, _ := strings.Cut(result.Stdout, "\n"); !json.Valid([]byte(event)) {
				t.Errorf("the redacted JSON event is not valid JSON: %s", event)
			}
		})
	}
}

// TestComposeStageEnv_AdapterExportReplacesTheInheritedValue: an export
// replaces the host's value of the same name instead of landing beside it,
// which the opencode adapter relies on to re-point XDG_CONFIG_HOME,
// GH_CONFIG_DIR and NIGHTGAUGE_CONFIG_HOME (ADR-022 § 8). A reader that takes
// the first match, rather than the last, would otherwise see the operator's.
func TestComposeStageEnv_AdapterExportReplacesTheInheritedValue(t *testing.T) {
	inherited := []string{"PATH=/usr/bin", "XDG_CONFIG_HOME=/operator/xdg", "GH_CONFIG_DIR=/operator/gh"}
	env := composeStageEnv(inherited, nil, map[string]string{
		"XDG_CONFIG_HOME": "/run/config",
		"GH_CONFIG_DIR":   "/operator/gh",
	}, "", "")
	for k, want := range map[string]string{"XDG_CONFIG_HOME": "/run/config", "GH_CONFIG_DIR": "/operator/gh"} {
		n := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, k+"=") {
				n++
			}
		}
		if v, _ := lookupEnv(env, k); n != 1 || v != want {
			t.Errorf("%s appears %d times with final value %q; want once, %q", k, n, v, want)
		}
	}
}

// chatOnlyAdapter is a stand-in for an adapter without an agentic tool loop.
// No built-in adapter is chat-only since #2128 removed lm-studio and ollama,
// so the truth-gate is exercised through this wrapper.
type chatOnlyAdapter struct{ adapters.SkillRunner }

func (chatOnlyAdapter) Agentic() bool { return false }
