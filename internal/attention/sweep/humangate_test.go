package sweep

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// --- fakes ------------------------------------------------------------------

type gateForge struct{ prs *gatePRs }

func (f *gateForge) Issues() forge.IssueService      { return nil }
func (f *gateForge) PRs() forge.PRService            { return f.prs }
func (f *gateForge) Project() forge.ProjectService   { return nil }
func (f *gateForge) Board() forge.BoardService       { return nil }
func (f *gateForge) CI() forge.CIService             { return nil }
func (f *gateForge) Labels() forge.LabelService      { return nil }
func (f *gateForge) Rulesets() forge.RulesetService  { return nil }
func (f *gateForge) Security() forge.SecurityService { return nil }
func (f *gateForge) Auth() forge.AuthService         { return nil }
func (f *gateForge) Repo() forge.RepoService         { return nil }

type gatePRs struct {
	list     []types.PullRequest
	err      error
	sawState string
}

func (p *gatePRs) ListPRs(_ context.Context, _, _ string, state, _ string) ([]types.PullRequest, error) {
	p.sawState = state
	return p.list, p.err
}

func (p *gatePRs) GetPR(context.Context, string, string, int) (*types.PullRequest, error) {
	return nil, forge.ErrUnsupported
}
func (p *gatePRs) IteratePRs(context.Context, string, string, string, string) forge.Iterator[types.PullRequest] {
	return nil
}
func (p *gatePRs) CreatePR(context.Context, string, string, string, string, string) (*types.PullRequest, error) {
	return nil, forge.ErrUnsupported
}
func (p *gatePRs) UpdatePR(context.Context, string, forge.UpdatePROptions) (*types.PullRequest, error) {
	return nil, forge.ErrUnsupported
}
func (p *gatePRs) ClosePR(context.Context, string) error { return forge.ErrUnsupported }
func (p *gatePRs) MergePR(context.Context, string) error { return forge.ErrUnsupported }
func (p *gatePRs) MergePRWithStrategy(context.Context, string, string) (string, error) {
	return "", forge.ErrUnsupported
}
func (p *gatePRs) DeleteBranch(context.Context, string, string, string) error {
	return forge.ErrUnsupported
}
func (p *gatePRs) CreateEpicPR(context.Context, string, string, int, string, string, string) (*forgetypes.EpicPRResult, error) {
	return nil, forge.ErrUnsupported
}
func (p *gatePRs) MergeEpicPR(context.Context, string, string, string, string) error {
	return forge.ErrUnsupported
}

// greenPR is the baseline: open, not draft, checks passed. Each test mutates
// only the field under examination, so what makes a case pass or fail is
// visible in the case itself.
func greenPR(number int, mergeState, review string) types.PullRequest {
	return types.PullRequest{
		Number:           number,
		Title:            fmt.Sprintf("feat: thing %d", number),
		State:            "OPEN",
		URL:              fmt.Sprintf("https://forge/pr/%d", number),
		CheckStatus:      "SUCCESS",
		MergeStateStatus: mergeState,
		ReviewStatus:     review,
		CreatedAt:        fixedNow.Add(-26 * time.Hour).Format(time.RFC3339),
	}
}

func gateInput(prs *gatePRs, existing ...attention.DecisionRequest) Input {
	return Input{
		Repo:     "octocat/acme",
		Owner:    "octocat",
		Name:     "acme",
		Forge:    &gateForge{prs: prs},
		Existing: existing,
	}
}

func newHumanGate() *HumanGate {
	return &HumanGate{Now: func() time.Time { return fixedNow }}
}

func evaluateGate(t *testing.T, p *HumanGate, in Input) []attention.DecisionRequest {
	t.Helper()
	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return got
}

// --- tests ------------------------------------------------------------------

func TestHumanGate_ReviewRequiredRaisesApprove(t *testing.T) {
	p := newHumanGate()
	in := gateInput(&gatePRs{list: []types.PullRequest{
		greenPR(42, "BLOCKED", "REVIEW_REQUIRED"),
	}})

	got := evaluateGate(t, p, in)
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
	req := got[0]
	// approve, not unblock: the operator IS the missing step here, rather than
	// having to go do something before the merge can proceed.
	if req.Kind != attention.KindApprove {
		t.Errorf("kind = %q, want %q", req.Kind, attention.KindApprove)
	}
	if req.Severity != attention.SeverityBlockingRun {
		t.Errorf("severity = %q, want %q — one PR stalled is not the fleet stopped", req.Severity, attention.SeverityBlockingRun)
	}
	if !strings.Contains(req.Title, "review") {
		t.Errorf("title %q does not name the requirement", req.Title)
	}
	if req.Context.PR != 42 {
		t.Errorf("context PR = %d, want 42", req.Context.PR)
	}
	if req.Context.URL != "https://forge/pr/42" {
		t.Errorf("context URL = %q, want the PR URL", req.Context.URL)
	}
	// "how long it has been waiting" — 26h, rendered coarsely.
	if !strings.Contains(req.Body, "1d") {
		t.Errorf("body does not say how long it has waited: %s", req.Body)
	}
}

func TestHumanGate_MergeStateBlockersRaiseUnblockNamingTheReason(t *testing.T) {
	cases := []struct {
		name       string
		mergeState string
		wantIn     string
	}{
		{"conflict", "DIRTY", "conflict"},
		{"stale base", "BEHIND", "moved on"},
		{"protection", "BLOCKED", "branch protection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHumanGate()
			in := gateInput(&gatePRs{list: []types.PullRequest{greenPR(7, tc.mergeState, "APPROVED")}})

			got := evaluateGate(t, p, in)
			if len(got) != 1 {
				t.Fatalf("observations = %d, want 1", len(got))
			}
			if got[0].Kind != attention.KindUnblock {
				t.Errorf("kind = %q, want %q", got[0].Kind, attention.KindUnblock)
			}
			if !strings.Contains(got[0].Body, tc.wantIn) {
				t.Errorf("body does not name the blocker %q: %s", tc.wantIn, got[0].Body)
			}
		})
	}
}

func TestHumanGate_RedDraftAndPendingNeverRaise(t *testing.T) {
	cases := []struct {
		name string
		pr   types.PullRequest
		why  string
	}{
		{
			name: "red",
			pr: func() types.PullRequest {
				pr := greenPR(1, "BLOCKED", "REVIEW_REQUIRED")
				pr.CheckStatus = "FAILURE"
				return pr
			}(),
			why: "a red PR is the author's problem and already visible to them",
		},
		{
			name: "draft",
			pr: func() types.PullRequest {
				pr := greenPR(2, "BLOCKED", "REVIEW_REQUIRED")
				pr.IsDraft = true
				return pr
			}(),
			why: "a draft is not asking to be merged",
		},
		{
			name: "checks pending",
			pr: func() types.PullRequest {
				pr := greenPR(3, "UNSTABLE", "APPROVED")
				pr.CheckStatus = "PENDING"
				return pr
			}(),
			why: "it is still the pipeline's problem, not a human's",
		},
		{
			name: "no CI at all",
			pr: func() types.PullRequest {
				pr := greenPR(4, "BLOCKED", "REVIEW_REQUIRED")
				pr.CheckStatus = ""
				return pr
			}(),
			why: "an empty rollup is not evidence of green",
		},
		{
			name: "changes requested",
			pr:   greenPR(5, "BLOCKED", "CHANGES_REQUESTED"),
			why:  "the reviewer already told the author",
		},
		{
			name: "mergeable",
			pr:   greenPR(6, "CLEAN", "APPROVED"),
			why:  "nothing is blocking it",
		},
		{
			name: "mergeability unknown",
			pr:   greenPR(8, "UNKNOWN", "APPROVED"),
			why:  "the forge has not finished computing it; asking again next sweep is free",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHumanGate()
			got := evaluateGate(t, p, gateInput(&gatePRs{list: []types.PullRequest{tc.pr}}))
			if len(got) != 0 {
				t.Fatalf("observations = %d, want 0 — %s", len(got), tc.why)
			}
		})
	}
}

func TestHumanGate_DefersToTheRunScopedBranchProtectionCard(t *testing.T) {
	p := newHumanGate()
	// The scheduler already raised for PR 42 when a run punted on its merge.
	existing := attention.DecisionRequest{
		IdempotencyKey: "branch-protection:octocat/acme#7",
		Producer:       producerBranchProtection,
		Context:        attention.Context{Repo: "octocat/acme", Issue: 7, PR: 42},
		Lifecycle:      attention.Lifecycle{State: attention.StateOpen},
	}
	in := gateInput(&gatePRs{list: []types.PullRequest{
		greenPR(42, "BLOCKED", "REVIEW_REQUIRED"),
		greenPR(43, "BLOCKED", "REVIEW_REQUIRED"),
	}}, existing)

	got := evaluateGate(t, p, in)
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1 — PR 42 is already carded by the run-scoped producer", len(got))
	}
	if got[0].Context.PR != 43 {
		t.Errorf("carded PR %d, want 43", got[0].Context.PR)
	}
}

func TestHumanGate_ResolvedRunScopedCardStopsDeferring(t *testing.T) {
	p := newHumanGate()
	// A terminal card is not an active one: if the run-scoped request was
	// resolved and the PR is still blocked, someone should still be told.
	existing := attention.DecisionRequest{
		Producer:  producerBranchProtection,
		Context:   attention.Context{Repo: "octocat/acme", PR: 42},
		Lifecycle: attention.Lifecycle{State: attention.StateResolved},
	}
	in := gateInput(&gatePRs{list: []types.PullRequest{greenPR(42, "BLOCKED", "REVIEW_REQUIRED")}}, existing)

	got := evaluateGate(t, p, in)
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
}

func TestHumanGate_AboveTheCapOneAggregateReplacesTheIndividuals(t *testing.T) {
	p := &HumanGate{Now: func() time.Time { return fixedNow }, MaxIndividual: 3}
	var list []types.PullRequest
	for n := 1; n <= 4; n++ {
		list = append(list, greenPR(n, "BLOCKED", "REVIEW_REQUIRED"))
	}

	got := evaluateGate(t, p, gateInput(&gatePRs{list: list}))
	if len(got) != 1 {
		t.Fatalf("observations = %d, want exactly 1 aggregate — four backlog cards would bury the inbox", len(got))
	}
	req := got[0]
	if !strings.Contains(req.IdempotencyKey, "backlog") {
		t.Errorf("idempotency key = %q, want the aggregate key", req.IdempotencyKey)
	}
	for n := 1; n <= 4; n++ {
		if !strings.Contains(req.Body, fmt.Sprintf("#%d", n)) {
			t.Errorf("aggregate body omits PR #%d: %s", n, req.Body)
		}
	}
}

func TestHumanGate_AtTheCapIndividualsAreKept(t *testing.T) {
	p := &HumanGate{Now: func() time.Time { return fixedNow }, MaxIndividual: 3}
	var list []types.PullRequest
	for n := 1; n <= 3; n++ {
		list = append(list, greenPR(n, "BLOCKED", "REVIEW_REQUIRED"))
	}

	got := evaluateGate(t, p, gateInput(&gatePRs{list: list}))
	if len(got) != 3 {
		t.Fatalf("observations = %d, want 3 individual cards at the cap", len(got))
	}
}

func TestHumanGate_AggregateFingerprintTracksCompositionNotCount(t *testing.T) {
	p := &HumanGate{Now: func() time.Time { return fixedNow }, MaxIndividual: 1}
	before := evaluateGate(t, p, gateInput(&gatePRs{list: []types.PullRequest{
		greenPR(1, "BLOCKED", "REVIEW_REQUIRED"),
		greenPR(2, "BLOCKED", "REVIEW_REQUIRED"),
	}}))
	// One merges, another blocks. The count is identical; the backlog is not.
	after := evaluateGate(t, p, gateInput(&gatePRs{list: []types.PullRequest{
		greenPR(2, "BLOCKED", "REVIEW_REQUIRED"),
		greenPR(3, "BLOCKED", "REVIEW_REQUIRED"),
	}}))

	if before[0].Fingerprint == after[0].Fingerprint {
		t.Fatal("fingerprint unchanged after the backlog composition changed — a count-based fingerprint hides a swap")
	}
}

func TestHumanGate_FingerprintMovesWhenTheGateChanges(t *testing.T) {
	p := newHumanGate()
	review := evaluateGate(t, p, gateInput(&gatePRs{list: []types.PullRequest{greenPR(9, "BLOCKED", "REVIEW_REQUIRED")}}))
	conflict := evaluateGate(t, p, gateInput(&gatePRs{list: []types.PullRequest{greenPR(9, "DIRTY", "APPROVED")}}))

	if review[0].IdempotencyKey != conflict[0].IdempotencyKey {
		t.Error("idempotency key moved with the gate — the card would duplicate instead of updating")
	}
	if review[0].Fingerprint == conflict[0].Fingerprint {
		t.Error("fingerprint unchanged when the blocker changed from review to conflict — the operator would never be re-alerted")
	}
}

func TestHumanGate_FingerprintIgnoresWaitTime(t *testing.T) {
	early := &HumanGate{Now: func() time.Time { return fixedNow }}
	late := &HumanGate{Now: func() time.Time { return fixedNow.Add(72 * time.Hour) }}
	in := func() Input {
		return gateInput(&gatePRs{list: []types.PullRequest{greenPR(9, "BLOCKED", "REVIEW_REQUIRED")}})
	}

	a := evaluateGate(t, early, in())
	b := evaluateGate(t, late, in())
	if a[0].Fingerprint != b[0].Fingerprint {
		t.Errorf("fingerprint moved with elapsed time: %q vs %q", a[0].Fingerprint, b[0].Fingerprint)
	}
}

func TestHumanGate_MergedPRAutoResolvesByObservingNothing(t *testing.T) {
	p := newHumanGate()
	// The PR left the open list entirely. An empty slice is the assertion that
	// clears the card; an error would leave it up forever.
	got := evaluateGate(t, p, gateInput(&gatePRs{list: nil}))
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0", len(got))
	}
}

func TestHumanGate_OnlyOpenPRsAreListed(t *testing.T) {
	p := newHumanGate()
	prs := &gatePRs{list: []types.PullRequest{greenPR(1, "CLEAN", "APPROVED")}}
	evaluateGate(t, p, gateInput(prs))
	if prs.sawState != "OPEN" {
		t.Errorf("listed state %q, want OPEN", prs.sawState)
	}
}

func TestHumanGate_ForgeErrorPropagatesForSweepToHandle(t *testing.T) {
	p := newHumanGate()
	_, err := p.Evaluate(context.Background(), gateInput(&gatePRs{err: forge.ErrUnauthorized}))
	if err == nil {
		t.Fatal("Evaluate returned nil error on an auth failure — the sweep would retract live cards")
	}
	if !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("err = %v, want it to wrap forge.ErrUnauthorized", err)
	}
}

func TestHumanGate_RegisteredInTheDefaultRegistry(t *testing.T) {
	var found bool
	for _, p := range Default.Producers() {
		if p.Name() == ProducerHumanGate {
			found = true
		}
	}
	if !found {
		t.Fatalf("%q is not in the default registry — `nightgauge attention sweep` would never run it", ProducerHumanGate)
	}
}

// --- pr.updateBranch on the `behind` gate (#1575) ----------------------------

// Exactly one of the four gate codes is repairable by a registered verb, and
// the producer must be able to tell them apart. A card that offered the repair
// on `conflict` would be the dead affordance Invariant 3 forbids: the operator
// clicks, the forge refuses the merge, and the next card is one they have
// learned to distrust.
func TestHumanGate_OnlyTheBehindGateCarriesTheRepairOption(t *testing.T) {
	cases := []struct {
		name       string
		mergeState string
		review     string
		wantRepair bool
	}{
		{"behind", "BEHIND", "APPROVED", true},
		{"conflict", "DIRTY", "APPROVED", false},
		{"review required", "BLOCKED", "REVIEW_REQUIRED", false},
		{"branch protection", "BLOCKED", "APPROVED", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHumanGate()
			in := gateInput(&gatePRs{list: []types.PullRequest{greenPR(9, tc.mergeState, tc.review)}})

			got := evaluateGate(t, p, in)
			if len(got) != 1 {
				t.Fatalf("observations = %d, want 1", len(got))
			}
			opts := got[0].Options

			var repair *attention.Option
			for i := range opts {
				if opts[i].Verb == attention.VerbPRUpdateBranch {
					repair = &opts[i]
				}
			}
			if !tc.wantRepair {
				if repair != nil {
					t.Fatalf("gate %q must not offer pr.updateBranch — nothing in the registry repairs it", tc.name)
				}
				if len(opts) != 1 || opts[0].Verb != attention.VerbNoop {
					t.Fatalf("gate %q options = %+v, want dismiss only", tc.name, opts)
				}
				return
			}

			if repair == nil {
				t.Fatalf("the behind gate must offer pr.updateBranch, got %+v", opts)
			}
			// PRIMARY and FIRST: the operator was clicking dismiss on this card
			// to mean "yes, update it", so the update is the default reading of
			// the card, not an alternative buried under it.
			if opts[0].Verb != attention.VerbPRUpdateBranch {
				t.Errorf("options[0].Verb = %q, want the repair first", opts[0].Verb)
			}
			if repair.Style != attention.StylePrimary {
				t.Errorf("repair style = %q, want %q", repair.Style, attention.StylePrimary)
			}
			// NO ARGS. The executor reads both the repo and the PR number from
			// the request's Context; an args map here would be the first step
			// toward a surface naming its own pull request.
			if len(repair.Args) != 0 {
				t.Errorf("repair args = %v, want none", repair.Args)
			}
			// dismiss survives as the secondary — "off on purpose" is still a
			// legitimate answer to a card.
			if len(opts) != 2 || opts[1].Verb != attention.VerbNoop || opts[1].Style != attention.StyleDefault {
				t.Errorf("options = %+v, want dismiss as the secondary", opts)
			}
		})
	}
}

// The card the producer emits and the executor that runs its button must agree
// about the target. The producer supplies both coordinates through Context; the
// executor reads them from there and nowhere else, so a card built here
// executes against exactly the PR it names.
func TestHumanGate_BehindCardExecutesAgainstThePRItNames(t *testing.T) {
	p := newHumanGate()
	in := gateInput(&gatePRs{list: []types.PullRequest{greenPR(1546, "BEHIND", "APPROVED")}})

	got := evaluateGate(t, p, in)
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
	card := got[0]
	if card.Context.Repo != "octocat/acme" || card.Context.PR != 1546 {
		t.Fatalf("context = %q#%d, want octocat/acme#1546", card.Context.Repo, card.Context.PR)
	}

	var updated []string
	err := attention.ExecuteUpdatePRBranch(context.Background(),
		updaterFunc(func(_ context.Context, owner, repo string, number int) error {
			updated = append(updated, fmt.Sprintf("%s/%s#%d", owner, repo, number))
			return nil
		}),
		&card, card.Options[0], []string{"octocat/acme"})
	if err != nil {
		t.Fatalf("ExecuteUpdatePRBranch: %v", err)
	}
	if len(updated) != 1 || updated[0] != "octocat/acme#1546" {
		t.Fatalf("forge saw %v, want [octocat/acme#1546]", updated)
	}

	// And the producer's matcher agrees with the executor's: a repo the sweep
	// would card but configuration does not cover must be refused rather than
	// quietly executed, which is the dead-affordance failure in reverse.
	err = attention.ExecuteUpdatePRBranch(context.Background(),
		updaterFunc(func(context.Context, string, string, int) error {
			t.Fatal("the forge must not be reached for an unconfigured repo")
			return nil
		}),
		&card, card.Options[0], []string{"octocat/other"})
	if !errors.Is(err, attention.ErrVerbTargetNotConfigured) {
		t.Fatalf("error = %v, want ErrVerbTargetNotConfigured", err)
	}
}

type updaterFunc func(ctx context.Context, owner, repo string, number int) error

func (f updaterFunc) UpdatePRBranch(ctx context.Context, owner, repo string, number int) error {
	return f(ctx, owner, repo, number)
}
