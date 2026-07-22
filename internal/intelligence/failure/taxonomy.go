// Package failure classifies pipeline failures for retry/escalation decisions.
package failure

import (
	"strings"
)

// Category classifies a failure type.
type Category string

const (
	CatTransient     Category = "transient"     // Retry likely to succeed
	CatDeterministic Category = "deterministic" // Same input = same failure
	CatResource      Category = "resource"      // Out of tokens, memory, time
	CatPermission    Category = "permission"    // Auth/access errors
	CatInfra         Category = "infra"         // Network, API down
	CatUnknown       Category = "unknown"
	// CatIssueTooLarge is assigned when an issue exceeds size gate thresholds.
	// Not retryable — requires human decomposition into sub-issues.
	CatIssueTooLarge Category = "issue-too-large"
	// CatRulesetBlocked is assigned when a PR merge fails because an active
	// branch ruleset blocks it (e.g., copilot_code_review, required reviewers).
	// Not retryable — requires either the skill to auto-satisfy the specific
	// blocker or a human to relax the rule. See issue #2780 for context.
	CatRulesetBlocked Category = "ruleset-blocked"
	// CatStaleBranchMergeConflict is assigned when pr-create's proactive main
	// branch merge (Phase 2.3) detects conflicts between the feature branch and
	// the base branch. Not retryable — requires manual conflict resolution.
	// The PR is NOT created; the skill exits before gh pr create is called.
	// See issue #2781 for context.
	CatStaleBranchMergeConflict Category = "stale-branch-merge-conflict"
	// CatSdkDistStale is assigned when the VSCode extension build fails because
	// nightgauge-sdk/dist/index.js is missing or stale. Auto-recoverable:
	// run `npm run -w @nightgauge/sdk build` then retry.
	// At most one auto-retry per stage. See issue #2917.
	CatSdkDistStale Category = "stale-sdk-dist"
	// CatBlockedDependency is emitted when issue-pickup is deferred because the
	// issue has an OPEN native `blockedBy` dependency (the blocker's PR is not
	// merged). NOT a failure — a controlled hold. Not retryable in place; the
	// item is re-eligible when its blockers close (deps-gate promote sweep or
	// the autonomous cascade requeues it). See issue #231.
	CatBlockedDependency Category = "blocked-dependency"
)

// Severity indicates how severe a failure is.
type Severity string

const (
	SevLow      Severity = "low"
	SevMedium   Severity = "medium"
	SevHigh     Severity = "high"
	SevCritical Severity = "critical"
)

// Classification is the result of failure analysis.
type Classification struct {
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	Retryable   bool     `json:"retryable"`
	MaxRetries  int      `json:"maxRetries"`
	Escalate    bool     `json:"escalate"`
	Description string   `json:"description"`
}

// Classifier categorizes pipeline failures.
type Classifier struct{}

// NewClassifier creates a failure classifier.
func NewClassifier() *Classifier {
	return &Classifier{}
}

// Classify analyzes a failure and returns its classification.
func (c *Classifier) Classify(stage string, exitCode int, stderr string) Classification {
	lower := strings.ToLower(stderr)

	// Blocked-dependency deferral (Issue #231) — check FIRST. The pickup gate
	// held the issue because it has an OPEN native `blockedBy` dependency
	// (blocker's PR not merged). This is a controlled hold, NOT a failure: the
	// run exits cleanly and this classification exists only so retro/audit paths
	// that reach the classifier bucket the marker correctly rather than treating
	// it as an organic pipeline failure. Not retryable in place (re-running won't
	// close the blocker); the item is re-eligible when its blockers close.
	if containsAny(lower, "[blocked-dependency]", "blocked by open dependency", "blocked-dependency") {
		return Classification{
			Category:    CatBlockedDependency,
			Severity:    SevLow,
			Retryable:   false,
			Escalate:    false,
			Description: "pickup deferred — blocked by open dependency; re-eligible when blockers close",
		}
	}

	// Issue size gate — not retryable, requires human decomposition
	if containsAny(lower, "issue too large", "size gate: rejected", "oversized scope", "scope exceeds threshold", "decomposition required") {
		return Classification{
			Category:    CatIssueTooLarge,
			Severity:    SevMedium,
			Retryable:   false,
			Escalate:    true,
			Description: "issue scope exceeds pipeline size thresholds — decompose into sub-issues and retry",
		}
	}

	// Branch ruleset / protection blocker — not retryable. The pr-merge
	// skill's Step 6.0 ruleset pre-check tries to auto-satisfy known blockers
	// (e.g. requesting Copilot review). If the merge still fails with one of
	// these signatures, the blocker is outside the skill's control and
	// requires admin intervention. Covers the base-branch-policy phrasing
	// (#2780) and the required-status-check GraphQL rejection that was
	// previously misclassified CatUnknown/retryable and re-run into an
	// identical dead end (#185 / bowlsheet#233 forensics).
	if containsAny(lower, "base branch policy prohibits the merge", "merge blocked by base branch ruleset") ||
		isRequiredCheckBlocked(lower) {
		return Classification{
			Category:    CatRulesetBlocked,
			Severity:    SevHigh,
			Retryable:   false,
			Escalate:    true,
			Description: "merge blocked by branch ruleset / required status checks — repo-config blocker, do not retry; see docs/CI_INTEGRATION.md §Ruleset Interactions",
		}
	}

	// Rate limit errors — transient, retryable
	if containsAny(lower, "rate limit", "429", "too many requests", "secondary rate", "quota exceeded") {
		return Classification{
			Category:    CatTransient,
			Severity:    SevLow,
			Retryable:   true,
			MaxRetries:  3,
			Description: "rate limited — will retry with backoff",
		}
	}

	// Network/connectivity errors — transient, retryable
	if containsAny(lower, "network", "connection refused", "timeout", "econnreset", "dns") {
		return Classification{
			Category:    CatInfra,
			Severity:    SevMedium,
			Retryable:   true,
			MaxRetries:  2,
			Description: "network error — will retry",
		}
	}

	// Auth/permission errors — not retryable
	if containsAny(lower, "401", "403", "unauthorized", "forbidden", "permission denied") {
		return Classification{
			Category:    CatPermission,
			Severity:    SevHigh,
			Retryable:   false,
			Escalate:    true,
			Description: "authentication or permission error",
		}
	}

	// Token/resource limits
	if containsAny(lower, "token limit", "context length", "max_tokens", "out of memory") {
		return Classification{
			Category:    CatResource,
			Severity:    SevMedium,
			Retryable:   false,
			Escalate:    true,
			Description: "resource limit exceeded — consider breaking down the task",
		}
	}

	// SDK dist stale or missing — auto-recoverable (run SDK build, retry once).
	// Check BEFORE the generic build-failed handler — this is a specific, known
	// recoverable case that should not be treated as a hard build error.
	if containsAny(lower, "recoverable: stale_sdk_dist", "sdk dist/index.js not found", "sdk dist is stale") {
		return Classification{
			Category:    CatSdkDistStale,
			Severity:    SevMedium,
			Retryable:   true,
			MaxRetries:  1,
			Description: "sdk dist is stale — run `npm run -w @nightgauge/sdk build` then retry",
		}
	}

	// Stale branch merge conflict from pr-create Phase 2.3 proactive merge —
	// the feature branch is behind the base branch and has conflicts. The PR was
	// NOT created. Requires manual rebase/merge and conflict resolution.
	// Check this BEFORE the generic merge conflict handler (more specific).
	if containsAny(lower, "stale-branch-merge-conflict", "outcome: stale-branch-merge-conflict") {
		return Classification{
			Category:    CatStaleBranchMergeConflict,
			Severity:    SevHigh,
			Retryable:   false,
			Escalate:    true,
			Description: "feature branch is behind base branch and has merge conflicts — manually rebase and resolve conflicts, then re-run the pipeline",
		}
	}

	// Merge conflicts — deterministic, needs human
	if containsAny(lower, "merge conflict", "conflict", "cannot merge") {
		return Classification{
			Category:    CatDeterministic,
			Severity:    SevMedium,
			Retryable:   false,
			Escalate:    true,
			Description: "merge conflict — requires manual resolution",
		}
	}

	// Test failures — deterministic
	if containsAny(lower, "test failed", "assertion", "expect", "vitest", "jest") {
		return Classification{
			Category:    CatDeterministic,
			Severity:    SevMedium,
			Retryable:   true,
			MaxRetries:  1,
			Description: "test failure — may resolve with different approach",
		}
	}

	// Build/compile errors — deterministic
	if containsAny(lower, "build failed", "compile error", "syntax error", "type error") {
		return Classification{
			Category:    CatDeterministic,
			Severity:    SevMedium,
			Retryable:   true,
			MaxRetries:  1,
			Description: "build error — may resolve with different approach",
		}
	}

	// Generic non-zero exit
	if exitCode != 0 {
		return Classification{
			Category:    CatUnknown,
			Severity:    SevMedium,
			Retryable:   true,
			MaxRetries:  1,
			Description: "unknown failure — limited retry",
		}
	}

	return Classification{
		Category:    CatUnknown,
		Severity:    SevLow,
		Retryable:   false,
		Description: "unclassified",
	}
}

// ShouldRetry returns true if the failure should be retried given the attempt count.
func (cl Classification) ShouldRetry(attemptNumber int) bool {
	return cl.Retryable && attemptNumber < cl.MaxRetries
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// isRequiredCheckBlocked matches the GraphQL/REST merge-rejection signatures
// for required status checks (classic or ruleset-enforced), e.g.
//
//	GraphQL: Required status check "Sentry Smoke (integration)" is expected.
//
// plus the required-check-config-mismatch blocker emitted by
// `pr ruleset-precheck` (#184). These are repo-config blockers: retrying the
// merge is deterministically unwinnable until a human changes the config.
// The bare phrase "required status check" is NOT enough — probe output like
// "No required status checks found" must not classify as blocked.
func isRequiredCheckBlocked(lower string) bool {
	if strings.Contains(lower, "required-check-config-mismatch") {
		return true
	}
	if !strings.Contains(lower, "required status check") {
		return false
	}
	return containsAny(lower,
		"is expected", "are expected",
		"is failing", "are failing",
		"has not passed", "have not passed", "not yet passed")
}

// IsRulesetBlocked reports whether a terminal error text describes a pr-merge
// blocked by a required status check / branch-ruleset config that no pipeline
// retry can clear — a human must change repo config (remove the check from
// required, or drop continue-on-error). It is the exported, case-insensitive
// wrapper over the same detection that assigns CatRulesetBlocked, so run-outcome
// classification (a "blocked", needs-human outcome_type) shares one source of
// truth with failure-category classification. See isRequiredCheckBlocked.
func IsRulesetBlocked(errorText string) bool {
	if errorText == "" {
		return false
	}
	return isRequiredCheckBlocked(strings.ToLower(errorText))
}
