//go:build windows

package flock

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const supported = true

func exclusive(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	backoff := 2 * time.Millisecond
	for {
		overlapped := new(windows.Overlapped)
		err := windows.LockFileEx(
			windows.Handle(f.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
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
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
