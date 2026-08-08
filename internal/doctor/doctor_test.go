package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestRunDoctor_NilClient verifies that a nil GitHub client causes required auth
// checks to fail and produces ExitCode 2 (broken environment).
func TestRunDoctor_NilClient(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	if result.ExitCode != 2 {
		t.Errorf("expected ExitCode 2, got %d", result.ExitCode)
	}
	if result.Healthy {
		t.Error("expected Healthy=false when client is nil")
	}
	if result.V != 1 {
		t.Errorf("expected schema version V=1, got %d", result.V)
	}

	authCheck, ok := result.Checks["github_auth"]
	if !ok {
		t.Fatal("expected github_auth check to be present")
	}
	if authCheck.OK {
		t.Error("expected github_auth.OK=false when client is nil")
	}
	if authCheck.Error == "" {
		t.Error("expected non-empty github_auth.Error when client is nil")
	}

	// api_user and scopes should both be skipped (not OK)
	if result.Checks["api_user"].OK {
		t.Error("expected api_user.OK=false when client is nil")
	}
	if result.Checks["scopes"].OK {
		t.Error("expected scopes.OK=false when client is nil")
	}

	// At least one error mentioning authentication
	if len(result.Errors) == 0 {
		t.Fatal("expected at least one error when client is nil")
	}
	hasAuthError := false
	for _, e := range result.Errors {
		if strings.Contains(strings.ToLower(e), "auth") || strings.Contains(strings.ToLower(e), "github") {
			hasAuthError = true
			break
		}
	}
	if !hasAuthError {
		t.Errorf("expected an auth-related error in result.Errors, got: %v", result.Errors)
	}
}

// TestRunDoctor_NilConfig_NilClient verifies that a nil config produces warnings
// about the missing config.yaml (not hard errors), while a nil client still
// causes required auth failures that drive ExitCode to 2.
func TestRunDoctor_NilConfig_NilClient(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	// With nil client, ExitCode must be 2 (auth is a required check)
	if result.ExitCode != 2 {
		t.Errorf("expected ExitCode 2 (auth fails), got %d", result.ExitCode)
	}

	// config check should be present and not-ok (fresh repo)
	configCheck, ok := result.Checks["config"]
	if !ok {
		t.Fatal("expected config check to be present")
	}
	if configCheck.OK {
		t.Error("expected config.OK=false when cfg is nil")
	}

	// Config absence must appear as a warning (not a hard error) so the exit
	// code is driven by auth failure, not by missing config.yaml.
	hasConfigWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "config") || strings.Contains(w, "repo-init") {
			hasConfigWarning = true
			break
		}
	}
	if !hasConfigWarning {
		t.Errorf("expected a config-related warning when cfg is nil, got warnings: %v", result.Warnings)
	}
}

// TestRunDoctor_ValidConfig_NilClient verifies that a properly configured project
// with a missing auth client still exits as broken (ExitCode 2) because auth
// is a required check, while config/project checks pass.
func TestRunDoctor_ValidConfig_NilClient(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Owner:         "nightgauge",
		ProjectNumber: 42,
	}
	result := RunDoctor(ctx, cfg, nil, nil)

	if result.ExitCode != 2 {
		t.Errorf("expected ExitCode 2 with nil client (auth required), got %d", result.ExitCode)
	}

	// project check should pass since cfg has all required fields
	projectCheck, ok := result.Checks["project"]
	if !ok {
		t.Fatal("expected project check to be present")
	}
	if !projectCheck.OK {
		t.Errorf("expected project.OK=true with valid cfg, got false: %s", projectCheck.Error)
	}
	if !strings.Contains(projectCheck.Detail, "42") {
		t.Errorf("expected project detail to mention project number 42, got: %q", projectCheck.Detail)
	}

	// config check should pass
	configCheck, ok := result.Checks["config"]
	if !ok {
		t.Fatal("expected config check to be present")
	}
	if !configCheck.OK {
		t.Errorf("expected config.OK=true with valid cfg, got false: %s", configCheck.Error)
	}
}

// TestRunDoctor_MissingProject verifies that a config with ProjectNumber=0 or
// empty Owner causes the project check to fail as a required error (ExitCode 2).
func TestRunDoctor_MissingProject(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		cfg  *config.Config
	}{
		{"zero project number", &config.Config{Owner: "nightgauge", ProjectNumber: 0}},
		{"empty owner", &config.Config{Owner: "", ProjectNumber: 42}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := RunDoctor(ctx, tc.cfg, nil, nil)

			// ExitCode must be 2: both auth (nil client) and project fail
			if result.ExitCode != 2 {
				t.Errorf("expected ExitCode 2, got %d", result.ExitCode)
			}

			projectCheck, ok := result.Checks["project"]
			if !ok {
				t.Fatal("expected project check to be present")
			}
			if projectCheck.OK {
				t.Error("expected project.OK=false for incomplete config")
			}
			if projectCheck.Error == "" {
				t.Error("expected non-empty project.Error for incomplete config")
			}

			// Project error must appear in result.Errors (it is a required check)
			hasProjectError := false
			for _, e := range result.Errors {
				if strings.Contains(e, "project") || strings.Contains(e, "owner") {
					hasProjectError = true
					break
				}
			}
			if !hasProjectError {
				t.Errorf("expected project-related error in result.Errors, got: %v", result.Errors)
			}
		})
	}
}

// TestRunDoctor_BinaryNotInPath exercises the full RunDoctor binary check path
// when nightgauge is not in PATH. Since RunDoctor runs with a nil client
// (auth always fails), the result has ExitCode 2, but the binary check specifically
// should populate InstallInstructions and emit a warning.
func TestRunDoctor_BinaryNotInPath(t *testing.T) {
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })

	// Empty PATH so exec.LookPath("nightgauge") fails
	_ = os.Setenv("PATH", "")
	// Unset NIGHTGAUGE_BIN and HOME/repo-relative cascade steps so none of
	// the other five cascade steps accidentally resolve in CI.
	t.Setenv("NIGHTGAUGE_BIN", "")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	binaryCheck := result.Checks["binary"]
	if binaryCheck.OK {
		t.Skip("nightgauge still found with empty PATH (hard-coded path) — skipping")
	}

	// Binary check failed: InstallInstructions must be populated
	if result.InstallInstructions == "" {
		t.Error("expected InstallInstructions to be populated when binary is missing")
	}
	if !strings.Contains(result.InstallInstructions, "go install") {
		t.Errorf("expected InstallInstructions to mention 'go install', got: %q", result.InstallInstructions)
	}

	// Binary failure appears in Warnings (not Errors — it is a warning-level check)
	hasBinaryWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "nightgauge") || strings.Contains(w, "binary") || strings.Contains(w, "PATH") {
			hasBinaryWarning = true
			break
		}
	}
	if !hasBinaryWarning {
		t.Errorf("expected binary warning in result.Warnings, got: %v", result.Warnings)
	}
}

// TestRunDoctor_BinaryOffPathResolvable_Degraded verifies that a binary
// resolvable only via NIGHTGAUGE_BIN (off PATH) is reported as OK, and that
// the binary check alone never drives ExitCode to 2 (#277 AC3).
func TestRunDoctor_BinaryOffPathResolvable_Degraded(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "nightgauge")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake binary: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })
	_ = os.Setenv("PATH", "")
	t.Setenv("NIGHTGAUGE_BIN", fakeBinary)

	cfg := &config.Config{Owner: "nightgauge", ProjectNumber: 42}
	ctx := context.Background()
	result := RunDoctor(ctx, cfg, nil, nil)

	binaryCheck := result.Checks["binary"]
	if !binaryCheck.OK {
		t.Fatalf("expected binary.OK=true when resolvable via NIGHTGAUGE_BIN, got: %s", binaryCheck.Error)
	}
	if !strings.Contains(binaryCheck.Detail, "NIGHTGAUGE_BIN") {
		t.Errorf("expected binary.Detail to mention the resolving step, got: %q", binaryCheck.Detail)
	}

	// ExitCode is driven by the nil client (auth required failure), never by
	// the binary check itself — assert the binary check contributed no
	// warning/error.
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "binary") {
			t.Errorf("resolvable off-PATH binary must not produce a warning, got: %q", w)
		}
	}
	for _, fc := range result.FailedChecks {
		if fc == "binary" {
			t.Error("binary check must never appear in FailedChecks — it is warning-only")
		}
	}
}

// TestRunDoctor_FailedChecksNamesRequiredFailures verifies that a required
// check failure is named in result.FailedChecks so PREFLIGHT.md can print
// the failing check(s) on exit 2 (#277 AC4).
func TestRunDoctor_FailedChecksNamesRequiredFailures(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	if result.ExitCode != 2 {
		t.Fatalf("expected ExitCode 2 with nil client, got %d", result.ExitCode)
	}

	found := false
	for _, fc := range result.FailedChecks {
		if fc == "github_auth" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected FailedChecks to contain %q, got: %v", "github_auth", result.FailedChecks)
	}
}

// TestRunDoctor_UnhealthyAdapterIsWarningNotError guards the issue's core
// guarantee (#4031): an unhealthy adapter is surfaced as a WARNING and appears
// in result.Adapters, never as a required failure that adds to result.Errors.
func TestRunDoctor_UnhealthyAdapterIsWarningNotError(t *testing.T) {
	ctx := context.Background()
	// ollama with no model env set → not ready (an unhealthy adapter).
	t.Setenv("NIGHTGAUGE_OLLAMA_MODEL", "")

	result := RunDoctor(ctx, nil, nil, []string{"ollama"})

	if len(result.Adapters) != 1 || result.Adapters[0].Adapter != "ollama" {
		t.Fatalf("expected one ollama adapter entry, got %+v", result.Adapters)
	}
	if result.Adapters[0].OK {
		t.Error("expected ollama to be not-OK with no model env")
	}
	for _, e := range result.Errors {
		if strings.Contains(e, "ollama") {
			t.Errorf("adapter readiness must never appear in Errors, got: %v", result.Errors)
		}
	}
	hasAdapterWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "ollama") {
			hasAdapterWarning = true
		}
	}
	if !hasAdapterWarning {
		t.Errorf("expected an ollama warning, got warnings: %v", result.Warnings)
	}
}

// TestRunDoctor_NoAdapterSectionByDefault verifies the adapter section is
// omitted entirely when no adapters are requested (default doctor behavior
// unchanged — skill preflight parses the same shape as before).
func TestRunDoctor_NoAdapterSectionByDefault(t *testing.T) {
	result := RunDoctor(context.Background(), nil, nil, nil)
	if result.Adapters != nil {
		t.Errorf("expected nil Adapters when none requested, got %+v", result.Adapters)
	}
}

// TestRunDoctor_ProjectMappingMismatch verifies that a workspace manifest
// whose repositories[].project_number disagrees with the runtime-resolved
// autonomous.repositories.<repo>.project_number is a required failure
// (ExitCode 2), never a warning — issue #271.
func TestRunDoctor_ProjectMappingMismatch(t *testing.T) {
	dir := t.TempDir()
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	manifest := `
workspace:
  name: test-workspace
repositories:
  - name: nightgauge/nightgauge
    path: .
    project_number: 1
`
	if err := os.WriteFile(filepath.Join(vscodeDir, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Chdir(dir)

	cfg := &config.Config{
		Owner:       "nightgauge",
		DefaultRepo: "primary-repo", // not the manifest's repo, so cross-repo mapping applies
		Autonomous: &config.AutonomousConfig{
			Repositories: map[string]*config.RepositoryConfig{
				"nightgauge/nightgauge": {ProjectNumber: 4},
			},
		},
	}

	result := RunDoctor(context.Background(), cfg, nil, nil)

	if result.ExitCode != 2 {
		t.Fatalf("expected ExitCode 2 on project mapping mismatch, got %d (errors=%v)", result.ExitCode, result.Errors)
	}
	check, ok := result.Checks["project_mapping"]
	if !ok {
		t.Fatal("expected project_mapping check to be present")
	}
	if check.OK {
		t.Error("expected project_mapping.OK=false on mismatch")
	}
	if !strings.Contains(check.Error, "workspace yaml says project 1") || !strings.Contains(check.Error, "runtime config resolves to 4") {
		t.Errorf("expected mismatch detail naming both project numbers, got: %q", check.Error)
	}
}

// TestRunDoctor_ProjectMappingAgrees verifies that a matching manifest and
// runtime config produces a passing project_mapping check.
func TestRunDoctor_ProjectMappingAgrees(t *testing.T) {
	dir := t.TempDir()
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	manifest := `
workspace:
  name: test-workspace
repositories:
  - name: nightgauge/nightgauge
    path: .
    project_number: 4
`
	if err := os.WriteFile(filepath.Join(vscodeDir, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Chdir(dir)

	cfg := &config.Config{
		Owner:       "nightgauge",
		DefaultRepo: "primary-repo",
		Autonomous: &config.AutonomousConfig{
			Repositories: map[string]*config.RepositoryConfig{
				"nightgauge/nightgauge": {ProjectNumber: 4},
			},
		},
	}

	result := RunDoctor(context.Background(), cfg, nil, nil)

	check, ok := result.Checks["project_mapping"]
	if !ok {
		t.Fatal("expected project_mapping check to be present")
	}
	if !check.OK {
		t.Errorf("expected project_mapping.OK=true when sources agree, got error: %q", check.Error)
	}
}

// TestDoctorResult_Schema verifies that DoctorResult always has V=1 and that
// all expected check keys are present (regression guard for schema stability).
func TestDoctorResult_Schema(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	if result.V != 1 {
		t.Errorf("expected schema version V=1, got %d", result.V)
	}
	if result.Checks == nil {
		t.Fatal("expected non-nil Checks map")
	}

	// These check keys must always be present. The leak carriers are in the
	// list because a carrier that silently stops registering is exactly the
	// failure they exist to prevent (#330, #332, #341): a missing key renders
	// as no output at all, which reads as health.
	expectedKeys := []string{
		"binary", "gh", "github_auth", "api_user", "scopes", "rate_limit", "config", "project",
		"orphaned_processes",
	}
	for _, key := range expectedKeys {
		if _, ok := result.Checks[key]; !ok {
			t.Errorf("expected check key %q to be present in result.Checks", key)
		}
	}
}

// TestReadOrgWarning verifies the read:org advisory warning honors the
// admin:org/write:org org-scope hierarchy (#23): admin:org alone must not
// produce the warning, while a token with neither admin:org, write:org, nor
// read:org still does.
func TestReadOrgWarning(t *testing.T) {
	tests := []struct {
		name       string
		scopes     []string
		wantWarned bool
	}{
		{"admin:org satisfies, no warning", []string{"repo", "project", "admin:org"}, false},
		{"write:org satisfies, no warning", []string{"repo", "project", "write:org"}, false},
		{"read:org satisfies, no warning", []string{"repo", "project", "read:org"}, false},
		{"no org scope warns", []string{"repo", "project"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readOrgWarning(tt.scopes)
			if warned := got != ""; warned != tt.wantWarned {
				t.Errorf("readOrgWarning(%v) = %q, wantWarned=%v", tt.scopes, got, tt.wantWarned)
			}
		})
	}
}
