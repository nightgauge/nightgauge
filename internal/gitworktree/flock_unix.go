//go:build unix

package gitworktree

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// errFlockUnsupported is returned by flockExclusive on platforms with no
// advisory whole-file lock this package knows how to take. It is never
// returned here.
var errFlockUnsupported = errors.New("advisory file locking unsupported on this platform")

// flockExclusive takes an exclusive advisory lock on f, waiting up to timeout.
//
// It polls with LOCK_NB rather than making one blocking LOCK_EX call, because
// a blocking flock is uninterruptible from Go: the syscall would park the
// thread until the holder releases, with no way to bound the wait. The bound
// is what keeps a wedged (as opposed to crashed) holder from stalling the
// daemon forever — see flockTimeout.
func flockExclusive(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	backoff := 2 * time.Millisecond
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the cross-process worktree lock")
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func funlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// crossProcessLockSupported reports whether this platform has the advisory
// file lock the cross-process half of the guard needs. Tests skip the
// cross-process assertion where it does not.
const crossProcessLockSupported = true
