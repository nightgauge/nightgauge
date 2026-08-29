//go:build !unix

package gitworktree

import (
	"errors"
	"os"
	"time"
)

// errFlockUnsupported marks the platforms where this package has no
// cross-process lock. Callers fall back to the in-process mutex alone, and
// say nothing about it: an unsupported platform is a static fact, not an
// incident worth a WARN on every worktree operation.
var errFlockUnsupported = errors.New("advisory file locking unsupported on this platform")

func flockExclusive(_ *os.File, _ time.Duration) error { return errFlockUnsupported }

func funlock(_ *os.File) error { return nil }

// crossProcessLockSupported reports whether this platform has the advisory
// file lock the cross-process half of the guard needs.
const crossProcessLockSupported = false
