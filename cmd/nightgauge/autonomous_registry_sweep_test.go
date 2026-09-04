package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// The `autonomous run` arm of the #1426 registry sweep.
//
// There are two arms. `serve` sweeps from StartServeSidecar, which the runstate
// suite pins; this command sweeps for the workspace shape that arm cannot
// reach — a machine that only ever runs the scheduler directly, which writes a
// lock file per workspace into the same directory and starts no sidecar, so
// nothing else would ever clean up after it. That made it the one arm covering
// its own case and the one arm with no test: deleting the call left the whole
// tree green.
//
// The command is driven for real, not a helper standing in for it. It gets as
// far as the sweep and then stops on the first thing it needs and has not been
// given — a board, which no config above a fresh temp dir supplies — so the
// assertions are about a real invocation of the verb rather than about a
// function the verb might no longer call.
func TestAutonomousRun_SweepsTheClaimRegistryAfterTakingItsLease(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	t.Setenv("HOME", t.TempDir())
	// A directory with no config.yaml above it, so owner resolution fails
	// deterministically and this test never reaches a board, a token, or the
	// scheduler loop. It is also the workspace whose lease the command takes.
	t.Chdir(t.TempDir())
	// The token the client would otherwise resolve from the ambient `gh`
	// account, which is present on a maintainer machine and absent in CI. A
	// non-empty --token short-circuits clientFromConfig with no network call,
	// so the stop below is the same one everywhere.
	prevToken := globalToken
	globalToken = "test-token-not-a-credential"
	t.Cleanup(func() { globalToken = prevToken })

	// A dead record and an orphaned lock: the two shapes the issue measured
	// 143 and 174 of.
	gone := filepath.Join(t.TempDir(), "reclaimed-workspace")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := runstate.WriteServeSidecar(gone, runstate.ServeSidecar{
		PID:             1,
		StartedAt:       time.Now().Add(-48 * time.Hour),
		LastHeartbeatAt: time.Now().Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	recordPath, err := runstate.ServeSidecarPath(gone)
	if err != nil {
		t.Fatalf("ServeSidecarPath: %v", err)
	}
	orphanLock, err := runstate.ServeLeasePath(filepath.Join(t.TempDir(), "another-reclaimed-workspace"))
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	if err := os.WriteFile(orphanLock, nil, 0o644); err != nil {
		t.Fatalf("plant orphan lock: %v", err)
	}

	cmd := autonomousRunCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	runErr := cmd.Execute()
	if runErr == nil || !strings.Contains(runErr.Error(), "is required (or set in config.yaml)") {
		t.Fatalf("expected the run to stop at board resolution, which is the first thing after the sweep; got %v", runErr)
	}

	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Errorf("a record naming a workspace root that no longer exists survived `autonomous run` (stat err = %v)", err)
	}
	if _, err := os.Stat(orphanLock); !os.IsNotExist(err) {
		t.Errorf("a lock file with no record and no holder survived `autonomous run` (stat err = %v)", err)
	}
	if !strings.Contains(stderr.String(), "Pruned the machine-global claim registry") {
		t.Errorf("the sweep removed files and said nothing; stderr = %q", stderr.String())
	}
}

// The sweep runs AFTER the lease, and that ordering is load-bearing: the sweep
// unlinks any lock file it can lock, and the only thing stopping it reclaiming
// the file this process's own lease depends on is that this process is holding
// it. flock is per open-file-description, so the sweep's second descriptor
// contends with the lease exactly as another process's would.
func TestAutonomousRun_DoesNotSweepItsOwnLease(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("AcquireServeLease: %v", err)
	}
	defer lease.Release()
	lockPath, err := runstate.ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}

	pruneServeClaimRegistry(io.Discard)

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("the sweep reclaimed the lock file of the lease this process holds: %v", err)
	}
	if _, err := runstate.AcquireServeLease(root); !errors.Is(err, runstate.ErrServeLeaseHeld) {
		t.Fatalf("after the sweep the lease no longer reads as held; AcquireServeLease err = %v", err)
	}
}
