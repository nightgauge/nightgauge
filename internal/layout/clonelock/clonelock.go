// Package clonelock takes CLONE/.lock, the lock appends to shared per-clone
// JSONL hold for the duration of the append (ADR-024 § 7). Keyed files in
// CLONE are written temp-file-plus-rename and need no lock; an append to a
// log several checkouts share (pipeline history, traces, the GitHub API
// ledger, the exit records) does, because two daemons of one clone (the main
// checkout and a linked worktree) append to the same file.
//
// It is a separate package so internal/layout stays a leaf over the standard
// library.
package clonelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Timeout bounds how long an append waits for the lock. An append holds it
// for one write, so a wait this long means a wedged holder; the append then
// proceeds unlocked rather than losing the record (O_APPEND keeps a single
// write whole on POSIX).
var Timeout = 5 * time.Second

// ForPath takes CLONE/.lock when p lies inside a per-clone root and returns
// its release. Outside a per-clone root, on a platform without advisory
// locks, or when the lock cannot be taken in Timeout, it returns a no-op
// release: the lock orders appends, and a missing lock must never drop one.
func ForPath(p string) (release func()) {
	clone, ok := layout.CloneRootOf(p)
	if !ok {
		return func() {}
	}
	release, err := Acquire(clone)
	if err != nil {
		return func() {}
	}
	return release
}

// Acquire takes clone/.lock exclusively, waiting up to Timeout.
func Acquire(clone string) (release func(), err error) {
	info, err := os.Lstat(clone)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("clonelock: %s is not a directory", clone)
	}
	f, err := os.OpenFile(filepath.Join(clone, layout.CloneLockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("clonelock: open: %w", err)
	}
	switch err := flock.Exclusive(f, Timeout); {
	case err == nil:
	case errors.Is(err, flock.ErrUnsupported):
		return func() { _ = f.Close() }, nil
	default:
		_ = f.Close()
		return nil, fmt.Errorf("clonelock: lock %s: %w", f.Name(), err)
	}
	return func() {
		_ = flock.Unlock(f)
		_ = f.Close()
	}, nil
}
