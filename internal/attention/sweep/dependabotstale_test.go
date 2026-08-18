package sweep

// Tests for the stale-remediation producer (#649).
//
// Modelled on dependabotalerts_test.go and humangate_test.go: the producer must
// be exercisable with no network, and the identical producer must run against a
// GitLab adapter without a line changing — which is only true if it never sees
// anything but the forge interfaces.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// --- fakes ------------------------------------------------------------------

type staleForge struct {
	sec *alertSecurity
	prs *stalePRs
}

func (f *staleForge) Issues() forge.IssueService    { return nil }
func (f *staleForge) Project() forge.ProjectService { return nil }
func (f *staleForge) Board() forge.BoardService     { return nil }
func (f *staleForge) CI() forge.CIService           { return nil }
func (f *staleForge) Labels() forge.LabelService    { return nil }
func (f *staleForge) Rulesets() forge.RulesetService {
	return nil
}
func (f *staleForge) Auth() forge.AuthService { return nil }
func (f *staleForge) Repo() forge.RepoService { return nil }
func (f *staleForge) PRs() forge.PRService {
	if f.prs == nil {
		return nil
	}
	return f.prs
}
func (f *staleForge) Security() forge.SecurityService {
	if f.sec == nil {
		return nil
	}
	return f.sec
}

// stalePRs answers GetPR from a fixed table and counts the calls, so "the cheap
// filter really is cheap" is a pinned property rather than an assertion in a
// comment.
type stalePRs struct {
	byNumber map[int]types.PullRequest
	err      error
	calls    int
}

func (p *stalePRs) GetPR(_ context.Context, _, _ string, number int) (*types.PullRequest, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	pr, ok := p.byNumber[number]
	if !ok {
		return nil, fmt.Errorf("no such PR #%d", number)
	}
	return &pr, nil
}

func (p *stalePRs) ListPRs(context.Context, string, string, string, string) ([]types.PullRequest, error) {
	return nil, forge.ErrUnsupported
}
func (p *stalePRs) IteratePRs(context.Context, string, string, string, string) forge.Iterator[types.PullRequest] {
	return nil
}
func (p *stalePRs) CreatePR(context.Context, string, string, string, string, string) (*types.PullRequest, error) {
	return nil, forge.ErrUnsupported
}
func (p *stalePRs) UpdatePR(context.Context, string, forge.UpdatePROptions) (*types.PullRequest, error) {
	return nil, forge.ErrUnsupported
}
func (p *stalePRs) ClosePR(context.Context, string) error { return forge.ErrUnsupported }
func (p *stalePRs) MergePR(context.Context, string) error { return forge.ErrUnsupported }
func (p *stalePRs) MergePRWithStrategy(context.Context, string, string) (string, error) {
	return "", forge.ErrUnsupported
}
func (p *stalePRs) DeleteBranch(context.Context, string, string, string) error {
	return forge.ErrUnsupported
}
func (p *stalePRs) CreateEpicPR(context.Context, string, string, int, string, string, string) (*forgetypes.EpicPRResult, error) {
	return nil, forge.ErrUnsupported
}
func (p *stalePRs) MergeEpicPR(context.Context, string, string, string, string) error {
	return forge.ErrUnsupported
}

// openPR is an open remediation PR the forge is happy to describe.
func openPR(number int) types.PullRequest {
	return types.PullRequest{
		Number: number,
		Title:  fmt.Sprintf("chore(deps): bump thing (#%d)", number),
		State:  "OPEN",
		URL:    fmt.Sprintf("https://forge/pull/%d", number),
	}
}

func prTable(prs ...types.PullRequest) *stalePRs {
	byNumber := make(map[int]types.PullRequest, len(prs))
	for _, pr := range prs {
		byNumber[pr.Number] = pr
	}
	return &stalePRs{byNumber: byNumber}
}

// staleAlert is an advisory with an open remediation PR, first seen `age` ago.
func staleAlert(number, pr int, age time.Duration) forgetypes.SecurityAlert {
	return forgetypes.SecurityAlert{
		Number:              number,
		URL:                 fmt.Sprintf("https://forge/octocat/acme/security/dependabot/%d", number),
		Severity:            forgetypes.AlertSeverityHigh,
		AdvisoryID:          fmt.Sprintf("GHSA-%04d-aaaa-bbbb", number),
		CVE:                 fmt.Sprintf("CVE-2026-%04d", number),
		Summary:             "prototype pollution in widget",
		AdvisoryURL:         fmt.Sprintf("https://forge/advisories/GHSA-%04d-aaaa-bbbb", number),
		Package:             "widget",
		Ecosystem:           "npm",
		ManifestPath:        "package-lock.json",
		FirstPatchedVersion: "2.1.0",
		FirstSeenAt:         fixedNow.Add(-age).Format(time.RFC3339),
		Remediation: forgetypes.Remediation{
			State:    forgetypes.RemediationPROpen,
			PRNumber: pr,
			PRURL:    fmt.Sprintf("https://forge/pull/%d", pr),
			PRTitle:  "chore(deps): bump widget to 2.1.0",
		},
	}
}

func staleInput(sec *alertSecurity, prs *stalePRs, existing ...attention.DecisionRequest) Input {
	return Input{
		Repo:     "octocat/acme",
		Owner:    "octocat",
		Name:     "acme",
		Forge:    &staleForge{sec: sec, prs: prs},
		Existing: existing,
	}
}

func newStaleProducer() *DependabotStaleRemediation {
	return &DependabotStaleRemediation{Now: func() time.Time { return fixedNow }}
}

func evaluateStale(t *testing.T, in Input) []attention.DecisionRequest {
	t.Helper()
	got, err := newStaleProducer().Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return got
}

// --- tests ------------------------------------------------------------------

// TestStaleRemediationIsRegisteredInTheDefaultRegistry is the anti-dead-code
// fence. A producer nothing evaluates is exactly the defect #649 exists to
// avoid repeating, and the only thing that wires this one in is the init() in
// dependabotstale.go — deleting it breaks nothing else that compiles.
func TestStaleRemediationIsRegisteredInTheDefaultRegistry(t *testing.T) {
	for _, p := range Default.Producers() {
		if p.Name() == ProducerDependabotStaleRemediation {
			return
		}
	}
	t.Fatalf("%s is not in the default registry — nothing evaluates it, so it raises nothing",
		ProducerDependabotStaleRemediation)
}

// TestStaleRemediationCardsAPRPastTheThreshold — the acceptance criterion,
// stated as one card that names the advisory, the package and the PR.
func TestStaleRemediationCardsAPRPastTheThreshold(t *testing.T) {
	prs := prTable(openPR(412))
	got := evaluateStale(t, staleInput(enabled(staleAlert(7, 412, 10*24*time.Hour)), prs))
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	req := got[0]

	if want := "dependabot-stale-remediation:octocat/acme#412"; req.IdempotencyKey != want {
		t.Errorf("idempotency_key = %q, want %q", req.IdempotencyKey, want)
	}
	if req.Context.PR != 412 {
		t.Errorf("context.pr = %d, want 412", req.Context.PR)
	}
	if req.Context.URL != "https://forge/pull/412" {
		t.Errorf("context.url = %q, want the PR — the only place the operator can act", req.Context.URL)
	}
	if req.Severity != attention.SeverityBlockingRun {
		t.Errorf("severity = %q, want %q — a finished unit of work is stalled",
			req.Severity, attention.SeverityBlockingRun)
	}
	// Naming: the advisory, the package, the PR. Each is a separate criterion
	// so a regression names which fact went missing.
	for _, want := range []string{"GHSA-0007-aaaa-bbbb", "widget", "#412"} {
		if !strings.Contains(req.Title+req.Body+req.Context.Blocker, want) {
			t.Errorf("card never mentions %q — title=%q blocker=%q", want, req.Title, req.Context.Blocker)
		}
	}
	// No option may claim to repair this: nothing in the registry merges a PR.
	for _, o := range req.Options {
		if o.Verb != attention.VerbNoop {
			t.Errorf("option %q binds %q — no verb can merge a pull request", o.ID, o.Verb)
		}
	}
}

// TestStaleRemediationIgnoresAPRInsideTheThreshold — and spends nothing looking
// at it. The upper-bound filter (a PR cannot predate its advisory) is what
// keeps this producer free on a healthy repo.
func TestStaleRemediationIgnoresAPRInsideTheThreshold(t *testing.T) {
	prs := prTable(openPR(412))
	got := evaluateStale(t, staleInput(enabled(staleAlert(7, 412, 3*24*time.Hour)), prs))
	if len(got) != 0 {
		t.Fatalf("requests = %d, want 0 — three days is inside the seven-day default", len(got))
	}
	if prs.calls != 0 {
		t.Errorf("GetPR calls = %d, want 0 — an advisory inside the threshold must cost no PR read", prs.calls)
	}
}

// TestStaleRemediationFingerprintBucketsTheWait pins BOTH directions of the
// fingerprint rule, which is the whole reason this is a separate producer:
// stable inside a bucket (so it refreshes quietly), moving across one (so a
// genuine escalation alerts).
func TestStaleRemediationFingerprintBucketsTheWait(t *testing.T) {
	fingerprintAt := func(age time.Duration) string {
		t.Helper()
		got := evaluateStale(t, staleInput(enabled(staleAlert(7, 412, age)), prTable(openPR(412))))
		if len(got) != 1 {
			t.Fatalf("age %s: requests = %d, want 1", age, len(got))
		}
		return got[0].Fingerprint
	}

	// Inside one bucket: 7d, 7d+1h, and 13d23h are all "one threshold overdue".
	base := fingerprintAt(7 * 24 * time.Hour)
	for _, age := range []time.Duration{
		7*24*time.Hour + time.Hour,
		10 * 24 * time.Hour,
		14*24*time.Hour - time.Hour,
	} {
		if got := fingerprintAt(age); got != base {
			t.Errorf("age %s: fingerprint = %q, want the bucket-1 fingerprint %q — a fingerprint "+
				"that moves with the clock re-alerts on every sweep", age, got, base)
		}
	}

	// Across the boundary: exactly two thresholds is a new bucket, and so is
	// three.
	two := fingerprintAt(14 * 24 * time.Hour)
	three := fingerprintAt(21 * 24 * time.Hour)
	if two == base {
		t.Errorf("crossing into the second threshold multiple left the fingerprint at %q — "+
			"the escalation would never alert", two)
	}
	if three == two || three == base {
		t.Errorf("third bucket fingerprint %q collides with an earlier one", three)
	}
}

// TestStaleRemediationFingerprintTracksTheAdvisorySet — a second advisory
// attaching to the same PR is a real transition; the forge's ordering is not.
func TestStaleRemediationFingerprintTracksTheAdvisorySet(t *testing.T) {
	one := evaluateStale(t, staleInput(
		enabled(staleAlert(7, 412, 10*24*time.Hour)), prTable(openPR(412))))
	two := evaluateStale(t, staleInput(
		enabled(staleAlert(7, 412, 10*24*time.Hour), staleAlert(9, 412, 9*24*time.Hour)),
		prTable(openPR(412))))
	reordered := evaluateStale(t, staleInput(
		enabled(staleAlert(9, 412, 9*24*time.Hour), staleAlert(7, 412, 10*24*time.Hour)),
		prTable(openPR(412))))

	if len(one) != 1 || len(two) != 1 || len(reordered) != 1 {
		t.Fatalf("want one card per PR in every case, got %d/%d/%d", len(one), len(two), len(reordered))
	}
	if one[0].Fingerprint == two[0].Fingerprint {
		t.Error("a second advisory on the same PR left the fingerprint unmoved")
	}
	if two[0].Fingerprint != reordered[0].Fingerprint {
		t.Errorf("the forge's alert ordering changed the fingerprint (%q vs %q) — it must be sorted",
			two[0].Fingerprint, reordered[0].Fingerprint)
	}
}

// TestStaleRemediationGroupsAdvisoriesSharingOnePR.
//
// One bump routinely fixes several advisories. Emitting one observation per
// advisory would put the same PR-keyed idempotency key in a sweep more than
// once, which ReconcileStanding rejects — taking the whole repo's sweep down,
// including every other producer's reconciliation.
func TestStaleRemediationGroupsAdvisoriesSharingOnePR(t *testing.T) {
	prs := prTable(openPR(412), openPR(500))
	got := evaluateStale(t, staleInput(enabled(
		staleAlert(7, 412, 10*24*time.Hour),
		staleAlert(8, 412, 12*24*time.Hour),
		staleAlert(9, 412, 8*24*time.Hour),
		staleAlert(11, 500, 30*24*time.Hour),
	), prs))

	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2 (one per PR)", len(got))
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.IdempotencyKey] {
			t.Fatalf("duplicate idempotency_key %q in one observation — ReconcileStanding "+
				"rejects the whole sweep for this", r.IdempotencyKey)
		}
		seen[r.IdempotencyKey] = true
	}
	// Observation order must not depend on Go's map iteration.
	if got[0].Context.PR != 412 || got[1].Context.PR != 500 {
		t.Errorf("observations are not in PR order: %d then %d", got[0].Context.PR, got[1].Context.PR)
	}
	// The wait is the OLDEST advisory's: 12 days, not 8.
	if !strings.Contains(got[0].Body, "12d") {
		t.Errorf("body does not report the longest wait (12d):\n%s", got[0].Body)
	}
	if !strings.Contains(got[0].Body, "Advisories this PR fixes (3)") {
		t.Errorf("body does not name all three advisories:\n%s", got[0].Body)
	}
}

// TestStaleRemediationStopsObservingAMergedOrClosedPR — the retraction signal.
// The advisory alone cannot say this: the forge reports its dependabotUpdate
// pull request whatever state it is in.
func TestStaleRemediationStopsObservingAMergedOrClosedPR(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		pr := openPR(412)
		pr.State = state
		got := evaluateStale(t, staleInput(
			enabled(staleAlert(7, 412, 30*24*time.Hour)), prTable(pr)))
		if len(got) != 0 {
			t.Errorf("state %s: requests = %d, want 0 — the card must retract", state, len(got))
		}
	}
}

// TestStaleRemediationErrorsRatherThanRetracting — the NEVER FATAL commitment
// from the producer's side: every "I could not look" is an error, so the sweep
// drops this producer from reconciliation and leaves its cards exactly where
// they were. A nil slice here would auto-resolve live cards as
// "condition_cleared".
func TestStaleRemediationErrorsRatherThanRetracting(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"read failed", staleInput(&alertSecurity{err: errors.New("boom")}, prTable(openPR(412)))},
		{"forge returned nothing", staleInput(&alertSecurity{}, prTable(openPR(412)))},
		{"scanning disabled", staleInput(&alertSecurity{res: &forgetypes.SecurityAlerts{
			Status: forgetypes.SecurityAlertsDisabled,
		}}, prTable(openPR(412)))},
		{"alert list truncated", staleInput(&alertSecurity{res: &forgetypes.SecurityAlerts{
			Status:    forgetypes.SecurityAlertsEnabled,
			Alerts:    []forgetypes.SecurityAlert{staleAlert(7, 412, 30*24*time.Hour)},
			TotalOpen: 500,
			Truncated: true,
		}}, prTable(openPR(412)))},
		{"PR read failed", staleInput(
			enabled(staleAlert(7, 412, 30*24*time.Hour)),
			&stalePRs{err: errors.New("boom")})},
		{"PR unknown to the forge", staleInput(
			enabled(staleAlert(7, 412, 30*24*time.Hour)), prTable())},
		{"no PR service", staleInput(enabled(staleAlert(7, 412, 30*24*time.Hour)), nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newStaleProducer().Evaluate(context.Background(), tc.in)
			if err == nil {
				t.Fatalf("Evaluate returned nil error — an unobservable repo must not look like a clean one")
			}
			if len(got) != 0 {
				t.Errorf("requests = %d alongside an error, want 0", len(got))
			}
		})
	}
}

// TestStaleRemediationRefusesAnUninspectablePile — the same call
// errAlertsTruncated makes: a partial pass is not an observation.
func TestStaleRemediationRefusesAnUninspectablePile(t *testing.T) {
	alerts := make([]forgetypes.SecurityAlert, 0, StaleRemediationMaxInspect+1)
	prs := prTable()
	for i := 0; i <= StaleRemediationMaxInspect; i++ {
		alerts = append(alerts, staleAlert(100+i, 1000+i, 30*24*time.Hour))
		prs.byNumber[1000+i] = openPR(1000 + i)
	}
	got, err := newStaleProducer().Evaluate(context.Background(), staleInput(enabled(alerts...), prs))
	if !errors.Is(err, errStaleRemediationTooMany) {
		t.Fatalf("err = %v, want errStaleRemediationTooMany", err)
	}
	if len(got) != 0 {
		t.Errorf("requests = %d alongside the error, want 0", len(got))
	}
	if prs.calls != 0 {
		t.Errorf("GetPR calls = %d, want 0 — the refusal must precede the spend", prs.calls)
	}
}

// TestStaleRemediationSkipsAnAlertWithNoMeasurableAge. The producer's entire
// claim is about elapsed time; with no first-seen timestamp there is none.
// dependabot-alerts still cards the advisory itself, so nothing is lost.
func TestStaleRemediationSkipsAnAlertWithNoMeasurableAge(t *testing.T) {
	a := staleAlert(7, 412, 30*24*time.Hour)
	a.FirstSeenAt = ""
	got := evaluateStale(t, staleInput(enabled(a), prTable(openPR(412))))
	if len(got) != 0 {
		t.Fatalf("requests = %d, want 0 — an unmeasurable wait cannot be asserted stale", len(got))
	}
}

// TestStaleRemediationDoesNotDuplicateTheAdvisoryCard — the Input.Existing
// criterion.
//
// It is a CONTENT rule, not a skip, and the two assertions below are equally
// load-bearing. dependabot-alerts cards every open advisory, so a
// `OpenRequestForPR(dependabot-alerts, pr) → continue` guard would suppress
// this producer for as long as the advisory card stayed open — i.e. for the
// whole 40-day window it exists to catch — and would make this card come and go
// with another producer's card lifecycle (the auto-resolve-then-re-raise defect
// dependabotalerts.go's header records).
func TestStaleRemediationDoesNotDuplicateTheAdvisoryCard(t *testing.T) {
	alert := staleAlert(7, 412, 30*24*time.Hour)
	advisoryCard := attention.DecisionRequest{
		ID:             "dr_advisory",
		IdempotencyKey: fmt.Sprintf("%s:octocat/acme#%d", ProducerDependabotAlerts, alert.Number),
		Producer:       ProducerDependabotAlerts,
		Context:        attention.Context{Repo: "octocat/acme", PR: 412},
	}

	withCard := evaluateStale(t, staleInput(enabled(alert), prTable(openPR(412)), advisoryCard))
	if len(withCard) != 1 {
		t.Fatalf("requests = %d, want 1 — an open advisory card must not suppress the escalation", len(withCard))
	}
	if strings.Contains(withCard[0].Body, alert.Summary) {
		t.Errorf("the body restates the advisory summary that the open advisory card already carries:\n%s",
			withCard[0].Body)
	}
	if !strings.Contains(withCard[0].Body, "Already in the Action Center") {
		t.Errorf("the body does not point at the advisory card it declines to restate:\n%s", withCard[0].Body)
	}

	// With no advisory card open — dismissed, expired, or raised before this
	// producer existed — this card is the only place the advisory is described,
	// so it states it.
	alone := evaluateStale(t, staleInput(enabled(alert), prTable(openPR(412))))
	if len(alone) != 1 {
		t.Fatalf("requests = %d, want 1", len(alone))
	}
	if !strings.Contains(alone[0].Body, alert.Summary) {
		t.Errorf("with no advisory card open, the body must state the advisory itself:\n%s", alone[0].Body)
	}

	// The fingerprint may not move with another producer's card lifecycle: a
	// dismissal over there would otherwise re-alert over here.
	if withCard[0].Fingerprint != alone[0].Fingerprint {
		t.Errorf("fingerprint moved with the advisory card's presence (%q vs %q)",
			withCard[0].Fingerprint, alone[0].Fingerprint)
	}

	// A terminal advisory card is not an open one.
	dismissed := advisoryCard
	dismissed.Lifecycle.State = attention.StateResolved
	afterDismissal := evaluateStale(t, staleInput(enabled(alert), prTable(openPR(412)), dismissed))
	if len(afterDismissal) != 1 || !strings.Contains(afterDismissal[0].Body, alert.Summary) {
		t.Errorf("a dismissed advisory card was treated as still carrying the advisory:\n%s",
			afterDismissal[0].Body)
	}
}

// TestStaleRemediationThresholdComesFromConfig — the threshold is a policy, and
// an operator has to be able to disagree with seven days.
func TestStaleRemediationThresholdComesFromConfig(t *testing.T) {
	// LoadMerged reads the machine tier too; point it at nothing so the
	// developer's own ~/.nightgauge/config.yaml cannot decide this test.
	restore := config.SwapMachineConfigPathForTest(func() (string, error) {
		return filepath.Join(t.TempDir(), "absent.yaml"), nil
	})
	t.Cleanup(restore)

	writeRoot := func(t *testing.T, body string) string {
		t.Helper()
		root := t.TempDir()
		dir := filepath.Join(root, ".nightgauge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return root
	}

	// A ten-day-old fix. The compiled default (7) cards it; a configured 30
	// does not; a configured 3 does, in its third bucket.
	cases := []struct {
		name       string
		yaml       string
		wantCards  int
		wantInBody string
	}{
		{
			name:       "default when the section is absent",
			yaml:       "owner: octocat\nrepo: acme\n",
			wantCards:  1,
			wantInBody: "threshold: 7 days",
		},
		{
			name:      "a longer configured threshold suppresses it",
			yaml:      "owner: octocat\nrepo: acme\nattention:\n  dependabot_stale_remediation_days: 30\n",
			wantCards: 0,
		},
		{
			name:       "a shorter configured threshold escalates it",
			yaml:       "owner: octocat\nrepo: acme\nattention:\n  dependabot_stale_remediation_days: 3\n",
			wantCards:  1,
			wantInBody: "threshold: 3 days",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := staleInput(enabled(staleAlert(7, 412, 10*24*time.Hour)), prTable(openPR(412)))
			in.WorkspaceRoot = writeRoot(t, tc.yaml)
			got := evaluateStale(t, in)
			if len(got) != tc.wantCards {
				t.Fatalf("requests = %d, want %d — the threshold did not come from config", len(got), tc.wantCards)
			}
			if tc.wantInBody != "" && !strings.Contains(got[0].Body, tc.wantInBody) {
				t.Errorf("body does not state %q:\n%s", tc.wantInBody, got[0].Body)
			}
		})
	}

	// The configured threshold is the bucket width too, so it belongs in the
	// fingerprint: the same wait means something different under a different
	// policy.
	shortIn := staleInput(enabled(staleAlert(7, 412, 10*24*time.Hour)), prTable(openPR(412)))
	shortIn.WorkspaceRoot = writeRoot(t, "owner: octocat\nrepo: acme\nattention:\n  dependabot_stale_remediation_days: 3\n")
	defaultIn := staleInput(enabled(staleAlert(7, 412, 10*24*time.Hour)), prTable(openPR(412)))
	if evaluateStale(t, shortIn)[0].Fingerprint == evaluateStale(t, defaultIn)[0].Fingerprint {
		t.Error("changing the configured threshold left the fingerprint unmoved — the card would " +
			"keep asserting the old figure until the next multiple came round")
	}
}

// TestStaleRemediationSurvivesAMissingWorkspaceRoot — a configuration problem
// is not a failure to observe the repo, and must never reach the error return.
func TestStaleRemediationSurvivesAMissingWorkspaceRoot(t *testing.T) {
	in := staleInput(enabled(staleAlert(7, 412, 10*24*time.Hour)), prTable(openPR(412)))
	in.WorkspaceRoot = filepath.Join(t.TempDir(), "does-not-exist")
	got := evaluateStale(t, in)
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1 on the compiled default", len(got))
	}
}
