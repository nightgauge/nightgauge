package orchestrator

import (
	"context"
	"log"
	"time"
)

// This file holds the loop goroutine Start spawns. It lives outside
// autonomous.go because it owns its own lifecycle: loopDone is its join, so
// it is not a goTracked background spawn (#428, #2891).

// Start claims the loop synchronously and runs it on a new goroutine. When
// Start returns nil, IsRunning() is true and Stop() reaches the loop; a Stop
// sent before the loop's first select is held by the buffered stopCh. Join the
// goroutine with WaitStopped. Returns ErrAlreadyRunning when the loop is up.
func (as *AutonomousScheduler) Start(ctx context.Context) error {
	as.mu.Lock()
	if err := as.claimRunLocked(); err != nil {
		as.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	as.loopDone = done
	as.mu.Unlock()
	go func() {
		defer close(done)
		if err := as.runLoop(ctx); err != nil {
			log.Printf("autonomous scheduler exited: %v", err)
		}
	}()
	return nil
}

// WaitStopped waits for the loop goroutine the last Start spawned to return,
// so complete()'s persisted state and exit event are on disk. It returns
// false on timeout, and true at once when Start never ran. It does not cover
// a loop entered by a direct Run call, nor background board work (#94).
func (as *AutonomousScheduler) WaitStopped(timeout time.Duration) bool {
	as.mu.Lock()
	done := as.loopDone
	as.mu.Unlock()
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
