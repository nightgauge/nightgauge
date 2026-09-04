package gitworktree

// The cross-process half of the worktree mutation guard (#1163).
//
// The implementation moved to internal/flock in #1349, when the serve lease
// needed the same primitive. This file keeps the package-local names the guard
// reads so the call sites stay legible, and so there is exactly ONE advisory
// lock in the tree: a second copy would be fixed in one place and not the
// other, and a locking bug stays invisible until the day two processes do the
// thing the lock existed to prevent.

import (
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
)

// errFlockUnsupported is returned by flockExclusive on platforms with no
// advisory whole-file lock. Callers fall back to the in-process mutex alone
// and say nothing about it: an unsupported platform is a static fact, not an
// incident worth a WARN on every worktree operation.
var errFlockUnsupported = flock.ErrUnsupported

func flockExclusive(f *os.File, timeout time.Duration) error {
	return flock.Exclusive(f, timeout)
}

func funlock(f *os.File) error { return flock.Unlock(f) }

// crossProcessLockSupported reports whether this platform has the advisory
// file lock the cross-process half of the guard needs. Tests skip the
// cross-process assertion where it does not.
const crossProcessLockSupported = flock.Supported
