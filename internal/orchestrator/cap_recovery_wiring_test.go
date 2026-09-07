package orchestrator

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Acceptance criterion 5: the operator learns about a cap-driven routing change
// from the PRODUCT, not from `go-backend.log` at 3am. These pin the card, and
// that the scheduler call site persists exactly what the shared builder makes —
// the docs/ATTENTION_PRODUCERS.md § Run-scoped producers contract, so no second
// call site can render a different card for the same condition.

func TestCapFallbackCardDelegatesToTheSharedBuilder(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    CapRecoveryDecision
	}{
		{"descend", CapRecoveryDecision{
			Verdict:   CapRecoveryDescendTier,
			Downgrade: DowngradeDecision{NewTier: "opus"},
			Why:       "usage cap hit while running fable on anthropic; descending to opus",
		}},
		{"hop", CapRecoveryDecision{
			Verdict:     CapRecoveryHopProvider,
			NextAdapter: "codex",
			Why:         "the whole tier ladder is spent; re-running the stage on codex",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttentionProducerRunScheduler(t)
			item := types.BoardItem{Repo: "octocat/acme", Number: 42}
			rt := &state.RuntimeState{RunID: "run-1"}

			s.raiseCapFallback(item, rt, state.StageFeaturePlanning, tc.d)

			reqs := openRunRequests(t, s)
			if len(reqs) != 1 {
				t.Fatalf("got %d cards, want 1", len(reqs))
			}
			want := BuildCapFallback("octocat/acme", 42, "run-1", state.StageFeaturePlanning, tc.d)
			if got, wantJSON := buildersIdentity(t, reqs[0]), buildersIdentity(t, want); got != wantJSON {
				t.Errorf("scheduler card != BuildCapFallback\n got: %s\nwant: %s", got, wantJSON)
			}
			// The card's body IS the decision's reason, so the card, the log
			// line and the run record all say the same thing.
			if reqs[0].Body != tc.d.Why {
				t.Errorf("card body = %q, want the decision's own reason %q", reqs[0].Body, tc.d.Why)
			}
			if reqs[0].Severity != attention.SeverityFYI {
				t.Errorf("severity = %q, want fyi — the pipeline already recovered, nothing is blocked", reqs[0].Severity)
			}
			assertSteerSet(t, reqs[0])
		})
	}
}

// TestACoolDownRaisesNoCard pins the deduplication. An exhausted cap already
// surfaces through the quota-cooldown reason, and a second card would tell the
// operator the same thing twice.
func TestACoolDownRaisesNoCard(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	s.raiseCapFallback(types.BoardItem{Repo: "octocat/acme", Number: 42}, nil,
		state.StageFeaturePlanning,
		CapRecoveryDecision{Verdict: CapRecoveryCoolDown, Why: "everything is exhausted"})

	if reqs := openRunRequests(t, s); len(reqs) != 0 {
		t.Fatalf("got %d cards for a cool_down, want 0", len(reqs))
	}
}

// TestTwoDescentsOnOneStageAreTwoCards pins the idempotency key's shape: it
// carries the DESTINATION, so a run that walks fable → opus → sonnet reports
// both moves instead of folding the second into the first.
func TestTwoDescentsOnOneStageAreTwoCards(t *testing.T) {
	s := newAttentionProducerRunScheduler(t)
	item := types.BoardItem{Repo: "octocat/acme", Number: 42}
	rt := &state.RuntimeState{RunID: "run-1"}

	for _, tier := range []string{"opus", "sonnet"} {
		s.raiseCapFallback(item, rt, state.StageFeatureDev, CapRecoveryDecision{
			Verdict:   CapRecoveryDescendTier,
			Downgrade: DowngradeDecision{NewTier: tier},
			Why:       "descending to " + tier,
		})
	}

	reqs := openRunRequests(t, s)
	if len(reqs) != 2 {
		t.Fatalf("got %d cards, want 2 — each descent is its own move", len(reqs))
	}
	// ...and re-raising one of them folds rather than duplicating.
	s.raiseCapFallback(item, rt, state.StageFeatureDev, CapRecoveryDecision{
		Verdict:   CapRecoveryDescendTier,
		Downgrade: DowngradeDecision{NewTier: "opus"},
		Why:       "descending to opus",
	})
	if reqs := openRunRequests(t, s); len(reqs) != 2 {
		t.Fatalf("got %d cards after a repeat of the same move, want 2", len(reqs))
	}
}

// TestTheCardNamesTheStageAndTheDestination keeps the title readable at a
// glance in the Action Center list, where the body is not shown.
func TestTheCardNamesTheStageAndTheDestination(t *testing.T) {
	hop := BuildCapFallback("octocat/acme", 42, "run-1", state.StageFeatureDev, CapRecoveryDecision{
		Verdict: CapRecoveryHopProvider, NextAdapter: "grok", Why: "why",
	})
	for _, want := range []string{"Usage cap", "feature-dev", "grok"} {
		if !strings.Contains(hop.Title, want) {
			t.Errorf("title %q must name %q", hop.Title, want)
		}
	}
}
