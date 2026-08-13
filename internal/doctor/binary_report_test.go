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

	// AC3 says report on EVERY outcome, and this is the outcome an operator is
	// actually investigating: the resolving STEP and the resolved binary's own
	// VERSION are the two facts that answer "is this really an old build?".
	// Discarding Detail here left them reported only for healthy machines.
	if check.Detail == "" {
		t.Fatal("the divergence outcome must keep its Detail — it carries the resolving step and the binary's version")
	}
	for _, want := range []string{wantPath, string(StepVSCodeExtension), "bundle dir(s) on disk"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("Detail must name %q, got %q", want, check.Detail)
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
	if !strings.Contains(check.Detail, "step-4 selection") {
		t.Errorf("Detail must identify the recorded bundle as the step-4 selection, got %q", check.Detail)
	}
	if strings.Contains(check.Detail, "in use") {
		t.Errorf("Detail must not claim the recorded bundle is in use without regard to the winning cascade step, got %q", check.Detail)
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
	writeVersionBinary(t, repoBin, strings.TrimPrefix(layout.RecordedRelativeLocation, bundleDirPrefix))
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
	if strings.Contains(check.Detail, "in use") {
		t.Errorf("Detail must not claim the recorded bundle is in use when %s wins, got %q", StepRepoBin, check.Detail)
	}
}

func TestRunDoctor_CrossStepVersionMismatchWarns(t *testing.T) {
	repoBin, recordedBin := setupCrossStepVersionBinaries(
		t,
		"nightgauge v0.1.0-12-g11111111",
		"nightgauge v0.2.0-34-g22222222",
		true,
	)

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if check.OK {
		t.Fatalf("expected a cross-step version mismatch warning, got OK with detail %q", check.Detail)
	}
	for _, want := range []string{
		"nightgauge v0.1.0-12-g11111111",
		"nightgauge v0.2.0-34-g22222222",
		repoBin,
		recordedBin,
		string(StepRepoBin),
	} {
		if !strings.Contains(check.Error, want) {
			t.Errorf("cross-step warning must name %q, got %q", want, check.Error)
		}
	}
	if !containsString(result.Warnings, check.Error) {
		t.Errorf("cross-step finding must be present in Warnings, got %v", result.Warnings)
	}
	if containsString(result.FailedChecks, "binary") {
		t.Errorf("cross-step staleness must stay warning-only, got FailedChecks %v", result.FailedChecks)
	}
}

func TestRunDoctor_CrossStepMatchingVersionsAreHealthy(t *testing.T) {
	const version = "nightgauge v0.2.0-34-g22222222"
	repoBin, recordedBin := setupCrossStepVersionBinaries(t, version, version, true)

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if !check.OK {
		t.Fatalf("matching cross-step versions must be healthy, got %q", check.Error)
	}
	for _, want := range []string{repoBin, recordedBin, version, string(StepRepoBin)} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("matching comparison detail must name %q, got %q", want, check.Detail)
		}
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, repoBin) || strings.Contains(warning, recordedBin) {
			t.Errorf("matching versions must not emit a binary warning, got %q", warning)
		}
	}
}

func TestRunDoctor_CrossStepComparisonRequiresInstallRecord(t *testing.T) {
	_, unrecordedBin := setupCrossStepVersionBinaries(
		t,
		"nightgauge v0.1.0-12-g11111111",
		"nightgauge v0.2.0-34-g22222222",
		false,
	)

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if !check.OK {
		t.Fatalf("an unrecorded bundle must not drive cross-step staleness, got %q", check.Error)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, unrecordedBin) {
			t.Errorf("an unrecorded bundle must not emit a comparison warning, got %q", warning)
		}
	}
}

func setupCrossStepVersionBinaries(
	t *testing.T,
	resolvedVersion string,
	bundleVersion string,
	recordBundle bool,
) (string, string) {
	t.Helper()
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	isolateCascade(t)

	home := os.Getenv("HOME")
	bundleDir := bundleDirPrefix + "0.2.0-darwin-arm64"
	bundleBin := installBundle(t, home, bundleDir, true)
	writeVersionBinary(t, bundleBin, bundleVersion)
	if recordBundle {
		writeExtensionsIndex(t, home, bundleDir)
	}

	repo := realPath(t, t.TempDir())
	runGit(t, repo, "init")
	repoBin := filepath.Join(repo, "bin", "nightgauge")
	writeVersionBinary(t, repoBin, resolvedVersion)
	t.Chdir(repo)
	return repoBin, bundleBin
}

func writeVersionBinary(t *testing.T, path, version string) {
	t.Helper()
	writeFakeBinary(t, path)
	body := "#!/bin/sh\nprintf '%s\\n' \"" + version + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write version binary %s: %v", path, err)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
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
