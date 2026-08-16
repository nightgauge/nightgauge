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
// Every test here runs against a faked forge client: the producer must be
// exercisable with no network, and the same producer must be able to run
// against a GitLab adapter without a line changing.

type branchForge struct {
	repo *repoSvc
	ci   *branchCI
}

func (f *branchForge) Issues() forge.IssueService      { return nil }
func (f *branchForge) PRs() forge.PRService            { return nil }
func (f *branchForge) Project() forge.ProjectService   { return nil }
func (f *branchForge) Board() forge.BoardService       { return nil }
func (f *branchForge) CI() forge.CIService             { return f.ci }
func (f *branchForge) Labels() forge.LabelService      { return nil }
func (f *branchForge) Rulesets() forge.RulesetService  { return nil }
func (f *branchForge) Security() forge.SecurityService { return nil }
func (f *branchForge) Auth() forge.AuthService         { return nil }
func (f *branchForge) Repo() forge.RepoService         { return f.repo }

type repoSvc struct {
	defaultBranch string
	err           error
}

func (r *repoSvc) RepoMetadata(_ context.Context, owner, name string) (*forgetypes.Repo, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &forgetypes.Repo{
		NameWithOwner: owner + "/" + name,
		Owner:         owner,
		Name:          name,
		DefaultBranch: r.defaultBranch,
	}, nil
}

type branchCI struct {
	required    []string
	requiredErr error
	runs        []forgetypes.CheckDetail
	runsErr     error
	sawBranch   string
}

func (c *branchCI) GetRequiredCheckNames(_ context.Context, _, _, branch string) ([]string, error) {
	c.sawBranch = branch
	return c.required, c.requiredErr
}

func (c *branchCI) GetIndividualCheckRuns(_ context.Context, _, _, _ string) ([]forgetypes.CheckDetail, error) {
	return c.runs, c.runsErr
}

func (c *branchCI) GetCheckStatus(context.Context, string, string, int) (*forgetypes.CheckStatus, error) {
	return nil, forge.ErrUnsupported
}
func (c *branchCI) WaitForChecks(context.Context, string, string, int, forgetypes.WaitConfig) (*forgetypes.CheckStatus, error) {
	return nil, forge.ErrUnsupported
}
func (c *branchCI) GetRunLogs(context.Context, string, string, int64) (*forgetypes.CIRunLog, error) {
	return nil, forge.ErrUnsupported
}

var fixedNow = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

func branchInput(repo *repoSvc, ci *branchCI) Input {
	return Input{
		Repo:  "octocat/acme",
		Owner: "octocat",
		Name:  "acme",
		Forge: &branchForge{repo: repo, ci: ci},
	}
}

func newDefaultBranchProducer() *DefaultBranchHealth {
	return &DefaultBranchHealth{Now: func() time.Time { return fixedNow }}
}

// ago renders an RFC3339 timestamp d before the fixed clock.
func ago(d time.Duration) string { return fixedNow.Add(-d).Format(time.RFC3339) }

// --- tests ------------------------------------------------------------------

func TestDefaultBranch_FailingRequiredCheckRaisesOneFleetBlocker(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: []string{"build", "lint"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", CompletedAt: ago(2 * time.Hour), DetailsURL: "https://forge/run/1", HeadSHA: "abcdef1234567"},
				{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS", CompletedAt: ago(2 * time.Hour)},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("observations = %d, want exactly 1", len(got))
	}
	req := got[0]
	if req.Severity != attention.SeverityBlockingFleet {
		t.Errorf("severity = %q, want %q", req.Severity, attention.SeverityBlockingFleet)
	}
	if req.Kind != attention.KindUnblock {
		t.Errorf("kind = %q, want %q", req.Kind, attention.KindUnblock)
	}
	// Naming the check is the whole point: "CI is red" does not tell the
	// operator whether to re-run a flake or go read a dependency advisory.
	if !strings.Contains(req.Title, "build") {
		t.Errorf("title %q does not name the failing check", req.Title)
	}
	if req.Context.URL != "https://forge/run/1" {
		t.Errorf("context URL = %q, want the failing run's URL", req.Context.URL)
	}
	if !strings.Contains(req.Body, "abcdef1") {
		t.Errorf("body does not carry the failing commit: %s", req.Body)
	}
	if req.Fingerprint != "checks:build" {
		t.Errorf("fingerprint = %q, want %q", req.Fingerprint, "checks:build")
	}
}

func TestDefaultBranch_PendingChecksNeverRaise(t *testing.T) {
	p := newDefaultBranchProducer()
	// A fresh push: required checks queued and running, none concluded. This is
	// what a healthy branch looks like for the first few minutes after a merge.
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: []string{"build", "lint"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "IN_PROGRESS", Conclusion: ""},
				{Name: "lint", Status: "QUEUED", Conclusion: ""},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0 — a pending check is not a blocker", len(got))
	}
}

func TestDefaultBranch_FailureInsideGraceNeverSurfaces(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: []string{"build"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", CompletedAt: ago(2 * time.Minute)},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0 — the failure is inside the grace window", len(got))
	}
}

func TestDefaultBranch_FailureOutlivingGraceSurfaces(t *testing.T) {
	p := &DefaultBranchHealth{Now: func() time.Time { return fixedNow }, Grace: time.Minute}
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: []string{"build"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", CompletedAt: ago(5 * time.Minute)},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("observations = %d, want 1", len(got))
	}
}

func TestDefaultBranch_GreenBranchAutoResolvesByObservingNothing(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: []string{"build"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS", CompletedAt: ago(time.Hour)},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// An empty slice with a nil error is the positive assertion that clears the
	// card. Returning an error here instead would leave a stale blocker up.
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0", len(got))
	}
}

func TestDefaultBranch_NonRequiredFailureIsNotAFleetBlocker(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: []string{"build"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS", CompletedAt: ago(time.Hour)},
				{Name: "optional-bench", Status: "COMPLETED", Conclusion: "FAILURE", CompletedAt: ago(time.Hour)},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0 — a non-required check blocks nothing", len(got))
	}
}

func TestDefaultBranch_NoRequiredChecksConfiguredStaysSilent(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(
		&repoSvc{defaultBranch: "main"},
		&branchCI{
			required: nil,
			runs: []forgetypes.CheckDetail{
				{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE", CompletedAt: ago(time.Hour)},
			},
		},
	)

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Nothing is required to merge, so nothing is blocked — this producer's
	// claim is "the fleet is stopped", and it would be false here.
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0", len(got))
	}
}

func TestDefaultBranch_SecondFailingCheckMovesTheFingerprint(t *testing.T) {
	p := newDefaultBranchProducer()
	one := branchInput(&repoSvc{defaultBranch: "main"}, &branchCI{
		required: []string{"build", "lint"},
		runs: []forgetypes.CheckDetail{
			{Name: "build", Conclusion: "FAILURE", CompletedAt: ago(time.Hour)},
		},
	})
	two := branchInput(&repoSvc{defaultBranch: "main"}, &branchCI{
		required: []string{"build", "lint"},
		runs: []forgetypes.CheckDetail{
			{Name: "lint", Conclusion: "FAILURE", CompletedAt: ago(time.Hour)},
			{Name: "build", Conclusion: "FAILURE", CompletedAt: ago(time.Hour)},
		},
	})

	first, err := p.Evaluate(context.Background(), one)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	second, err := p.Evaluate(context.Background(), two)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if first[0].Fingerprint == second[0].Fingerprint {
		t.Fatal("fingerprint unchanged when a second required check started failing — the operator would never be re-alerted")
	}
	// Sorted, so check-run ordering from the forge cannot fake a transition.
	if second[0].Fingerprint != "checks:build,lint" {
		t.Errorf("fingerprint = %q, want %q", second[0].Fingerprint, "checks:build,lint")
	}
	if first[0].IdempotencyKey != second[0].IdempotencyKey {
		t.Error("idempotency key moved with the condition — the card would duplicate instead of updating")
	}
}

func TestDefaultBranch_FingerprintIgnoresElapsedTimeAndCommit(t *testing.T) {
	early := &DefaultBranchHealth{Now: func() time.Time { return fixedNow }}
	late := &DefaultBranchHealth{Now: func() time.Time { return fixedNow.Add(48 * time.Hour) }}
	mk := func(sha string) Input {
		return branchInput(&repoSvc{defaultBranch: "main"}, &branchCI{
			required: []string{"build"},
			runs: []forgetypes.CheckDetail{
				{Name: "build", Conclusion: "FAILURE", CompletedAt: ago(time.Hour), HeadSHA: sha},
			},
		})
	}

	a, err := early.Evaluate(context.Background(), mk("aaaaaaaaaa"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	b, err := late.Evaluate(context.Background(), mk("bbbbbbbbbb"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// Two days later, on a different commit, the same check is still failing.
	// That is the same condition — re-alerting on it is exactly the behaviour
	// that trains operators to ignore the inbox.
	if a[0].Fingerprint != b[0].Fingerprint {
		t.Errorf("fingerprint moved with time/commit: %q vs %q", a[0].Fingerprint, b[0].Fingerprint)
	}
}

func TestDefaultBranch_UnknownDefaultBranchDeclinesToObserve(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(&repoSvc{defaultBranch: ""}, &branchCI{required: []string{"build"}})

	got, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("observations = %d, want 0", len(got))
	}
}

func TestDefaultBranch_ForgeErrorPropagatesForSweepToHandle(t *testing.T) {
	p := newDefaultBranchProducer()
	in := branchInput(&repoSvc{defaultBranch: "main"}, &branchCI{
		required: []string{"build"},
		runsErr:  forge.ErrRateLimited,
	})

	got, err := p.Evaluate(context.Background(), in)
	if err == nil {
		t.Fatal("Evaluate returned nil error on a rate limit — the sweep would read the empty slice as 'condition cleared' and retract a live card")
	}
	if got != nil {
		t.Errorf("observations = %v, want nil alongside the error", got)
	}
	// The sentinel has to survive wrapping or the sweeper cannot tell a
	// repo-wide failure from one producer's bad day.
	if !errors.Is(err, forge.ErrRateLimited) {
		t.Errorf("err = %v, want it to wrap forge.ErrRateLimited", err)
	}
}

func TestDefaultBranch_EvaluatesTheReportedDefaultBranchNotAGuess(t *testing.T) {
	p := newDefaultBranchProducer()
	ci := &branchCI{required: []string{"build"}}
	in := branchInput(&repoSvc{defaultBranch: "trunk"}, ci)

	if _, err := p.Evaluate(context.Background(), in); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ci.sawBranch != "trunk" {
		t.Errorf("evaluated branch %q, want %q — hardcoding main breaks every repo that renamed", ci.sawBranch, "trunk")
	}
}

func TestDefaultBranch_RegisteredInTheDefaultRegistry(t *testing.T) {
	var found bool
	for _, p := range Default.Producers() {
		if p.Name() == ProducerDefaultBranchHealth {
			found = true
		}
	}
	if !found {
		t.Fatalf("%q is not in the default registry — `nightgauge attention sweep` would never run it", ProducerDefaultBranchHealth)
	}
}
