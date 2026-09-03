package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// recordedGateFetch captures the exact owner/name slug a gate hands to the
// forge. Swapping fetchGateIssueLabels for this fake is what lets the tests
// observe the constructed slug without a token or a network: the production
// seam builds the GitHub client itself, so a real run in the neutralized
// sizeGateEnv would stop at "create GitHub client" before the slug is visible.
type recordedGateFetch struct {
	called bool
	owner  string
	repo   string
	issue  int
}

// stubGateIssueFetch installs a fake fetchGateIssueLabels that records its
// arguments and returns labels/err, restoring the real seam on cleanup.
func stubGateIssueFetch(t *testing.T, labels []string, err error) *recordedGateFetch {
	t.Helper()
	rec := &recordedGateFetch{}
	prev := fetchGateIssueLabels
	fetchGateIssueLabels = func(_ context.Context, owner, repo string, issueNum int) ([]string, error) {
		rec.called = true
		rec.owner, rec.repo, rec.issue = owner, repo, issueNum
		return labels, err
	}
	t.Cleanup(func() { fetchGateIssueLabels = prev })
	return rec
}

// assertWellFormedGateSlug is AC-3 of #548: the owner/name slug handed to the
// forge has no empty segment on either side and equals the fixture slug.
func assertWellFormedGateSlug(t *testing.T, rec *recordedGateFetch) {
	t.Helper()
	if !rec.called {
		t.Fatal("the issue fetch was never attempted — the gate short-circuited before building the slug")
	}
	slug := rec.owner + "/" + rec.repo
	if strings.HasSuffix(slug, "/") {
		t.Errorf("slug %q ends with %q — the repo segment is empty (#548)", slug, "/")
	}
	for _, seg := range strings.Split(slug, "/") {
		if strings.TrimSpace(seg) == "" {
			t.Errorf("slug %q has an empty segment", slug)
		}
	}
	if slug != gateFixtureSlug {
		t.Errorf("issue fetch targeted %q, want %q", slug, gateFixtureSlug)
	}
}

// writeDevContext places a minimal dev-{N}.json so scope-drift check does not
// short-circuit on "dev context unavailable" — the very reason #548 was latent.
func writeDevContext(t *testing.T, dir string, issueNum int, body string) {
	t.Helper()
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline dir: %v", err)
	}
	name := filepath.Join(pipelineDir, fmt.Sprintf("dev-%d.json", issueNum))
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// runGateCheck drives <path...> through the ASSEMBLED root command so
// PersistentPreRunE — the config back-fill under test — actually runs. See
// runSizeGateCheck for why direct RunE calls would not prove anything.
func runGateCheck(t *testing.T, path []string, args ...string) (*cobra.Command, error) {
	t.Helper()
	root := rootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append(append([]string{}, path...), args...))
	err := root.Execute()
	return findSubcommand(t, root, path...), err
}

const scopeDriftDevContext = `{"files_changed":{"created":[],"modified":["README.md"]}}`

// TestScopeDriftCheck_ConfigRepoBackfill is the #548 regression: scope-drift
// check registered --repo with a bare StringVar, so the root back-fill set
// --owner from config but never --repo, and the issue fetch targeted "acme/".
func TestScopeDriftCheck_ConfigRepoBackfill(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)
	writeDevContext(t, dir, 1, scopeDriftDevContext)
	rec := stubGateIssueFetch(t, []string{"type:docs"}, nil)

	check, err := runGateCheck(t, []string{"scope-drift", "check"}, "--issue", "1")
	if err != nil {
		t.Fatalf("scope-drift check: %v", err)
	}

	if got := check.Annotations[repoBackfillAnnotation]; got != "true" {
		t.Errorf("scope-drift check Annotations[%q] = %q, want %q — --repo must be registered via repoNameFlag (#548)", repoBackfillAnnotation, got, "true")
	}
	if got := check.Flags().Lookup("repo").Value.String(); got != gateFixtureRepo {
		t.Errorf("--repo after back-fill = %q, want %q", got, gateFixtureRepo)
	}
	assertWellFormedGateSlug(t, rec)
	if rec.issue != 1 {
		t.Errorf("fetched issue #%d, want #1", rec.issue)
	}
}

// TestScopeDriftCheck_IssueTypeOverrideStillTargetsConfigRepo pins the
// best-effort label fetch on the --issue-type path to the same resolved slug.
func TestScopeDriftCheck_IssueTypeOverrideStillTargetsConfigRepo(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)
	writeDevContext(t, dir, 1, scopeDriftDevContext)
	rec := stubGateIssueFetch(t, nil, errors.New("offline"))

	if _, err := runGateCheck(t, []string{"scope-drift", "check"}, "--issue", "1", "--issue-type", "docs"); err != nil {
		t.Fatalf("a failed best-effort label fetch must not fail the gate: %v", err)
	}
	assertWellFormedGateSlug(t, rec)
}

// TestScopeDriftCheck_MalformedRepoSlug proves the guard is the shared
// resolveGateRepo, not a re-implementation: `--repo 'acme/'` is rejected with
// its message before any fetch is attempted.
func TestScopeDriftCheck_MalformedRepoSlug(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)
	writeDevContext(t, dir, 1, scopeDriftDevContext)
	rec := stubGateIssueFetch(t, nil, nil)

	_, err := runGateCheck(t, []string{"scope-drift", "check"}, "--issue", "1", "--repo", "acme/")

	assertGuardShortCircuited(t, err, `malformed --repo "acme/"`, `expected "name" or "owner/name"`)
	if rec.called {
		t.Errorf("issue fetch attempted against %q/%q despite a malformed --repo", rec.owner, rec.repo)
	}
}

// TestScopeDriftCheck_RepoHelpIsTrue covers AC-4: the --repo description
// promises a config default, and the annotation is what makes that promise
// true, so the two are asserted together.
func TestScopeDriftCheck_RepoHelpIsTrue(t *testing.T) {
	check := findSubcommand(t, rootCmd(), "scope-drift", "check")
	flag := check.Flags().Lookup("repo")
	if flag == nil {
		t.Fatal("scope-drift check has no --repo flag")
	}
	if !strings.Contains(flag.Usage, "defaults to config") {
		t.Errorf("--repo usage = %q, want it to state the config default", flag.Usage)
	}
	if check.Annotations[repoBackfillAnnotation] != "true" {
		t.Errorf("--repo help promises a config default but the flag is not opted into the back-fill")
	}
}
