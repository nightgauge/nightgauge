// Package pidtest supplies the one dead-pid fixture the Go suites share.
//
// # Why this package exists (#474, from #427's review round)
//
// Four copies of "give me a pid that has certainly exited" lived in the tree —
// internal/runstate, internal/ipc (twice) and internal/state — and three of
// them were self-cancelling:
//
//	pid := <a real child, reaped>
//	if ProcessAlive(pid) {
//	    t.Skipf("pid was recycled")   // <-- the bug
//	}
//
// ProcessAlive is the predicate those tests exist to exercise. Gating the
// fixture on it means the exact regression they pin — a dead pid reading alive
// — turns every one of them into a SKIP. `go test ./...` then prints `ok` for
// the package and CI stays green while the guard has silently stopped
// guarding: the same silent-success shape as a test that never ran.
//
// The corrected form (#427's reapedPID, generalised here) probes the kernel
// directly and separates the two cases the old shape conflated:
//
//   - kill(pid, 0) succeeds, or fails with EPERM — a process really does occupy
//     that pid (EPERM means one exists that we may not signal, which is NOT
//     free). The pid was recycled; there is no fixture to build and skipping is
//     honest.
//   - kill(pid, 0) fails with ESRCH — the kernel says the pid is free. If the
//     predicate under test disagrees, that IS the regression. Fail, never skip.
//
// # Why the predicate is a parameter
//
// The callers live in three packages, one of which (internal/runstate) is the
// package that defines ProcessAlive. A helper that imported runstate could not
// be used from runstate's own in-package tests — an import cycle. Passing the
// predicate in also keeps the helper honest about what it is doing: it is
// checking a caller-supplied claim against the kernel, not consulting an
// authority of its own.
package pidtest

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
)

// Reaped returns a pid that has certainly exited, verified against the kernel
// rather than against processAlive — the predicate the caller is testing.
//
// It skips only when the pid was genuinely recycled between the child's exit
// and the probe, which is a real (if rare) race with nothing to assert about.
// It FAILS when the kernel says the pid is free and processAlive calls it
// alive, because that is the regression the callers exist to catch: a run whose
// recorded child pid has died reads as still running, and the reconcile ladder
// pins it forever.
//
// Invented "large number" pids are deliberately not used — the kernel recycles
// pids, so a made-up number is not reliably dead and the fixture would be
// pinning nothing on a busy machine.
func Reaped(t testing.TB, processAlive func(int) bool) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run throwaway child: %v", err)
	}
	pid := cmd.Process.Pid

	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		t.Skipf("pid %d was recycled before the test could use it as a dead pid", pid)
	}
	if processAlive(pid) {
		t.Fatalf("the pid predicate calls reaped pid %d alive (kernel: ESRCH) — "+
			"ladder arm 3 would pin such a run forever. This is the regression this fixture "+
			"exists to catch, so it fails rather than skipping (#474).", pid)
	}
	return pid
}
