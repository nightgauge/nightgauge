package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/spf13/cobra"
)

// Fixtures use owner "acme" and repo "widgets" on purpose. With the repository's
// own values (nightgauge/nightgauge) an owner/repo confusion is undetectable:
// every assertion about the resolved slug passes whichever segment the code
// picked. Distinct values make a swap fail.
const (
	gateFixtureOwner = "acme"
	gateFixtureRepo  = "widgets"
	gateFixtureSlug  = gateFixtureOwner + "/" + gateFixtureRepo
)

// sizeGateEnv creates an isolated workdir for a size-gate test and neutralizes
// every ambient input the command would otherwise inherit from the machine it
// runs on. It returns the workdir.
//
// Two distinct isolations, both load-bearing:
//
//  1. CONFIG. config.Load → config.LoadMerged reads the MACHINE tier
//     (~/.nightgauge/config.yaml) and merges it under the project tier. Without
//     the swap, an "owner-only" fixture is not owner-only — it inherits whatever
//     defaultRepo the developer has at home, the guard never fires, and the test
//     passes here while failing in CI (or, worse, masks a real regression in a
//     PR whose entire subject is config resolution).
//
//  2. TOKENS. The command constructs a GitHub client immediately after the
//     guard. Making that construction FAIL deterministically does two jobs: no
//     test can reach the network, and the guard's POSITION becomes observable —
//     a guard that runs first yields the guard's message, a guard moved below
//     clientFromConfig yields "create GitHub client". Without this the ordering
//     claim has no assertion behind it and can only be checked by eyeballing a
//     stray "warning: Using gh CLI" line on stderr.
func sizeGateEnv(t *testing.T) string {
	t.Helper()
	dir := chdirTemp(t)

	absentMachineConfig := filepath.Join(t.TempDir(), "no-machine-config.yaml")
	t.Cleanup(config.SwapMachineConfigPathForTest(func() (string, error) {
		return absentMachineConfig, nil
	}))
	// On Linux the loader falls back to $HOME/.nightgauge/config.yaml when the
	// canonical machine path is absent — unless one of these is set. Setting it
	// keeps the swap above authoritative on every platform.
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", filepath.Dir(absentMachineConfig))

	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	// No `gh` binary is reachable, so the gh-CLI token fallback fails too.
	t.Setenv("PATH", t.TempDir())

	return dir
}

// writeGateConfig writes body to <dir>/.nightgauge/config.yaml.
func writeGateConfig(t *testing.T, dir, body string) {
	t.Helper()
	nightgaugeDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(nightgaugeDir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nightgaugeDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

// productionShapeConfig is the shape the repository ships: a top-level owner
// plus a project: block carrying owner/repo. The project: MAPPING is what
// selects config.go's nested parser.
const productionShapeConfig = "owner: " + gateFixtureOwner + "\nowner_type: org\nproject:\n  owner: " + gateFixtureOwner + "\n  repo: " + gateFixtureRepo + "\n"

// runSizeGateCheck drives `size-gate check` through the ASSEMBLED root command
// so PersistentPreRunE — the config back-fill under test — actually runs, and
// returns the leaf command plus the execution error.
//
// Calling check.RunE (or root.PersistentPreRunE) directly would bypass cobra
// dispatch, and that is not a hypothetical distinction: cobra does NOT chain
// PersistentPreRunE, so a subcommand that grows its own hook silently loses the
// root's back-fill in the real binary while a direct-call test keeps passing.
//
// Output is discarded rather than captured: RunE and the GitHub client write
// their results to os.Stdout/os.Stderr directly, not to cmd.OutOrStdout(), so a
// buffer here would collect nothing but cobra's own error echo and invite
// assertions against a buffer that can never contain the command's output.
func runSizeGateCheck(t *testing.T, args ...string) (*cobra.Command, error) {
	t.Helper()
	root := rootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"size-gate", "check"}, args...))
	err := root.Execute()
	return findSubcommand(t, root, "size-gate", "check"), err
}

// findSubcommand walks a path of subcommand names from root and returns the
// leaf command, failing the test when any hop is missing.
func findSubcommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cur := root
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("subcommand %q not found under %q", name, cur.Name())
		}
		cur = next
	}
	return cur
}

// assertGuardShortCircuited asserts that err is the size gate's own
// pre-flight error and that execution never got past it. Two things are pinned:
//
//   - No GitHub lookup was attempted ("fetch issue" / "Could not resolve to a
//     Repository" would say otherwise).
//   - The guard ran BEFORE clientFromConfig. Token resolution is neutralized by
//     sizeGateEnv, so a guard that had been moved below client construction
//     would surface "create GitHub client" here instead.
func assertGuardShortCircuited(t *testing.T, err error, wantSubstrings ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the guard to reject the invocation, got nil error")
	}
	msg := err.Error()
	for _, want := range wantSubstrings {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "create GitHub client") {
		t.Errorf("guard ran AFTER the GitHub client was constructed — it must fail before any client or network work: %q", msg)
	}
	if strings.Contains(msg, "fetch issue") || strings.Contains(msg, "Could not resolve to a Repository") {
		t.Errorf("guard did not short-circuit before the GitHub fetch: %q", msg)
	}
}

// assertReachedClientConstruction asserts the inverse: the guard ACCEPTED the
// resolved owner/repo and execution proceeded to client construction, which
// sizeGateEnv has made fail. This is the positive control for every
// "repo was configured correctly" case — without it, a guard that rejected a
// perfectly good config would still satisfy a test that only checks flag values.
func assertReachedClientConstruction(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected client construction to fail with tokens neutralized, got nil error")
	}
	if !strings.Contains(err.Error(), "create GitHub client") {
		t.Fatalf("expected execution to reach client construction, got %q", err.Error())
	}
}

// TestSizeGateCheck_ConfigRepoBackfill is a regression test for #536: the
// size-gate check subcommand registered --repo with a bare
// cmd.Flags().StringVar, which does NOT carry repoBackfillAnnotation, so the
// root PersistentPreRunE back-filled --owner but never --repo. splitRepo then
// produced the slug "acme/" and every issue fetch failed with "Could not
// resolve to a Repository with the name 'acme/'".
func TestSizeGateCheck_ConfigRepoBackfill(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)

	check, err := runSizeGateCheck(t, "--issue", "520")

	// The MECHANISM, not just the value: the back-fill is gated on this
	// annotation, so asserting only the resolved --repo would still pass if the
	// value arrived by some other route.
	if got := check.Annotations[repoBackfillAnnotation]; got != "true" {
		t.Errorf("size-gate check Annotations[%q] = %q, want %q — --repo must be registered via repoNameFlag (#536)", repoBackfillAnnotation, got, "true")
	}

	gotOwner := check.Flags().Lookup("owner").Value.String()
	gotRepo := check.Flags().Lookup("repo").Value.String()
	if gotOwner != gateFixtureOwner {
		t.Errorf("--owner after back-fill = %q, want %q", gotOwner, gateFixtureOwner)
	}
	if gotRepo != gateFixtureRepo {
		t.Errorf("--repo after back-fill = %q, want %q — size-gate check must register --repo via repoNameFlag so PersistentPreRunE back-fills it (#536)", gotRepo, gateFixtureRepo)
	}

	// AC-2: the slug handed to the GitHub API must be well formed — no trailing
	// "/" and no empty segment on either side.
	ownerPart, repoPart, resolveErr := resolveGateRepo(gotOwner, gotRepo)
	if resolveErr != nil {
		t.Fatalf("resolveGateRepo(%q, %q) = %v, want a resolved slug", gotOwner, gotRepo, resolveErr)
	}
	slug := ownerPart + "/" + repoPart
	if strings.HasSuffix(slug, "/") {
		t.Errorf("resolved slug %q ends with %q — repo segment is empty", slug, "/")
	}
	for _, seg := range strings.Split(slug, "/") {
		if strings.TrimSpace(seg) == "" {
			t.Errorf("resolved slug %q has an empty segment", slug)
		}
	}
	if slug != gateFixtureSlug {
		t.Errorf("resolved slug = %q, want %q", slug, gateFixtureSlug)
	}

	assertReachedClientConstruction(t, err)
}

// TestSizeGateCheck_ConfiguredRepoKeysResolve verifies the config keys the
// guard's error message NAMES are the keys that actually work — separately for
// each parser. config.go picks its parser from the document shape: a `project:`
// MAPPING selects the nested parser (project.repo → repo → github.repo);
// anything else selects the flat parser (defaultRepo → github.repo), which
// never reads a top-level `repo:`. A message that advertises the wrong key set
// for a supported shape tells the operator to do exactly what they already did.
func TestSizeGateCheck_ConfiguredRepoKeysResolve(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "flat/defaultRepo",
			body: "owner: " + gateFixtureOwner + "\nowner_type: org\ndefaultRepo: " + gateFixtureRepo + "\n",
		},
		{
			name: "flat/github.repo",
			body: "github:\n  owner: " + gateFixtureOwner + "\n  repo: " + gateFixtureRepo + "\n",
		},
		{
			name: "nested/project.repo",
			body: "owner: " + gateFixtureOwner + "\nproject:\n  owner: " + gateFixtureOwner + "\n  repo: " + gateFixtureRepo + "\n",
		},
		{
			name: "nested/top-level repo",
			body: "owner: " + gateFixtureOwner + "\nrepo: " + gateFixtureRepo + "\nproject:\n  number: 7\n",
		},
		{
			name: "nested/github.repo",
			body: "github:\n  owner: " + gateFixtureOwner + "\n  repo: " + gateFixtureRepo + "\nproject:\n  number: 7\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := sizeGateEnv(t)
			writeGateConfig(t, dir, tc.body)

			check, err := runSizeGateCheck(t, "--issue", "520")

			if got := check.Flags().Lookup("owner").Value.String(); got != gateFixtureOwner {
				t.Errorf("--owner = %q, want %q", got, gateFixtureOwner)
			}
			if got := check.Flags().Lookup("repo").Value.String(); got != gateFixtureRepo {
				t.Errorf("--repo = %q, want %q — this key is named in the guard's remedy, so it must resolve", got, gateFixtureRepo)
			}
			if err != nil && strings.Contains(err.Error(), "not configured") {
				t.Errorf("guard fired for a config that DOES declare the repo: %v", err)
			}
			assertReachedClientConstruction(t, err)
		})
	}
}

// TestSizeGateCheck_RepoNotConfigured covers AC-4: when nothing supplies a
// repo, the command fails fast with a message that names remedies that WORK,
// for both parser shapes, and reports what it did resolve and where it looked.
func TestSizeGateCheck_RepoNotConfigured(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// No `project:` key at all → flat parser → defaultRepo / github.repo.
		{name: "flat", body: "owner: " + gateFixtureOwner + "\nowner_type: org\n"},
		// A `project:` mapping with no repo → nested parser → project.repo / repo.
		{name: "nested", body: "owner: " + gateFixtureOwner + "\nowner_type: org\nproject:\n  owner: " + gateFixtureOwner + "\n  number: 7\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := sizeGateEnv(t)
			writeGateConfig(t, dir, tc.body)

			_, err := runSizeGateCheck(t, "--issue", "520")

			assertGuardShortCircuited(t, err,
				"repo not configured",
				"--repo",
				// The keys the FLAT parser honors...
				"defaultRepo",
				"github.repo",
				// ...and the keys the NESTED parser honors. Both must appear:
				// the message cannot know which parser ran.
				"project.repo",
				// What was resolved, so a swapped owner/repo is visible...
				`owner="`+gateFixtureOwner+`"`,
				// ...and WHICH file was consulted, which is not necessarily the
				// one the operator just edited (cwd-relative) and is the only
				// clue when PersistentPreRunE swallowed a config.Load error.
				filepath.Join(dir, ".nightgauge", "config.yaml"),
			)
		})
	}
}

// TestSizeGateCheck_OwnerNotConfigured covers the owner half of the guard,
// which is reachable production code: a config that declares a repo but no
// owner produces the malformed slug "/widgets" — the #536 symptom with the
// segments reversed. An explicit empty --owner suppresses the back-fill
// (PersistentPreRunE only fills flags the caller did not set).
func TestSizeGateCheck_OwnerNotConfigured(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)

	_, err := runSizeGateCheck(t, "--issue", "520", "--owner", "", "--repo", gateFixtureRepo)

	assertGuardShortCircuited(t, err,
		"owner not configured",
		"--owner",
		"project.owner",
		"github.owner",
		// The resolved repo is echoed, so swapping the two guards' messages
		// cannot go unnoticed.
		`repo="`+gateFixtureRepo+`"`,
		filepath.Join(dir, ".nightgauge", "config.yaml"),
	)
	if strings.Contains(err.Error(), "repo not configured") {
		t.Errorf("owner guard reported a repo problem: %q", err.Error())
	}
}

// TestSizeGateCheck_WhitespaceOnlyRepo pins that the guard's whitespace
// awareness is wired into the command, not just the helper: `--repo '   '` must
// be rejected as unconfigured rather than stitched into the slug "acme/   ".
func TestSizeGateCheck_WhitespaceOnlyRepo(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)

	_, err := runSizeGateCheck(t, "--issue", "520", "--repo", "   ")

	assertGuardShortCircuited(t, err, "repo not configured", `owner="`+gateFixtureOwner+`"`)
}

// TestSizeGateCheck_MalformedRepoSlug pins the misattribution fix: a repo value
// of "/" makes splitRepo return ("", ""), so the OWNER guard used to fire and
// the operator was told owner: was missing while `owner: acme` sat in their
// config. A malformed slug must be reported as its own defect.
func TestSizeGateCheck_MalformedRepoSlug(t *testing.T) {
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)

	_, err := runSizeGateCheck(t, "--issue", "520", "--repo", "/")

	assertGuardShortCircuited(t, err, `malformed --repo "/"`, `expected "name" or "owner/name"`)
	if strings.Contains(err.Error(), "owner not configured") {
		t.Errorf("a malformed --repo was misreported as a missing owner: %q", err.Error())
	}
}

// TestResolveGateRepo covers the resolver directly. The values it RETURNS are
// the values RunE forwards to GetIssue, so the trimming assertions here are
// assertions about what reaches the GitHub API — a check that trimmed only for
// its own comparison and forwarded the raw input would reproduce the exact
// opaque "Could not resolve to a Repository with the name 'acme/  widgets '"
// error this guard exists to eliminate.
func TestResolveGateRepo(t *testing.T) {
	cases := []struct {
		name      string
		owner     string
		repo      string
		wantOwner string
		wantRepo  string
		wantErr   string // substring; empty means success expected
	}{
		{name: "bare name", owner: "acme", repo: "widgets", wantOwner: "acme", wantRepo: "widgets"},
		{name: "owner/name slug overrides --owner", owner: "ignored", repo: "acme/widgets", wantOwner: "acme", wantRepo: "widgets"},
		{name: "padded name is trimmed before forwarding", owner: "acme", repo: "  widgets ", wantOwner: "acme", wantRepo: "widgets"},
		{name: "padded owner is trimmed before forwarding", owner: "  acme  ", repo: "widgets", wantOwner: "acme", wantRepo: "widgets"},
		{name: "padded slug is trimmed on both sides", owner: "", repo: " acme / widgets ", wantOwner: "acme", wantRepo: "widgets"},
		{name: "whitespace-only repo", owner: "acme", repo: "   ", wantErr: "repo not configured"},
		{name: "empty repo", owner: "acme", repo: "", wantErr: "repo not configured"},
		{name: "whitespace-only owner", owner: "   ", repo: "widgets", wantErr: "owner not configured"},
		{name: "empty owner", owner: "", repo: "widgets", wantErr: "owner not configured"},
		{name: "bare slash", owner: "acme", repo: "/", wantErr: `malformed --repo "/"`},
		{name: "missing owner segment", owner: "acme", repo: "/widgets", wantErr: `malformed --repo "/widgets"`},
		{name: "missing name segment", owner: "acme", repo: "acme/", wantErr: `malformed --repo "acme/"`},
		{name: "whitespace-only segment", owner: "acme", repo: "acme/  ", wantErr: `malformed --repo "acme/  "`},
		{name: "three segments", owner: "acme", repo: "acme/widgets/extra", wantErr: `malformed --repo "acme/widgets/extra"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOwner, gotRepo, err := resolveGateRepo(tc.owner, tc.repo)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveGateRepo(%q, %q) = (%q, %q), want error containing %q", tc.owner, tc.repo, gotOwner, gotRepo, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveGateRepo(%q, %q) = %v, want (%q, %q)", tc.owner, tc.repo, err, tc.wantOwner, tc.wantRepo)
			}
			if gotOwner != tc.wantOwner || gotRepo != tc.wantRepo {
				t.Errorf("resolveGateRepo(%q, %q) = (%q, %q), want (%q, %q) — these are the exact values forwarded to GetIssue", tc.owner, tc.repo, gotOwner, gotRepo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}
