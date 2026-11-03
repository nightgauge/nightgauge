package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func newStartTestScheduler(t *testing.T) (*AutonomousScheduler, string) {
	t.Helper()
	root := layouttest.Repo(t)
	cfg := DefaultAutonomousConfig()
	cfg.MaxConcurrent = 1
	cfg.RefinementEnabled = false
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: root})
	as := NewAutonomousScheduler(sched, nil, nil, nil, cfg, root)
	t.Cleanup(func() {
		as.Stop()
		if !as.WaitStopped(10 * time.Second) {
			t.Errorf("loop still running at cleanup")
		}
		deadline := time.Now().Add(10 * time.Second)
		for as.BackgroundInFlight() > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	return as, root
}

// Start claims the loop before it returns (#2891): a `go Run` left a window in
// which Stop() was a no-op and the loop came up afterwards.
func TestStartClaimsSynchronously(t *testing.T) {
	as, _ := newStartTestScheduler(t)
	if err := as.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !as.IsRunning() {
		t.Fatal("IsRunning() must be true the moment Start returns")
	}
}

func TestStartThenStopIsJoinedByWaitStopped(t *testing.T) {
	as, root := newStartTestScheduler(t)
	if err := as.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	as.Stop()
	if !as.WaitStopped(5 * time.Second) {
		t.Fatal("WaitStopped timed out after Stop")
	}
	if as.IsRunning() {
		t.Fatal("IsRunning() true after WaitStopped")
	}
	dir, err := layout.CheckoutDir(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, layout.CheckoutAutonomous, "state.json"))
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	var st AutonomousState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "stopped" {
		t.Fatalf("persisted status = %q, want stopped", st.Status)
	}
}

func TestStartWhileRunningReturnsErrAlreadyRunning(t *testing.T) {
	as, _ := newStartTestScheduler(t)
	if err := as.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := as.loopDone
	if err := as.Start(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}
	if as.loopDone != first {
		t.Fatal("a rejected Start must not replace the running loop's join channel")
	}
}

func TestWaitStoppedZeroValue(t *testing.T) {
	if !(&AutonomousScheduler{}).WaitStopped(time.Millisecond) {
		t.Fatal("WaitStopped on a never-started scheduler must return true")
	}
}
