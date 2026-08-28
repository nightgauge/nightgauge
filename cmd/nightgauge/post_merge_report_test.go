package main

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

// TestRaisePostMergeFailureCard_IsAcceptedByTheStore is the guard that the card
// is well-formed (#1025).
//
// attention.Store.validateForRaise hard-requires ID, IdempotencyKey, Producer,
// a valid Kind, a valid Severity and a Title, and rejects a standing request
// with no fingerprint. raisePostMergeFailureCard is fail-open by design — the
// hook must never turn a successful merge into an error — so a malformed
// request would be rejected, logged to stderr, and produce exactly the silence
// this issue is about. Nothing else would ever notice.
func TestRaisePostMergeFailureCard_IsAcceptedByTheStore(t *testing.T) {
	root := t.TempDir()

	raisePostMergeFailureCard(root, "nightgauge", "nightgauge", 206, hooks.PostMergeResult{
		Reason:     "auto_close_error",
		EpicReason: "check_failed",
		Error:      "GraphQL: rate limit exceeded",
		EpicNumber: 206,
		Failed:     true,
	})

	open, err := attention.New(root).List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open card(s), want 1 — a request the store rejects fails open and "+
			"leaves exactly the silence this issue is about", len(open))
	}

	card := open[0]
	if card.Producer != "post-merge-hook" {
		t.Errorf("Producer = %q, want post-merge-hook", card.Producer)
	}
	if card.Context.Issue != 206 {
		t.Errorf("Context.Issue = %d, want 206", card.Context.Issue)
	}
	// The cause must reach the operator, not just the fact of failure.
	if !strings.Contains(card.Body, "check_failed") {
		t.Errorf("card body does not name the epic reason: %q", card.Body)
	}
	if !strings.Contains(card.Body, "rate limit exceeded") {
		t.Errorf("card body does not name the underlying error: %q", card.Body)
	}
	// Event-shaped, not standing: this verb runs once per merge and nothing
	// re-observes the condition, so a standing card could never auto-retract.
	if card.Standing {
		t.Error("card is Standing, but no sweep re-observes this condition — it would never retract")
	}
}

// TestRaisePostMergeFailureCard_IsIdempotentPerIssue stops a re-run of the hook
// from stacking duplicate cards on the same issue.
func TestRaisePostMergeFailureCard_IsIdempotentPerIssue(t *testing.T) {
	root := t.TempDir()
	res := hooks.PostMergeResult{Reason: "auto_close_error", EpicReason: "check_failed", Failed: true}

	raisePostMergeFailureCard(root, "nightgauge", "nightgauge", 206, res)
	raisePostMergeFailureCard(root, "nightgauge", "nightgauge", 206, res)

	open, err := attention.New(root).List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 {
		t.Errorf("got %d open card(s) after two raises, want 1 — the idempotency key must be "+
			"per issue so a retried hook does not stack cards", len(open))
	}
}

func TestPostMergeReportSuffixes(t *testing.T) {
	if got := epicReasonSuffix(""); got != "" {
		t.Errorf("epicReasonSuffix(\"\") = %q, want empty", got)
	}
	if got := epicReasonSuffix("has_open"); got != " (has_open)" {
		t.Errorf("epicReasonSuffix = %q", got)
	}
	if got := errorSuffix(""); got != "" {
		t.Errorf("errorSuffix(\"\") = %q, want empty", got)
	}
	if got := errorSuffix("boom"); got != ": boom" {
		t.Errorf("errorSuffix = %q", got)
	}
}
