package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// versionDowngradeEnv builds on sizeGateEnv and adds an empty package.json so
// buildEvaluateInput has something to snapshot. PATH is neutralized by
// sizeGateEnv, so `git show` fails and the baseline is treated as absent —
// which is the "no comparison" path and yields a pass.
func versionDowngradeEnv(t *testing.T) string {
	t.Helper()
	dir := sizeGateEnv(t)
	writeGateConfig(t, dir, productionShapeConfig)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":"0.1.0"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return dir
}

// TestVersionDowngradeCheck_ConfigRepoBackfill is the #548 regression for the
// check path: with no explicit --repo, the label lookup must target the config
// repository "acme/widgets", never "acme/".
func TestVersionDowngradeCheck_ConfigRepoBackfill(t *testing.T) {
	versionDowngradeEnv(t)
	rec := stubGateIssueFetch(t, []string{"version:downgrade-allowed"}, nil)

	check, err := runGateCheck(t, []string{"version-downgrade", "check"}, "--issue", "1")
	if err != nil {
		t.Fatalf("version-downgrade check: %v", err)
	}

	if got := check.Annotations[repoBackfillAnnotation]; got != "true" {
		t.Errorf("version-downgrade check Annotations[%q] = %q, want %q — --repo must be registered via repoNameFlag (#548)", repoBackfillAnnotation, got, "true")
	}
	if got := check.Flags().Lookup("repo").Value.String(); got != gateFixtureRepo {
		t.Errorf("--repo after back-fill = %q, want %q", got, gateFixtureRepo)
	}
	assertWellFormedGateSlug(t, rec)
	if rec.issue != 1 {
		t.Errorf("fetched issue #%d, want #1", rec.issue)
	}
}

// TestVersionDowngradeCheck_MalformedRepoSlug proves the malformed-slug guard
// is the shared resolveGateRepo: `--repo 'acme/'` fails with its message
// before any fetch is attempted.
func TestVersionDowngradeCheck_MalformedRepoSlug(t *testing.T) {
	versionDowngradeEnv(t)
	rec := stubGateIssueFetch(t, nil, nil)

	_, err := runGateCheck(t, []string{"version-downgrade", "check"}, "--issue", "1", "--repo", "acme/")

	assertGuardShortCircuited(t, err, `malformed --repo "acme/"`, `expected "name" or "owner/name"`)
	if rec.called {
		t.Errorf("issue fetch attempted against %q/%q despite a malformed --repo", rec.owner, rec.repo)
	}
}

// TestVersionDowngradeCheck_NoIssueSkipsRepoResolution pins CLI-direct usage:
// without --issue there is no label lookup, so an unconfigured repo must not
// block the gate.
func TestVersionDowngradeCheck_NoIssueSkipsRepoResolution(t *testing.T) {
	versionDowngradeEnv(t)
	rec := stubGateIssueFetch(t, nil, nil)

	if _, err := runGateCheck(t, []string{"version-downgrade", "check"}, "--repo", ""); err != nil {
		t.Fatalf("version-downgrade check without --issue: %v", err)
	}
	if rec.called {
		t.Error("issue fetch attempted without --issue")
	}
}

// TestVersionDowngradeCheck_RepoHelpIsTrue covers AC-4 for this command.
func TestVersionDowngradeCheck_RepoHelpIsTrue(t *testing.T) {
	check := findSubcommand(t, rootCmd(), "version-downgrade", "check")
	flag := check.Flags().Lookup("repo")
	if flag == nil {
		t.Fatal("version-downgrade check has no --repo flag")
	}
	if !strings.Contains(flag.Usage, "defaults to config") {
		t.Errorf("--repo usage = %q, want it to state the config default", flag.Usage)
	}
	if check.Annotations[repoBackfillAnnotation] != "true" {
		t.Errorf("--repo help promises a config default but the flag is not opted into the back-fill")
	}
}
