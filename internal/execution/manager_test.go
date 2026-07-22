package execution

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
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
