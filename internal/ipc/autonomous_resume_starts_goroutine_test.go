// #3303 — autonomous.resume IPC handler must spawn the dispatch goroutine
// when the scheduler isn't already running.
//
// Failure mode this test pins: after the Go backend respawns (e.g. after a
// SIGKILL or a crash recovery), the persisted state is loaded from disk with
// status="safety_tripped" preserved, but no Run() goroutine is alive. The
// status bar's Resume action calls autonomous.resume, which previously only
// flipped state.Status from "safety_tripped" → "running" without starting
// the goroutine — leaving the system in a "running but never dispatching"
// silent dead state. autonomous.start already handled this; autonomous.resume
// did not. This test enforces the symmetry.
package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

func TestAutonomousResume_StartsGoroutineWhenNotRunning(t *testing.T) {
	tmpDir := t.TempDir()

	// Persist state with status="safety_tripped" — the exact shape on disk
	// after a Go backend respawn following a safety trip. NewAutonomousScheduler
	// will load it as-is (terminal states are preserved by loadState).
	autoDir := filepath.Join(tmpDir, ".nightgauge", "autonomous")
	if err := os.MkdirAll(autoDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	statePath := filepath.Join(autoDir, "state.json")
	persisted := orchestrator.AutonomousState{Status: "safety_tripped"}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	// Construct the dependencies. Empty repos slice makes runCycle's
	// depgraph.BuildGraph return an error on the first iteration without
	// touching GitHub — sufficient for exercising the goroutine-startup
	// path. nil adapter mirrors IPC mode (cmd/nightgauge main.go).
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: tmpDir,
		Adapter:       nil,
	})
	cfg := orchestrator.DefaultAutonomousConfig()
	cfg.MaxConcurrent = 1
	as := orchestrator.NewAutonomousScheduler(sched, nil, nil, nil, cfg, tmpDir)

	if got := as.Status().Status; got != "safety_tripped" {
		t.Fatalf("expected loaded status=safety_tripped, got %q", got)
	}
	if as.IsRunning() {
		t.Fatal("scheduler goroutine must not be alive before resume — bug only manifests when goroutine is dormant")
	}

	server := NewServer(nil, WithAutonomousScheduler(as))
	handler, ok := server.methods["autonomous.resume"]
	if !ok {
		t.Fatal("autonomous.resume handler not registered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		// Wait for the spawned goroutine to drain. Without this, the test
		// process leaks the autonomous scheduler goroutine into subsequent
		// tests, producing intermittent failures from shared state.
		deadline := time.Now().Add(2 * time.Second)
		for as.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})

	if _, err := handler(ctx, nil); err != nil {
		t.Fatalf("autonomous.resume returned error: %v", err)
	}

	// The handler waits 50ms internally for status to settle. Allow a
	// little extra slack for the goroutine's first scheduling.
	time.Sleep(100 * time.Millisecond)

	if !as.IsRunning() {
		t.Fatal("autonomous.resume did NOT spawn the dispatch goroutine — silent dead-state regression (#3303)")
	}
	if got := as.Status().Status; got != "running" {
		t.Errorf("expected status=running after resume, got %q", got)
	}
}

func TestAutonomousResume_NoGoroutineLeakWhenAlreadyRunning(t *testing.T) {
	// Symmetry check: if the goroutine is already alive, resume must NOT
	// spawn a second one. Run() refuses re-entry with an error
	// ("autonomous scheduler is already running"); a duplicate go func()
	// call would log that error and waste a goroutine.
	tmpDir := t.TempDir()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: tmpDir,
		Adapter:       nil,
	})
	cfg := orchestrator.DefaultAutonomousConfig()
	cfg.MaxConcurrent = 1
	as := orchestrator.NewAutonomousScheduler(sched, nil, nil, nil, cfg, tmpDir)

	server := NewServer(nil, WithAutonomousScheduler(as))
	startHandler := server.methods["autonomous.start"]
	resumeHandler := server.methods["autonomous.resume"]

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		deadline := time.Now().Add(2 * time.Second)
		for as.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})

	if _, err := startHandler(ctx, nil); err != nil {
		t.Fatalf("autonomous.start failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !as.IsRunning() {
		t.Fatal("autonomous.start did not spawn goroutine")
	}

	// Now call resume — must be a no-op for goroutine count, just transitions state.
	if _, err := resumeHandler(ctx, nil); err != nil {
		t.Fatalf("autonomous.resume failed: %v", err)
	}
	if !as.IsRunning() {
		t.Error("after resume on already-running scheduler, IsRunning still must be true")
	}
}
