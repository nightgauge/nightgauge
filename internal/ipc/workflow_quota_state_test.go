package ipc

import (
	"path/filepath"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestWorkflowQuotaState_NoSignals verifies the bridge is safe and inert when
// neither a rate-limit tracker reading nor a dispatch cooldown is available:
// the sentinels (-1/-1/0) signal "no data" and the gate reports not-exhausted
// so the WorkflowExecutor (#3908) proceeds rather than blocking on missing data.
func TestWorkflowQuotaState_NoSignals(t *testing.T) {
	// Inject an empty tracker rooted in a temp dir so the server does NOT fall
	// back to the real $HOME tracker (which may hold live readings).
	emptyTracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	s := NewServer(nil, WithRateLimitTracker(emptyTracker))

	var result WorkflowQuotaStateResult
	callMethod(t, s, "workflow.quotaState", WorkflowQuotaStateParams{}, &result)

	if result.Remaining != -1 || result.Limit != -1 || result.ResetsAt != 0 {
		t.Errorf("expected -1/-1/0 sentinels, got remaining=%d limit=%d resetsAt=%d",
			result.Remaining, result.Limit, result.ResetsAt)
	}
	if result.Exhausted {
		t.Errorf("expected exhausted=false with no signals")
	}
	if result.Bucket != "" {
		t.Errorf("expected empty bucket with no signals, got %q", result.Bucket)
	}
	if result.CooldownUntil != "" {
		t.Errorf("expected empty cooldownUntil with no scheduler, got %q", result.CooldownUntil)
	}
}

// TestWorkflowQuotaState_HealthyBucket verifies a non-depleted GitHub bucket is
// surfaced verbatim and does NOT trip the gate — this is the "status=allowed
// stall" case the executor must distinguish from genuine exhaustion.
func TestWorkflowQuotaState_HealthyBucket(t *testing.T) {
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	reset := time.Now().Add(30 * time.Minute).Unix()
	if err := tracker.Set("", &gh.RateLimitInfo{Remaining: 4200, Limit: 5000, ResetAt: reset}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	s := NewServer(gh.NewClientWithToken("t"), WithRateLimitTracker(tracker))

	var result WorkflowQuotaStateResult
	callMethod(t, s, "workflow.quotaState", WorkflowQuotaStateParams{}, &result)

	if result.Remaining != 4200 || result.Limit != 5000 || result.ResetsAt != reset {
		t.Errorf("bucket not surfaced: got remaining=%d limit=%d resetsAt=%d",
			result.Remaining, result.Limit, result.ResetsAt)
	}
	if result.Exhausted {
		t.Errorf("healthy bucket must not be exhausted")
	}
	if result.Bucket != "" {
		t.Errorf("expected empty bucket attribution, got %q", result.Bucket)
	}
}

// TestWorkflowQuotaState_DepletedBucketGatesFanout is the acceptance test: a
// depleted GitHub tracker bucket must trip the gate so the executor defers a
// large fan-out instead of dispatching into an exhausted quota.
func TestWorkflowQuotaState_DepletedBucketGatesFanout(t *testing.T) {
	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	reset := time.Now().Add(20 * time.Minute).Unix()
	if err := tracker.Set("", &gh.RateLimitInfo{Remaining: 0, Limit: 5000, ResetAt: reset}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	s := NewServer(gh.NewClientWithToken("t"), WithRateLimitTracker(tracker))

	var result WorkflowQuotaStateResult
	callMethod(t, s, "workflow.quotaState", WorkflowQuotaStateParams{}, &result)

	if !result.Exhausted {
		t.Fatalf("depleted bucket (remaining=0) must report exhausted=true")
	}
	if result.Bucket != "github-rest" {
		t.Errorf("bucket = %q, want %q", result.Bucket, "github-rest")
	}
	if result.ResetsAt != reset {
		t.Errorf("resetsAt = %d, want %d so the executor knows when to retry", result.ResetsAt, reset)
	}
}

// TestQuotaCooldownBucket pins the reason-text → bucket attribution used when an
// active dispatch cooldown gates the fan-out. The reason strings match the
// phrasing written by the autonomous scheduler's two cooldown setters.
func TestQuotaCooldownBucket(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"GitHub API quota low (default) — dispatch suspended until X", "github-quota"},
		{"rate-limit-quota-exhausted (Anthropic API quota exhausted; first observed via Y)", "anthropic-five-hour"},
		{"", "dispatch-cooldown"},
		{"some unrecognized reason", "dispatch-cooldown"},
	}
	for _, c := range cases {
		if got := quotaCooldownBucket(c.reason); got != c.want {
			t.Errorf("quotaCooldownBucket(%q) = %q, want %q", c.reason, got, c.want)
		}
	}
}
