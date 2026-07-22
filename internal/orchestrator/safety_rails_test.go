package orchestrator

import (
	"testing"
	"time"
)

func TestBudgetCeiling_AllowWhenUnder(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling: 100_000,
	})
	allowed, reason := sr.CheckBeforeEnqueue(50_000)
	if !allowed {
		t.Errorf("expected allowed under budget, got denied: %s", reason)
	}
}

func TestBudgetCeiling_DenyWhenOver(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling: 100_000,
	})
	// Simulate prior usage
	sr.RecordCompletion(true, 80_000)

	allowed, reason := sr.CheckBeforeEnqueue(30_000)
	if allowed {
		t.Error("expected denied when estimate would exceed budget")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestBudgetCeiling_UnlimitedWhenZero(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling: 0, // unlimited
	})
	sr.RecordCompletion(true, 999_999_999)

	allowed, _ := sr.CheckBeforeEnqueue(999_999_999)
	if !allowed {
		t.Error("expected allowed when budget ceiling is 0 (unlimited)")
	}
}

func TestBudgetCeiling_ExactlyAtLimit(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling: 100_000,
	})
	sr.RecordCompletion(true, 50_000)

	// Exactly at ceiling: 50000 + 50000 = 100000, NOT > 100000
	allowed, _ := sr.CheckBeforeEnqueue(50_000)
	if !allowed {
		t.Error("expected allowed when exactly at budget ceiling (not over)")
	}

	// One over: should deny
	allowed, _ = sr.CheckBeforeEnqueue(50_001)
	if allowed {
		t.Error("expected denied when estimate exceeds budget ceiling by 1")
	}
}

func TestCircuitBreaker_TripsAfterMaxFailures(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		CircuitBreakerMax: 3,
	})

	// 2 failures: still allowed
	sr.RecordCompletion(false, 1000)
	sr.RecordCompletion(false, 1000)

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed with 2 consecutive failures (threshold 3)")
	}

	// 3rd failure: tripped
	sr.RecordCompletion(false, 1000)

	allowed, reason := sr.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("expected denied after 3 consecutive failures")
	}
	if reason == "" {
		t.Error("expected reason for circuit breaker trip")
	}
}

func TestCircuitBreaker_SuccessResetsCounter(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		CircuitBreakerMax: 3,
	})

	// 2 failures, then 1 success
	sr.RecordCompletion(false, 1000)
	sr.RecordCompletion(false, 1000)
	sr.RecordCompletion(true, 1000)

	// Counter is reset, 2 more failures should not trip
	sr.RecordCompletion(false, 1000)
	sr.RecordCompletion(false, 1000)

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed: success should have reset consecutive failure counter")
	}
}

func TestCircuitBreaker_DisabledWhenZero(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		CircuitBreakerMax: 0, // disabled
	})

	for i := 0; i < 100; i++ {
		sr.RecordCompletion(false, 1000)
	}

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed when circuit breaker is disabled (max=0)")
	}
}

func TestRateLimit_AllowUpToLimit(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		RateLimitPerHour: 5,
	})

	for i := 0; i < 5; i++ {
		allowed, reason := sr.CheckBeforeEnqueue(0)
		if !allowed {
			t.Errorf("expected allowed for start %d, got denied: %s", i+1, reason)
		}
		sr.RecordPipelineStart()
	}

	// 6th should be denied
	allowed, reason := sr.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("expected denied after rate limit reached")
	}
	if reason == "" {
		t.Error("expected reason for rate limit")
	}
}

func TestRateLimit_WindowReset(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		RateLimitPerHour: 2,
	})

	// Fill the window
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("expected denied at rate limit")
	}

	// Simulate hour passing by backdating the window start
	sr.mu.Lock()
	sr.state.HourWindowStart = time.Now().Add(-2 * time.Hour)
	sr.mu.Unlock()

	allowed, _ = sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed after hour window reset")
	}
}

func TestRateLimit_DisabledWhenZero(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		RateLimitPerHour: 0, // disabled
	})

	for i := 0; i < 100; i++ {
		sr.RecordPipelineStart()
	}

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed when rate limit is disabled (0)")
	}
}

func TestEpicCheckpoint_PausesAfterEpicComplete(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		EpicCheckpoint: true,
	})

	sr.RecordEpicComplete(42)

	allowed, reason := sr.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("expected denied: paused for epic checkpoint")
	}
	if reason == "" {
		t.Error("expected checkpoint reason")
	}

	state := sr.State()
	if !state.PausedForCheckpoint {
		t.Error("expected PausedForCheckpoint to be true")
	}
	if state.LastEpicNumber != 42 {
		t.Errorf("expected LastEpicNumber 42, got %d", state.LastEpicNumber)
	}
}

func TestEpicCheckpoint_DisabledWhenFalse(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		EpicCheckpoint: false,
	})

	sr.RecordEpicComplete(42)

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed: epic checkpoint is disabled")
	}

	state := sr.State()
	if state.PausedForCheckpoint {
		t.Error("expected PausedForCheckpoint to be false when disabled")
	}
}

func TestEpicCheckpoint_ResumeClears(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		EpicCheckpoint: true,
	})

	sr.RecordEpicComplete(42)

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("expected denied before resume")
	}

	sr.ResumeCheckpoint()

	allowed, _ = sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed after checkpoint resume")
	}
}

func TestHealthGate_DenyWhenBelowThreshold(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		HealthGateMin: 30,
	})

	sr.UpdateHealthScore(20)

	allowed, reason := sr.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("expected denied when health score below threshold")
	}
	if reason == "" {
		t.Error("expected reason for health gate")
	}
}

func TestHealthGate_AllowWhenAboveThreshold(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		HealthGateMin: 30,
	})

	sr.UpdateHealthScore(50)

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed when health score above threshold")
	}
}

func TestHealthGate_AllowWhenScoreNotSet(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		HealthGateMin: 30,
	})

	// LastHealthScore is 0 (not yet populated) — should not block
	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed when health score not yet set (0)")
	}
}

func TestHealthGate_DisabledWhenZero(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		HealthGateMin: 0, // disabled
	})

	sr.UpdateHealthScore(5)

	allowed, _ := sr.CheckBeforeEnqueue(0)
	if !allowed {
		t.Error("expected allowed when health gate is disabled (min=0)")
	}
}

func TestMultipleRails_PriorityOrder(t *testing.T) {
	// Budget trips before circuit breaker
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling:     1000,
		CircuitBreakerMax: 2,
		RateLimitPerHour:  1,
		HealthGateMin:     50,
	})

	// Trip everything: budget, circuit breaker, rate limit, health
	sr.RecordCompletion(false, 1500)
	sr.RecordCompletion(false, 0)
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.UpdateHealthScore(10)

	allowed, reason := sr.CheckBeforeEnqueue(100)
	if allowed {
		t.Error("expected denied with multiple rails tripped")
	}

	// Budget should be the reason (highest priority)
	if reason == "" {
		t.Error("expected non-empty reason")
	}
	// Verify budget is checked first
	if !contains(reason, "budget") {
		t.Errorf("expected budget to be the first tripped rail, got: %s", reason)
	}
}

func TestMultipleRails_CircuitBeforRate(t *testing.T) {
	// When budget is fine, circuit breaker should trip before rate limit
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling:     100_000,
		CircuitBreakerMax: 2,
		RateLimitPerHour:  1,
	})

	sr.RecordCompletion(false, 100)
	sr.RecordCompletion(false, 100)
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()

	_, reason := sr.CheckBeforeEnqueue(0)
	if !contains(reason, "circuit breaker") {
		t.Errorf("expected circuit breaker reason, got: %s", reason)
	}
}

func TestMultipleRails_RateBeforeHealth(t *testing.T) {
	// When budget and circuit breaker are fine, rate limit before health
	sr := NewSafetyRails(SafetyConfig{
		RateLimitPerHour: 1,
		HealthGateMin:    50,
	})

	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.UpdateHealthScore(10)

	_, reason := sr.CheckBeforeEnqueue(0)
	if !contains(reason, "rate limit") {
		t.Errorf("expected rate limit reason, got: %s", reason)
	}
}

func TestReset_ClearsCounters(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling:     100_000,
		CircuitBreakerMax: 3,
		RateLimitPerHour:  5,
		EpicCheckpoint:    true,
	})

	// Trip circuit breaker and rate limit, set checkpoint
	sr.RecordCompletion(false, 1000)
	sr.RecordCompletion(false, 1000)
	sr.RecordCompletion(false, 1000)
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.RecordEpicComplete(10)

	// Verify tripped
	if !sr.IsTripped() {
		t.Error("expected tripped before reset")
	}

	sr.Reset()

	state := sr.State()
	if state.ConsecutiveFailures != 0 {
		t.Errorf("expected 0 consecutive failures after reset, got %d", state.ConsecutiveFailures)
	}
	if state.PipelineStartsThisHour != 0 {
		t.Errorf("expected 0 starts after reset, got %d", state.PipelineStartsThisHour)
	}
	if state.PausedForCheckpoint {
		t.Error("expected checkpoint cleared after reset")
	}
	if state.TripReason != "" {
		t.Errorf("expected empty trip reason after reset, got %q", state.TripReason)
	}
	// Budget counter should NOT be reset
	if state.TokensUsed != 3000 {
		t.Errorf("expected tokens preserved at 3000 after reset, got %d", state.TokensUsed)
	}
}

func TestIsTripped_Budget(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling: 1000,
	})
	sr.RecordCompletion(true, 1500)

	if !sr.IsTripped() {
		t.Error("expected tripped when budget exceeded")
	}
}

func TestIsTripped_CircuitBreaker(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		CircuitBreakerMax: 2,
	})
	sr.RecordCompletion(false, 0)
	sr.RecordCompletion(false, 0)

	if !sr.IsTripped() {
		t.Error("expected tripped after 2 consecutive failures (max 2)")
	}
}

func TestIsTripped_RateLimit(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		RateLimitPerHour: 2,
	})
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()

	if !sr.IsTripped() {
		t.Error("expected tripped when rate limit reached")
	}
}

func TestIsTripped_HealthGate(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		HealthGateMin: 50,
	})
	sr.UpdateHealthScore(30)

	if !sr.IsTripped() {
		t.Error("expected tripped when health below gate")
	}
}

func TestIsTripped_Checkpoint(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		EpicCheckpoint: true,
	})
	sr.RecordEpicComplete(1)

	if !sr.IsTripped() {
		t.Error("expected tripped when paused for checkpoint")
	}
}

func TestIsTripped_NothingTripped(t *testing.T) {
	sr := NewSafetyRails(DefaultSafetyConfig())

	if sr.IsTripped() {
		t.Error("expected not tripped with default config and fresh state")
	}
}

func TestStateSnapshot(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling:     100_000,
		CircuitBreakerMax: 3,
	})
	sr.RecordCompletion(false, 5000)
	sr.RecordCompletion(false, 3000)

	state := sr.State()
	if state.TokensUsed != 8000 {
		t.Errorf("expected 8000 tokens, got %d", state.TokensUsed)
	}
	if state.ConsecutiveFailures != 2 {
		t.Errorf("expected 2 failures, got %d", state.ConsecutiveFailures)
	}
}

func TestConfigSnapshot(t *testing.T) {
	cfg := SafetyConfig{
		BudgetCeiling:     42,
		CircuitBreakerMax: 7,
		RateLimitPerHour:  99,
		EpicCheckpoint:    true,
		HealthGateMin:     55,
	}
	sr := NewSafetyRails(cfg)

	got := sr.Config()
	if got != cfg {
		t.Errorf("Config() does not match: got %+v, want %+v", got, cfg)
	}
}

func TestRecordPipelineStart_IncrementsCounter(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		RateLimitPerHour: 10,
	})

	sr.RecordPipelineStart()
	sr.RecordPipelineStart()
	sr.RecordPipelineStart()

	state := sr.State()
	if state.PipelineStartsThisHour != 3 {
		t.Errorf("expected 3 starts, got %d", state.PipelineStartsThisHour)
	}
}

func TestConcurrentAccess(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		BudgetCeiling:     1_000_000,
		CircuitBreakerMax: 100,
		RateLimitPerHour:  1000,
	})

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				sr.CheckBeforeEnqueue(100)
				sr.RecordPipelineStart()
				sr.RecordCompletion(j%2 == 0, 100)
				sr.IsTripped()
				sr.State()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// No panic = pass
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
