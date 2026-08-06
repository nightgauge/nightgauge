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
// The check is warning-only, so a superseded bundle must never appear in
// FailedChecks nor drive ExitCode to 2.
func TestRunDoctor_ReportsHookResolutionFromCwd(t *testing.T) {
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)

	home := os.Getenv("HOME")
	paths := materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
	newest := newestVersion(t, versionsOf(paths))
	wantPath := paths[newest]

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if !check.OK {
		t.Fatalf("expected binary check OK with a healthy newest bundle, got error %q", check.Error)
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
	if !strings.Contains(check.Detail, newest) {
		t.Errorf("Detail must name the selected bundle version %q, got %q", newest, check.Detail)
	}
	for _, fc := range result.FailedChecks {
		if fc == "binary" {
			t.Error("binary check must never appear in FailedChecks — it is warning-only")
		}
	}
}

// TestRunDoctor_ReportsSupersededBundle covers #356 AC1 on the doctor side:
// when the resolved bundle is NOT the newest installed bundle, doctor must
// say so, naming BOTH versions and the resolved path — and must stay a
// warning rather than a required failure.
func TestRunDoctor_ReportsSupersededBundle(t *testing.T) {
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)

	home := os.Getenv("HOME")
	allVersions := make([]string, 0, len(layout.Bundles))
	for _, b := range layout.Bundles {
		allVersions = append(allVersions, b.BundleVersion)
	}
	newest := newestVersion(t, allVersions)

	// Newest bundle present but its binary is not runnable → the hooks fall
	// back to an older bundle. That is a stale resolved binary.
	paths := materializeCapturedBundles(t, home, layout, func(b capturedBundle) bool {
		return b.BundleVersion != newest
	})
	older := make([]string, 0, len(paths))
	for v := range paths {
		if v != newest {
			older = append(older, v)
		}
	}
	if len(older) == 0 {
		t.Fatal("fixture cannot express a superseded selection")
	}
	selected := newestVersion(t, older)
	wantPath := paths[selected]

	result := RunDoctor(context.Background(), nil, nil, nil)
	check := result.Checks["binary"]
	if check.OK {
		t.Fatalf("expected binary check to flag the superseded bundle, got OK with detail %q", check.Detail)
	}
	if check.Error == "" {
		t.Fatal("a failing binary check must carry a non-empty Error")
	}
	for _, want := range []string{selected, newest, wantPath} {
		if !strings.Contains(check.Error, want) {
			t.Errorf("Error must name %q (both versions and the resolved path), got %q", want, check.Error)
		}
	}

	// Warning-level, never a required failure, and never mistaken for a
	// missing binary.
	for _, fc := range result.FailedChecks {
		if fc == "binary" {
			t.Error("a superseded bundle must not be a required failure")
		}
	}
	if result.InstallInstructions != "" {
		t.Errorf("a resolvable-but-superseded binary is not a missing binary; InstallInstructions must stay empty, got %q", result.InstallInstructions)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, newest) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the superseded-bundle warning in result.Warnings, got %v", result.Warnings)
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
	if !strings.Contains(check.Detail, "bundle") {
		t.Errorf("Detail must report the extension-bundle inventory even when an earlier step wins, got %q", check.Detail)
	}
}
