//go:build unix

package flock

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const supported = true

// exclusive polls with LOCK_NB rather than making one blocking LOCK_EX call,
// because a blocking flock is uninterruptible from Go: the syscall would park
// the thread until the holder releases, with no way to bound the wait. The
// bound is what keeps a wedged (as opposed to crashed) holder from stalling
// the caller forever.
func exclusive(f *os.File, timeout time.Duration) error {
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
		// Checked after the first attempt, so a zero timeout is one try rather
		// than none — a caller asking "is it free right now?" must still get
		// the lock when it is.
		if !time.Now().Before(deadline) {
			return ErrWouldBlock
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
