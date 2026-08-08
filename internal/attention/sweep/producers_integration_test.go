package sweep

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// These exercise the producers THROUGH the sweeper and the store, because the
// acceptance criteria that matter most ("auto-resolves when the branch goes
// green", "exactly one request") are properties of the reconciliation, not of
// a producer's return value in isolation.

func TestDefaultBranchLifecycle_RaiseRefreshAutoResolve(t *testing.T) {
	ci := &branchCI{
		required: []string{"build"},
		runs: []forgetypes.CheckDetail{
			{Name: "build", Conclusion: "FAILURE", CompletedAt: ago(time.Hour), DetailsURL: "https://forge/run/1"},
		},
	}
	sw, store := newSweeper(t, &branchForge{repo: &repoSvc{defaultBranch: "main"}, ci: ci}, newDefaultBranchProducer())

	// 1. Red branch → one card.
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 1 {
		t.Fatalf("first sweep: created = %d, want 1 (%+v)", res.Reconciled.Created, res.Reconciled)
	}

	// 2. Still red, nothing changed → refreshed, not re-raised. This is the
	//    "one condition, one notification" rule: the operator hears about a red
	//    main once, not once per sweep for as long as it stays red.
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 0 || res.Reconciled.Refreshed != 1 {
		t.Fatalf("second sweep: want refreshed=1 created=0, got %+v", res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("a second sweep must not produce a second card")
	}

	// 3. Branch goes green → the card retracts itself.
	ci.runs = []forgetypes.CheckDetail{{Name: "build", Conclusion: "SUCCESS", CompletedAt: ago(time.Minute)}}
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 1 {
		t.Fatalf("third sweep: auto_resolved = %d, want 1 (%+v)", res.Reconciled.AutoResolved, res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 0 {
		t.Fatal("a green branch must leave no open card")
	}

	// The terminal state has to be distinguishable from a human's resolution —
	// "the problem fixed itself" and "someone dealt with it" are different
	// facts and the scorecard has to be able to tell them apart.
	all, err := store.List(attention.ListFilter{Repo: "octocat/acme", IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("stored requests = %d, want 1", len(all))
	}
	if all[0].Lifecycle.State != attention.StateAutoResolved {
		t.Errorf("state = %q, want %q", all[0].Lifecycle.State, attention.StateAutoResolved)
	}
}

func TestSweepPopulatesExistingRequestsForCrossProducerDedupe(t *testing.T) {
	// A run-scoped card is already in the store when the sweep starts.
	sw, store := newSweeper(t, &fakeForge{ci: &fakeCI{}})
	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	runScoped := attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "branch-protection:octocat/acme#7",
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingRun,
		Title:          "PR #42 blocked by branch protection",
		Producer:       producerBranchProtection,
		Context:        attention.Context{Repo: "octocat/acme", Issue: 7, PR: 42},
		DefaultAction:  attention.ExpireNoop,
	}
	if _, _, err := store.Raise(runScoped); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	spy := &scriptedProducer{name: "spy"}
	sw.Registry.Register(spy)
	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if len(spy.sawRepo.Existing) != 1 {
		t.Fatalf("producer saw %d existing requests, want 1 — without them it cannot avoid double-carding a PR another producer already raised", len(spy.sawRepo.Existing))
	}
	if _, ok := spy.sawRepo.OpenRequestForPR(producerBranchProtection, 42); !ok {
		t.Error("OpenRequestForPR did not find the open run-scoped card for PR 42")
	}
	if _, ok := spy.sawRepo.OpenRequestForPR(producerBranchProtection, 99); ok {
		t.Error("OpenRequestForPR matched a PR that has no card")
	}
}

func TestHumanGateLifecycle_AggregateReplacesIndividualsInTheStore(t *testing.T) {
	prs := &gatePRs{list: []types.PullRequest{
		greenPR(1, "BLOCKED", "REVIEW_REQUIRED"),
		greenPR(2, "BLOCKED", "REVIEW_REQUIRED"),
	}}
	gate := &HumanGate{Now: func() time.Time { return fixedNow }, MaxIndividual: 2}
	sw, store := newSweeper(t, &gateForge{prs: prs}, gate)

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 2 {
		t.Fatalf("created = %d, want 2 individual cards at the cap", res.Reconciled.Created)
	}

	// The backlog grows past the cap. The individual cards must not simply
	// accumulate alongside the aggregate — reconciliation retracts them,
	// because the aggregate is now the whole truth about those PRs.
	prs.list = append(prs.list, greenPR(3, "BLOCKED", "REVIEW_REQUIRED"))
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 1 {
		t.Errorf("created = %d, want the single aggregate card", res.Reconciled.Created)
	}
	if res.Reconciled.AutoResolved != 2 {
		t.Errorf("auto_resolved = %d, want the 2 individual cards retracted", res.Reconciled.AutoResolved)
	}
	open, err := store.List(attention.ListFilter{Repo: "octocat/acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open cards = %d, want 1 aggregate", len(open))
	}

	// And back down below the cap, the individuals return.
	prs.list = prs.list[:1]
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 1 {
		t.Errorf("auto_resolved = %d, want the aggregate retracted", res.Reconciled.AutoResolved)
	}
	open, err = store.List(attention.ListFilter{Repo: "octocat/acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 || open[0].Context.PR != 1 {
		t.Fatalf("want one individual card for PR 1, got %d cards", len(open))
	}
}
