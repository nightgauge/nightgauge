package orchestrator

import (
	"sync"
	"time"
)

// RalphConfig configures the RALPH loop controller.
type RalphConfig struct {
	MaxIterations int           // Max fix attempts (default: 3)
	TokenBudget   int           // Total token budget for all iterations (default: 10000)
	Timeout       time.Duration // Total RALPH loop timeout (default: 5m)
}

// DefaultRalphConfig returns safe default RALPH configuration.
func DefaultRalphConfig() RalphConfig {
	return RalphConfig{
		MaxIterations: 3,
		TokenBudget:   10000,
		Timeout:       5 * time.Minute,
	}
}

// RalphDecision is the result of ShouldRetry.
type RalphDecision struct {
	ShouldRetry    bool
	Reason         string
	IterationsDone int
}

// RalphLoopController manages RALPH loop state and enforces limits.
//
// Safe for concurrent use: parallel-wave subagents share a single Scheduler
// (and therefore a single RalphLoopController), and would otherwise race on
// iterations / tokensUsed / active. See issue #3198.
type RalphLoopController struct {
	mu         sync.Mutex
	config     RalphConfig
	iterations int
	tokensUsed int
	startedAt  time.Time
	active     bool
}

// NewRalphLoopController creates a new RALPH loop controller.
func NewRalphLoopController(cfg RalphConfig) *RalphLoopController {
	return &RalphLoopController{
		config: cfg,
	}
}

// Start begins a new RALPH loop session. Must be called before ShouldRetry.
func (r *RalphLoopController) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.iterations = 0
	r.tokensUsed = 0
	r.startedAt = time.Now()
	r.active = true
}

// ShouldRetry returns whether another RALPH iteration should run.
func (r *RalphLoopController) ShouldRetry() RalphDecision {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return RalphDecision{
			ShouldRetry:    false,
			Reason:         "ralph_loop_not_active",
			IterationsDone: r.iterations,
		}
	}

	// Check iteration limit
	if r.iterations >= r.config.MaxIterations {
		return RalphDecision{
			ShouldRetry:    false,
			Reason:         "max_iterations_reached",
			IterationsDone: r.iterations,
		}
	}

	// Check token budget
	if r.tokensUsed >= r.config.TokenBudget {
		return RalphDecision{
			ShouldRetry:    false,
			Reason:         "token_budget_exhausted",
			IterationsDone: r.iterations,
		}
	}

	// Check timeout
	if time.Since(r.startedAt) >= r.config.Timeout {
		return RalphDecision{
			ShouldRetry:    false,
			Reason:         "timeout_exceeded",
			IterationsDone: r.iterations,
		}
	}

	return RalphDecision{
		ShouldRetry:    true,
		Reason:         "retry_allowed",
		IterationsDone: r.iterations,
	}
}

// RecordIteration records a completed RALPH iteration.
func (r *RalphLoopController) RecordIteration(success bool, tokensUsed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.iterations++
	r.tokensUsed += tokensUsed
	if success {
		r.active = false // Success ends the loop
	}
}

// Iterations returns the number of completed iterations.
func (r *RalphLoopController) Iterations() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.iterations
}

// TokensUsed returns total tokens consumed by RALPH iterations.
func (r *RalphLoopController) TokensUsed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokensUsed
}

// IsActive returns whether the RALPH loop is currently active.
func (r *RalphLoopController) IsActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Reset clears all state for reuse with a new stage.
func (r *RalphLoopController) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.iterations = 0
	r.tokensUsed = 0
	r.startedAt = time.Time{}
	r.active = false
}
