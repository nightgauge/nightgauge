package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
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
// truth-gate: chat-completion-only adapters (ollama/lm-studio bridges) must be
// rejected with remediation before any command is built or spawned.
func TestRunStage_NonAgenticAdapter_RejectedBeforeSpawn(t *testing.T) {
	m := NewManager(t.TempDir(), adapters.NewOllamaAdapter())

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
		env := composeStageEnv(inherited, map[string]string{"NIGHTGAUGE_STAGE": "issue-refine"}, "", "")

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
		env := composeStageEnv(inherited, adapterEnv, "", inner)

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
type sigtermTrapAdapter struct{}

func (sigtermTrapAdapter) Name() string { return "sigterm-trap-fake" }

func (sigtermTrapAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "sh", []string{"-c", `trap "echo received SIGTERM >&2; exit 0" TERM; sleep 30`}, nil
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

	m := NewManager(root, sigtermTrapAdapter{})
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
	// the shell itself needs a moment to actually reach the `trap` builtin.
	// A SIGTERM that lands before then hits sh's default (uncaught)
	// disposition and kills it by signal instead of running the trap, which
	// would surface as ExitCode == -1, not the graceful ExitCode == 0 this
	// test exists to pin. Same startup-race guard as
	// TestCancelWithGrace_ForceKill_WhenProcessIgnoresSIGTERM's sleep.
	time.Sleep(50 * time.Millisecond)

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
