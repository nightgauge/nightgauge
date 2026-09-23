package runstate

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
)

func leaseWorkspace(t *testing.T) string {
	t.Helper()
	isolatedHome(t)
	return t.TempDir()
}

func TestServeLeasePathSitsBesideTheSidecar(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	sidecar, err := ServeSidecarPath(root)
	if err != nil {
		t.Fatalf("ServeSidecarPath: %v", err)
	}
	lock, err := ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	if filepath.Dir(lock) != filepath.Dir(sidecar) {
		t.Errorf("lock %s and sidecar %s are in different directories", lock, sidecar)
	}
	if want := strings.TrimSuffix(sidecar, ".json") + ".lock"; lock != want {
		t.Errorf("ServeLeasePath = %s, want %s", lock, want)
	}
	// The lock must NOT be the sidecar. The sidecar is rewritten through
	// temp+rename on every heartbeat, which replaces the inode an advisory
	// lock lives on — a flock there is released by its own holder's next tick.
	if lock == sidecar {
		t.Fatal("the lease locks the sidecar itself; its own heartbeat would release it")
	}
}

// The condition #1349 exists for: a second scheduler must refuse, and must say
// which process it is refusing to duplicate.
func TestSecondAcquireIsRefusedAndNamesTheHolder(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := leaseWorkspace(t)
	now := time.Now()

	// A holder with a live PID and a fresh heartbeat, exactly as a running
	// daemon leaves it.
	if err := WriteServeSidecar(root, ServeSidecar{
		PID: os.Getpid(), StartedAt: now.Add(-time.Hour), LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	first, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	t.Cleanup(first.Release)

	second, err := acquireServeLease(root, now)
	if err == nil {
		second.Release()
		t.Fatal("the second acquire succeeded — two schedulers would dispatch the same board")
	}
	if !errors.Is(err, ErrServeLeaseHeld) {
		t.Fatalf("err = %v, want ErrServeLeaseHeld", err)
	}
	var held *ServeLeaseError
	if !errors.As(err, &held) {
		t.Fatalf("err %v does not unwrap to *ServeLeaseError", err)
	}
	if !held.Holder.Known || held.Holder.PID != os.Getpid() {
		t.Errorf("holder = %+v, want the live PID named", held.Holder)
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error text %q does not say a scheduler is already running", err.Error())
	}
}

// Releasing must actually free the lease, or a restart of the daemon locks
// itself out of its own workspace.
func TestReleaseFreesTheLease(t *testing.T) {
	root := leaseWorkspace(t)
	now := time.Now()

	first, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	first.Release()
	first.Release() // idempotent — every exit path may call it

	second, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	second.Release()
}

// A SIGKILL'd daemon never runs its defer. The kernel drops its advisory lock
// anyway, which is the entire reason the lock is the authority: a stale claim
// file must not lock a workspace out of serving forever.
func TestLeaseIsReclaimableAfterTheHolderDies(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := leaseWorkspace(t)
	now := time.Now()

	// A dead PID with a stale heartbeat — what a killed daemon leaves behind.
	dead := deadPID(t)
	if err := WriteServeSidecar(root, ServeSidecar{
		PID: dead, StartedAt: now.Add(-3 * time.Hour), LastHeartbeatAt: now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	lease, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("a claim from a dead process blocked the lease: %v", err)
	}
	lease.Release()
}

// A wedged holder still holds the lock, so it is not stealable — but the
// refusal must say the holder looks wedged, because the operator's repair is
// to stop it rather than to wait it out.
func TestStaleHeartbeatIsReportedButNotStolen(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := leaseWorkspace(t)
	now := time.Now()
	if err := WriteServeSidecar(root, ServeSidecar{
		PID:             os.Getpid(),
		StartedAt:       now.Add(-5 * time.Hour),
		LastHeartbeatAt: now.Add(-ServeLeaseStaleAfter - time.Minute),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	held, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	t.Cleanup(held.Release)

	_, err = acquireServeLease(root, now)
	var lerr *ServeLeaseError
	if !errors.As(err, &lerr) {
		t.Fatalf("a wedged holder's lease was taken: err = %v — stealing a lock from a live process produces the two-scheduler state this prevents", err)
	}
	if !lerr.Holder.Stale {
		t.Error("holder.Stale = false for a heartbeat older than ServeLeaseStaleAfter")
	}
	if !strings.Contains(lerr.Holder.Describe(), "wedged") {
		t.Errorf("Describe() = %q, want it to name the wedged state and the repair", lerr.Holder.Describe())
	}
}

// The lock is the authority on WHETHER; the sidecar only on WHO. A missing
// sidecar must downgrade the message, never the verdict.
func TestRefusalSurvivesAnUnreadableSidecar(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := leaseWorkspace(t)
	now := time.Now()

	held, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	t.Cleanup(held.Release)
	// No sidecar was ever written.

	_, err = acquireServeLease(root, now)
	var lerr *ServeLeaseError
	if !errors.As(err, &lerr) {
		t.Fatalf("a missing sidecar turned a refusal into an approval: err = %v", err)
	}
	if lerr.Holder.Known {
		t.Errorf("holder = %+v, want Known false when no record could be read", lerr.Holder)
	}
	if strings.Contains(lerr.Holder.Describe(), "pid 0") {
		t.Errorf("Describe() = %q — it prints a meaningless zero PID instead of admitting the record is unreadable",
			lerr.Holder.Describe())
	}
}

func TestInspectServeLeaseReportsWithoutTaking(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := leaseWorkspace(t)
	now := time.Now()

	if holder, held := inspectServeLease(root, now); held {
		t.Fatalf("an unheld lease reported a holder: %+v", holder)
	}

	lease, err := acquireServeLease(root, now)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	holder, held := inspectServeLease(root, now)
	if !held {
		t.Fatal("a held lease reported as free")
	}
	_ = holder

	// Inspecting must not have consumed the lease: the holder still holds it,
	// and a would-be second scheduler must still be refused afterwards.
	if _, err := acquireServeLease(root, now); !errors.Is(err, ErrServeLeaseHeld) {
		t.Fatalf("after an inspect, a second acquire got %v — the read released the holder's lease", err)
	}
	lease.Release()

	if _, held := inspectServeLease(root, now); held {
		t.Error("a released lease still reports a holder")
	}
}

func TestServeHeartbeatStale(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		sc   ServeSidecar
		want bool
	}{
		{"fresh heartbeat", ServeSidecar{LastHeartbeatAt: now.Add(-time.Minute)}, false},
		{"one missed tick is not stale", ServeSidecar{LastHeartbeatAt: now.Add(-ServeHeartbeatInterval - time.Minute)}, false},
		{"two missed ticks is stale", ServeSidecar{LastHeartbeatAt: now.Add(-ServeLeaseStaleAfter - time.Minute)}, true},
		{"falls back to StartedAt before the first tick", ServeSidecar{StartedAt: now.Add(-time.Minute)}, false},
		{"a record with no times reads as fresh", ServeSidecar{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveHeartbeatStale(tc.sc, now); got != tc.want {
				t.Errorf("serveHeartbeatStale = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAcquireServeLeaseRejectsAnEmptyRoot(t *testing.T) {
	if _, err := AcquireServeLease("  "); err == nil {
		t.Fatal("an empty workspace root produced a lease; every caller would share one")
	}
}

// deadPID returns a PID that has certainly exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot spawn a short-lived process: %v", err)
	}
	pid := cmd.Process.Pid
	if ProcessAlive(pid) {
		t.Skipf("pid %d was recycled immediately; skipping rather than asserting on a race", pid)
	}
	return pid
}

// A daemon from the release before #2031 holds its lease in
// ~/.nightgauge/serve. A new acquire must see it as held, or two schedulers
// run one workspace (#1349); and a new holder must hold the legacy lock too,
// so an older daemon started afterwards refuses in turn.
func TestLeaseHonoursAPreviousReleasesLegacyLock(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	home := isolatedHome(t)
	root := t.TempDir()
	legacyDir := filepath.Join(home, ".nightgauge", "serve")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(ServeSidecarName(root), serveRecordSuffix)
	legacyLock := filepath.Join(legacyDir, base+serveLockSuffix)
	rec, _ := json.Marshal(ServeSidecar{PID: 4242, StartedAt: time.Now(), LastHeartbeatAt: time.Now(), WorkspaceRoot: root})
	if err := os.WriteFile(filepath.Join(legacyDir, base+serveRecordSuffix), rec, 0o644); err != nil {
		t.Fatal(err)
	}

	// The old daemon holds its legacy lock.
	old, err := os.OpenFile(legacyLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := flock.Exclusive(old, 0); err != nil {
		t.Fatal(err)
	}
	_, err = AcquireServeLease(root)
	var held *ServeLeaseError
	if !errors.As(err, &held) || held.Holder.PID != 4242 {
		t.Fatalf("AcquireServeLease beside a live legacy holder = %v, want ServeLeaseError naming pid 4242", err)
	}
	// The refusal left the new lease free.
	_ = flock.Unlock(old)
	_ = old.Close()

	lease, err := AcquireServeLease(root)
	if err != nil {
		t.Fatalf("AcquireServeLease after the old daemon exited: %v", err)
	}
	// Now an old daemon starting up finds its legacy lock taken.
	probe, err := os.OpenFile(legacyLock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := flock.Exclusive(probe, 0); !errors.Is(err, flock.ErrWouldBlock) {
		t.Errorf("the new lease does not hold the legacy lock: %v", err)
	}
	lease.Release()
	if err := flock.Exclusive(probe, 0); err != nil {
		t.Errorf("Release did not free the legacy lock: %v", err)
	}
}

// No legacy directory, no legacy lock: nothing is created in ~/.nightgauge.
func TestLeaseCreatesNoLegacyDirectory(t *testing.T) {
	home := isolatedHome(t)
	lease, err := AcquireServeLease(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := os.Lstat(filepath.Join(home, ".nightgauge", "serve")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the lease created the legacy directory: %v", err)
	}
}
