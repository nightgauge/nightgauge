package sweep

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func openCredentialCards(t *testing.T, store *attention.Store) []attention.DecisionRequest {
	t.Helper()
	all, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []attention.DecisionRequest
	for _, r := range all {
		if r.Producer == ProducerForgeCredential {
			out = append(out, r)
		}
	}
	return out
}

func unauthorized(repos ...string) []CredentialObservation {
	out := make([]CredentialObservation, 0, len(repos))
	for _, r := range repos {
		out = append(out, CredentialObservation{Repo: r, Unauthorized: true, Reason: "default-branch-health: repo view " + r + ": REST 401: Bad credentials"})
	}
	return out
}

func evaluated(repos ...string) []CredentialObservation {
	out := make([]CredentialObservation, 0, len(repos))
	for _, r := range repos {
		out = append(out, CredentialObservation{Repo: r, Evaluated: true})
	}
	return out
}

// The identity is the owner alone: the same dead credential re-observed on
// the next sweep, with other repos or another error text, is the same card.
func TestCredentialCard_IdentityIsTheOwner(t *testing.T) {
	a := CredentialCard("acme", []string{"acme/web", "acme/api"}, "REST 401: Bad credentials")
	b := CredentialCard("acme", []string{"acme/api"}, "a different message")
	if a.IdempotencyKey != b.IdempotencyKey || a.Fingerprint != b.Fingerprint {
		t.Errorf("identity moved with the repos or the reason: %q/%q vs %q/%q", a.IdempotencyKey, a.Fingerprint, b.IdempotencyKey, b.Fingerprint)
	}
	if a.IdempotencyKey != CredentialCardKey("acme") || !a.Standing || a.Producer != ProducerForgeCredential {
		t.Errorf("card = %+v", a)
	}
	for _, want := range []string{"acme/api", "acme/web", "nightgauge doctor", "401"} {
		if !strings.Contains(a.Body, want) {
			t.Errorf("body does not name %q:\n%s", want, a.Body)
		}
	}
	if a.Context.Repo != "acme/api" {
		t.Errorf("card repo = %q, want the first affected repo in order", a.Context.Repo)
	}
}

// AC3: a sweep where every repo fails on 401 raises ONE card, not one per repo
// and not none; the next 401 sweep refreshes it; a clean sweep retracts it.
func TestReconcileCredentialCards_OneCardPerOwnerUntilItSweepsCleanly(t *testing.T) {
	store := attention.New(layouttest.Repo(t))
	repos := []string{"acme/web", "acme/api", "acme/jobs"}

	res, err := ReconcileCredentialCards(store, unauthorized(repos...))
	if err != nil {
		t.Fatal(err)
	}
	cards := openCredentialCards(t, store)
	if res.Created != 1 || len(cards) != 1 {
		t.Fatalf("an all-401 sweep raised %d cards (result %+v), want exactly 1", len(cards), res)
	}

	res, err = ReconcileCredentialCards(store, unauthorized(repos...))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 || res.Updated != 0 || len(openCredentialCards(t, store)) != 1 {
		t.Fatalf("a repeat 401 sweep must refresh the card silently, got %+v", res)
	}

	// A pass that skipped one repo for another reason observed nothing about
	// the credential there: the card stays.
	partial := append(evaluated("acme/web", "acme/api"), CredentialObservation{Repo: "acme/jobs", Reason: "rate limited"})
	if res, err = ReconcileCredentialCards(store, partial); err != nil || res.AutoResolved != 0 {
		t.Fatalf("a partial pass retracted the card: %+v (%v)", res, err)
	}

	res, err = ReconcileCredentialCards(store, evaluated(repos...))
	if err != nil {
		t.Fatal(err)
	}
	if res.AutoResolved != 1 || len(openCredentialCards(t, store)) != 0 {
		t.Fatalf("a clean sweep must retract the card, got %+v", res)
	}
}

// Owners are independent: a dead credential for one does not raise or hold a
// card for another.
func TestReconcileCredentialCards_OwnersAreIndependent(t *testing.T) {
	store := attention.New(layouttest.Repo(t))
	obs := append(unauthorized("acme/web"), evaluated("octo/site")...)
	if _, err := ReconcileCredentialCards(store, obs); err != nil {
		t.Fatal(err)
	}
	cards := openCredentialCards(t, store)
	if len(cards) != 1 || cards[0].IdempotencyKey != CredentialCardKey("acme") {
		t.Fatalf("cards = %+v, want only acme's", cards)
	}
}
