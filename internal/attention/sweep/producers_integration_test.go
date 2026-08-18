package sweep

import (
	"context"
	"errors"
	"strings"
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

// TestStaleRemediationLifecycle_RaiseEscalateAutoResolve drives the #649
// producer through the real Sweeper and the real store, because every criterion
// that matters is a property of the RECONCILIATION rather than of the
// producer's return value: that a second sweep refreshes instead of re-raising,
// that crossing a threshold multiple updates (and therefore alerts) instead of
// refreshing, that an operator's mute survives the quiet sweeps and is dropped
// by the escalation, and that merging the PR retracts the card.
func TestStaleRemediationLifecycle_RaiseEscalateAutoResolve(t *testing.T) {
	now := fixedNow
	sec := enabled(staleAlert(7, 412, 10*24*time.Hour))
	prs := prTable(openPR(412))
	sw, store := newSweeper(t, &staleForge{sec: sec, prs: prs},
		&DependabotStaleRemediation{Now: func() time.Time { return now }})

	runSweep := func(t *testing.T) attention.StandingResult {
		t.Helper()
		res, err := sw.Sweep(context.Background(), "octocat/acme")
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if len(res.Failed) != 0 {
			t.Fatalf("producer failed: %v", res.Failed)
		}
		return res.Reconciled
	}

	// 1. Ten days unmerged, one threshold overdue → one card.
	if got := runSweep(t); got.Created != 1 {
		t.Fatalf("first sweep: created = %d, want 1 (%+v)", got.Created, got)
	}

	// 2. Nothing moved → refreshed, not re-raised. "One condition, one
	//    notification": the operator hears about this PR once per threshold
	//    multiple, not once per sweep for as long as it sits there.
	if got := runSweep(t); got.Created != 0 || got.Updated != 0 || got.Refreshed != 1 {
		t.Fatalf("second sweep: want refreshed=1 created=0 updated=0, got %+v", got)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("a second sweep must not produce a second card")
	}

	// 3. The operator mutes it — mute lasts until the CONDITION changes, so a
	//    quiet sweep must not lift it.
	card := onlyOpen(t, store)
	if _, err := store.Mute(card.ID, "operator"); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if got := runSweep(t); got.Refreshed != 1 {
		t.Fatalf("muted sweep: want refreshed=1, got %+v", got)
	}
	if !onlyOpen(t, store).IsMuted() {
		t.Error("a re-observation of the unchanged condition lifted the mute")
	}

	// 4. Four more days: the wait crosses into the second threshold multiple.
	//    That is the escalation — updated (which alerts), and the mute the
	//    operator applied to the one-week card is dropped.
	now = now.Add(4 * 24 * time.Hour)
	got := runSweep(t)
	if got.Updated != 1 || got.Created != 0 {
		t.Fatalf("bucket transition: want updated=1 created=0, got %+v", got)
	}
	escalated := onlyOpen(t, store)
	if escalated.IsMuted() {
		t.Error("the mute survived a genuine escalation — mute is until-changed, not a timer")
	}
	if !strings.Contains(escalated.Title, "14+ days") {
		t.Errorf("escalated title = %q, want the second threshold multiple", escalated.Title)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("the escalation forked a second card instead of moving the first")
	}

	// 5. Someone merges it. The card retracts itself.
	merged := openPR(412)
	merged.State = "MERGED"
	prs.byNumber[412] = merged
	if got := runSweep(t); got.AutoResolved != 1 {
		t.Fatalf("after the merge: auto_resolved = %d, want 1 (%+v)", got.AutoResolved, got)
	}
	if openCount(t, store, "octocat/acme") != 0 {
		t.Fatal("a merged remediation PR must leave no open card")
	}

	all, err := store.List(attention.ListFilter{Repo: "octocat/acme", IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("stored requests = %d, want 1", len(all))
	}
	if all[0].Lifecycle.State != attention.StateAutoResolved {
		t.Errorf("state = %q, want %q — 'it fixed itself' and 'someone dealt with it' are different facts",
			all[0].Lifecycle.State, attention.StateAutoResolved)
	}
}

// TestStaleRemediationSweep_ProducerErrorLeavesTheCardAlone is the NEVER FATAL
// commitment measured where it counts: through the sweeper, against a card that
// already exists. A producer that returned an empty observation on a failed
// read would retract it as "condition_cleared".
func TestStaleRemediationSweep_ProducerErrorLeavesTheCardAlone(t *testing.T) {
	sec := enabled(staleAlert(7, 412, 10*24*time.Hour))
	prs := prTable(openPR(412))
	sw, store := newSweeper(t, &staleForge{sec: sec, prs: prs},
		&DependabotStaleRemediation{Now: func() time.Time { return fixedNow }})

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 1 {
		t.Fatalf("created = %d, want 1", res.Reconciled.Created)
	}

	// The security read now fails. The producer must be excluded from
	// reconciliation, not counted as having observed nothing.
	sec.res, sec.err = nil, errors.New("forge is having a day")
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Failed) != 1 || res.Failed[ProducerDependabotStaleRemediation] == "" {
		t.Fatalf("failed = %v, want the producer reported", res.Failed)
	}
	for _, name := range res.Evaluated {
		if name == ProducerDependabotStaleRemediation {
			t.Fatal("a producer that errored was listed as evaluated — its cards would auto-resolve")
		}
	}
	if res.Reconciled.AutoResolved != 0 {
		t.Errorf("auto_resolved = %d, want 0 — a transient read failure retracted a live card",
			res.Reconciled.AutoResolved)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Error("the card did not survive a producer error")
	}
}

// onlyOpen returns the single open request in the store, failing otherwise.
func onlyOpen(t *testing.T, s *attention.Store) *attention.DecisionRequest {
	t.Helper()
	reqs, err := s.List(attention.ListFilter{Repo: "octocat/acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("open requests = %d, want 1", len(reqs))
	}
	return &reqs[0]
}
