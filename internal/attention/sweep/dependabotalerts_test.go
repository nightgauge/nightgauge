package sweep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// --- fakes ------------------------------------------------------------------
//
// Modelled on defaultbranch_test.go: the producer must be exercisable with no
// network, and the identical producer must run against a GitLab adapter without
// a line changing — which is only true if it never sees anything but the forge
// interfaces.

type alertForge struct {
	sec *alertSecurity
}

func (f *alertForge) Issues() forge.IssueService      { return nil }
func (f *alertForge) PRs() forge.PRService            { return nil }
func (f *alertForge) Project() forge.ProjectService   { return nil }
func (f *alertForge) Board() forge.BoardService       { return nil }
func (f *alertForge) CI() forge.CIService             { return nil }
func (f *alertForge) Labels() forge.LabelService      { return nil }
func (f *alertForge) Rulesets() forge.RulesetService  { return nil }
func (f *alertForge) Auth() forge.AuthService         { return nil }
func (f *alertForge) Repo() forge.RepoService         { return nil }
func (f *alertForge) Security() forge.SecurityService { return f.sec }

type alertSecurity struct {
	res *forgetypes.SecurityAlerts
	err error
	// calls counts requests so the single-request-per-sweep budget is a
	// pinned property rather than an aspiration.
	calls int
}

func (s *alertSecurity) ListOpenAlerts(context.Context, string, string) (*forgetypes.SecurityAlerts, error) {
	s.calls++
	return s.res, s.err
}

func alertInput(sec *alertSecurity, existing ...attention.DecisionRequest) Input {
	return Input{
		Repo:     "octocat/acme",
		Owner:    "octocat",
		Name:     "acme",
		Forge:    &alertForge{sec: sec},
		Existing: existing,
	}
}

func newAlertProducer() *DependabotAlerts {
	return &DependabotAlerts{Now: func() time.Time { return fixedNow }}
}

// enabled wraps alerts in an "enabled" answer.
func enabled(alerts ...forgetypes.SecurityAlert) *alertSecurity {
	return &alertSecurity{res: &forgetypes.SecurityAlerts{
		Status:    forgetypes.SecurityAlertsEnabled,
		Alerts:    alerts,
		TotalOpen: len(alerts),
	}}
}

// seenLongAgo is a first-seen timestamp comfortably outside the grace window.
var seenLongAgo = fixedNow.Add(-72 * time.Hour).Format(time.RFC3339)

// alertWithPR is a high-severity advisory the forge has already prepared a fix
// for.
func alertWithPR() forgetypes.SecurityAlert {
	return forgetypes.SecurityAlert{
		Number:              7,
		URL:                 "https://forge/security/dependabot/7",
		Severity:            forgetypes.AlertSeverityHigh,
		AdvisoryID:          "GHSA-aaaa-bbbb-cccc",
		CVE:                 "CVE-2026-0001",
		Summary:             "prototype pollution in widget",
		AdvisoryURL:         "https://forge/advisories/GHSA-aaaa-bbbb-cccc",
		Package:             "widget",
		Ecosystem:           "npm",
		ManifestPath:        "package-lock.json",
		Scope:               "runtime",
		Relationship:        "direct",
		VulnerableRange:     "< 2.1.0",
		FirstPatchedVersion: "2.1.0",
		FirstSeenAt:         seenLongAgo,
		Remediation: forgetypes.Remediation{
			State:    forgetypes.RemediationPROpen,
			PRNumber: 412,
			PRURL:    "https://forge/pull/412",
			PRTitle:  "chore(deps): bump widget to 2.1.0",
		},
	}
}

// alertWithoutPR is the class the issue exists for: no fix the forge can apply,
// so nothing in the pipeline reported it before.
func alertWithoutPR() forgetypes.SecurityAlert {
	return forgetypes.SecurityAlert{
		Number:          9,
		URL:             "https://forge/security/dependabot/9",
		Severity:        forgetypes.AlertSeverityCritical,
		AdvisoryID:      "GHSA-dddd-eeee-ffff",
		Summary:         "remote code execution in sprocket",
		Package:         "sprocket",
		Ecosystem:       "npm",
		ManifestPath:    "tools/package-lock.json",
		Scope:           "development",
		Relationship:    "transitive",
		VulnerableRange: ">= 1.0.0",
		FirstSeenAt:     seenLongAgo,
		Remediation: forgetypes.Remediation{
			State:        forgetypes.RemediationNotPossible,
			Reason:       "security_update_not_possible",
			ReasonDetail: "sprocket cannot be updated to a non-vulnerable version",
		},
	}
}

// --- tests ------------------------------------------------------------------

func TestDependabotAlerts_OneCardPerOpenAlert(t *testing.T) {
	p := newAlertProducer()
	sec := enabled(alertWithPR(), alertWithoutPR())

	got, err := p.Evaluate(context.Background(), alertInput(sec))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("observations = %d, want 2 (one per open alert)", len(got))
	}
	// A single request per repo per sweep: producers share one 30s budget, so a
	// fan-out here is spent out of every other producer's pocket.
	if sec.calls != 1 {
		t.Errorf("forge requests = %d, want exactly 1", sec.calls)
	}
	// Most severe first, so the worst advisory is not buried behind a low one.
	if !strings.HasPrefix(got[0].Title, "critical") {
		t.Errorf("first card title = %q, want the critical advisory first", got[0].Title)
	}
	if got[0].IdempotencyKey == got[1].IdempotencyKey {
		t.Fatal("two alerts share one idempotency key — one would overwrite the other")
	}
}

func TestDependabotAlerts_CardCarriesTheAdvisorysOwnFacts(t *testing.T) {
	p := newAlertProducer()
	got, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
	req := got[0]

	// Severity read from the advisory, not guessed from a label — the whole
	// premise of the issue.
	if !strings.Contains(req.Title, "high") {
		t.Errorf("title %q does not carry the advisory severity", req.Title)
	}
	for _, want := range []string{
		"GHSA-aaaa-bbbb-cccc", // advisory identifier
		"CVE-2026-0001",       // CVE
		"widget",              // package
		"package-lock.json",   // manifest — which lockfile matters in a monorepo
		"< 2.1.0",             // vulnerable range
		"2.1.0",               // first patched version
	} {
		if !strings.Contains(req.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, req.Body)
		}
	}
	if req.Severity != attention.SeverityFYI {
		t.Errorf("severity = %q, want %q — a vulnerability blocks no merge and stalls no run",
			req.Severity, attention.SeverityFYI)
	}
	if req.Kind != attention.KindHandoff {
		t.Errorf("kind = %q, want %q", req.Kind, attention.KindHandoff)
	}
}

func TestDependabotAlerts_NoRemediationPRRendersDifferentlyFromWithPR(t *testing.T) {
	p := newAlertProducer()

	with, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR())))
	if err != nil {
		t.Fatalf("Evaluate(with PR): %v", err)
	}
	without, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithoutPR())))
	if err != nil {
		t.Fatalf("Evaluate(without PR): %v", err)
	}

	// The card WITH a remediation PR names it — the operator's action is
	// "review and merge #412".
	if !strings.Contains(with[0].Title, "412") {
		t.Errorf("with-PR title does not name the PR: %q", with[0].Title)
	}
	if !strings.Contains(with[0].Body, "#412") {
		t.Errorf("with-PR body does not name the PR:\n%s", with[0].Body)
	}
	if with[0].Context.PR != 412 {
		t.Errorf("with-PR context.PR = %d, want 412", with[0].Context.PR)
	}
	if with[0].Context.URL != "https://forge/pull/412" {
		t.Errorf("with-PR URL = %q, want the PR", with[0].Context.URL)
	}

	// The card WITHOUT one says so plainly, and carries the forge's own reason
	// rather than an inference. These lead to opposite actions and must never
	// render the same.
	if !strings.Contains(without[0].Title, "no remediation PR") {
		t.Errorf("no-PR title does not state the absence: %q", without[0].Title)
	}
	if !strings.Contains(without[0].Body, "NO REMEDIATION PR EXISTS") {
		t.Errorf("no-PR body does not state the absence:\n%s", without[0].Body)
	}
	if !strings.Contains(without[0].Body, "security_update_not_possible") {
		t.Errorf("no-PR body drops the forge's own reason:\n%s", without[0].Body)
	}
	if without[0].Context.PR != 0 {
		t.Errorf("no-PR context.PR = %d, want 0", without[0].Context.PR)
	}
	if with[0].Title == without[0].Title || with[0].Body == without[0].Body {
		t.Fatal("with-PR and no-PR cards render identically")
	}
}

func TestDependabotAlerts_ZeroOpenAlertsIsAPositiveAssertion(t *testing.T) {
	p := newAlertProducer()
	got, err := p.Evaluate(context.Background(), alertInput(enabled()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// An empty slice with a nil error is what retracts a fixed alert's card.
	// Returning an error here would leave stale vulnerability cards up forever.
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0", len(got))
	}
}

func TestDependabotAlerts_ScanningDisabledIsNotZeroAlerts(t *testing.T) {
	p := newAlertProducer()
	sec := &alertSecurity{res: &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsDisabled}}

	got, err := p.Evaluate(context.Background(), alertInput(sec))
	if err == nil {
		t.Fatal("Evaluate returned nil error with scanning disabled — the sweep would read the empty slice as 'every vulnerability cleared' and retract live cards, making a repository setting a way to silence security cards")
	}
	if got != nil {
		t.Errorf("observations = %v, want nil alongside the error", got)
	}
	if !errors.Is(err, errAlertsDisabled) {
		t.Errorf("err = %v, want it to wrap errAlertsDisabled", err)
	}
	// Not repo-wide: the sweep must keep evaluating every other producer.
	if isSweepFatal(err) {
		t.Error("a disabled scanner skipped the whole sweep; only auth and rate limits are repo-wide")
	}
}

func TestDependabotAlerts_AuthFailurePropagatesAsRepoWide(t *testing.T) {
	p := newAlertProducer()
	sec := &alertSecurity{err: forge.ErrPermissionDenied}

	got, err := p.Evaluate(context.Background(), alertInput(sec))
	if err == nil {
		t.Fatal("Evaluate returned nil error on a permission failure — an unreadable alert feed would read as a clean repository")
	}
	if got != nil {
		t.Errorf("observations = %v, want nil alongside the error", got)
	}
	// The sentinel has to survive wrapping or the sweeper cannot tell a
	// repo-wide failure from one producer's bad day.
	if !errors.Is(err, forge.ErrPermissionDenied) {
		t.Errorf("err = %v, want it to wrap forge.ErrPermissionDenied", err)
	}
	if !isSweepFatal(err) {
		t.Error("a permission failure must skip the whole sweep — no producer would fare better and a partial view drives false auto-resolves")
	}
}

func TestDependabotAlerts_AlertInsideGraceNeverSurfaces(t *testing.T) {
	p := newAlertProducer()
	fresh := alertWithoutPR()
	// Raised two minutes ago: the forge may still be opening the remediation
	// PR, so carding now would publish "no remediation PR" about an alert that
	// is about to have one.
	fresh.FirstSeenAt = fixedNow.Add(-2 * time.Minute).Format(time.RFC3339)

	got, err := p.Evaluate(context.Background(), alertInput(enabled(fresh)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0 — the alert is inside the grace window", len(got))
	}
}

func TestDependabotAlerts_AlertWithNoFirstSeenIsStillCarded(t *testing.T) {
	p := newAlertProducer()
	undated := alertWithoutPR()
	undated.FirstSeenAt = ""

	got, err := p.Evaluate(context.Background(), alertInput(enabled(undated)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Suppressing a real critical advisory forever because an adapter did not
	// populate a timestamp is the worse failure.
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
}

func TestDependabotAlerts_FingerprintMovesOnSeverity(t *testing.T) {
	p := newAlertProducer()
	low := alertWithPR()
	low.Severity = forgetypes.AlertSeverityLow

	before, err := p.Evaluate(context.Background(), alertInput(enabled(low)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	after, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if before[0].Fingerprint == after[0].Fingerprint {
		t.Fatal("fingerprint unchanged when the advisory severity moved low → high — the operator would never be re-alerted")
	}
	if before[0].IdempotencyKey != after[0].IdempotencyKey {
		t.Error("idempotency key moved with the condition — the card would duplicate instead of updating")
	}
}

func TestDependabotAlerts_FingerprintMovesOnRemediationState(t *testing.T) {
	p := newAlertProducer()
	stuck := alertWithPR()
	stuck.Remediation = forgetypes.Remediation{State: forgetypes.RemediationNone}

	none, err := p.Evaluate(context.Background(), alertInput(enabled(stuck)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	withPR, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// A fix appearing is the single most actionable transition this producer
	// can report. Missing it would leave the operator staring at a card that
	// says there is nothing to merge while a PR sits open.
	if none[0].Fingerprint == withPR[0].Fingerprint {
		t.Fatal("fingerprint unchanged when a remediation PR appeared")
	}
}

func TestDependabotAlerts_FingerprintIgnoresElapsedTime(t *testing.T) {
	early := &DependabotAlerts{Now: func() time.Time { return fixedNow }}
	late := &DependabotAlerts{Now: func() time.Time { return fixedNow.Add(30 * 24 * time.Hour) }}

	a, err := early.Evaluate(context.Background(), alertInput(enabled(alertWithoutPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	b, err := late.Evaluate(context.Background(), alertInput(enabled(alertWithoutPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// A month later the same advisory is still open. That is the same
	// condition; re-alerting on it is what trains operators to ignore the inbox.
	if a[0].Fingerprint != b[0].Fingerprint {
		t.Errorf("fingerprint moved with elapsed time: %q vs %q", a[0].Fingerprint, b[0].Fingerprint)
	}
}

func TestDependabotAlerts_ShipsNoRepairVerb(t *testing.T) {
	p := newAlertProducer()
	got, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for _, opt := range got[0].Options {
		if opt.Verb != attention.VerbNoop {
			t.Errorf("option %q binds verb %q — nothing in the registry can patch a vulnerability, and a button that implies otherwise teaches operators to distrust every card",
				opt.ID, opt.Verb)
		}
	}
	if got[0].DefaultAction != attention.ExpireNoop {
		t.Errorf("default_action = %q, want %q", got[0].DefaultAction, attention.ExpireNoop)
	}
}

func TestDependabotAlerts_DefersToAnotherProducerCardingTheSamePR(t *testing.T) {
	p := newAlertProducer()
	// The run-scoped branch-protection producer already carded the very PR that
	// remediates this alert. One condition, one card.
	existing := attention.DecisionRequest{
		Producer:       producerBranchProtection,
		IdempotencyKey: "branch-protection:octocat/acme#412",
		Context:        attention.Context{Repo: "octocat/acme", PR: 412},
		Lifecycle:      attention.Lifecycle{State: attention.StateOpen},
	}

	got, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR()), existing))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0 — another producer already points the operator at PR #412", len(got))
	}

	// The same alert with NO open card elsewhere still surfaces, so the dedupe
	// cannot be silently swallowing everything.
	got, err = p.Evaluate(context.Background(), alertInput(enabled(alertWithPR())))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1 with no competing card", len(got))
	}
}

func TestDependabotAlerts_RegisteredInTheDefaultRegistry(t *testing.T) {
	var found bool
	for _, p := range Default.Producers() {
		if p.Name() == ProducerDependabotAlerts {
			found = true
		}
	}
	if !found {
		t.Fatalf("%q is not in the default registry — `nightgauge attention sweep` would never run it", ProducerDependabotAlerts)
	}
}

// --- through the sweeper and the store --------------------------------------
//
// Re-observation and auto-resolution are properties of RECONCILIATION, not of a
// producer's return value, so they are only really tested end to end.

func TestDependabotAlertsLifecycle_RaiseRefreshAutoResolve(t *testing.T) {
	sec := enabled(alertWithoutPR())
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer())

	// 1. An open advisory → one card.
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 1 {
		t.Fatalf("first sweep: created = %d, want 1 (%+v)", res.Reconciled.Created, res.Reconciled)
	}

	// 2. Still open, nothing changed → refreshed, never a second card.
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 0 || res.Reconciled.Refreshed != 1 {
		t.Fatalf("second sweep: want refreshed=1 created=0, got %+v", res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("a second sweep must not produce a second card for the same alert")
	}

	// 3. The alert is fixed or dismissed on the forge → the card retracts.
	sec.res.Alerts = nil
	sec.res.TotalOpen = 0
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 1 {
		t.Fatalf("third sweep: auto_resolved = %d, want 1 (%+v)", res.Reconciled.AutoResolved, res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 0 {
		t.Fatal("a resolved advisory must leave no open card")
	}

	all, err := store.List(attention.ListFilter{Repo: "octocat/acme", IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("stored requests = %d, want 1", len(all))
	}
	// "The vulnerability was patched" and "a human dismissed it" are different
	// facts and the audit trail has to be able to tell them apart.
	if all[0].Lifecycle.State != attention.StateAutoResolved {
		t.Errorf("state = %q, want %q", all[0].Lifecycle.State, attention.StateAutoResolved)
	}
}

func TestDependabotAlertsLifecycle_RemediationAppearingUpdatesTheSameCard(t *testing.T) {
	stuck := alertWithPR()
	stuck.Remediation = forgetypes.Remediation{State: forgetypes.RemediationNone}
	sec := enabled(stuck)
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer())

	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// The forge opens the remediation PR.
	sec.res.Alerts = []forgetypes.SecurityAlert{alertWithPR()}
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	// ActionUpdated, not ActionCreated and not ActionRefreshed: the condition
	// materially changed, so the operator is re-alerted, on the SAME card.
	if res.Reconciled.Updated != 1 {
		t.Fatalf("want updated=1, got %+v", res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("a remediation PR appearing must update the card, not add a second one")
	}
	open, err := store.List(attention.ListFilter{Repo: "octocat/acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(open[0].Title, "412") {
		t.Errorf("card was not re-rendered with the new PR: %q", open[0].Title)
	}
}

func TestDependabotAlertsLifecycle_ProducerErrorLeavesCardsAlone(t *testing.T) {
	sec := enabled(alertWithoutPR())
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer())

	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("setup: expected one open card")
	}

	// The forge stops answering. "I could not look" is not "the vulnerability
	// is gone" — the sweep package's NEVER FATAL commitment.
	sec.res, sec.err = nil, errors.New("dial tcp: connection refused")
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 0 {
		t.Fatalf("a failed observation retracted %d card(s)", res.Reconciled.AutoResolved)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("the card was retracted on a transient forge failure")
	}
	if _, failed := res.Failed[ProducerDependabotAlerts]; !failed {
		t.Errorf("producer failure was not reported on the result: %+v", res)
	}
	if res.Skipped {
		t.Error("a plain transport error skipped the whole sweep; only auth and rate limits are repo-wide")
	}
}

func TestDependabotAlertsLifecycle_DisabledScannerDoesNotRetract(t *testing.T) {
	sec := enabled(alertWithoutPR())
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer())

	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("setup: expected one open card")
	}

	// Somebody switches dependency scanning off. If that retracted the cards,
	// a repository setting would double as a way to erase security findings.
	sec.res = &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsDisabled}
	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 0 {
		t.Fatalf("disabling the scanner retracted %d card(s)", res.Reconciled.AutoResolved)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("the card was retracted when scanning was turned off")
	}
}
