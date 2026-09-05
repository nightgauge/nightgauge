package orchestrator

import (
	"testing"
	"time"
)

// WaitForRunning (#494) is the primitive the autonomous.* IPC handlers use in
// place of a 50ms nap. Its contract is narrow and the handlers depend on all
// of it: it returns as soon as the state matches, it is woken BY the
// transition rather than by a timer, and when it gives up it reports the
// state as it actually is — never the state the caller asked for.
func TestWaitForRunning(t *testing.T) {
	t.Run("already satisfied returns immediately", func(t *testing.T) {
		as := &AutonomousScheduler{}
		began := time.Now()
		if !as.WaitForRunning(false, time.Minute) {
			t.Fatal("want=false on a scheduler that is not running must be satisfied at once")
		}
		if elapsed := time.Since(began); elapsed > 5*time.Second {
			t.Fatalf("waited %s for a condition that already held", elapsed)
		}
	})

	t.Run("woken by the transition, not the deadline", func(t *testing.T) {
		as := &AutonomousScheduler{}
		flipped := make(chan struct{})
		go func() {
			// Give the waiter time to park on the channel. Even if this fires
			// first, the pre-check at the top of WaitForRunning covers it —
			// the assertion below is about the deadline never being reached.
			time.Sleep(20 * time.Millisecond)
			as.mu.Lock()
			as.setRunningLocked(true)
			as.mu.Unlock()
			close(flipped)
		}()
		began := time.Now()
		if !as.WaitForRunning(true, 30*time.Second) {
			t.Fatal("WaitForRunning(true) must observe the transition to running")
		}
		if elapsed := time.Since(began); elapsed > 10*time.Second {
			t.Fatalf("took %s — the wait must be woken by the state change, not poll its deadline", elapsed)
		}
		<-flipped
	})

	t.Run("timeout reports the actual state", func(t *testing.T) {
		as := &AutonomousScheduler{}
		began := time.Now()
		if as.WaitForRunning(true, 50*time.Millisecond) {
			t.Fatal("WaitForRunning must not claim a transition that never happened")
		}
		if elapsed := time.Since(began); elapsed < 50*time.Millisecond {
			t.Fatalf("returned after %s — the deadline must actually be waited out", elapsed)
		}
		if as.IsRunning() {
			t.Fatal("test premise broken: nothing should have started the scheduler")
		}
	})

	t.Run("a transition the waiter missed is reported, not waited out", func(t *testing.T) {
		as := &AutonomousScheduler{}
		// The scheduler starts and exits again in one atomic step, so a
		// waiter parked on the change channel can never see the intermediate
		// "running" it asked for. It must report the state as it now is
		// rather than sit on its deadline waiting for a transition that has
		// already gone past.
		go func() {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				as.mu.Lock()
				if as.runningChangedCh != nil { // the waiter is parked
					as.setRunningLocked(true)
					as.setRunningLocked(false)
					as.mu.Unlock()
					return
				}
				as.mu.Unlock()
				time.Sleep(time.Millisecond)
			}
		}()
		began := time.Now()
		if as.WaitForRunning(true, 30*time.Second) {
			t.Fatal("WaitForRunning(true) must report false when the scheduler is not running")
		}
		if elapsed := time.Since(began); elapsed > 10*time.Second {
			t.Fatalf("took %s — a transition that already completed must not be waited out", elapsed)
		}
	})
}
