package pidtest

import (
	"os"
	"syscall"
	"testing"
)

// TestReaped_ReturnsAPidTheKernelCallsFree is the floor: the fixture must
// actually deliver what it promises, or every consumer is asserting against a
// pid that is quietly still occupied.
func TestReaped_ReturnsAPidTheKernelCallsFree(t *testing.T) {
	pid := Reaped(t, func(int) bool { return false })
	if pid <= 0 {
		t.Fatalf("Reaped returned pid %d, want a real (positive) pid", pid)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("pid %d is still occupied — the fixture handed back a live pid", pid)
	}
}

// TestReaped_FailsWhenThePredicateCallsAReapedPidAlive is the whole point of
// #474, exercised on the helper itself rather than only through its consumers.
//
// The shape it replaces skipped here. A skip prints `ok`, so the regression it
// was built to catch — a dead pid reading alive — would have retired the guard
// silently across three packages. This asserts the helper fails instead, using
// a recording TB so the failure is observable rather than fatal to this test.
func TestReaped_FailsWhenThePredicateCallsAReapedPidAlive(t *testing.T) {
	rec := &recordingTB{TB: t}
	func() {
		// Reaped calls t.Fatalf, which on a real *testing.T would runtime.Goexit.
		// recordingTB records instead, so control returns here normally.
		defer func() { _ = recover() }()
		Reaped(rec, func(int) bool { return true })
	}()

	if rec.skipped {
		t.Error("the fixture SKIPPED when the predicate called a reaped pid alive — " +
			"that is the self-cancelling shape #474 removed: the package would print ok " +
			"while the guard had stopped guarding")
	}
	if !rec.failed {
		t.Error("the fixture neither failed nor skipped when the predicate called a reaped " +
			"pid alive; the arm-3 regression would pass unnoticed")
	}
}

// recordingTB is the minimum testing.TB that records a verdict instead of
// acting on it. Only the methods Reaped can reach are overridden; everything
// else is delegated to the embedded real TB.
type recordingTB struct {
	testing.TB
	failed  bool
	skipped bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(string, ...any) {
	r.failed = true
	panic(errStopRecordingTB)
}

func (r *recordingTB) Skipf(string, ...any) {
	r.skipped = true
	panic(errStopRecordingTB)
}

// errStopRecordingTB unwinds out of Reaped the way Goexit would, without
// killing the test goroutine.
var errStopRecordingTB = os.ErrClosed
