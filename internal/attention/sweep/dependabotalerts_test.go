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
//
// URL is shaped exactly as an adapter emits it — the repository's own web URL
// plus /security/dependabot/<number>, which is REST's html_url verbatim. A
// fixture inventing a value no adapter can produce would exercise the card's
// URL-preference chain on data that never occurs.
func alertWithPR() forgetypes.SecurityAlert {
	return forgetypes.SecurityAlert{
		Number:              7,
		URL:                 "https://forge/octocat/acme/security/dependabot/7",
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
		URL:             "https://forge/octocat/acme/security/dependabot/9",
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

// TestDependabotAlerts_PermissionDenialIsScopedToTheSecuritySurface pins the
// blast radius of the one failure this producer can suffer that no other
// producer would.
//
// Reading Dependabot alerts needs a token scope (GitHub's `security_events`)
// that reading pull requests, checks and rulesets does not. isSweepFatal treats
// forge.ErrPermissionDenied as repo-wide — it aborts the cycle before
// ReconcileStanding runs — so letting that sentinel out of here would switch
// off default-branch-health, human-gate and stranded-ready for the repository
// on every sweep, permanently, and freeze whatever cards are already open.
func TestDependabotAlerts_PermissionDenialIsScopedToTheSecuritySurface(t *testing.T) {
	p := newAlertProducer()
	sec := &alertSecurity{err: fmt.Errorf("the forge refused to serve alerts: %w", forge.ErrPermissionDenied)}

	got, err := p.Evaluate(context.Background(), alertInput(sec))
	if err == nil {
		t.Fatal("Evaluate returned nil error on a permission failure — an unreadable alert feed would read as a clean repository")
	}
	if got != nil {
		t.Errorf("observations = %v, want nil alongside the error", got)
	}
	// Still an error, so this producer's own cards are left exactly where they
	// were: "I could not look" is never "the vulnerability is gone".
	if !errors.Is(err, errAlertsUnreadable) {
		t.Errorf("err = %v, want it to wrap errAlertsUnreadable", err)
	}
	if errors.Is(err, forge.ErrPermissionDenied) {
		t.Error("the repo-wide sentinel escaped the producer — one surface's missing scope would disable every other producer for this repository")
	}
	if isSweepFatal(err) {
		t.Error("a security-surface denial skipped the whole sweep; every other producer reads a different surface and would have succeeded")
	}
	// The diagnosis has to survive, or an operator cannot tell this apart from
	// a transport blip.
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %v, want the forge's own words carried through", err)
	}
}

// TestDependabotAlerts_BadCredentialsAndRateLimitsStayRepoWide is the other
// half: the two failures that genuinely are repo-wide must keep skipping the
// sweep, because no producer would fare better against either.
func TestDependabotAlerts_BadCredentialsAndRateLimitsStayRepoWide(t *testing.T) {
	for name, sentinel := range map[string]error{
		"the credential itself is bad": forge.ErrUnauthorized,
		"the shared quota is spent":    forge.ErrRateLimited,
	} {
		t.Run(name, func(t *testing.T) {
			p := newAlertProducer()
			sec := &alertSecurity{err: fmt.Errorf("read alerts: %w", sentinel)}

			_, err := p.Evaluate(context.Background(), alertInput(sec))
			if !errors.Is(err, sentinel) {
				t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
			}
			if !isSweepFatal(err) {
				t.Error("the sweep kept going on a failure every other producer would also hit")
			}
		})
	}
}

// TestDependabotAlerts_TruncatedAnswerIsNotAnObservation covers the forge
// telling us, in its own words, that we are looking at a page rather than the
// open set. Reconciling a page auto-resolves every alert that fell off it.
func TestDependabotAlerts_TruncatedAnswerIsNotAnObservation(t *testing.T) {
	p := newAlertProducer()
	sec := enabled(alertWithPR())
	sec.res.Truncated = true
	sec.res.TotalOpen = 250

	got, err := p.Evaluate(context.Background(), alertInput(sec))
	if err == nil {
		t.Fatal("Evaluate returned a truncated page as if it were the complete open set — every alert past the first page would be retracted as 'condition_cleared'")
	}
	if got != nil {
		t.Errorf("observations = %v, want nil alongside the error", got)
	}
	if !errors.Is(err, errAlertsTruncated) {
		t.Errorf("err = %v, want it to wrap errAlertsTruncated", err)
	}
	// The shortfall is the operator's to act on, so the numbers have to be in
	// the message the sweep result carries.
	if !strings.Contains(err.Error(), "250") {
		t.Errorf("err = %v, want the forge's own open count in the message", err)
	}
	// One surface's overflow is not a reason to stop evaluating the others.
	if isSweepFatal(err) {
		t.Error("a truncated alert page skipped the whole sweep")
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

// TestDependabotAlerts_KeepsObservingAnAlertAnotherProducerHasCardedThePRFor
// is the counterpart to humangate.go's deliberate deferral, and the difference
// matters.
//
// Human-gate defers to branch-protection because both observe ONE condition
// ("this PR cannot merge"). An open advisory and a blocked PR are two
// conditions. Dropping this observation does not suppress a duplicate card — it
// tells the reconciler the vulnerability cleared, and the reconciler retracts
// the only card that names the severity, the GHSA, the CVE and the package.
func TestDependabotAlerts_KeepsObservingAnAlertAnotherProducerHasCardedThePRFor(t *testing.T) {
	for _, producer := range []string{producerBranchProtection, ProducerHumanGate} {
		t.Run(producer, func(t *testing.T) {
			p := newAlertProducer()
			existing := attention.DecisionRequest{
				Producer:       producer,
				IdempotencyKey: producer + ":octocat/acme#412",
				// The ordinary life of a remediation PR: green, review required.
				Title:     "PR #412 is green and waiting on a review",
				Context:   attention.Context{Repo: "octocat/acme", PR: 412},
				Lifecycle: attention.Lifecycle{State: attention.StateOpen},
			}

			got, err := p.Evaluate(context.Background(), alertInput(enabled(alertWithPR()), existing))
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("observations = %d, want 1 — the advisory is still open, and not re-observing it auto-resolves its card", len(got))
			}
			// The other producer's card states none of this, which is why the
			// two are not interchangeable.
			for _, want := range []string{"high", "widget"} {
				if !strings.Contains(got[0].Title, want) {
					t.Errorf("title %q dropped %q", got[0].Title, want)
				}
			}
		})
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

// TestDependabotAlertsLifecycle_TruncatedPageNeverRetracts is the partial
// observation, end to end.
//
// A busy repository crosses the per-request cap, a new alert pushes an older
// one off the single page, and the alert that fell off is STILL OPEN. Read as a
// complete answer, its card is retracted with the reason "condition_cleared".
func TestDependabotAlertsLifecycle_TruncatedPageNeverRetracts(t *testing.T) {
	sec := enabled(alertWithPR(), alertWithoutPR())
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer())

	if _, err := sw.Sweep(context.Background(), "octocat/acme"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if openCount(t, store, "octocat/acme") != 2 {
		t.Fatalf("setup: open cards = %d, want 2", openCount(t, store, "octocat/acme"))
	}

	// A newly raised alert pushes #9 — a critical RCE, still open — off the
	// page the forge returns.
	newcomer := alertWithoutPR()
	newcomer.Number = 21
	sec.res.Alerts = []forgetypes.SecurityAlert{alertWithPR(), newcomer}
	sec.res.TotalOpen = 3
	sec.res.Truncated = true

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 0 {
		t.Fatalf("a page the forge itself flagged as incomplete retracted %d card(s) for still-open alerts", res.Reconciled.AutoResolved)
	}
	if openCount(t, store, "octocat/acme") != 2 {
		t.Fatalf("open cards = %d, want the original 2 untouched", openCount(t, store, "octocat/acme"))
	}
	if _, failed := res.Failed[ProducerDependabotAlerts]; !failed {
		t.Errorf("the shortfall was not reported on the sweep result: %+v", res)
	}
	if res.Skipped {
		t.Error("one producer's overflow skipped the whole sweep")
	}
}

// TestDependabotAlertsLifecycle_AnotherProducerCardingThePRDoesNotRetractIt
// reproduces the loop the cross-producer dedupe used to create: an open
// security card auto-resolves as "condition_cleared" the moment human-gate
// starts carding the remediation PR — for a vulnerability the forge is still
// reporting.
func TestDependabotAlertsLifecycle_AnotherProducerCardingThePRDoesNotRetractIt(t *testing.T) {
	sec := enabled(alertWithPR())
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer())

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.Created != 1 {
		t.Fatalf("first sweep: created = %d, want 1 (%+v)", res.Reconciled.Created, res.Reconciled)
	}
	before, err := store.List(attention.ListFilter{Repo: "octocat/acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	securityID := before[0].ID

	// Human-gate raises its ordinary card for the very same PR — the normal life
	// of a remediation PR: green, review required.
	gateID, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID:             gateID,
		IdempotencyKey: "human-gate:octocat/acme#412",
		Producer:       ProducerHumanGate,
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityFYI,
		Title:          "PR #412 is green and waiting on a review",
		Context:        attention.Context{Repo: "octocat/acme", PR: 412},
		DefaultAction:  attention.ExpireNoop,
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 0 {
		t.Fatalf("another producer carding the remediation PR retracted %d security card(s) — the advisory is still open and still returned by the forge (%+v)", res.Reconciled.AutoResolved, res.Reconciled)
	}
	if res.Reconciled.Created != 0 {
		t.Fatalf("the security card was re-created rather than refreshed: %+v", res.Reconciled)
	}

	open, err := store.List(attention.ListFilter{Repo: "octocat/acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var security *attention.DecisionRequest
	for i := range open {
		if open[i].Producer == ProducerDependabotAlerts {
			security = &open[i]
		}
	}
	if security == nil {
		t.Fatal("the security card is gone — the retraction removed the only card that names the severity, the GHSA, the CVE and the package")
	}
	if security.ID != securityID {
		t.Errorf("card id moved %q → %q — a new id is a new notification for a condition whose fingerprint never moved", securityID, security.ID)
	}
	if security.Lifecycle.State != attention.StateOpen {
		t.Errorf("state = %q, want %q", security.Lifecycle.State, attention.StateOpen)
	}
}

// TestDependabotAlertsLifecycle_PermissionDenialLeavesTheRestOfTheSweepWorking
// is the blast-radius test: a token that cannot read the security tab must cost
// exactly the security cards, not the whole Action Center for that repository.
func TestDependabotAlertsLifecycle_PermissionDenialLeavesTheRestOfTheSweepWorking(t *testing.T) {
	sec := &alertSecurity{err: fmt.Errorf("the forge refused to serve alerts: %w", forge.ErrPermissionDenied)}
	other := &scriptedProducer{name: "test-default-branch", reqs: []attention.DecisionRequest{
		observation("test-default-branch:octocat/acme:main", "check:build=failure"),
	}}
	sw, store := newSweeper(t, &alertForge{sec: sec}, newAlertProducer(), other)

	res, err := sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Skipped {
		t.Fatalf("the whole sweep was skipped over one surface's missing scope: %q", res.SkipReason)
	}
	if res.Reconciled.Created != 1 {
		t.Fatalf("created = %d, want 1 — main is red and nothing said so (%+v)", res.Reconciled.Created, res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 1 {
		t.Fatal("the red-main card was never raised")
	}
	if _, failed := res.Failed[ProducerDependabotAlerts]; !failed {
		t.Errorf("the security producer's failure was not reported: %+v", res)
	}

	// And the cards that DO get raised must still be able to retract: a skipped
	// sweep freezes them until StandingExpiry, 30 days later.
	other.reqs = nil
	res, err = sw.Sweep(context.Background(), "octocat/acme")
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Reconciled.AutoResolved != 1 {
		t.Fatalf("main went green and the stale card could not be retracted: %+v", res.Reconciled)
	}
	if openCount(t, store, "octocat/acme") != 0 {
		t.Fatal("a cleared condition left its card open")
	}
}

// TestDependabotAlerts_NoRemediationPRCardLinksToTheAlert is the destination
// half of the card class this issue exists for.
//
// The advisory URL is a public database page that names neither the repository,
// nor the manifest, nor offers a dismiss. A card whose whole message is "there
// is nothing to merge, go decide something" has to land on the alert itself.
func TestDependabotAlerts_NoRemediationPRCardLinksToTheAlert(t *testing.T) {
	p := newAlertProducer()
	// Shaped exactly as the GitHub adapter emits it: URL is the alert's own
	// deep link, AdvisoryURL the public advisory page.
	a := alertWithoutPR()
	a.URL = "https://forge/octocat/acme/security/dependabot/9"
	a.AdvisoryURL = "https://forge/advisories/GHSA-dddd-eeee-ffff"

	got, err := p.Evaluate(context.Background(), alertInput(enabled(a)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got[0].Context.URL != a.URL {
		t.Errorf("card URL = %q, want the alert's own deep link %q", got[0].Context.URL, a.URL)
	}
	if got[0].Context.URL == a.AdvisoryURL {
		t.Error("the card sends the operator to the public advisory database page, which names neither the repository nor the manifest")
	}
}

// TestDependabotAlerts_AlertWithNoAdvisoryStillIdentifiesItself covers the
// forge's nullable securityAdvisory / securityVulnerability. Without a fallback
// the title reads "unknown severity in  — no remediation PR exists", which the
// operator cannot even tell apart from the next such card.
func TestDependabotAlerts_AlertWithNoAdvisoryStillIdentifiesItself(t *testing.T) {
	p := newAlertProducer()
	bare := forgetypes.SecurityAlert{
		Number:      63,
		URL:         "https://forge/octocat/acme/security/dependabot/63",
		Severity:    forgetypes.AlertSeverityUnknown,
		FirstSeenAt: seenLongAgo,
		Remediation: forgetypes.Remediation{State: forgetypes.RemediationNone},
	}

	got, err := p.Evaluate(context.Background(), alertInput(enabled(bare)))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
	if !strings.Contains(got[0].Title, "#63") {
		t.Errorf("title %q identifies nothing — the package, advisory id and CVE are all absent", got[0].Title)
	}
	if got[0].Context.URL != bare.URL {
		t.Errorf("card URL = %q, want the alert deep link — with no advisory there is no other link at all", got[0].Context.URL)
	}
}
