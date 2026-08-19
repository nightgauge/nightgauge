package sweep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// gapProducer builds a CoverageGap with both discovery sources stubbed, so a
// test states exactly what is present without touching the filesystem or a
// forge.
func gapProducer(local, board []string) *CoverageGap {
	return &CoverageGap{
		DiscoverLocal: func(string) ([]string, error) { return local, nil },
		DiscoverBoard: func(context.Context, WorkspaceInput) ([]string, error) { return board, nil },
	}
}

func gapInput(configured ...string) WorkspaceInput {
	return WorkspaceInput{ConfiguredRepos: normalizeRepos(configured)}
}

// THE acceptance criterion: a repo present in the workspace but outside the
// configured list produces exactly one coverage card for it.
func TestCoverageGap_UncoveredRepo_RaisesExactlyOneCard(t *testing.T) {
	p := gapProducer([]string{"acme/web", "acme/unwatched"}, nil)

	reqs, err := p.Evaluate(context.Background(), gapInput("acme/web"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("want exactly 1 card, got %d: %+v", len(reqs), reqs)
	}
	r := reqs[0]
	if r.Context.Repo != "acme/unwatched" {
		t.Errorf("card should name the uncovered repo; got %q", r.Context.Repo)
	}
	if r.Severity != attention.SeverityFYI {
		t.Errorf("an intentionally-excluded repo must be dismissable without friction; severity = %q", r.Severity)
	}
	if r.IdempotencyKey != ProducerCoverageGap+":acme/unwatched" {
		t.Errorf("unexpected idempotency key %q", r.IdempotencyKey)
	}
	// Since #706 the card is no longer a dead end: it offers the bounded
	// workspace.addRepo repair alongside dismiss. Dismiss must survive — a
	// deliberate omission is still a first-class answer.
	if len(r.Options) != 2 {
		t.Fatalf("want a repair option and a dismiss option; got %+v", r.Options)
	}
	repair, dismiss := r.Options[0], r.Options[1]
	if repair.Verb != attention.VerbWorkspaceAddRepo {
		t.Errorf("first option should repair via workspace.addRepo; got %q", repair.Verb)
	}
	// The verb reads its target from Context.Repo. An option carrying args
	// would hand the resolving surface a way to redirect the write, and
	// ExecuteAddRepo rejects args outright — so a card that shipped them would
	// be a guaranteed-failing button.
	if len(repair.Args) != 0 {
		t.Errorf("the repair option must carry no args; got %+v", repair.Args)
	}
	if dismiss.Verb != attention.VerbNoop {
		t.Errorf("dismiss must remain a no-op; got %q", dismiss.Verb)
	}
}

// Both discovery sources contribute; a repo seen only on the board still counts
// as present, since a repo can be linked to the project without a local clone.
func TestCoverageGap_BoardOnlyRepo_IsStillPresent(t *testing.T) {
	p := gapProducer([]string{"acme/web"}, []string{"acme/web", "acme/board-only"})

	reqs, err := p.Evaluate(context.Background(), gapInput("acme/web"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Context.Repo != "acme/board-only" {
		t.Fatalf("want one card for acme/board-only, got %+v", reqs)
	}
}

// Full coverage is the positive observation that auto-resolves prior cards.
func TestCoverageGap_EverythingCovered_ReturnsEmpty(t *testing.T) {
	p := gapProducer([]string{"acme/web", "acme/api"}, []string{"acme/web"})

	reqs, err := p.Evaluate(context.Background(), gapInput("acme/web", "acme/api"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("everything covered must raise nothing, got %+v", reqs)
	}
}

// Invariant 1, the sharpest case for THIS producer: if neither discovery
// source worked we have looked nowhere, and an empty slice would assert "no
// coverage gaps" on that basis — auto-resolving real cards.
func TestCoverageGap_BothDiscoverySourcesFail_Errors(t *testing.T) {
	p := &CoverageGap{
		DiscoverLocal: func(string) ([]string, error) { return nil, os.ErrNotExist },
		DiscoverBoard: func(context.Context, WorkspaceInput) ([]string, error) { return nil, os.ErrNotExist },
	}
	if _, err := p.Evaluate(context.Background(), gapInput("acme/web")); err == nil {
		t.Fatal("looking nowhere must error, not report a clean workspace")
	}
}

// One source failing is survivable — the other is still a real observation.
func TestCoverageGap_OneDiscoverySourceFails_StillReports(t *testing.T) {
	p := &CoverageGap{
		DiscoverLocal: func(string) ([]string, error) { return nil, os.ErrNotExist },
		DiscoverBoard: func(context.Context, WorkspaceInput) ([]string, error) {
			return []string{"acme/web", "acme/unwatched"}, nil
		},
	}
	reqs, err := p.Evaluate(context.Background(), gapInput("acme/web"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("want 1 card from the surviving source, got %d", len(reqs))
	}
}

// The fingerprint is the SET of uncovered repos, so dismissing one card stays
// dismissed while the set holds, and a NEW uncovered repo re-alerts.
func TestCoverageGap_FingerprintTracksTheSet(t *testing.T) {
	one, err := gapProducer([]string{"acme/web", "acme/a"}, nil).
		Evaluate(context.Background(), gapInput("acme/web"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	two, err := gapProducer([]string{"acme/web", "acme/a", "acme/b"}, nil).
		Evaluate(context.Background(), gapInput("acme/web"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if one[0].Fingerprint == two[0].Fingerprint {
		t.Error("a newly-uncovered repo changes the condition and must re-alert")
	}

	// Same set observed twice — including in a different discovery order —
	// is the same condition and must NOT re-alert.
	again, err := gapProducer([]string{"acme/a", "acme/web"}, nil).
		Evaluate(context.Background(), gapInput("acme/web"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if one[0].Fingerprint != again[0].Fingerprint {
		t.Errorf("re-observing the same set must not re-alert: %q vs %q", one[0].Fingerprint, again[0].Fingerprint)
	}
}

// Covers matches a bare name against an "owner/name" spec in either direction,
// so a manifest entry written either way counts as coverage.
func TestWorkspaceInput_CoversMatchesBareAndQualifiedNames(t *testing.T) {
	in := gapInput("acme/web")
	for _, repo := range []string{"acme/web", "web", "ACME/WEB"} {
		if !in.Covers(repo) {
			t.Errorf("Covers(%q) = false, want true", repo)
		}
	}
	if in.Covers("acme/other") {
		t.Error("Covers(acme/other) = true, want false")
	}
}

// --- remote-URL parsing ----------------------------------------------------

func TestRepoSpecFromRemoteURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"git@github.com:acme/web.git", "acme/web"},
		{"git@github.com:acme/web", "acme/web"},
		{"https://github.com/acme/web.git", "acme/web"},
		{"https://github.com/acme/web", "acme/web"},
		{"ssh://git@gitlab.example.com/acme/web.git", "acme/web"},
	}
	for _, c := range cases {
		got, ok := repoSpecFromRemoteURL(c.url)
		if !ok || got != c.want {
			t.Errorf("repoSpecFromRemoteURL(%q) = (%q, %v), want (%q, true)", c.url, got, ok, c.want)
		}
	}
	if _, ok := repoSpecFromRemoteURL(""); ok {
		t.Error("empty URL must not parse")
	}
}

// discoverLocalCheckouts reads origin from .git/config, including siblings of
// the workspace root — the layout where the manifest lives inside one member
// repo and its peers sit next to it.
func TestDiscoverLocalCheckouts_FindsSiblings(t *testing.T) {
	parent := t.TempDir()
	mkRepo := func(name, url string) string {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = " + url + "\n\tfetch = +refs/heads/*\n"
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	root := mkRepo("web", "git@github.com:acme/web.git")
	mkRepo("unwatched", "https://github.com/acme/unwatched.git")

	got, err := discoverLocalCheckouts(root)
	if err != nil {
		t.Fatalf("discoverLocalCheckouts: %v", err)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "acme/web") || !strings.Contains(joined, "acme/unwatched") {
		t.Fatalf("want both the root and its sibling, got %v", got)
	}
}

// --- coverage record -------------------------------------------------------

// The footer's input is a record of what was SWEPT, never a config read: a
// missing record must report "nothing looked at", not zero repos.
func TestCoverage_RoundTripAndMissingRecord(t *testing.T) {
	root := t.TempDir()

	if _, ok := ReadCoverage(root); ok {
		t.Fatal("no sweep has run — ReadCoverage must report absent")
	}

	if err := WriteCoverage(root, []string{"acme/web", "acme/api", "acme/web"}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatalf("WriteCoverage: %v", err)
	}
	cov, ok := ReadCoverage(root)
	if !ok {
		t.Fatal("ReadCoverage after write: not found")
	}
	if len(cov.Repos) != 2 {
		t.Errorf("duplicates should collapse; got %v", cov.Repos)
	}
	if cov.SweptAt == "" {
		t.Error("SweptAt must be recorded")
	}

	// It must NOT live inside the attention directory, where Store.List parses
	// every *.json as a DecisionRequest.
	if _, err := os.Stat(filepath.Join(root, ".nightgauge", "attention", "attention-coverage.json")); err == nil {
		t.Error("coverage record must not sit inside the attention store directory")
	}
}

// --- end-to-end through the sweeper ----------------------------------------

// scriptedWorkspaceProducer returns a fixed answer, so the SweepWorkspace
// reconciliation can be tested without a forge or a filesystem.
type scriptedWorkspaceProducer struct {
	name string
	reqs []attention.DecisionRequest
	err  error
}

func (p *scriptedWorkspaceProducer) Name() string { return p.name }
func (p *scriptedWorkspaceProducer) Evaluate(context.Context, WorkspaceInput) ([]attention.DecisionRequest, error) {
	return p.reqs, p.err
}

func coverageCard(repo string) attention.DecisionRequest {
	return attention.DecisionRequest{
		IdempotencyKey: ProducerCoverageGap + ":" + repo,
		Kind:           attention.KindHandoff,
		Severity:       attention.SeverityFYI,
		Title:          repo + " is unwatched",
		Body:           "…",
		Fingerprint:    "uncovered:" + repo,
		Standing:       true,
		Context:        attention.Context{Repo: repo},
		Options:        []attention.Option{{ID: "dismiss", Label: "Dismiss", Verb: attention.VerbNoop}},
		DefaultAction:  attention.ExpireNoop,
	}
}

func workspaceSweeper(t *testing.T, p WorkspaceProducer) (*Sweeper, *attention.Store, string) {
	t.Helper()
	root := t.TempDir()
	store := attention.New(root)
	reg := NewRegistry()
	reg.RegisterWorkspace(p)
	return &Sweeper{Store: store, Registry: reg, WorkspaceRoot: root}, store, root
}

// A workspace producer's card reaches the same store every surface reads, and
// the sweep records what it covered.
func TestSweepWorkspace_RaisesAndRecordsCoverage(t *testing.T) {
	s, store, root := workspaceSweeper(t, &scriptedWorkspaceProducer{
		name: ProducerCoverageGap,
		reqs: []attention.DecisionRequest{coverageCard("acme/unwatched")},
	})

	res, err := s.SweepWorkspace(context.Background(), []string{"acme/web", "acme/api"})
	if err != nil {
		t.Fatalf("SweepWorkspace: %v", err)
	}
	if res.SweptRepos != 2 {
		t.Errorf("SweptRepos = %d, want 2", res.SweptRepos)
	}
	if res.Created != 1 {
		t.Errorf("Created = %d, want 1", res.Created)
	}
	open, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 || open[0].Context.Repo != "acme/unwatched" {
		t.Fatalf("want one open card for acme/unwatched, got %+v", open)
	}
	cov, ok := ReadCoverage(root)
	if !ok || len(cov.Repos) != 2 {
		t.Fatalf("sweep must record what it covered; got %+v ok=%v", cov, ok)
	}
}

// Acceptance criterion: once the repo IS configured, the condition stops
// holding and the card auto-resolves rather than lingering.
func TestSweepWorkspace_ConditionClears_AutoResolves(t *testing.T) {
	s, store, _ := workspaceSweeper(t, &scriptedWorkspaceProducer{
		name: ProducerCoverageGap,
		reqs: []attention.DecisionRequest{coverageCard("acme/unwatched")},
	})
	if _, err := s.SweepWorkspace(context.Background(), []string{"acme/web"}); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	// The repo is now configured, so the producer observes nothing.
	s.Registry = NewRegistry()
	s.Registry.RegisterWorkspace(&scriptedWorkspaceProducer{name: ProducerCoverageGap})
	res, err := s.SweepWorkspace(context.Background(), []string{"acme/web", "acme/unwatched"})
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if res.AutoResolved != 1 {
		t.Errorf("AutoResolved = %d, want 1", res.AutoResolved)
	}
	open, _ := store.List(attention.ListFilter{})
	if len(open) != 0 {
		t.Fatalf("card should be retracted once the repo is covered, got %+v", open)
	}
}

// Invariant 1 at the sweeper level: a producer that ERRORS must leave its
// existing cards alone. Auto-resolving on a failed observation is how a
// transient error silently retracts a real signal.
func TestSweepWorkspace_ProducerError_LeavesCardsUntouched(t *testing.T) {
	s, store, _ := workspaceSweeper(t, &scriptedWorkspaceProducer{
		name: ProducerCoverageGap,
		reqs: []attention.DecisionRequest{coverageCard("acme/unwatched")},
	})
	if _, err := s.SweepWorkspace(context.Background(), []string{"acme/web"}); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	s.Registry = NewRegistry()
	s.Registry.RegisterWorkspace(&scriptedWorkspaceProducer{
		name: ProducerCoverageGap,
		err:  os.ErrNotExist,
	})
	res, err := s.SweepWorkspace(context.Background(), []string{"acme/web"})
	if err != nil {
		t.Fatalf("SweepWorkspace must not fail on a producer error: %v", err)
	}
	if len(res.Failed) != 1 {
		t.Errorf("the failure should be reported, got %+v", res.Failed)
	}
	if res.AutoResolved != 0 {
		t.Errorf("a failed observation must not retract anything; AutoResolved = %d", res.AutoResolved)
	}
	open, _ := store.List(attention.ListFilter{})
	if len(open) != 1 {
		t.Fatalf("card must survive a producer error, got %+v", open)
	}
}

// --- configured-repo resolution --------------------------------------------

// captureProducer records the WorkspaceInput it was handed.
type captureProducer struct{ got WorkspaceInput }

func (p *captureProducer) Name() string { return "capture" }
func (p *captureProducer) Evaluate(_ context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error) {
	p.got = in
	return nil, nil
}

func writeWorkspaceFixture(t *testing.T, root, configYAML, manifestYAML string) {
	t.Helper()
	if configYAML != "" {
		if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(configYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if manifestYAML != "" {
		if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"), []byte(manifestYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Regression: coverage is measured against CONFIGURATION, never against the
// repos this particular invocation happened to sweep.
//
// `attention sweep --repo X` sweeps one repo. Passing that as the configured
// list made every other repo look uncovered — including four that were
// configured perfectly well — and raised six cards where three were true.
func TestSweepWorkspace_ConfiguredReposComeFromConfigNotInvocation(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFixture(t, root, `
project:
  owner: acme
  number: 3
  repo: web
autonomous:
  enabled_repos:
    - api
    - jobs
`, `
workspace:
  name: test
repositories:
  - name: web
    path: .
  - name: docs-site
    path: ../docs-site
`)

	cap := &captureProducer{}
	reg := NewRegistry()
	reg.RegisterWorkspace(cap)
	s := &Sweeper{Store: attention.New(root), Registry: reg, WorkspaceRoot: root}

	// Sweeping ONE repo must not imply the others are unconfigured.
	if _, err := s.SweepWorkspace(context.Background(), []string{"acme/web"}); err != nil {
		t.Fatalf("SweepWorkspace: %v", err)
	}

	for _, want := range []string{"acme/web", "acme/api", "acme/jobs", "acme/docs-site"} {
		if !cap.got.Covers(want) {
			t.Errorf("%s is configured but was not reported as covered; got %v", want, cap.got.ConfiguredRepos)
		}
	}
	if cap.got.Covers("acme/never-configured") {
		t.Errorf("an unconfigured repo must not read as covered; got %v", cap.got.ConfiguredRepos)
	}
}

// With no config at all, the invocation list is the only evidence of intent —
// better than declaring every repo uncovered.
func TestSweepWorkspace_NoConfig_FallsBackToInvocationList(t *testing.T) {
	root := t.TempDir()
	cap := &captureProducer{}
	reg := NewRegistry()
	reg.RegisterWorkspace(cap)
	s := &Sweeper{Store: attention.New(root), Registry: reg, WorkspaceRoot: root}

	if _, err := s.SweepWorkspace(context.Background(), []string{"acme/web"}); err != nil {
		t.Fatalf("SweepWorkspace: %v", err)
	}
	if !cap.got.Covers("acme/web") {
		t.Errorf("want the invocation list as fallback, got %v", cap.got.ConfiguredRepos)
	}
}

// The producer and the repair verb MUST agree about coverage.
//
// The producer cards a repo it considers uncovered; ExecuteAddRepo refuses a
// target it considers covered. If the two matchers disagreed, the card would
// ship a button the executor rejects on click — a dead affordance on the one
// card that exists to have a working one (#706). This walks every card the
// producer raises through the executor's own gate.
func TestCoverageGap_EveryCardedRepoIsAcceptedByTheRepairVerb(t *testing.T) {
	configured := []string{"acme/web", "shared"}
	p := gapProducer([]string{"acme/web", "acme/unwatched", "shared", "acme/docs"}, nil)

	reqs, err := p.Evaluate(context.Background(), gapInput(configured...))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("expected at least one card to exercise the gate")
	}

	for _, r := range reqs {
		req := r
		if attention.RepoInConfiguredSet(configured, req.Context.Repo) {
			t.Errorf("producer carded %q but the verb considers it already configured — "+
				"the card's repair button would be refused on click", req.Context.Repo)
		}
	}
}

// The inverse: a repo the producer does NOT card is one the verb refuses, so a
// stale card cannot re-add a repository somebody already configured.
func TestCoverageGap_CoveredReposAreRefusedByTheRepairVerb(t *testing.T) {
	configured := []string{"acme/web", "shared"}
	for _, covered := range []string{"acme/web", "shared", "web"} {
		if !attention.RepoInConfiguredSet(configured, covered) {
			t.Errorf("%q is covered by config but the verb would accept re-adding it", covered)
		}
	}
}
