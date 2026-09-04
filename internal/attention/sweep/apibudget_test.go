package sweep

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/github"
)

func budgetProducer(w github.LedgerWindow, err error, now time.Time) *APIBudget {
	return &APIBudget{
		ReadWindow: func(string, time.Duration, time.Time) (github.LedgerWindow, error) { return w, err },
		Now:        func() time.Time { return now },
	}
}

func evalBudget(t *testing.T, p *APIBudget) ([]attention.DecisionRequest, error) {
	t.Helper()
	return p.Evaluate(context.Background(), WorkspaceInput{WorkspaceRoot: t.TempDir()})
}

// The condition the producer exists for: an exhaustion nobody was watching,
// still legible afterwards and still attributed.
func TestAPIBudgetCardsAnExhaustionAndNamesTheCaller(t *testing.T) {
	now := time.Now().UTC()
	reqs, err := evalBudget(t, budgetProducer(github.LedgerWindow{
		Exhausted:         true,
		ExhaustedResource: "graphql",
		ExhaustedAt:       now.Add(-90 * time.Minute),
		Points:            5000,
		Calls:             311,
		Cached:            40,
		TopCallers: []github.LedgerCallerSpend{
			{Caller: "sweep.Producers", Points: 4200, Calls: 250},
			{Caller: "depgraph.Rebuild", Points: 800, Calls: 60},
			{Caller: "free.Loop", Points: 0, Calls: 1},
		},
	}, nil, now))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("got %d cards, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Severity != attention.SeverityFYI {
		t.Errorf("Severity = %q, want fyi — nothing is blocked once the window resets", r.Severity)
	}
	if !strings.Contains(r.Title, "sweep.Producers") {
		t.Errorf("Title = %q, want the top spender named in the title", r.Title)
	}
	if !strings.Contains(r.Body, "depgraph.Rebuild") {
		t.Errorf("Body = %q, want the runners-up listed", r.Body)
	}
	if strings.Contains(r.Body, "free.Loop") {
		t.Errorf("Body names a zero-point caller, which cannot be the answer:\n%s", r.Body)
	}
	if !strings.Contains(r.Body, "1h30m0s ago") {
		t.Errorf("Body = %q, want the time of the exhaustion", r.Body)
	}
	// No repair verb exists for "a caller is too expensive"; a button that
	// silently does nothing is worse than none.
	for _, o := range r.Options {
		if o.Verb != attention.VerbNoop {
			t.Errorf("option %q carries verb %q; this card has no bounded repair", o.ID, o.Verb)
		}
	}
}

// Invariant 1: an empty slice is a POSITIVE assertion that the condition does
// not hold, which auto-resolves the standing card once the budget recovers.
func TestAPIBudgetHealthyWindowResolves(t *testing.T) {
	now := time.Now().UTC()
	reqs, err := evalBudget(t, budgetProducer(github.LedgerWindow{
		Points: 300, Calls: 20,
		TopCallers: []github.LedgerCallerSpend{{Caller: "boardcache.Refresh", Points: 300, Calls: 20}},
	}, nil, now))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("got %d cards on a healthy window, want 0", len(reqs))
	}
}

// An absent ledger is an opt-out or a workspace that has not called GitHub —
// a genuine "no exhaustion", not a failure to look. Returning an error here
// would strand a card forever on a workspace that switched the ledger off.
func TestAPIBudgetAbsentLedgerResolvesRatherThanErrors(t *testing.T) {
	now := time.Now().UTC()
	reqs, err := evalBudget(t, budgetProducer(github.LedgerWindow{},
		fmt.Errorf("%w at /w/.nightgauge/logs/github-api.jsonl", github.ErrNoLedger), now))
	if err != nil {
		t.Fatalf("Evaluate returned an error for an absent ledger: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("got %d cards, want 0", len(reqs))
	}
}

// Invariant 1's other half: a ledger that could not be READ is "I could not
// look" and must leave existing cards untouched.
func TestAPIBudgetUnreadableLedgerIsAnError(t *testing.T) {
	now := time.Now().UTC()
	_, err := evalBudget(t, budgetProducer(github.LedgerWindow{}, errors.New("permission denied"), now))
	if err == nil {
		t.Fatal("Evaluate returned nil error for an unreadable ledger — that would " +
			"auto-resolve every open card on the strength of having looked nowhere")
	}
}

func TestAPIBudgetNoWorkspaceRootIsAnError(t *testing.T) {
	p := budgetProducer(github.LedgerWindow{}, nil, time.Now())
	if _, err := p.Evaluate(context.Background(), WorkspaceInput{}); err == nil {
		t.Fatal("Evaluate with no workspace root returned nil error; the ledger was never located")
	}
}

// Re-observing the same outage must not re-alert; a new one must.
func TestAPIBudgetFingerprintTracksTheOutage(t *testing.T) {
	now := time.Now().UTC()
	at := now.Add(-time.Hour)
	base := github.LedgerWindow{
		Exhausted: true, ExhaustedResource: "graphql", ExhaustedAt: at,
		TopCallers: []github.LedgerCallerSpend{{Caller: "sweep.Producers", Points: 5000, Calls: 10}},
	}

	first, _ := evalBudget(t, budgetProducer(base, nil, now))
	again, _ := evalBudget(t, budgetProducer(base, nil, now.Add(20*time.Minute)))
	if first[0].Fingerprint != again[0].Fingerprint {
		t.Errorf("fingerprint changed on re-observing the SAME exhaustion (%q vs %q) — the card would re-alert every sweep",
			first[0].Fingerprint, again[0].Fingerprint)
	}
	if first[0].IdempotencyKey != again[0].IdempotencyKey {
		t.Errorf("idempotency key is not stable: %q vs %q", first[0].IdempotencyKey, again[0].IdempotencyKey)
	}

	later := base
	later.ExhaustedAt = now.Add(-10 * time.Minute)
	fresh, _ := evalBudget(t, budgetProducer(later, nil, now))
	if fresh[0].Fingerprint == first[0].Fingerprint {
		t.Errorf("fingerprint unchanged across a SECOND, distinct exhaustion (%q) — a dismissal would mute the new one too",
			fresh[0].Fingerprint)
	}
}

func TestAPIBudgetRegisteredInDefaultRegistry(t *testing.T) {
	for _, p := range Default.WorkspaceProducers() {
		if p.Name() == ProducerAPIBudget {
			return
		}
	}
	t.Fatalf("%q is not registered in the default registry, so no sweep evaluates it", ProducerAPIBudget)
}
