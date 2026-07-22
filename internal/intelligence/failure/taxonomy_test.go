package failure

import "testing"

func TestClassifier_RateLimit(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-dev", 1, "Error: rate limit exceeded")

	if cl.Category != CatTransient {
		t.Errorf("category = %s, want transient", cl.Category)
	}
	if !cl.Retryable {
		t.Error("rate limit should be retryable")
	}
	if cl.MaxRetries != 3 {
		t.Errorf("maxRetries = %d, want 3", cl.MaxRetries)
	}
}

func TestClassifier_NetworkError(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-dev", 1, "connection refused to api.github.com")

	if cl.Category != CatInfra {
		t.Errorf("category = %s, want infra", cl.Category)
	}
	if !cl.Retryable {
		t.Error("network error should be retryable")
	}
}

func TestClassifier_AuthError(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("issue-pickup", 1, "HTTP 401 Unauthorized")

	if cl.Category != CatPermission {
		t.Errorf("category = %s, want permission", cl.Category)
	}
	if cl.Retryable {
		t.Error("auth error should not be retryable")
	}
	if !cl.Escalate {
		t.Error("auth error should escalate")
	}
}

func TestClassifier_TokenLimit(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-dev", 1, "Error: token limit exceeded, max_tokens reached")

	if cl.Category != CatResource {
		t.Errorf("category = %s, want resource", cl.Category)
	}
	if cl.Retryable {
		t.Error("token limit should not be retryable")
	}
}

func TestClassifier_MergeConflict(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("pr-merge", 1, "merge conflict in src/app.ts")

	if cl.Category != CatDeterministic {
		t.Errorf("category = %s, want deterministic", cl.Category)
	}
	if !cl.Escalate {
		t.Error("merge conflict should escalate")
	}
}

func TestClassifier_TestFailure(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-validate", 1, "Test failed: FAIL src/app.test.ts")

	if cl.Category != CatDeterministic {
		t.Errorf("category = %s, want deterministic", cl.Category)
	}
	if !cl.Retryable {
		t.Error("test failure should be retryable (once)")
	}
	if cl.MaxRetries != 1 {
		t.Errorf("maxRetries = %d, want 1", cl.MaxRetries)
	}
}

func TestClassifier_RulesetBlocked(t *testing.T) {
	c := NewClassifier()

	cases := []string{
		"Error: base branch policy prohibits the merge",
		"GraphQL: base branch policy prohibits the merge (mergePullRequest.input.pullRequestId)",
		"merge blocked by base branch ruleset on 'main'",
	}
	for _, stderr := range cases {
		cl := c.Classify("pr-merge", 1, stderr)
		if cl.Category != CatRulesetBlocked {
			t.Errorf("stderr=%q: category = %s, want ruleset-blocked", stderr, cl.Category)
		}
		if cl.Retryable {
			t.Errorf("stderr=%q: ruleset blocker should not be retryable", stderr)
		}
		if !cl.Escalate {
			t.Errorf("stderr=%q: ruleset blocker should escalate", stderr)
		}
	}
}

func TestClassifier_RequiredStatusCheckBlocked(t *testing.T) {
	c := NewClassifier()

	// The bowlsheet#233 incident signature and friends (#185): previously
	// fell through to CatUnknown → Retryable:true → identical-prompt re-run.
	cases := []string{
		`GraphQL: Required status check "Sentry Smoke (integration)" is expected. (mergePullRequest)`,
		`GraphQL: Required status check "ci" is failing.`,
		`5 of 5 required status checks are expected.`,
		`Required status checks have not passed for this pull request.`,
		`CONFIG BLOCKER (non-retryable): required-check-config-mismatch:Sentry Smoke (integration)`,
	}
	for _, stderr := range cases {
		cl := c.Classify("pr-merge", 1, stderr)
		if cl.Category != CatRulesetBlocked {
			t.Errorf("stderr=%q: category = %s, want ruleset-blocked", stderr, cl.Category)
		}
		if cl.Retryable {
			t.Errorf("stderr=%q: required-check blocker should not be retryable", stderr)
		}
		if !cl.Escalate {
			t.Errorf("stderr=%q: required-check blocker should escalate", stderr)
		}
	}

	// Probe chatter must NOT classify as blocked: "No required status checks
	// found" is the (previously misleading) probe output, not a rejection.
	probe := c.Classify("pr-merge", 1, "No required status checks found")
	if probe.Category == CatRulesetBlocked {
		t.Errorf("probe output misclassified as ruleset-blocked")
	}
}

func TestClassifier_SdkDistStale_RecoverableMarker(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-validate", 1, "RECOVERABLE: stale_sdk_dist\nnpm ERR! build failed")

	if cl.Category != CatSdkDistStale {
		t.Errorf("category = %s, want stale-sdk-dist", cl.Category)
	}
	if !cl.Retryable {
		t.Error("stale sdk dist should be retryable")
	}
	if cl.MaxRetries != 1 {
		t.Errorf("maxRetries = %d, want 1", cl.MaxRetries)
	}
	if cl.Escalate {
		t.Error("stale sdk dist should not escalate — it is auto-recoverable")
	}
}

func TestClassifier_SdkDistMissing(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-dev", 1, "ERROR: SDK dist/index.js not found — run `npm run -w @nightgauge/sdk build` first")

	if cl.Category != CatSdkDistStale {
		t.Errorf("category = %s, want stale-sdk-dist", cl.Category)
	}
	if !cl.Retryable {
		t.Error("missing sdk dist should be retryable")
	}
	if cl.MaxRetries != 1 {
		t.Errorf("maxRetries = %d, want 1", cl.MaxRetries)
	}
}

func TestClassifier_SdkDistStale(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("feature-validate", 1, "ERROR: SDK dist is stale — run `npm run -w @nightgauge/sdk build` first")

	if cl.Category != CatSdkDistStale {
		t.Errorf("category = %s, want stale-sdk-dist", cl.Category)
	}
	if !cl.Retryable {
		t.Error("stale sdk dist should be retryable")
	}
}

// TestClassifier_BlockedDependency verifies the blocked-dependency deferral
// (Issue #231) is classified as a controlled hold, not an organic failure:
// CatBlockedDependency, not retryable, not escalated.
func TestClassifier_BlockedDependency(t *testing.T) {
	c := NewClassifier()
	cl := c.Classify("issue-pickup", 1, "[blocked-dependency] blocked by open dependency #123 (PR not merged)")

	if cl.Category != CatBlockedDependency {
		t.Errorf("category = %s, want blocked-dependency", cl.Category)
	}
	if cl.Retryable {
		t.Error("blocked-dependency deferral must not be retryable in place")
	}
	if cl.Escalate {
		t.Error("blocked-dependency deferral must not escalate — it is a controlled hold")
	}
}

func TestClassification_ShouldRetry(t *testing.T) {
	cl := Classification{Retryable: true, MaxRetries: 2}
	if !cl.ShouldRetry(0) {
		t.Error("attempt 0 should retry")
	}
	if !cl.ShouldRetry(1) {
		t.Error("attempt 1 should retry")
	}
	if cl.ShouldRetry(2) {
		t.Error("attempt 2 should not retry (at max)")
	}
}
