// Package flock is the workspace's one advisory whole-file lock.
//
// It was extracted from internal/gitworktree (#1163) when the serve lease
// (#1349) needed the same primitive. Copying it would have created the
// dual-path drift this repo names as a defect class: two implementations of a
// cross-process lock, each fixed independently, each with its own idea of what
// an unsupported platform means — and a locking bug is invisible until the day
// two processes do the thing the lock existed to prevent.
package flock

import (
	"errors"
	"os"
	"time"
)

// ErrUnsupported is returned by Exclusive on platforms with no advisory
// whole-file lock this package knows how to take.
//
// Callers must decide what an unsupported platform means for THEM. The
// worktree guard falls back to its in-process mutex and says nothing, because
// an unsupported platform is a static fact rather than an incident. The serve
// lease falls back to a PID-and-heartbeat liveness test, because "we cannot
// lock" must not silently become "nobody else is running".
var ErrUnsupported = errors.New("advisory file locking unsupported on this platform")

// ErrWouldBlock reports that another process holds the lock right now.
//
// Distinguished from every other failure on purpose: "someone else has it" is
// a normal, expected answer that a caller acts on (report the holder, refuse
// to start), while an EPERM or a bad descriptor is a malfunction. Collapsing
// them makes a broken lock indistinguishable from a busy one.
var ErrWouldBlock = errors.New("the advisory file lock is held by another process")

// Exclusive takes an exclusive advisory lock on f, waiting up to timeout.
//
// A zero timeout is a single try: it returns ErrWouldBlock immediately rather
// than waiting, which is what a caller wants when the point is to REPORT the
// holder rather than to queue behind it.
func Exclusive(f *os.File, timeout time.Duration) error {
	return exclusive(f, timeout)
}

// Unlock releases a lock taken by Exclusive. Closing the file releases it too;
// this is for callers that keep the descriptor.
func Unlock(f *os.File) error { return unlock(f) }

// Supported reports whether this platform has an advisory file lock. Tests
// skip cross-process assertions where it does not.
const Supported = supported
