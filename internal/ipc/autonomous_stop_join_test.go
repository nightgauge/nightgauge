package ipc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

type fileStamp struct {
	size  int64
	mtime time.Time
}

func snapshotTree(t *testing.T, dir string) map[string]fileStamp {
	t.Helper()
	out := map[string]fileStamp{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out[p] = fileStamp{info.Size(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched pins #2891: a resume
// spawned the loop with `go Run`, so an immediate Stop() found running=false,
// did nothing, and the loop came up afterwards and wrote state.json while
// t.TempDir was removing the checkout.
func TestResumedLoopStoppedAndWaitedLeavesCheckoutUntouched(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())
	if err := server.resumeAndEnsureRunning(ctx); err != nil {
		t.Fatalf("resume: %v", err)
	}
	as.Stop()
	if !as.WaitStopped(5 * time.Second) {
		t.Fatal("WaitStopped timed out")
	}
	if as.IsRunning() {
		t.Fatal("loop running after Stop+WaitStopped")
	}
	dir, err := layout.CheckoutDir(server.workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, dir)
	time.Sleep(100 * time.Millisecond)
	if as.IsRunning() {
		t.Fatal("a loop came up after Stop+WaitStopped returned")
	}
	after := snapshotTree(t, dir)
	for p, b := range before {
		if a, ok := after[p]; !ok || a != b {
			t.Errorf("checkout changed after the loop stopped: %s", p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("checkout gained %s after the loop stopped", p)
		}
	}
}

// stopAndJoinLoop stops a loop a test started, joins its goroutine and drains
// its background spawns, so no write lands after t.TempDir cleanup (#2891).
func stopAndJoinLoop(t *testing.T, as *orchestrator.AutonomousScheduler, timeout time.Duration) {
	t.Helper()
	as.Stop()
	if !as.WaitStopped(timeout) {
		t.Errorf("the autonomous dispatch loop is still running after Stop()")
	}
	deadline := time.Now().Add(timeout)
	for as.BackgroundInFlight() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := as.BackgroundInFlight(); n > 0 {
		t.Errorf("%d background goroutines still in flight after the loop stopped", n)
	}
}
