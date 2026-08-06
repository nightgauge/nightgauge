package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDoctor_ReportsHookResolutionFromCwd covers #356 AC3: `nightgauge
// doctor` must say which binary the HOOKS would resolve from the current
// directory, so the cwd-dependence that made #356 invisible is inspectable.
//
// The check is warning-only, so a diverging bundle must never appear in
// FailedChecks nor drive ExitCode to 2.
func TestRunDoctor_ReportsHookResolutionFromCwd(t *testing.T) {
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)

	home := os.Getenv("HOME")
	paths := materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
	writeCapturedExtensionsIndex(t, home)
	wantPath := paths[layout.RecordedRelativeLocation]
	recordedVersion := strings.TrimPrefix(layout.RecordedRelativeLocation, bundleDirPrefix)

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if !check.OK {
		t.Fatalf("expected binary check OK with a healthy recorded bundle, got error %q", check.Error)
	}
	if check.Detail == "" {
		t.Fatal("binary check Detail must never be empty when the binary resolved — it is the whole inspectability surface")
	}
	if !strings.Contains(check.Detail, wantPath) {
		t.Errorf("Detail must name the resolved path %q, got %q", wantPath, check.Detail)
	}
	if !strings.Contains(check.Detail, string(StepVSCodeExtension)) {
		t.Errorf("Detail must name the resolving step %q, got %q", StepVSCodeExtension, check.Detail)
	}
	if !strings.Contains(strings.ToLower(check.Detail), "hook") {
		t.Errorf("Detail must frame the result as what the hooks resolve, got %q", check.Detail)
	}
	if !strings.Contains(check.Detail, recordedVersion) {
		t.Errorf("Detail must name the recorded-installed bundle version %q, got %q", recordedVersion, check.Detail)
	}
	if !strings.Contains(check.Detail, "2 VSCode extension bundle dir(s) on disk") {
		t.Errorf("Detail must report the bundle-dir count, got %q", check.Detail)
	}
	for _, fc := range result.FailedChecks {
		if fc == "binary" {
			t.Error("binary check must never appear in FailedChecks — it is warning-only")
		}
	}
}

// TestRunDoctor_ReportsUnusableRecord covers #356 AC1 on the doctor side: when
// the resolved bundle is NOT the one VS Code records as installed, doctor must
// say so, naming the recorded version, the resolved version and the resolved
// path — and must stay a warning rather than a required failure.
func TestRunDoctor_ReportsUnusableRecord(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	wantPath := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
	installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", false)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.1-darwin-arm64")

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if check.OK {
		t.Fatalf("expected binary check to flag the unusable record, got OK with detail %q", check.Detail)
	}
	if check.Error == "" {
		t.Fatal("a failing binary check must carry a non-empty Error")
	}
	for _, want := range []string{"0.2.1-darwin-arm64", "0.2.0-darwin-arm64", wantPath} {
		if !strings.Contains(check.Error, want) {
			t.Errorf("Error must name %q (recorded version, resolved version, resolved path), got %q", want, check.Error)
		}
	}

	// Warning-level, never a required failure, and never mistaken for a
	// missing binary.
	for _, fc := range result.FailedChecks {
		if fc == "binary" {
			t.Error("a diverging bundle must not be a required failure")
		}
	}
	if result.InstallInstructions != "" {
		t.Errorf("a resolvable-but-diverging binary is not a missing binary; InstallInstructions must stay empty, got %q", result.InstallInstructions)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "0.2.1-darwin-arm64") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the divergence warning in result.Warnings, got %v", result.Warnings)
	}
}

// TestRunDoctor_RecordedDowngradeIsHealthy is the negative counterpart, and
// the reason this whole redesign exists: a machine running the bundle VS Code
// recorded is HEALTHY even when a higher-numbered directory is sitting next to
// it. A version-ranking implementation reports this machine as fine while
// running the wrong binary; a version-ranking implementation with a staleness
// check reports it as broken. Both are wrong.
func TestRunDoctor_RecordedDowngradeIsHealthy(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	installBundle(t, home, bundleDirPrefix+"0.1.0", true)
	recorded := installBundle(t, home, bundleDirPrefix+"0.2.0", true)
	installBundle(t, home, bundleDirPrefix+"0.3.0", true)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.0")

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if !check.OK {
		t.Fatalf("a recorded install is healthy regardless of what else is on disk; got error %q", check.Error)
	}
	if !strings.Contains(check.Detail, recorded) {
		t.Errorf("Detail must name the recorded bundle %q, got %q", recorded, check.Detail)
	}
	if !strings.Contains(check.Detail, "in use") {
		t.Errorf("Detail must report that the recorded bundle is the one in use, got %q", check.Detail)
	}
}

// TestRunDoctor_BinaryDetailNamesBundleInventoryFromRepo asserts the bundle
// inventory is reported even when an EARLIER cascade step wins. This is the
// exact confusion #356 describes: inside a nightgauge checkout the hooks use
// `bin/nightgauge` and the extension bundles are invisible, so a maintainer
// who tests there learns nothing about what other repos will run.
func TestRunDoctor_BinaryDetailNamesBundleInventoryFromRepo(t *testing.T) {
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)

	home := os.Getenv("HOME")
	materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
	writeCapturedExtensionsIndex(t, home)

	repo := realPath(t, t.TempDir())
	runGit(t, repo, "init")
	repoBin := filepath.Join(repo, "bin", "nightgauge")
	writeFakeBinary(t, repoBin)
	t.Chdir(repo)

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if !check.OK {
		t.Fatalf("expected binary check OK, got error %q", check.Error)
	}
	if !strings.Contains(check.Detail, repoBin) {
		t.Errorf("Detail must name the repo binary %q, got %q", repoBin, check.Detail)
	}
	if !strings.Contains(check.Detail, string(StepRepoBin)) {
		t.Errorf("Detail must name step %q, got %q", StepRepoBin, check.Detail)
	}
	if !strings.Contains(check.Detail, "bundle dir(s) on disk") {
		t.Errorf("Detail must report the extension-bundle inventory even when an earlier step wins, got %q", check.Detail)
	}
	if !strings.Contains(check.Detail, strings.TrimPrefix(layout.RecordedRelativeLocation, bundleDirPrefix)) {
		t.Errorf("Detail must name the recorded-installed bundle even when an earlier step wins, got %q", check.Detail)
	}
}

// TestRunDoctor_NotFoundKeepsBundleInventory: with bundles installed but every
// bundled binary non-executable (partial install, lost exec bit), ResolveBinary
// returns no path but a fully populated scan. Discarding it leaves the operator
// with a generic "not found" plus install instructions that will not help,
// instead of "2 bundle dir(s) on disk, VSCode records X as installed".
func TestRunDoctor_NotFoundKeepsBundleInventory(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", false)
	installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", false)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.1-darwin-arm64")

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if check.OK {
		t.Fatalf("expected an unresolved binary, got OK with detail %q", check.Detail)
	}
	if !strings.Contains(check.Error, "2 VSCode extension bundle dir(s) on disk") {
		t.Errorf("the not-found error must carry the bundle inventory, got %q", check.Error)
	}
	if !strings.Contains(check.Error, "0.2.1-darwin-arm64") {
		t.Errorf("the not-found error must name the recorded install, got %q", check.Error)
	}
	if result.InstallInstructions == "" {
		t.Error("a genuinely unresolved binary must still carry install instructions")
	}
}
