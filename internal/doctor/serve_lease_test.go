package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// A workspace with no daemon is a clean, informative result — not silence.
// "Nothing is serving this workspace" is the answer to "why is nothing
// running?", and it was previously unavailable from any command.
func TestServeLeaseFreeIsReported(t *testing.T) {
	isolateMachineState(t)
	item, warning := checkServeLease(t.TempDir(), time.Now())
	if !item.OK || warning != "" {
		t.Fatalf("a free lease produced %+v / %q, want a clean result", item, warning)
	}
	if !strings.Contains(item.Detail, "free") {
		t.Errorf("Detail = %q, want it to say the lease is free", item.Detail)
	}
}

func TestServeLeaseNoWorkspaceRoot(t *testing.T) {
	item, warning := checkServeLease("", time.Now())
	if !item.OK || warning != "" {
		t.Errorf("checkServeLease(\"\") = %+v / %q, want a clean skip", item, warning)
	}
}

// A healthy holder is not a finding. An arm that warns whenever a daemon is
// running is an arm operators stop reading.
func TestServeLeaseHealthyHolderIsNotAFinding(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()

	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID: 4242, StartedAt: now.Add(-time.Hour), LastHeartbeatAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	item, warning := checkServeLease(root, now)
	if !item.OK || warning != "" {
		t.Fatalf("a healthy holder produced %+v / %q, want a clean result", item, warning)
	}
	if !strings.Contains(item.Detail, "4242") {
		t.Errorf("Detail = %q, want the holding PID named", item.Detail)
	}
}

// The failure mode the lease itself introduced: a wedged holder blocks every
// start while looking, from outside, exactly like a healthy daemon. The
// refusal message only reaches whoever tries to start something; an operator
// whose queue merely stopped moving needs this arm to see it at all.
func TestServeLeaseWedgedHolderIsAFinding(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()

	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID:             4242,
		StartedAt:       now.Add(-5 * time.Hour),
		LastHeartbeatAt: now.Add(-runstate.ServeLeaseStaleAfter - time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	item, warning := checkServeLease(root, now)
	if item.OK {
		t.Fatalf("a wedged holder produced OK: %+v", item)
	}
	if !strings.Contains(warning, "serve-lease-wedged") {
		t.Errorf("warning = %q, want the finding id", warning)
	}
	if !strings.Contains(warning, "4242") {
		t.Errorf("warning = %q, want the PID an operator has to stop", warning)
	}
	if !strings.Contains(warning, "Stop pid") {
		t.Errorf("warning = %q, want the repair stated — the lease cannot be reclaimed any other way", warning)
	}
}

// An unreadable claim record means we cannot name the holder. It must not be
// reported as a malfunction of the daemon it failed to describe.
func TestServeLeaseUnknownHolderIsNotAFinding(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	isolateMachineState(t)
	root := t.TempDir()

	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	// No sidecar written.

	item, warning := checkServeLease(root, time.Now())
	if !item.OK || warning != "" {
		t.Fatalf("an unreadable claim produced %+v / %q, want a clean (if vague) result", item, warning)
	}
	if !strings.Contains(item.Detail, "could not be read") {
		t.Errorf("Detail = %q, want it to admit the record is unreadable", item.Detail)
	}
}
