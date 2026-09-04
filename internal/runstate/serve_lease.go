package runstate

// The serve claim becomes a lease (#1349).
//
// Two autonomous schedulers could run against one workspace. `nightgauge serve`
// attaches one in-process; `nightgauge autonomous run` constructs a second,
// independent one — and nothing checked whether a daemon was already live. The
// per-process `activeRuntimes` and `PerRepoMax` limits cannot see across a
// process boundary, so both schedulers read the same board and dispatch the
// same issues, each convinced it is within its concurrency budget.
//
// The PID sidecar beside this file already recorded which process serves which
// workspace, but it was never CONSULTED as a lease: ClaimServeSidecar's
// collision rule is deliberately last-writer-wins, because its job is orphan
// attribution for `doctor` (#388) and refusing to write a bookkeeping file
// would be a worse trade than an inaccurate one. A lease is the opposite
// contract — the whole point is to refuse — so it is a separate mechanism
// rather than a change of heart in that one.
//
// WHY A SEPARATE LOCK FILE. The sidecar is written through write-temp → fsync
// → rename, which replaces the inode on every heartbeat. An advisory lock is
// held on an INODE, so a flock on the sidecar would be silently released by the
// holder's own next heartbeat — a lock that reports success and protects
// nothing, which is worse than no lock at all. The lease therefore flocks a
// `.lock` file that is created once and never renamed, alongside the `.json`
// the sidecar keeps.
//
// WHY FLOCK IS THE AUTHORITY. The kernel releases an advisory lock when the
// holding process dies, however it dies — SIGKILL, panic, power loss on the
// next boot. That makes "is the lock held?" a direct observation of liveness,
// with none of the races a PID check has: a PID can be recycled between the
// read and the decision, and a heartbeat can be stale on a healthy daemon whose
// laptop was asleep. The PID and heartbeat are still recorded, but as the
// REPORT (who holds it, and does it look healthy?), not as the decision.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
)

// ServeLeaseStaleAfter is how long a lease may go without a heartbeat before
// it is described as stale.
//
// Twice the heartbeat interval, per #1349: one missed tick is a slow disk or a
// suspended laptop and must not condemn a healthy daemon, while two in a row
// is a daemon that has stopped doing the one thing it does unconditionally.
//
// This is a DESCRIPTION, not a licence to take the lease. A stale heartbeat on
// a process that still holds the flock means a wedged daemon, and stealing a
// lock from a live process produces exactly the two-scheduler state this file
// exists to prevent. Staleness is what `doctor` reports and what the refusal
// message says; it is never what makes a takeover legal.
var ServeLeaseStaleAfter = 2 * ServeHeartbeatInterval

// ErrServeLeaseHeld reports that a live process already holds this workspace's
// lease. Callers unwrap it for the holder's identity.
var ErrServeLeaseHeld = errors.New("the serve lease is held")

// ServeLeaseHolder describes whoever holds a lease we could not take.
type ServeLeaseHolder struct {
	PID             int       `json:"pid"`
	WorkspaceRoot   string    `json:"workspace_root"`
	StartedAt       time.Time `json:"started_at"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	// Stale reports a heartbeat older than ServeLeaseStaleAfter. The holder is
	// still live — it holds the lock — so this means "wedged", not "gone", and
	// the operator's repair is to stop it rather than to wait it out.
	Stale bool `json:"stale"`
	// Known is false when the lock was held but the sidecar could not be read,
	// so the PID and times above are meaningless. A refusal that cannot name
	// the holder must say so rather than print a zero PID.
	Known bool `json:"known"`
}

// Describe renders the holder for a refusal message.
func (h ServeLeaseHolder) Describe() string {
	if !h.Known {
		return "another process (its claim record could not be read)"
	}
	s := fmt.Sprintf("pid %d serving %s", h.PID, h.WorkspaceRoot)
	if h.Stale {
		s += fmt.Sprintf(" (no heartbeat for %s — it looks wedged; stop it rather than waiting)",
			time.Since(h.LastHeartbeatAt).Round(time.Minute))
	}
	return s
}

// ServeLeaseError is returned when the lease is already held.
type ServeLeaseError struct {
	Holder ServeLeaseHolder
}

func (e *ServeLeaseError) Error() string {
	return fmt.Sprintf("a scheduler is already running for this workspace: %s", e.Holder.Describe())
}

func (e *ServeLeaseError) Unwrap() error { return ErrServeLeaseHeld }

// ServeLease is a held lease. Release exactly once, on every exit path.
type ServeLease struct {
	f    *os.File
	path string
}

// ServeLeasePath is the lock file for a workspace: the sidecar's name with a
// .lock extension, in the same machine-global directory.
//
// That name is reversible (#1426), which matters far more here than it does
// for the record beside it. A lock file has no contents at all, so its name is
// the ONLY thing that can say which workspace a lock left behind by a killed
// daemon belongs to; under the truncated sha256 this replaced, an orphan named
// a workspace nothing on the machine could recover. See
// ServeRegistryWorkspaceRoot.
func ServeLeasePath(workspaceRoot string) (string, error) {
	dir, err := ServeSidecarDir()
	if err != nil {
		return "", err
	}
	name := strings.TrimSuffix(ServeSidecarName(workspaceRoot), serveRecordSuffix) + serveLockSuffix
	return filepath.Join(dir, name), nil
}

// AcquireServeLease takes this workspace's scheduler lease.
//
// Returns a *ServeLeaseError (wrapping ErrServeLeaseHeld) when a live process
// already holds it. Every other error is a malfunction of the locking itself,
// and is reported rather than swallowed: a lease that fails open is not a
// lease, and the failure it fails open into is the one this issue is about.
//
// On a platform with no advisory lock the decision falls back to the sidecar's
// PID and heartbeat. That is weaker — it races with PID recycling — but it is
// strictly better than treating "cannot lock" as "nobody is running", which is
// the reading that produces two schedulers.
func AcquireServeLease(workspaceRoot string) (*ServeLease, error) {
	return acquireServeLease(workspaceRoot, time.Now())
}

func acquireServeLease(workspaceRoot string, now time.Time) (*ServeLease, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("serve lease: no workspace root")
	}
	path, err := ServeLeasePath(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("serve lease: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("serve lease: create claim directory: %w", err)
	}

	// The open and the flock below are ONE operation, and the registry guard
	// is what makes them one (#1426).
	//
	// PruneServeRegistry unlinks lock files nobody holds. Between the two
	// statements below this process holds an open descriptor on a lock file it
	// has not locked yet — indistinguishable, to the sweep, from a lock file
	// nobody holds — so an unlink landing there leaves this process holding a
	// lease on an inode that is no longer at `path`, while the next acquirer
	// creates a fresh file there and locks that. Two holders of one
	// workspace's lease, which is the state this whole file exists to prevent.
	// The guard is released the moment the lease is taken; see
	// lockServeRegistry for the interleaving and for why serialising is the
	// fix rather than retrying.
	switch guard, guardErr := lockServeRegistry(); {
	case guardErr == nil:
		defer guard()
	case errors.Is(guardErr, flock.ErrUnsupported):
		// removeUnheldServeLock needs the same advisory lock this guard does,
		// so on a platform without one nothing ever unlinks a lock file and
		// there is no window to close.
	default:
		// A lease that fails open is not a lease, and this is the locking
		// itself malfunctioning rather than a lease being held.
		return nil, fmt.Errorf("serve lease: registry guard: %w", guardErr)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("serve lease: open %s: %w", path, err)
	}

	// Zero timeout: one try. The caller's job is to REPORT the holder and
	// stop, not to queue behind a daemon that may run for days. Safe as one
	// try only because the guard above excludes the sweep: without it a
	// momentary sweep of another workspace's lock reads as a held lease here,
	// and one try means that refusal is permanent.
	lockErr := flock.Exclusive(f, 0)
	switch {
	case lockErr == nil:
		return &ServeLease{f: f, path: path}, nil

	case errors.Is(lockErr, flock.ErrWouldBlock):
		f.Close()
		return nil, &ServeLeaseError{Holder: readServeLeaseHolder(workspaceRoot, now)}

	case errors.Is(lockErr, flock.ErrUnsupported):
		if holder, held := serveLeaseHeldByPID(workspaceRoot, now); held {
			f.Close()
			return nil, &ServeLeaseError{Holder: holder}
		}
		// No lock on this platform, and nothing live in the sidecar. The file
		// handle is still returned so Release has something to close and the
		// caller's shape does not branch per platform.
		return &ServeLease{f: f, path: path}, nil

	default:
		f.Close()
		return nil, fmt.Errorf("serve lease: lock %s: %w", path, lockErr)
	}
}

// Release drops the lease. Safe to call more than once.
func (l *ServeLease) Release() {
	if l == nil || l.f == nil {
		return
	}
	// Unlock before close so the release is explicit rather than a side effect
	// of the descriptor going away — an fd leaked into a child process would
	// otherwise hold the lease after this returns.
	_ = flock.Unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}

// readServeLeaseHolder describes the process holding the lock, from the
// sidecar written beside it.
//
// The lock is the authority on WHETHER someone holds the lease; the sidecar is
// only the authority on WHO. An unreadable sidecar therefore downgrades the
// message ("another process") and never the verdict — a refusal that becomes
// an approval because a metadata file was missing is the failure this whole
// mechanism exists to prevent.
func readServeLeaseHolder(workspaceRoot string, now time.Time) ServeLeaseHolder {
	sc, ok := ReadServeSidecar(workspaceRoot)
	if !ok {
		return ServeLeaseHolder{}
	}
	return ServeLeaseHolder{
		PID:             sc.PID,
		WorkspaceRoot:   firstNonEmpty(sc.WorkspaceRoot, normalizeWorkspaceRoot(workspaceRoot)),
		StartedAt:       sc.StartedAt,
		LastHeartbeatAt: sc.LastHeartbeatAt,
		Stale:           serveHeartbeatStale(sc, now),
		Known:           true,
	}
}

// serveLeaseHeldByPID is the no-flock fallback: a claim counts as held only
// when its process is alive AND its heartbeat is fresh.
//
// Both conditions, not either. A live PID with a dead heartbeat is a recycled
// PID or a wedged daemon, and on a platform with no lock there is no way to
// tell those apart — but a fresh heartbeat cannot be produced by anything
// except a running heartbeat goroutine, so requiring it keeps a recycled PID
// from locking a workspace out of serving forever.
func serveLeaseHeldByPID(workspaceRoot string, now time.Time) (ServeLeaseHolder, bool) {
	sc, ok := ReadServeSidecar(workspaceRoot)
	if !ok || sc.PID <= 0 || sc.PID == os.Getpid() {
		return ServeLeaseHolder{}, false
	}
	if !ProcessAlive(sc.PID) || serveHeartbeatStale(sc, now) {
		return ServeLeaseHolder{}, false
	}
	return ServeLeaseHolder{
		PID:             sc.PID,
		WorkspaceRoot:   firstNonEmpty(sc.WorkspaceRoot, normalizeWorkspaceRoot(workspaceRoot)),
		StartedAt:       sc.StartedAt,
		LastHeartbeatAt: sc.LastHeartbeatAt,
		Known:           true,
	}, true
}

// serveHeartbeatStale reports a heartbeat older than ServeLeaseStaleAfter,
// falling back to StartedAt for a claim that has not ticked yet.
//
// A zero StartedAt as well means the record predates both fields or was
// half-written; that is not evidence of staleness, so it reads as fresh. The
// bias is deliberate: calling an unreadable record stale is how a healthy
// daemon gets reported as wedged.
func serveHeartbeatStale(sc ServeSidecar, now time.Time) bool {
	last := sc.LastHeartbeatAt
	if last.IsZero() {
		last = sc.StartedAt
	}
	if last.IsZero() {
		return false
	}
	return now.Sub(last) > ServeLeaseStaleAfter
}

// InspectServeLease reports who holds this workspace's lease without taking it
// — the read `autonomous status` and `doctor` need.
//
// It answers by trying to take the lock and immediately releasing it, which is
// the only way to ask the kernel the question. Taking it for microseconds is
// safe: a real holder makes the attempt fail, and a would-be holder starting in
// that instant simply retries against a lock this call has already dropped.
func InspectServeLease(workspaceRoot string) (ServeLeaseHolder, bool) {
	return inspectServeLease(workspaceRoot, time.Now())
}

func inspectServeLease(workspaceRoot string, now time.Time) (ServeLeaseHolder, bool) {
	lease, err := acquireServeLease(workspaceRoot, now)
	if err == nil {
		lease.Release()
		return ServeLeaseHolder{}, false
	}
	var held *ServeLeaseError
	if errors.As(err, &held) {
		return held.Holder, true
	}
	// A malfunction is not "nobody holds it" — but it is also not a holder we
	// can name, and this is a read-only report. Say nothing rather than
	// inventing either answer.
	return ServeLeaseHolder{}, false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
