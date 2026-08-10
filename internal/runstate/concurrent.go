package runstate

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// ConcurrentRunRefusedError is returned when a fresh run is attempted but an
// existing record is in `running` state with a live writer PID. The
// autonomous orchestrator never bypasses this; user-driven CLI flows can
// pass --force-concurrent to MarkRunning.
type ConcurrentRunRefusedError struct {
	IssueNumber int
	HolderPID   int
	HostID      string
}

func (e *ConcurrentRunRefusedError) Error() string {
	return fmt.Sprintf("concurrent run refused for issue #%d (holder pid=%d host=%s)",
		e.IssueNumber, e.HolderPID, e.HostID)
}

// DetectConcurrent inspects the on-disk run-state and reports whether a
// concurrent run is in progress.
//
// Returns:
//   - (false, nil) when there is no run-state, or when the existing state is
//     not `running`, or when the writer PID is no longer alive.
//   - (true, *err)  when a `running` record's PID is still alive.
//
// The boolean is decoupled from the error so callers that only want a quick
// "is anyone holding this issue" check can ignore the error.
func DetectConcurrent(baseDir string) (bool, *ConcurrentRunRefusedError) {
	rs, err := Load(baseDir)
	if err != nil || rs == nil {
		return false, nil
	}
	if rs.State != StateRunning {
		return false, nil
	}
	last := lastAttempt(rs)
	if last == nil || last.PID == nil {
		return false, nil
	}
	if !ProcessAlive(*last.PID) {
		return false, nil
	}
	return true, &ConcurrentRunRefusedError{
		IssueNumber: rs.IssueNumber,
		HolderPID:   *last.PID,
		HostID:      strOrEmpty(last.HostID),
	}
}

// LivenessWindow is how long a run's last observed heartbeat vouches for it
// when no live process does — the tree's ONE "a run this quiet is not running"
// threshold (ADR-017 7.2 arms 1 and 4).
//
// 30 minutes, derived rather than picked: the timestamp lease is a coarse
// backstop for a lost terminal event, not the primary mechanism. It can only
// decide anything for a run that lost its terminal notification, crossed a
// 30-minute stage-boundary gap without persisting, and has no live process — so
// the window has to be longer than the longest silent stretch a healthy run can
// have, and shorter than an operator's patience with leaked state.
//
// It lives HERE, beside ProcessAlive, because the two are the same question
// asked of different evidence, and because it now has more than one reader: the
// IPC orphan reconciler's ladder (internal/ipc) and the snapshot-derived
// in-flight set the CLI worktree sweep uses (internal/state). Two copies of this
// number are two answers to "is that run still there?" waiting to disagree —
// the exact failure ProcessAlive was exported to end (#341).
const LivenessWindow = 30 * time.Minute

// ProcessAlive reports whether `pid` is a live process on the current host.
// Exported as the ONE liveness probe (#341): `autonomous status` carried an
// inline copy of this body, and two probes are two answers to "is the writer
// still there?" waiting to disagree.
// We use signal 0 (no actual signal delivered) on POSIX — a successful
// kill(pid, 0) means the process exists. On Windows, syscall.Kill is not
// available; we fall back to FindProcess which returns a non-nil result for
// any pid on Windows but we wrap with a Signal call that does fail. The
// codepath is best-effort across all platforms.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On POSIX, signal 0 is a liveness probe. Kill() returns nil when the
	// process exists (regardless of permissions to actually signal it).
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}
