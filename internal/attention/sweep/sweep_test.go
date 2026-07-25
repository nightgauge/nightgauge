package sweep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/internal/trace"
)

// --- fake forge client ------------------------------------------------------
//
// The sweep is forge-neutral by construction: it hands producers a
// forge.ForgeClient and never reaches for a GitHub client. The fake below is
// the whole point of the AC "no live API calls in CI" — a producer under test
// reads its CI status from here, and the same producer would run unchanged
// against a GitLab adapter.

type fakeForge struct {
	ci *fakeCI
}

func (f *fakeForge) Issues() forge.IssueService     { return nil }
func (f *fakeForge) PRs() forge.PRService           { return nil }
func (f *fakeForge) Project() forge.ProjectService  { return nil }
func (f *fakeForge) Board() forge.BoardService      { return nil }
func (f *fakeForge) CI() forge.CIService            { return f.ci }
func (f *fakeForge) Labels() forge.LabelService     { return nil }
func (f *fakeForge) Rulesets() forge.RulesetService { return nil }
func (f *fakeForge) Auth() forge.AuthService        { return nil }
func (f *fakeForge) Repo() forge.RepoService        { return nil }

// fakeCI serves canned check runs for a ref and counts calls, so a test can
// assert the sweep's forge traffic is bounded.
type fakeCI struct {
	runs  []forgetypes.CheckDetail
	err   error
	calls int
}

func (c *fakeCI) GetIndividualCheckRuns(_ context.Context, _, _, _ string) ([]forgetypes.CheckDetail, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.runs, nil
}

func (c *fakeCI) GetCheckStatus(context.Context, string, string, int) (*forgetypes.CheckStatus, error) {
	return nil, forge.ErrUnsupported
}
func (c *fakeCI) GetRequiredCheckNames(context.Context, string, string, string) ([]string, error) {
	return nil, forge.ErrUnsupported
}
func (c *fakeCI) WaitForChecks(context.Context, string, string, int, forgetypes.WaitConfig) (*forgetypes.CheckStatus, error) {
	return nil, forge.ErrUnsupported
}
func (c *fakeCI) GetRunLogs(context.Context, string, string, int64) (*forgetypes.CIRunLog, error) {
	return nil, forge.ErrUnsupported
}

// --- fake producers ---------------------------------------------------------

// scriptedProducer returns whatever it is told to, so registration and
// reconciliation can be tested without shipping a real producer.
type scriptedProducer struct {
	name    string
	reqs    []attention.DecisionRequest
	err     error
	calls   int
	sawRepo Input
}

func (p *scriptedProducer) Name() string { return p.name }

func (p *scriptedProducer) Evaluate(_ context.Context, in Input) ([]attention.DecisionRequest, error) {
	p.calls++
	p.sawRepo = in
	if p.err != nil {
		return nil, p.err
	}
	return append([]attention.DecisionRequest(nil), p.reqs...), nil
}

// ciProducer is a minimal REAL producer written against the forge abstraction,
// present to prove the extension point is usable by #90/#91 without changing
// the sweep.
type ciProducer struct{ branch string }

func (p *ciProducer) Name() string { return "test-default-branch" }

func (p *ciProducer) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	runs, err := in.Forge.CI().GetIndividualCheckRuns(ctx, in.Owner, in.Name, p.branch)
	if err != nil {
		return nil, err
	}
	var failing string
	for _, r := range runs {
		if r.Required && r.Conclusion == "failure" {
			failing = r.Name
			break
		}
	}
	if failing == "" {
		return nil, nil
	}
	return []attention.DecisionRequest{observation(
		fmt.Sprintf("%s:%s:%s", p.Name(), in.Repo, p.branch),
		"check:"+failing+"=failure",
	)}, nil
}

func observation(key, fingerprint string) attention.DecisionRequest {
	return attention.DecisionRequest{
		IdempotencyKey: key,
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingFleet,
		Title:          "a required check is failing on the default branch",
		Body:           "every open PR in this repo is unmergeable until it goes green",
		Fingerprint:    fingerprint,
		Options:        []attention.Option{{ID: "wait", Label: "Wait — human fixing", Verb: attention.VerbNoop}},
		DefaultAction:  attention.ExpireNoop,
	}
}

func newSweeper(t *testing.T, client forge.ForgeClient, producers ...Producer) (*Sweeper, *attention.Store) {
	t.Helper()
	root := t.TempDir()
	store := attention.New(root)
	reg := NewRegistry()
	for _, p := range producers {
		reg.Register(p)
	}
	return &Sweeper{
		Store:         store,
		Registry:      reg,
		Forge:         client,
		WorkspaceRoot: root,
		Logf:          func(string, ...any) {},
	}, store
}

func openCount(t *testing.T, s *attention.Store, repo string) int {
	t.Helper()
	reqs, err := s.List(attention.ListFilter{Repo: repo})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(reqs)
}

// --- tests ------------------------------------------------------------------

// TestSweepEvaluatesRegisteredProducersAndReconciles is the #89 headline: a
// repo can be evaluated for blockers with no run in flight and no platform.
func TestSweepEvaluatesRegisteredProducersAndReconciles(t *testing.T) {
	ci := &fakeCI{runs: []forgetypes.CheckDetail{
		{Name: "Security & license gates", Status: "completed", Conclusion: "failure", Required: true},
	}}
	sw, store := newSweeper(t, &fakeForge{ci: ci}, &ciProducer{branch: "main"})

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !res.OK() {
		t.Fatalf("a healthy sweep must report OK, got %+v", res)
	}
	if res.Reconciled.Created != 1 {
		t.Fatalf("want the failing required check raised once, got %+v", res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("the card must be readable from the store with no run in flight")
	}
	if ci.calls == 0 {
		t.Error("the producer must reach the forge through the abstraction")
	}
}

// TestProducerRegistrationNeedsNoSweepChanges — the extension point is an
// interface, so producers that do not exist yet already work.
func TestProducerRegistrationNeedsNoSweepChanges(t *testing.T) {
	a := &scriptedProducer{name: "producer-a", reqs: []attention.DecisionRequest{observation("a:1", "fa")}}
	b := &scriptedProducer{name: "producer-b", reqs: []attention.DecisionRequest{observation("b:1", "fb")}}
	sw, store := newSweeper(t, &fakeForge{ci: &fakeCI{}}, a, b)

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("every registered producer must be evaluated once: a=%d b=%d", a.calls, b.calls)
	}
	if len(res.Evaluated) != 2 || res.Reconciled.Created != 2 {
		t.Errorf("both producers' conditions must be raised: %+v", res)
	}
	if openCount(t, store, "octocat/acme") != 2 {
		t.Error("both cards must be in the store")
	}
	if a.sawRepo.Owner != "octocat" || a.sawRepo.Name != "acme" || a.sawRepo.Forge == nil {
		t.Errorf("a producer must receive the split repo and a forge client, got %+v", a.sawRepo)
	}
	// The producer name is stamped by the sweep, so a producer cannot
	// accidentally raise under another's identity.
	for _, o := range res.Reconciled.Outcomes {
		if o.Producer != "producer-a" && o.Producer != "producer-b" {
			t.Errorf("unexpected producer %q on outcome", o.Producer)
		}
	}
}

// TestRepeatedSweepsOverAnUnchangedRepoAreANoOp — the #89 idempotency AC end to
// end, through real producers rather than a hand-built reconcile input.
func TestRepeatedSweepsOverAnUnchangedRepoAreANoOp(t *testing.T) {
	ci := &fakeCI{runs: []forgetypes.CheckDetail{
		{Name: "build", Conclusion: "failure", Required: true},
	}}
	sw, store := newSweeper(t, &fakeForge{ci: ci}, &ciProducer{branch: "main"})

	first, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !first.Reconciled.Changed() {
		t.Fatal("the first sweep must raise the blocker")
	}
	for i := 0; i < 4; i++ {
		again, err := sw.Sweep(context.Background(), "octocat/acme")
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if again.Reconciled.Changed() {
			t.Fatalf("sweep %d over an unchanged repo must be a no-op, got %+v", i+2, again.Reconciled)
		}
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Error("repeated sweeps must not duplicate a card")
	}
}

// TestClearedConditionIsRetractedByTheNextSweep — the branch goes green.
func TestClearedConditionIsRetractedByTheNextSweep(t *testing.T) {
	ci := &fakeCI{runs: []forgetypes.CheckDetail{{Name: "build", Conclusion: "failure", Required: true}}}
	sw, store := newSweeper(t, &fakeForge{ci: ci}, &ciProducer{branch: "main"})

	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	ci.runs = []forgetypes.CheckDetail{{Name: "build", Conclusion: "success", Required: true}}

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 1 {
		t.Fatalf("a cleared condition must be retracted, got %+v", res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 0 {
		t.Error("the inbox must be empty once the repo is healthy again")
	}
}

// TestAProducerFailureLeavesItsCardsAloneAndIsNeverFatal — degrade, don't lie.
func TestAProducerFailureLeavesItsCardsAloneAndIsNeverFatal(t *testing.T) {
	flaky := &scriptedProducer{name: "flaky", reqs: []attention.DecisionRequest{observation("flaky:1", "f1")}}
	steady := &scriptedProducer{name: "steady", reqs: []attention.DecisionRequest{observation("steady:1", "s1")}}
	sw, store := newSweeper(t, &fakeForge{ci: &fakeCI{}}, flaky, steady)

	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("seed sweep: %v", err)
	}
	if openCount(t, store, "octocat/acme") != 2 {
		t.Fatal("expected both cards seeded")
	}

	flaky.err = errors.New("connection reset by peer")
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("a producer failure must never be fatal to the sweep, got error: %v", err)
	}
	if _, ok := res.Failed["flaky"]; !ok {
		t.Error("the failure must be reported, not swallowed")
	}
	if res.OK() {
		t.Error("a degraded sweep must not report OK")
	}
	if res.Reconciled.AutoResolved != 0 {
		t.Error("a producer that could not look must not retract its own cards")
	}
	if openCount(t, store, "octocat/acme") != 2 {
		t.Error("both cards must survive a partial sweep")
	}
	if len(res.Evaluated) != 1 || res.Evaluated[0] != "steady" {
		t.Errorf("only the producer that observed the repo counts as evaluated, got %v", res.Evaluated)
	}
}

// TestAuthAndRateLimitFailuresSkipTheWholeSweep — a repo-wide failure means no
// producer's view can be trusted, so nothing is reconciled at all.
func TestAuthAndRateLimitFailuresSkipTheWholeSweep(t *testing.T) {
	for name, cause := range map[string]error{
		"rate limited":      fmt.Errorf("checks: %w", forge.ErrRateLimited),
		"token expired":     fmt.Errorf("checks: %w", forge.ErrUnauthorized),
		"permission denied": fmt.Errorf("checks: %w", forge.ErrPermissionDenied),
		"deadline":          fmt.Errorf("checks: %w", context.DeadlineExceeded),
	} {
		t.Run(name, func(t *testing.T) {
			blocked := &scriptedProducer{name: "aaa-blocked"}
			other := &scriptedProducer{name: "zzz-other", reqs: []attention.DecisionRequest{observation("o:1", "f")}}
			sw, store := newSweeper(t, &fakeForge{ci: &fakeCI{}}, blocked, other)

			if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
				t.Fatalf("seed sweep: %v", err)
			}
			seeded := openCount(t, store, "octocat/acme")

			blocked.err = cause
			res, err := sw.Sweep(context.Background(), "octocat/acme")
			if err != nil {
				t.Fatalf("a degraded sweep must not error, got: %v", err)
			}
			if !res.Skipped || res.SkipReason == "" {
				t.Fatalf("want a logged skip, got %+v", res)
			}
			if res.OK() {
				t.Error("a skipped sweep must not report OK")
			}
			if res.Reconciled.Changed() {
				t.Error("a skipped sweep must not reconcile anything")
			}
			if openCount(t, store, "octocat/acme") != seeded {
				t.Error("a skipped sweep must leave every existing card exactly as it was")
			}
		})
	}
}

// TestSweepWithNoRegisteredProducersIsAHarmlessNoOp — a build with no
// repo-scoped producers must not fail, and must not touch anything.
func TestSweepWithNoRegisteredProducersIsAHarmlessNoOp(t *testing.T) {
	sw, store := newSweeper(t, &fakeForge{ci: &fakeCI{}})
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !res.OK() || res.Reconciled.Changed() {
		t.Errorf("want a clean no-op, got %+v", res)
	}
	if openCount(t, store, "octocat/acme") != 0 {
		t.Error("a no-op sweep must write nothing")
	}
}

// TestSweepRejectsCallerMistakes — misconfiguration is an error, unlike a
// runtime degradation.
func TestSweepRejectsCallerMistakes(t *testing.T) {
	good := &scriptedProducer{name: "p"}
	t.Run("malformed repo", func(t *testing.T) {
		sw, _ := newSweeper(t, &fakeForge{ci: &fakeCI{}}, good)
		for _, spec := range []string{"", "acme", "octocat/", "/acme", "a/b/c"} {
			if _, err := sw.Sweep(context.Background(), spec); err == nil {
				t.Errorf("expected %q to be rejected", spec)
			}
		}
	})
	t.Run("no forge client", func(t *testing.T) {
		sw, _ := newSweeper(t, nil, good)
		if _, err := sw.Sweep(context.Background(), "octocat/acme"); err == nil {
			t.Error("expected a missing forge client to be rejected")
		}
	})
	t.Run("no store", func(t *testing.T) {
		sw := &Sweeper{Forge: &fakeForge{ci: &fakeCI{}}}
		if _, err := sw.Sweep(context.Background(), "octocat/acme"); err == nil {
			t.Error("expected a missing store to be rejected")
		}
	})
}

// TestRegistryDeduplicatesByNameAndOrdersStably — a producer registered twice
// must not double-evaluate its condition.
func TestRegistryDeduplicatesByNameAndOrdersStably(t *testing.T) {
	reg := NewRegistry()
	first := &scriptedProducer{name: "zeta"}
	replacement := &scriptedProducer{name: "zeta"}
	reg.Register(first)
	reg.Register(&scriptedProducer{name: "alpha"})
	reg.Register(replacement)

	got := reg.Producers()
	if len(got) != 2 {
		t.Fatalf("re-registering a name must replace, not append: got %d", len(got))
	}
	if got[0].Name() != "alpha" || got[1].Name() != "zeta" {
		t.Errorf("producers must come back in a stable order, got %q,%q", got[0].Name(), got[1].Name())
	}
	if got[1] != replacement {
		t.Error("the later registration must win")
	}
	reg.Register(nil)
	if len(reg.Producers()) != 2 {
		t.Error("registering nil must be ignored")
	}
}

// TestSweepBoundsItsForgeTraffic — one sweep evaluates each producer at most
// once, so cost scales with registrations, not with how often the sweep runs.
func TestSweepBoundsItsForgeTraffic(t *testing.T) {
	ci := &fakeCI{runs: []forgetypes.CheckDetail{{Name: "build", Conclusion: "failure", Required: true}}}
	sw, _ := newSweeper(t, &fakeForge{ci: ci}, &ciProducer{branch: "main"})
	sw.Timeout = 5 * time.Second

	const sweeps = 3
	for i := 0; i < sweeps; i++ {
		if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
			t.Fatalf("Sweep: %v", err)
		}
	}
	if ci.calls != sweeps {
		t.Errorf("want one forge call per producer per sweep, got %d over %d sweeps", ci.calls, sweeps)
	}
}

// TestChangedOutcomesAreWrittenToTheDecisionTrace — an auto-resolution is
// replayable like any other resolution.
func TestChangedOutcomesAreWrittenToTheDecisionTrace(t *testing.T) {
	ci := &fakeCI{runs: []forgetypes.CheckDetail{{Name: "build", Conclusion: "failure", Required: true}}}
	sw, _ := newSweeper(t, &fakeForge{ci: ci}, &ciProducer{branch: "main"})

	raise, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if raise.SweepID == "" {
		t.Fatal("a sweep that changed the store must be traceable")
	}
	assertTracedTransition(t, sw.WorkspaceRoot, raise.SweepID, attention.ActionCreated)

	unchanged, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if unchanged.SweepID != "" {
		t.Error("a no-op sweep must not litter the trace directory")
	}

	ci.runs = nil
	cleared, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if cleared.SweepID == "" {
		t.Fatal("an auto-resolution must be written to the decision trace")
	}
	if cleared.SweepID == raise.SweepID {
		t.Error("each sweep must get its own trace identity")
	}
	assertTracedTransition(t, sw.WorkspaceRoot, cleared.SweepID, attention.ActionAutoResolved)
}

// assertTracedTransition fails unless the sweep's trace carries a
// decision_request event for the given transition.
func assertTracedTransition(t *testing.T, root, sweepID, transition string) {
	t.Helper()
	events, err := trace.ReadRun(root, sweepID)
	if err != nil {
		t.Fatalf("ReadRun(%s): %v", sweepID, err)
	}
	for _, e := range events {
		if e.Kind != trace.KindDecisionRequest {
			continue
		}
		var payload trace.DecisionRequestPayload
		raw, err := json.Marshal(e.Payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload.Transition == transition {
			return
		}
	}
	t.Errorf("no %q decision_request event in trace %s (%d events)", transition, sweepID, len(events))
}
