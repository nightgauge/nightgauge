package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func TestRalphLoop_MaxIterations(t *testing.T) {
	cfg := RalphConfig{
		MaxIterations: 2,
		TokenBudget:   100000,
		Timeout:       10 * time.Minute,
	}
	rl := NewRalphLoopController(cfg)
	rl.Start()

	// First iteration should be allowed
	d1 := rl.ShouldRetry()
	if !d1.ShouldRetry {
		t.Error("expected first retry to be allowed")
	}
	rl.RecordIteration(false, 100)

	// Second iteration should be allowed
	d2 := rl.ShouldRetry()
	if !d2.ShouldRetry {
		t.Error("expected second retry to be allowed")
	}
	rl.RecordIteration(false, 100)

	// Third should be blocked
	d3 := rl.ShouldRetry()
	if d3.ShouldRetry {
		t.Error("expected third retry to be blocked by max_iterations")
	}
	if d3.Reason != "max_iterations_reached" {
		t.Errorf("expected reason max_iterations_reached, got %s", d3.Reason)
	}
	if d3.IterationsDone != 2 {
		t.Errorf("expected 2 iterations done, got %d", d3.IterationsDone)
	}
}

func TestRalphLoop_TokenBudget(t *testing.T) {
	cfg := RalphConfig{
		MaxIterations: 10,
		TokenBudget:   500,
		Timeout:       10 * time.Minute,
	}
	rl := NewRalphLoopController(cfg)
	rl.Start()

	rl.RecordIteration(false, 300)
	rl.RecordIteration(false, 250) // total = 550 > 500

	d := rl.ShouldRetry()
	if d.ShouldRetry {
		t.Error("expected retry blocked by token budget")
	}
	if d.Reason != "token_budget_exhausted" {
		t.Errorf("expected token_budget_exhausted, got %s", d.Reason)
	}
}

func TestRalphLoop_Timeout(t *testing.T) {
	cfg := RalphConfig{
		MaxIterations: 10,
		TokenBudget:   100000,
		Timeout:       1 * time.Millisecond, // Very short timeout
	}
	rl := NewRalphLoopController(cfg)
	rl.Start()

	// Wait just enough for the timeout to fire
	time.Sleep(2 * time.Millisecond)

	d := rl.ShouldRetry()
	if d.ShouldRetry {
		t.Error("expected retry blocked by timeout")
	}
	if d.Reason != "timeout_exceeded" {
		t.Errorf("expected timeout_exceeded, got %s", d.Reason)
	}
}

func TestRalphLoop_Success_EndsLoop(t *testing.T) {
	cfg := DefaultRalphConfig()
	rl := NewRalphLoopController(cfg)
	rl.Start()

	rl.RecordIteration(true, 100) // Success!

	d := rl.ShouldRetry()
	if d.ShouldRetry {
		t.Error("expected no retry after success")
	}
	if d.Reason != "ralph_loop_not_active" {
		t.Errorf("expected ralph_loop_not_active, got %s", d.Reason)
	}
}

func TestRalphLoop_NotActive(t *testing.T) {
	rl := NewRalphLoopController(DefaultRalphConfig())
	// Don't call Start()

	d := rl.ShouldRetry()
	if d.ShouldRetry {
		t.Error("expected no retry when not active")
	}
}

func TestRalphLoop_Reset(t *testing.T) {
	rl := NewRalphLoopController(DefaultRalphConfig())
	rl.Start()
	rl.RecordIteration(false, 500)

	if rl.Iterations() != 1 {
		t.Error("expected 1 iteration before reset")
	}

	rl.Reset()

	if rl.Iterations() != 0 {
		t.Error("expected 0 iterations after reset")
	}
	if rl.TokensUsed() != 0 {
		t.Error("expected 0 tokens after reset")
	}
	if rl.IsActive() {
		t.Error("expected not active after reset")
	}
}

// TestRalphLoopController_ConcurrentAccess exercises the mutex protection
// added in Issue #3198. The test must pass under `go test -race`; any
// regression that drops a mutex will be caught by the detector.
func TestRalphLoopController_ConcurrentAccess(t *testing.T) {
	cfg := RalphConfig{
		MaxIterations: 1000,
		TokenBudget:   10000000,
		Timeout:       1 * time.Hour,
	}
	rl := NewRalphLoopController(cfg)
	rl.Start()

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				switch (id + j) % 7 {
				case 0:
					rl.RecordIteration(false, 1)
				case 1:
					_ = rl.ShouldRetry()
				case 2:
					_ = rl.Iterations()
				case 3:
					_ = rl.TokensUsed()
				case 4:
					_ = rl.IsActive()
				case 5:
					if j%50 == 0 {
						rl.Reset()
						rl.Start()
					} else {
						rl.RecordIteration(false, 2)
					}
				case 6:
					if j%75 == 0 {
						rl.Start()
					} else {
						_ = rl.ShouldRetry()
					}
				}
			}
		}(i)
	}
	wg.Wait()

	if rl.Iterations() < 0 {
		t.Errorf("Iterations should not be negative, got %d", rl.Iterations())
	}
	if rl.TokensUsed() < 0 {
		t.Errorf("TokensUsed should not be negative, got %d", rl.TokensUsed())
	}
}

func TestRalphLoop_TokensAccumulate(t *testing.T) {
	rl := NewRalphLoopController(DefaultRalphConfig())
	rl.Start()

	rl.RecordIteration(false, 100)
	rl.RecordIteration(false, 200)

	if rl.TokensUsed() != 300 {
		t.Errorf("expected 300 tokens, got %d", rl.TokensUsed())
	}
}
