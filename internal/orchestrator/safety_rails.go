// Package orchestrator — safety_rails.go enforces safety constraints on
// autonomous execution: budget ceiling, circuit breaker, rate limiting,
// epic checkpoints, and health gate.
//
// All checks are fast (no I/O) and thread-safe (mutex protected).
// SafetyRails is a standalone struct, independently testable.
package orchestrator

import (
	"fmt"
	"sync"
	"time"
)

// SafetyRails enforces safety constraints on autonomous execution.
// It is designed to be called by the AutonomousScheduler before each
// enqueue and after each pipeline completion.
type SafetyRails struct {
	config SafetyConfig
	state  SafetyState
	mu     sync.Mutex
}

// SafetyConfig holds configurable thresholds for autonomous safety rails.
type SafetyConfig struct {
	// BudgetCeiling is the global token limit across all pipeline runs. 0 = unlimited.
	BudgetCeiling int64 `json:"budgetCeiling" yaml:"budget_ceiling"`

	// CircuitBreakerMax is the number of consecutive failures before tripping.
	CircuitBreakerMax int `json:"circuitBreakerMax" yaml:"circuit_breaker_max"`

	// RateLimitPerHour is the max pipeline starts per sliding hour window.
	RateLimitPerHour int `json:"rateLimitPerHour" yaml:"rate_limit_per_hour"`

	// EpicCheckpoint pauses between epics for human review when true.
	EpicCheckpoint bool `json:"epicCheckpoint" yaml:"epic_checkpoint"`

	// HealthGateMin is the minimum health score (0–100) to continue execution.
	HealthGateMin int `json:"healthGateMin" yaml:"health_gate_min"`

	// RefinementRateLimitPerHour caps refinement starts per sliding hour window.
	// Separate from pipeline rate limit. Default: 10.
	RefinementRateLimitPerHour int `json:"refinementRateLimitPerHour" yaml:"refinement_rate_limit_per_hour"`
}

// SafetyState is the runtime state of all safety rails.
type SafetyState struct {
	TokensUsed             int64     `json:"tokensUsed"`
	ConsecutiveFailures    int       `json:"consecutiveFailures"`
	PipelineStartsThisHour int       `json:"pipelineStartsThisHour"`
	HourWindowStart        time.Time `json:"hourWindowStart"`
	PausedForCheckpoint    bool      `json:"pausedForCheckpoint"`
	LastEpicNumber         int       `json:"lastEpicNumber,omitempty"`
	LastHealthScore        int       `json:"lastHealthScore"`
	TripReason             string    `json:"tripReason,omitempty"`

	// Refinement rate limit (separate from pipeline rate limit)
	RefinementStartsThisHour int       `json:"refinementStartsThisHour"`
	RefinementWindowStart    time.Time `json:"refinementWindowStart"`
}

// DefaultSafetyConfig returns sensible default safety thresholds.
func DefaultSafetyConfig() SafetyConfig {
	return SafetyConfig{
		BudgetCeiling:              500_000, // 500K tokens
		CircuitBreakerMax:          3,       // 3 consecutive failures
		RateLimitPerHour:           20,      // 20 pipelines/hour
		EpicCheckpoint:             true,    // pause between epics
		HealthGateMin:              30,      // min health score 30/100
		RefinementRateLimitPerHour: 10,      // 10 refinements/hour
	}
}

// NewSafetyRails creates a SafetyRails with the given config and initialises
// the rate-limit window to now.
func NewSafetyRails(cfg SafetyConfig) *SafetyRails {
	now := time.Now()
	return &SafetyRails{
		config: cfg,
		state: SafetyState{
			HourWindowStart:       now,
			RefinementWindowStart: now,
		},
	}
}

// CheckBeforeEnqueue validates all safety constraints before enqueuing a new
// pipeline run. It returns (allowed, reason). When allowed is false, reason
// describes which rail tripped.
//
// Check priority: budget > circuit breaker > rate limit > health gate > checkpoint.
func (sr *SafetyRails) CheckBeforeEnqueue(tokensEstimate int64) (bool, string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	// 1. Budget ceiling
	if sr.config.BudgetCeiling > 0 && sr.state.TokensUsed+tokensEstimate > sr.config.BudgetCeiling {
		reason := fmt.Sprintf("budget ceiling exceeded: used %d + estimate %d > ceiling %d",
			sr.state.TokensUsed, tokensEstimate, sr.config.BudgetCeiling)
		sr.state.TripReason = reason
		return false, reason
	}

	// 2. Circuit breaker
	if sr.config.CircuitBreakerMax > 0 && sr.state.ConsecutiveFailures >= sr.config.CircuitBreakerMax {
		reason := fmt.Sprintf("circuit breaker tripped: %d consecutive failures (max %d)",
			sr.state.ConsecutiveFailures, sr.config.CircuitBreakerMax)
		sr.state.TripReason = reason
		return false, reason
	}

	// 3. Rate limit (sliding hour window)
	if sr.config.RateLimitPerHour > 0 {
		sr.advanceWindowLocked()
		if sr.state.PipelineStartsThisHour >= sr.config.RateLimitPerHour {
			reason := fmt.Sprintf("rate limit exceeded: %d starts this hour (max %d)",
				sr.state.PipelineStartsThisHour, sr.config.RateLimitPerHour)
			sr.state.TripReason = reason
			return false, reason
		}
	}

	// 4. Health gate
	if sr.config.HealthGateMin > 0 && sr.state.LastHealthScore > 0 &&
		sr.state.LastHealthScore < sr.config.HealthGateMin {
		reason := fmt.Sprintf("health gate failed: score %d < minimum %d",
			sr.state.LastHealthScore, sr.config.HealthGateMin)
		sr.state.TripReason = reason
		return false, reason
	}

	// 5. Epic checkpoint pause
	if sr.state.PausedForCheckpoint {
		reason := fmt.Sprintf("paused for epic checkpoint (epic #%d complete — awaiting human review)",
			sr.state.LastEpicNumber)
		sr.state.TripReason = reason
		return false, reason
	}

	return true, ""
}

// RecordPipelineStart increments the rate-limit counter. Call this when a
// pipeline is actually dispatched (after CheckBeforeEnqueue returns true).
func (sr *SafetyRails) RecordPipelineStart() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.advanceWindowLocked()
	sr.state.PipelineStartsThisHour++
}

// RecordCompletion updates state after a pipeline run completes.
// A successful completion resets the consecutive failure counter.
// A failure increments it.
func (sr *SafetyRails) RecordCompletion(success bool, tokensUsed int64) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.state.TokensUsed += tokensUsed
	if success {
		sr.state.ConsecutiveFailures = 0
	} else {
		sr.state.ConsecutiveFailures++
	}
}

// RecordEpicComplete triggers a checkpoint pause if EpicCheckpoint is enabled.
func (sr *SafetyRails) RecordEpicComplete(epicNumber int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.config.EpicCheckpoint {
		sr.state.PausedForCheckpoint = true
		sr.state.LastEpicNumber = epicNumber
	}
}

// UpdateHealthScore feeds in the latest health score (0–100).
func (sr *SafetyRails) UpdateHealthScore(score int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.state.LastHealthScore = score
}

// Reset clears the circuit breaker, rate limiter, checkpoint pause, and trip
// reason. It does NOT reset the token budget counter (budget is a hard limit).
func (sr *SafetyRails) Reset() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.state.ConsecutiveFailures = 0
	sr.state.PipelineStartsThisHour = 0
	sr.state.HourWindowStart = time.Now()
	sr.state.PausedForCheckpoint = false
	sr.state.TripReason = ""
}

// ResumeCheckpoint clears only the epic checkpoint pause.
func (sr *SafetyRails) ResumeCheckpoint() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.state.PausedForCheckpoint = false
	if sr.state.TripReason != "" && sr.state.PausedForCheckpoint == false {
		// Only clear TripReason if it was a checkpoint reason
		sr.state.TripReason = ""
	}
}

// IsTripped returns true if any safety rail has been triggered.
func (sr *SafetyRails) IsTripped() bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	// Budget
	if sr.config.BudgetCeiling > 0 && sr.state.TokensUsed >= sr.config.BudgetCeiling {
		return true
	}
	// Circuit breaker
	if sr.config.CircuitBreakerMax > 0 && sr.state.ConsecutiveFailures >= sr.config.CircuitBreakerMax {
		return true
	}
	// Rate limit
	if sr.config.RateLimitPerHour > 0 {
		sr.advanceWindowLocked()
		if sr.state.PipelineStartsThisHour >= sr.config.RateLimitPerHour {
			return true
		}
	}
	// Health gate
	if sr.config.HealthGateMin > 0 && sr.state.LastHealthScore > 0 &&
		sr.state.LastHealthScore < sr.config.HealthGateMin {
		return true
	}
	// Checkpoint
	if sr.state.PausedForCheckpoint {
		return true
	}

	return false
}

// State returns a snapshot of the current safety state.
func (sr *SafetyRails) State() SafetyState {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.state
}

// Config returns the current safety config.
func (sr *SafetyRails) Config() SafetyConfig {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.config
}

// advanceWindowLocked resets the rate-limit counter if the hour window has
// elapsed. Caller must hold sr.mu.
func (sr *SafetyRails) advanceWindowLocked() {
	if time.Since(sr.state.HourWindowStart) >= time.Hour {
		sr.state.PipelineStartsThisHour = 0
		sr.state.HourWindowStart = time.Now()
	}
}

// advanceRefinementWindowLocked resets the refinement rate-limit counter if
// the hour window has elapsed. Caller must hold sr.mu.
func (sr *SafetyRails) advanceRefinementWindowLocked() {
	if time.Since(sr.state.RefinementWindowStart) >= time.Hour {
		sr.state.RefinementStartsThisHour = 0
		sr.state.RefinementWindowStart = time.Now()
	}
}

// CheckBeforeRefine validates the refinement rate limit only.
// Budget ceiling and circuit breaker are checked for dispatch only — refinement
// failures do NOT trip the dispatch circuit breaker.
func (sr *SafetyRails) CheckBeforeRefine() (bool, string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.config.RefinementRateLimitPerHour > 0 {
		sr.advanceRefinementWindowLocked()
		if sr.state.RefinementStartsThisHour >= sr.config.RefinementRateLimitPerHour {
			reason := fmt.Sprintf("refinement rate limit exceeded: %d starts this hour (max %d)",
				sr.state.RefinementStartsThisHour, sr.config.RefinementRateLimitPerHour)
			return false, reason
		}
	}

	return true, ""
}

// RecordRefinementStart increments the refinement rate counter.
func (sr *SafetyRails) RecordRefinementStart() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.advanceRefinementWindowLocked()
	sr.state.RefinementStartsThisHour++
}
