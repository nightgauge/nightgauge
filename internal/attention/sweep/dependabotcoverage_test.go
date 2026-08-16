package sweep

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// --- fakes ------------------------------------------------------------------
//
// Modelled on dependabotalerts_test.go (#343): the producer is exercised
// through the forge INTERFACES only, so the same producer runs against a GitLab
// adapter without a line changing. The per-repo answer map is the one addition
// #344 needs — a workspace producer's whole subject is a list of repos, and its
// interesting cases are the ones where the repos answer differently.

type covForge struct{ sec forge.SecurityService }

func (f *covForge) Issues() forge.IssueService      { return nil }
func (f *covForge) PRs() forge.PRService            { return nil }
func (f *covForge) Project() forge.ProjectService   { return nil }
func (f *covForge) Board() forge.BoardService       { return nil }
func (f *covForge) CI() forge.CIService             { return nil }
func (f *covForge) Labels() forge.LabelService      { return nil }
func (f *covForge) Rulesets() forge.RulesetService  { return nil }
func (f *covForge) Auth() forge.AuthService         { return nil }
func (f *covForge) Repo() forge.RepoService         { return nil }
func (f *covForge) Security() forge.SecurityService { return f.sec }

type covAnswer struct {
	res *forgetypes.SecurityAlerts
	err error
}

type covSecurity struct {
	answers map[string]covAnswer
	// calls counts requests per repo, so "one forge request per repo per
	// sweep" is a pinned property rather than an aspiration — this producer
	// spends the shared 30-second budget linearly in the repo list.
	calls map[string]int
}

// ListOpenAlerts answers case-insensitively, as the forge does: `acme/api` and
// `Acme/API` are one repository, and a fixture that pretended otherwise would
// hide the very duplicate this producer has to collapse.
func (s *covSecurity) ListOpenAlerts(_ context.Context, owner, name string) (*forgetypes.SecurityAlerts, error) {
	spec := strings.ToLower(owner + "/" + name)
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[spec]++
	a, ok := s.answers[spec]
	if !ok {
		return nil, fmt.Errorf("test fixture has no scripted answer for %s", spec)
	}
	return a.res, a.err
}

func (s *covSecurity) total() int {
	n := 0
	for _, c := range s.calls {
		n += c
	}
	return n
}

// covEnabled is a repo the forge IS scanning. openAlerts>0 is the case that
// must still raise no coverage card: what is in the alert list belongs to
// dependabot-alerts, and an enabled repo with advisories is a success here.
func covEnabled(openAlerts int) covAnswer {
	alerts := make([]forgetypes.SecurityAlert, 0, openAlerts)
	for i := 0; i < openAlerts; i++ {
		alerts = append(alerts, forgetypes.SecurityAlert{Number: i + 1, Severity: forgetypes.AlertSeverityHigh})
	}
	return covAnswer{res: &forgetypes.SecurityAlerts{
		Status: forgetypes.SecurityAlertsEnabled, Alerts: alerts, TotalOpen: openAlerts,
	}}
}

// covTruncated is an enabled repo holding more alerts than one request returns.
// Coverage is still definitively answered: truncation qualifies the LIST.
func covTruncated() covAnswer {
	a := covEnabled(forge.MaxSecurityAlertsPerRequest)
	a.res.TotalOpen = forge.MaxSecurityAlertsPerRequest + 40
	a.res.Truncated = true
	return a
}

// covDisabled is THE condition: a successful observation that nobody is
// looking.
func covDisabled() covAnswer {
	return covAnswer{res: &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsDisabled}}
}

func covFailed(err error) covAnswer { return covAnswer{err: err} }

func covSec(answers map[string]covAnswer) *covSecurity {
	return &covSecurity{answers: answers, calls: map[string]int{}}
}

func covInput(sec forge.SecurityService, configured []string, existing ...attention.DecisionRequest) WorkspaceInput {
	return WorkspaceInput{
		ConfiguredRepos: normalizeRepos(configured),
		Forge:           &covForge{sec: sec},
		Existing:        existing,
	}
}

// covOpenCard is a minimal open card as an earlier sweep would have left it in
// the store — enough for blockedByUnobserved to recognise it as this
// producer's.
func covOpenCard(repo string) attention.DecisionRequest {
	return attention.DecisionRequest{
		IdempotencyKey: ProducerDependabotCoverage + ":" + repo,
		Producer:       ProducerDependabotCoverage,
		Fingerprint:    coverageFingerprint,
		Standing:       true,
		Context:        attention.Context{Repo: repo},
		Lifecycle:      attention.Lifecycle{State: attention.StateOpen},
	}
}

func repoSpecs(reqs []attention.DecisionRequest) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Context.Repo)
	}
	sort.Strings(out)
	return out
}

// --- the three outcomes -----------------------------------------------------

// THE acceptance criterion: a configured repo that cannot report alerts gets
// exactly one card, and the card is about that repo.
func TestDependabotCoverage_DisabledRepo_RaisesExactlyOneCard(t *testing.T) {
	sec := covSec(map[string]covAnswer{
		"acme/web": covEnabled(0),
		"acme/api": covDisabled(),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(), covInput(sec, []string{"acme/web", "acme/api"}))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 card, got %d: %v", len(got), repoSpecs(got))
	}
	r := got[0]

	// Context.Repo is what SweepWorkspace groups on before reconciling; a card
	// without it is dropped with a warning and never reaches the store.
	if r.Context.Repo != "acme/api" {
		t.Errorf("Context.Repo = %q, want the repo the card is about (acme/api)", r.Context.Repo)
	}
	if r.IdempotencyKey != ProducerDependabotCoverage+":acme/api" {
		t.Errorf("IdempotencyKey = %q", r.IdempotencyKey)
	}
	if r.Kind != attention.KindApprove {
		t.Errorf("Kind = %q, want %q — the fleet CAN perform this repair, so it is not a handoff", r.Kind, attention.KindApprove)
	}
	if r.Severity != attention.SeverityFYI {
		t.Errorf("Severity = %q — scanning being off blocks no run, and 'off on purpose' must stay dismissable", r.Severity)
	}
	if r.Fingerprint == "" {
		t.Error("a standing request without a fingerprint re-alerts on every sweep")
	}

	// The affordance is the point of #344: unlike the alert card (#343), which
	// deliberately ships only a dismiss, this condition is repairable.
	enable := r.FindOption("enable")
	if enable == nil {
		t.Fatalf("card must offer a repair option, got %+v", r.Options)
	}
	if enable.Verb != attention.VerbDependabotEnableAlerts {
		t.Errorf("repair verb = %q, want %q", enable.Verb, attention.VerbDependabotEnableAlerts)
	}
	if len(enable.Args) != 0 {
		t.Errorf("the repair option must carry NO args — the target comes from Context.Repo; got %v", enable.Args)
	}
	if dismiss := r.FindOption("dismiss"); dismiss == nil || dismiss.Verb != attention.VerbNoop {
		t.Errorf("card must also offer an honest dismiss, got %+v", r.Options)
	}
}

// Covered and clean is a SUCCESS, and so is covered and full of advisories —
// what is in the alert list is dependabot-alerts' subject, not this producer's.
func TestDependabotCoverage_EnabledRepos_RaiseNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer covAnswer
	}{
		{"enabled and clean", covEnabled(0)},
		{"enabled with open alerts", covEnabled(3)},
		{"enabled with a truncated alert page", covTruncated()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := covSec(map[string]covAnswer{"acme/web": tc.answer})
			p := &DependabotCoverage{Logf: func(string, ...any) {}}

			got, err := p.Evaluate(context.Background(), covInput(sec, []string{"acme/web"}))
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("a scanned repo must raise no coverage card, got %v", repoSpecs(got))
			}
		})
	}
}

// A read failure is UNOBSERVED, not blind. Carding a repo for being unreadable
// states a different (and usually transient) fact from the one the card claims.
func TestDependabotCoverage_ReadFailure_RaisesNoCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"permission denied", fmt.Errorf("read alerts: %w", forge.ErrPermissionDenied)},
		{"unauthorized", fmt.Errorf("read alerts: %w", forge.ErrUnauthorized)},
		{"rate limited", fmt.Errorf("read alerts: %w", forge.ErrRateLimited)},
		// A forge with no security surface at all: carding every one of its
		// repos as "scanning is off" would attach a repair button that cannot
		// possibly fire.
		{"forge does not support security alerts", fmt.Errorf("gitlab: %w", forge.ErrUnsupported)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := covSec(map[string]covAnswer{"acme/web": covFailed(tc.err)})
			p := &DependabotCoverage{Logf: func(string, ...any) {}}

			got, err := p.Evaluate(context.Background(), covInput(sec, []string{"acme/web"}))
			if err != nil {
				t.Fatalf("a repo with no card to lose must not fail the pass: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("an unreadable repo must not be carded as blind, got %v", repoSpecs(got))
			}
		})
	}
}

// An unknown status is not a licence to guess "disabled".
func TestDependabotCoverage_UnknownStatus_IsUnobserved(t *testing.T) {
	sec := covSec(map[string]covAnswer{
		"acme/web": {res: &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsStatus("partially_on")}},
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(), covInput(sec, []string{"acme/web"}))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an unrecognised status must not be carded as disabled, got %v", repoSpecs(got))
	}
}

// THE NEVER FATAL contract, at its sharpest: a repo that could not be read and
// ALREADY holds a card cannot be silently dropped from the observation, because
// SweepWorkspace's AutoResolveUnobserved pass reads its absence as "the
// condition cleared". Declining the whole pass is the only honest option.
func TestDependabotCoverage_ReadFailureWithOpenCard_RefusesToReportAPartialView(t *testing.T) {
	sec := covSec(map[string]covAnswer{
		"acme/web": covFailed(fmt.Errorf("read alerts: %w", forge.ErrPermissionDenied)),
		"acme/api": covDisabled(),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(),
		covInput(sec, []string{"acme/web", "acme/api"}, covOpenCard("acme/web")))
	if err == nil {
		t.Fatalf("a partial view that would retract a live card must error, got %v", repoSpecs(got))
	}
	if len(got) != 0 {
		t.Errorf("an erroring producer must return no observations, got %v", repoSpecs(got))
	}
	if !strings.Contains(err.Error(), "acme/web") {
		t.Errorf("the error should name the repo that could not be read: %v", err)
	}
}

// The card's repo string was written by an earlier sweep under whatever
// spelling configuration used then. Matching it to today's spelling with == —
// rather than with the coverage matcher — misses `web` against `acme/web` and
// retracts the card anyway, which is the exact outcome the guard exists to
// prevent.
func TestDependabotCoverage_OpenCardUnderAnotherSpellingStillBlocksThePass(t *testing.T) {
	sec := covSec(map[string]covAnswer{
		"acme/web": covFailed(fmt.Errorf("read alerts: %w", forge.ErrPermissionDenied)),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	// Configuration now says "acme/web"; the card still says "web".
	_, err := p.Evaluate(context.Background(),
		covInput(sec, []string{"acme/web"}, covOpenCard("web")))
	if err == nil {
		t.Fatal("a card written under the bare spelling must still be protected")
	}
}

// ...and the guard is conditional on purpose. A repo that is permanently
// unreadable but holds NO card must not silence the producer for the whole
// workspace — that would be this issue's own failure mode, one level up.
func TestDependabotCoverage_ReadFailureWithoutCard_StillReportsOtherRepos(t *testing.T) {
	sec := covSec(map[string]covAnswer{
		"acme/web":      covFailed(fmt.Errorf("read alerts: %w", forge.ErrPermissionDenied)),
		"acme/api":      covDisabled(),
		"acme/archived": covFailed(fmt.Errorf("read alerts: %w", forge.ErrUnsupported)),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(),
		covInput(sec, []string{"acme/web", "acme/api", "acme/archived"}))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if want := []string{"acme/api"}; !reflect.DeepEqual(repoSpecs(got), want) {
		t.Fatalf("cards = %v, want %v", repoSpecs(got), want)
	}
}

// An open card belonging to ANOTHER producer must not hold this one hostage.
func TestDependabotCoverage_OtherProducersCardsDoNotBlockThePass(t *testing.T) {
	foreign := covOpenCard("acme/web")
	foreign.Producer = ProducerDependabotAlerts

	sec := covSec(map[string]covAnswer{
		"acme/web": covFailed(fmt.Errorf("read alerts: %w", forge.ErrPermissionDenied)),
		"acme/api": covDisabled(),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(),
		covInput(sec, []string{"acme/web", "acme/api"}, foreign))
	if err != nil {
		t.Fatalf("another producer's card is not this producer's to protect: %v", err)
	}
	if want := []string{"acme/api"}; !reflect.DeepEqual(repoSpecs(got), want) {
		t.Fatalf("cards = %v, want %v", repoSpecs(got), want)
	}
}

// A card this producer already auto-resolved is terminal and cannot be
// retracted again, so it must not block the pass either.
func TestDependabotCoverage_TerminalCardDoesNotBlockThePass(t *testing.T) {
	done := covOpenCard("acme/web")
	done.Lifecycle.State = attention.StateAutoResolved

	sec := covSec(map[string]covAnswer{
		"acme/web": covFailed(fmt.Errorf("read alerts: %w", forge.ErrUnauthorized)),
		"acme/api": covDisabled(),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(),
		covInput(sec, []string{"acme/web", "acme/api"}, done))
	if err != nil {
		t.Fatalf("a terminal card has nothing left to lose: %v", err)
	}
	if want := []string{"acme/api"}; !reflect.DeepEqual(repoSpecs(got), want) {
		t.Fatalf("cards = %v, want %v", repoSpecs(got), want)
	}
}

// Invariant 1 at the top of the producer: with no security service we can look
// at nothing, and an empty slice would assert every repo is covered on the
// strength of having asked nobody.
func TestDependabotCoverage_NoSecurityService_Errors(t *testing.T) {
	p := &DependabotCoverage{Logf: func(string, ...any) {}}
	in := WorkspaceInput{ConfiguredRepos: []string{"acme/web"}, Forge: &covForge{}}

	if got, err := p.Evaluate(context.Background(), in); err == nil {
		t.Fatalf("looking nowhere must error, not report a covered workspace (got %v)", repoSpecs(got))
	}
}

// A cancelled budget mid-list is a partial view of the workspace, not a set of
// covered repos.
func TestDependabotCoverage_BudgetExhausted_Errors(t *testing.T) {
	sec := covSec(map[string]covAnswer{"acme/api": covDisabled(), "acme/web": covDisabled()})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got, err := p.Evaluate(ctx, covInput(sec, []string{"acme/web", "acme/api"})); err == nil {
		t.Fatalf("an exhausted budget must error, got %v", repoSpecs(got))
	}
}

// --- budget and de-duplication ----------------------------------------------

// One forge request per repo per sweep, and — because the configured list is a
// UNION of the manifest, autonomous.enabled_repos and the primary repo —
// exactly one for a repo written two ways. Without the Covers-based collapse
// this costs two requests out of a shared budget and raises two cards for one
// setting.
func TestDependabotCoverage_OneRequestAndOneCardPerRepo(t *testing.T) {
	sec := covSec(map[string]covAnswer{
		"acme/web": covEnabled(0),
		"acme/api": covDisabled(),
	})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	// Two repos, five spellings: the bare manifest form, the qualified config
	// form, and a case variant. normalizeRepos de-duplicates exact strings only,
	// so all five arrive.
	got, err := p.Evaluate(context.Background(),
		covInput(sec, []string{"acme/web", "web", "acme/api", "api", "Acme/API"}))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 || !strings.EqualFold(got[0].Context.Repo, "acme/api") {
		t.Fatalf("cards = %v, want exactly one for acme/api — five spellings name two repos", repoSpecs(got))
	}
	if sec.total() != 2 {
		t.Errorf("forge requests = %d, want 2 (one per repo per sweep); per-repo: %v", sec.total(), sec.calls)
	}
	// The qualified spelling is the one queried: a bare name cannot be turned
	// into a forge request at all.
	if sec.calls["acme/api"] != 1 || sec.calls["acme/web"] != 1 {
		t.Errorf("want one request per repo, got %v", sec.calls)
	}
}

// A configured entry with no owner cannot be queried. It is unobserved — not a
// covered repo, and not a blind one either.
func TestDependabotCoverage_UnqualifiedOnlyEntry_IsUnobserved(t *testing.T) {
	sec := covSec(map[string]covAnswer{"acme/api": covDisabled()})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	got, err := p.Evaluate(context.Background(), covInput(sec, []string{"orphan", "acme/api"}))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if want := []string{"acme/api"}; !reflect.DeepEqual(repoSpecs(got), want) {
		t.Fatalf("cards = %v, want %v", repoSpecs(got), want)
	}
	if sec.calls["orphan"] != 0 {
		t.Errorf("an unqualified entry must not reach the forge, got %v", sec.calls)
	}
}

// Nothing configured is nothing to measure, and it is coverage-gap's story to
// tell — a positive empty observation, so stale cards still retract.
func TestDependabotCoverage_NothingConfigured_RaisesNothing(t *testing.T) {
	p := &DependabotCoverage{Logf: func(string, ...any) {}}
	got, err := p.Evaluate(context.Background(), covInput(covSec(nil), nil))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no cards, got %v", repoSpecs(got))
	}
}

// --- fingerprint stability --------------------------------------------------

// Two identical sweeps must produce byte-identical cards. Go randomises map
// iteration, so run this with -count=20: an ordering-dependent fingerprint
// fails intermittently and would re-alert an operator about a condition that
// never moved.
func TestDependabotCoverage_IdenticalSweepsProduceIdenticalCards(t *testing.T) {
	answers := map[string]covAnswer{
		"acme/web":  covDisabled(),
		"acme/api":  covDisabled(),
		"acme/jobs": covEnabled(2),
	}
	p := &DependabotCoverage{Logf: func(string, ...any) {}}

	first, err := p.Evaluate(context.Background(),
		covInput(covSec(answers), []string{"acme/web", "acme/api", "acme/jobs"}))
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	// A different declaration order for the same configuration is the same
	// condition and must not look like a change.
	second, err := p.Evaluate(context.Background(),
		covInput(covSec(answers), []string{"acme/jobs", "acme/api", "acme/web"}))
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two identical sweeps produced different cards:\n%+v\n%+v", first, second)
	}
	if len(first) != 2 {
		t.Fatalf("want 2 cards, got %v", repoSpecs(first))
	}
	// One repo going blind must not re-alert the other: the fingerprint is this
	// repo's own condition, never the set.
	third, err := p.Evaluate(context.Background(), covInput(covSec(map[string]covAnswer{
		"acme/web": covDisabled(), "acme/api": covDisabled(), "acme/jobs": covDisabled(),
	}), []string{"acme/web", "acme/api", "acme/jobs"}))
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if third[0].Fingerprint != first[0].Fingerprint {
		t.Errorf("a THIRD repo going blind must not re-alert acme/api: %q vs %q",
			third[0].Fingerprint, first[0].Fingerprint)
	}
}

// --- coverage matching ------------------------------------------------------

// The producer decides which repos may be carded and the verb executor decides
// which carded repos may be acted on. They use twin matchers in two packages,
// and if the two ever disagree the card's repair button refuses the very repo
// that produced it. This pins them to the same answers.
func TestCoverageMatchers_SweepAndVerbAgree(t *testing.T) {
	configured := []string{"acme/web", "jobs", "Acme/API"}
	in := WorkspaceInput{ConfiguredRepos: configured}
	for _, repo := range []string{
		"acme/web", "web", "ACME/WEB", "jobs", "acme/jobs", "acme/api", "Acme/API",
		"other/web", "acme/unconfigured", "", "  ",
	} {
		sweepSays := in.Covers(repo)
		verbSays := attention.RepoInConfiguredSet(configured, repo)
		if sweepSays != verbSays {
			t.Errorf("Covers(%q) = %v but RepoInConfiguredSet = %v — a card the sweep raises "+
				"would carry a repair button the executor refuses", repo, sweepSays, verbSays)
		}
	}
}

// --- lifecycle through the sweeper and the store ----------------------------
//
// The acceptance criteria that matter most are properties of RECONCILIATION,
// not of a return value: SweepWorkspace's documented two-step (ReconcileStanding
// per repo, then AutoResolveUnobserved once for the producer) is what makes a
// card retract, and either half missing breaks it in a different direction.

func coverageSweeper(t *testing.T, p WorkspaceProducer) (*Sweeper, *attention.Store) {
	t.Helper()
	root := t.TempDir()
	store := attention.New(root)
	reg := NewRegistry()
	reg.RegisterWorkspace(p)
	return &Sweeper{
		Store:         store,
		Registry:      reg,
		WorkspaceRoot: root,
		Logf:          func(string, ...any) {},
	}, store
}

// END TO END: a configured repo with alerts disabled raises a coverage card;
// re-observing it produces no duplicate; enabling alerts auto-resolves the card
// on the NEXT sweep with no manual dismissal; and the sweep after that neither
// re-raises it nor retracts anything again.
func TestDependabotCoverageLifecycle_RaiseDedupeAutoResolveStayGone(t *testing.T) {
	answers := map[string]covAnswer{"acme/web": covDisabled()}
	sec := covSec(answers)
	p := &DependabotCoverage{
		ListAlerts: func(ctx context.Context, _ WorkspaceInput, owner, name string) (*forgetypes.SecurityAlerts, error) {
			return sec.ListOpenAlerts(ctx, owner, name)
		},
		Logf: func(string, ...any) {},
	}
	sw, store := coverageSweeper(t, p)
	configured := []string{"acme/web"}

	// 1. Blind repo → one card, in the store every surface reads.
	res, err := sw.SweepWorkspace(context.Background(), configured)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("first sweep: created = %d, want 1 (%+v)", res.Created, res)
	}
	open, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 || open[0].Context.Repo != "acme/web" {
		t.Fatalf("want one open card for acme/web, got %+v", open)
	}
	card := open[0]

	// 2. Still blind → refreshed, never a second card. "One condition, one
	//    notification": the operator hears about it once.
	res, err = sw.SweepWorkspace(context.Background(), configured)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if res.Created != 0 || res.AutoResolved != 0 {
		t.Fatalf("second sweep must neither duplicate nor retract: %+v", res)
	}
	open, _ = store.List(attention.ListFilter{})
	if len(open) != 1 {
		t.Fatalf("re-observation produced %d cards, want 1", len(open))
	}
	if open[0].ID != card.ID {
		t.Errorf("sticky identity broken: id %q became %q", card.ID, open[0].ID)
	}

	// 3. The operator clicks the repair button. This is what the verb does to
	//    the world, standing in for the daemon's forge call.
	if err := attention.ExecuteEnableAlerts(context.Background(),
		enablerFunc(func(_ context.Context, owner, repo string) error {
			answers[owner+"/"+repo] = covEnabled(0)
			return nil
		}), &card, *card.FindOption("enable"), configured); err != nil {
		t.Fatalf("ExecuteEnableAlerts: %v", err)
	}

	// 4. Next sweep: the condition is gone, so the card retracts itself. No
	//    manual dismissal — the AutoResolveUnobserved half of the two-step is
	//    the only thing that can close it, because a newly covered repo
	//    produces no observation for the per-repo half to visit.
	res, err = sw.SweepWorkspace(context.Background(), configured)
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if res.AutoResolved != 1 {
		t.Fatalf("third sweep: auto_resolved = %d, want 1 (%+v)", res.AutoResolved, res)
	}
	open, _ = store.List(attention.ListFilter{})
	if len(open) != 0 {
		t.Fatalf("an enabled repo must leave no open card, got %+v", open)
	}

	// The terminal state distinguishes "it fixed itself" from "a human dealt
	// with it".
	all, err := store.List(attention.ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].Lifecycle.State != attention.StateAutoResolved {
		t.Fatalf("want one auto-resolved record, got %+v", all)
	}

	// 5. And it stays gone. A card that retracts and then re-raises on the very
	//    next sweep is worse than one that never retracted.
	res, err = sw.SweepWorkspace(context.Background(), configured)
	if err != nil {
		t.Fatalf("fourth sweep: %v", err)
	}
	if res.Created != 0 || res.AutoResolved != 0 || res.Updated != 0 {
		t.Fatalf("fourth sweep must be a no-op: %+v", res)
	}
	open, _ = store.List(attention.ListFilter{})
	if len(open) != 0 {
		t.Fatalf("card re-raised after being resolved, got %+v", open)
	}
}

// NEVER FATAL through the sweeper: the producer errors, so it is excluded from
// reconciliation and its existing card survives untouched. Auto-resolving on a
// failed observation is how a transient error silently retracts a real signal.
func TestDependabotCoverage_ReadFailure_LeavesTheStoreUntouched(t *testing.T) {
	answers := map[string]covAnswer{"acme/web": covDisabled()}
	sec := covSec(answers)
	p := &DependabotCoverage{
		ListAlerts: func(ctx context.Context, _ WorkspaceInput, owner, name string) (*forgetypes.SecurityAlerts, error) {
			return sec.ListOpenAlerts(ctx, owner, name)
		},
		Logf: func(string, ...any) {},
	}
	sw, store := coverageSweeper(t, p)
	configured := []string{"acme/web"}

	if _, err := sw.SweepWorkspace(context.Background(), configured); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if n := len(mustList(t, store)); n != 1 {
		t.Fatalf("setup: want 1 open card, got %d", n)
	}

	// The token loses its security scope.
	answers["acme/web"] = covFailed(fmt.Errorf("read alerts: %w", forge.ErrPermissionDenied))

	res, err := sw.SweepWorkspace(context.Background(), configured)
	if err != nil {
		t.Fatalf("a producer failure must not fail the workspace sweep: %v", err)
	}
	if len(res.Failed) != 1 {
		t.Errorf("the failure should be reported on the result, got %+v", res.Failed)
	}
	if res.AutoResolved != 0 {
		t.Errorf("a failed observation must retract nothing; auto_resolved = %d", res.AutoResolved)
	}
	open := mustList(t, store)
	if len(open) != 1 || open[0].Lifecycle.State.IsTerminal() {
		t.Fatalf("the card must survive a read failure, got %+v", open)
	}
}

func mustList(t *testing.T, store *attention.Store) []attention.DecisionRequest {
	t.Helper()
	open, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return open
}

// --- the verb, against the card the producer actually emits -----------------

type enablerFunc func(ctx context.Context, owner, repo string) error

func (f enablerFunc) EnableSecurityAlerts(ctx context.Context, owner, repo string) error {
	return f(ctx, owner, repo)
}

// The verb enables alerts for a repo in the configured list, and refuses a
// target outside it — with the card the producer really builds, so the
// producer's repo spelling and the executor's allowlist are tested against each
// other rather than against a fixture.
func TestDependabotCoverage_EnableAlertsVerbIsBoundedToConfiguredRepos(t *testing.T) {
	sec := covSec(map[string]covAnswer{"acme/api": covDisabled()})
	p := &DependabotCoverage{Logf: func(string, ...any) {}}
	got, err := p.Evaluate(context.Background(), covInput(sec, []string{"acme/api"}))
	if err != nil || len(got) != 1 {
		t.Fatalf("setup: Evaluate = %v, %v", repoSpecs(got), err)
	}
	card := got[0]
	opt := *card.FindOption("enable")

	var enabled []string
	record := enablerFunc(func(_ context.Context, owner, repo string) error {
		enabled = append(enabled, owner+"/"+repo)
		return nil
	})

	// In the configured list, in either spelling.
	for _, configured := range [][]string{{"acme/api"}, {"api"}, {"acme/web", "acme/api"}} {
		enabled = nil
		if err := attention.ExecuteEnableAlerts(context.Background(), record, &card, opt, configured); err != nil {
			t.Fatalf("configured %v: %v", configured, err)
		}
		if want := []string{"acme/api"}; !reflect.DeepEqual(enabled, want) {
			t.Errorf("configured %v: enabled %v, want %v", configured, enabled, want)
		}
	}

	// Outside it: refused, and the forge is never touched.
	enabled = nil
	err = attention.ExecuteEnableAlerts(context.Background(), record, &card, opt, []string{"acme/web"})
	if !errors.Is(err, attention.ErrVerbTargetNotConfigured) {
		t.Fatalf("want ErrVerbTargetNotConfigured, got %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("a refused target must not reach the forge, got %v", enabled)
	}
}
