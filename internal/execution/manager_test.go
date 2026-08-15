package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
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
