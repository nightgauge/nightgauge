package runstate

import (
	"fmt"
	"os"
	"syscall"
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
	if !processAlive(*last.PID) {
		return false, nil
	}
	return true, &ConcurrentRunRefusedError{
		IssueNumber: rs.IssueNumber,
		HolderPID:   *last.PID,
		HostID:      strOrEmpty(last.HostID),
	}
}

// processAlive reports whether `pid` is a live process on the current host.
// We use signal 0 (no actual signal delivered) on POSIX — a successful
// kill(pid, 0) means the process exists. On Windows, syscall.Kill is not
// available; we fall back to FindProcess which returns a non-nil result for
// any pid on Windows but we wrap with a Signal call that does fail. The
// codepath is best-effort across all platforms.
func processAlive(pid int) bool {
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
