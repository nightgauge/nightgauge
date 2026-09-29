package doctor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestRunDoctor_NilClient verifies that a nil GitHub client causes required auth
// checks to fail and produces ExitCode 2 (broken environment).
func TestRunDoctor_NilClient(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)
	if !hasResult(result, "complexity_model") {
		t.Fatal("expected RunDoctor to emit the complexity_model check")
	}

	if result.ExitCode != 2 {
		t.Errorf("expected ExitCode 2, got %d", result.ExitCode)
	}
	if result.Healthy {
		t.Error("expected Healthy=false when client is nil")
	}
	if result.V != SchemaVersion {
		t.Errorf("expected schema version V=%d, got %d", SchemaVersion, result.V)
	}

	authCheck, ok := resultItemOK(result, "github_auth")
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
	if resultItem(result, "api_user").OK {
		t.Error("expected api_user.OK=false when client is nil")
	}
	if resultItem(result, "scopes").OK {
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
	configCheck, ok := resultItemOK(result, "config")
	if !ok {
		t.Fatal("expected config check to be present")
	}
	if configCheck.OK {
		t.Error("expected config.OK=false when cfg is nil")
	}

	// Config absence is a blocker (ADR-025, #2091) with the repo-init remedy.
	hasConfigWarning := false
	for _, w := range result.Errors {
		if strings.Contains(w, "config") || strings.Contains(w, "repo-init") {
			hasConfigWarning = true
			break
		}
	}
	if !hasConfigWarning {
		t.Errorf("expected a config-related error when cfg is nil, got errors: %v", result.Errors)
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
	repo := t.TempDir()
	writeRepoConfig(t, repo)
	t.Chdir(repo)
	result := RunDoctor(ctx, cfg, nil, nil)

	if result.ExitCode != 2 {
		t.Errorf("expected ExitCode 2 with nil client (auth required), got %d", result.ExitCode)
	}

	// project check should pass since cfg has all required fields
	projectCheck, ok := resultItemOK(result, "project")
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
	configCheck, ok := resultItemOK(result, "config")
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

			projectCheck, ok := resultItemOK(result, "project")
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

	binaryCheck := resultItem(result, "binary")
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

	// A binary the hooks cannot resolve is a blocker (ADR-025 NGD001, #2091).
	hasBinaryWarning := false
	for _, w := range result.Errors {
		if strings.Contains(w, "nightgauge") || strings.Contains(w, "binary") || strings.Contains(w, "PATH") {
			hasBinaryWarning = true
			break
		}
	}
	if !hasBinaryWarning {
		t.Errorf("expected binary error in result.Errors, got: %v", result.Errors)
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

	binaryCheck := resultItem(result, "binary")
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
	// ollama was removed (#2128) → not ready, with the migration as remediation.

	result := RunDoctor(ctx, nil, nil, []string{"ollama"})

	if len(result.Adapters) != 1 || result.Adapters[0].Adapter != "ollama" {
		t.Fatalf("expected one ollama adapter entry, got %+v", result.Adapters)
	}
	if result.Adapters[0].OK {
		t.Error("expected the removed ollama adapter to be not-OK")
	}
	if !strings.Contains(result.Adapters[0].Remediation, "openai-compatible") {
		t.Errorf("expected the migration remediation, got %q", result.Adapters[0].Remediation)
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
	check, ok := resultItemOK(result, "project_mapping")
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

	check, ok := resultItemOK(result, "project_mapping")
	if !ok {
		t.Fatal("expected project_mapping check to be present")
	}
	if !check.OK {
		t.Errorf("expected project_mapping.OK=true when sources agree, got error: %q", check.Error)
	}
}

// TestDoctorResult_Schema verifies that DoctorResult carries the JSON v2
// version and a result for every expected check (regression guard).
func TestDoctorResult_Schema(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	if result.V != SchemaVersion {
		t.Errorf("expected schema version V=%d, got %d", SchemaVersion, result.V)
	}

	// These check keys must always be present. The leak carriers are in the
	// list because a carrier that silently stops registering is exactly the
	// failure they exist to prevent (#330, #332, #341): a missing key renders
	// as no output at all, which reads as health.
	expectedKeys := []string{
		"binary", "gh", "github_auth", "api_user", "scopes", "rate_limit", "config", "project",
		"orphaned_processes",
	}
	// With no client the auth-dependent checks are skipped, which is still a
	// result (an info finding), never an absence.
	present := map[string]CheckStatus{}
	for _, r := range result.Results {
		present[r.ID] = r.Status
	}
	for _, key := range expectedKeys {
		if _, ok := present[key]; !ok {
			t.Errorf("expected a result for check %q", key)
		}
	}
	for _, key := range []string{"api_user", "scopes", "rate_limit", "github_identity"} {
		if present[key] != StatusSkipped {
			t.Errorf("check %q = %s, want skipped with no client", key, present[key])
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

// withAdapterProbe swaps the always-on adapter probe for the duration of a
// test, so a RunDoctor assertion depends on the injected environment rather
// than on whether the developer happens to have a coding agent installed.
func withAdapterProbe(t *testing.T, p adapterProbe) {
	t.Helper()
	prev := newAdapterProbe
	newAdapterProbe = func() adapterProbe { return p }
	t.Cleanup(func() { newAdapterProbe = prev })
}

// TestRunDoctor_NoUsableAdapterDegradesButNeverBreaks is #862's regression
// guard, and it asserts BOTH halves of the fix.
//
// The defect: with no coding agent installed the verdict read
// "healthy — environment ready for pipeline operations" at exit 0.
//
// The correction that is just as load-bearing: it must NOT become exit 2.
// PREFLIGHT halts a skill immediately on exit 2 and runs inside an agent
// session, so the only way this fires mid-run is a probe false negative — and
// under exit 2 that would halt a pipeline that was working fine.
func TestRunDoctor_NoUsableAdapterDegradesButNeverBreaks(t *testing.T) {
	ctx := context.Background()
	withAdapterProbe(t, fakeProbe{}.toProbe()) // nothing installed, no keys

	result := RunDoctor(ctx, nil, nil, nil)

	item, ok := resultItemOK(result, "ai_adapter")
	if !ok {
		t.Fatal("expected an ai_adapter row on the DEFAULT run, with no --adapters passed")
	}
	if item.OK {
		t.Errorf("expected the row to fail with no usable adapter, got %+v", item)
	}
	// The invariant is not "exit code != 2" — this scenario already exits 2 on
	// the nil-client auth failure. It is that the adapter row never CHANGES the
	// exit code: the same environment with a usable adapter must land on the
	// same verdict. That is what keeps a probe false negative from halting a
	// working run through PREFLIGHT's exit-2 gate.
	withAdapterProbe(t, fakeProbe{paths: map[string]string{"claude": "/opt/claude"}}.toProbe())
	control := RunDoctor(ctx, nil, nil, nil)
	if result.ExitCode != control.ExitCode {
		t.Errorf("adapter availability must not move the exit code: %d without an adapter vs %d with one",
			result.ExitCode, control.ExitCode)
	}

	for _, name := range result.FailedChecks {
		if name == "ai_adapter" {
			t.Error("ai_adapter must not appear in FailedChecks — it is warning-only")
		}
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "no usable AI coding agent") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the adapter warning to reach Warnings, got %v", result.Warnings)
	}
}

// TestRunDoctor_UsableAdapterAddsNoWarning verifies the other direction: a
// machine that can run a stage is unchanged by this check — the row passes and
// contributes no warning, so an otherwise-healthy environment still reads
// healthy.
func TestRunDoctor_UsableAdapterAddsNoWarning(t *testing.T) {
	ctx := context.Background()
	withAdapterProbe(t, fakeProbe{paths: map[string]string{"claude": "/opt/claude"}}.toProbe())

	result := RunDoctor(ctx, nil, nil, nil)

	item, ok := resultItemOK(result, "ai_adapter")
	if !ok {
		t.Fatal("expected an ai_adapter row")
	}
	if !item.OK {
		t.Errorf("expected the row to pass with claude usable, got %+v", item)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "AI coding agent") {
			t.Errorf("a usable adapter must add no adapter warning, got %q", w)
		}
	}
}

// TestRunDoctor_EmitsStrandedBranchesArm pins the wiring, not the classifier
// (#912). checkStrandedBranches has its own tests in leaked_state_test.go and
// every one of them passes while nothing in RunDoctor calls it — which is
// precisely the leak the arm exists to close, one level up. Asserted on
// presence rather than verdict: the row's OK depends on the machine the test
// runs on, its existence does not.
func TestRunDoctor_EmitsStrandedBranchesArm(t *testing.T) {
	ctx := context.Background()
	result := RunDoctor(ctx, nil, nil, nil)

	if !hasResult(result, "stranded_branches") {
		t.Fatalf("stranded_branches missing from doctor's checks; got %v", checkKeys(result))
	}
}

func checkKeys(r DoctorResult) []string {
	keys := make([]string, 0, len(r.Results))
	for _, res := range r.Results {
		keys = append(keys, res.ID)
	}
	sort.Strings(keys)
	return keys
}

func writeRepoConfig(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("project:\n  owner: nightgauge\n  number: 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunDoctor_ConfigWithoutRepoFileBlocks is #2205 under ADR-025 (#2091): a
// loaded config (defaults or a user-global file) in a repository with no
// .nightgauge/config.yaml is a blocker offering the confirm repo-init remedy
// and naming what was loaded.
func TestRunDoctor_ConfigWithoutRepoFileBlocks(t *testing.T) {
	t.Chdir(t.TempDir())
	result := RunDoctor(context.Background(), config.DefaultConfig(), nil, nil)
	check := resultItem(result, "config")
	if check.OK {
		t.Fatalf("config passed with no repository config: %+v", check)
	}
	if !strings.Contains(check.Error, "repo-init") || !strings.Contains(check.Error, "loaded:") {
		t.Errorf("config warning must name repo-init and the loaded files, got %q", check.Error)
	}
	if !containsString(result.FailedChecks, "config") {
		t.Error("a missing repository config is a blocker")
	}
}

// TestRunDoctor_ConfigNamesLoadedFiles: with a repository file, the row names it.
func TestRunDoctor_ConfigNamesLoadedFiles(t *testing.T) {
	repo := t.TempDir()
	writeRepoConfig(t, repo)
	t.Chdir(repo)
	result := RunDoctor(context.Background(), &config.Config{Owner: "o", ProjectNumber: 1}, nil, nil)
	check := resultItem(result, "config")
	if !check.OK || !strings.Contains(check.Detail, filepath.Join(".nightgauge", "config.yaml")) {
		t.Fatalf("config row must pass and name the repository file, got %+v", check)
	}
}

// hasResult reports whether the run produced a result for check id.
func hasResult(r DoctorResult, id string) bool {
	for _, res := range r.Results {
		if res.ID == id {
			return true
		}
	}
	return false
}

// runRegistered runs one registered check against env.
func runRegistered(t *testing.T, id string, env *Env) []Finding {
	t.Helper()
	for _, c := range DefaultRegistry().Checks() {
		if c.ID == id {
			return c.Run(context.Background(), env)
		}
	}
	t.Fatalf("check %s is not registered", id)
	return nil
}

type statusTransport int

func (s statusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: int(s), Status: http.StatusText(int(s)), Request: r,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(`{"message":"Bad credentials"}`))}, nil
}

func onlyFinding(t *testing.T, fs []Finding, code string, sev Severity) Finding {
	t.Helper()
	if len(fs) != 1 || fs[0].Code != code || fs[0].Severity != sev {
		t.Fatalf("want one %s %s finding, got %s", sev, code, findingsText(fs))
	}
	if fs[0].Fingerprint == "" || fs[0].Docs != DocsAnchor(code) || fs[0].Cause == "" {
		t.Errorf("finding lacks fingerprint, docs or cause: %+v", fs[0])
	}
	return fs[0]
}

// TestGitHubFindings pins the #2091 severities and remedies.
func TestGitHubFindings(t *testing.T) {
	t.Run("401 is an auth blocker", func(t *testing.T) {
		client := gh.NewClientWithHTTPClient(&http.Client{Transport: statusTransport(http.StatusUnauthorized)})
		f := onlyFinding(t, runRegistered(t, "github_auth", &Env{Client: client}), "NGD004", SeverityBlocker)
		if f.Evidence["http_status"] != "401" || f.Remedies[0].Kind != RemedyManual {
			t.Errorf("want http_status=401 and a manual remedy, got %+v", f)
		}
	})
	t.Run("no client is an auth blocker", func(t *testing.T) {
		onlyFinding(t, runRegistered(t, "github_auth", &Env{}), "NGD004", SeverityBlocker)
	})
	t.Run("missing config offers confirm repo-init", func(t *testing.T) {
		for _, fs := range [][]Finding{
			first(configFindings(nil, nil, "/w")),
			first(configFindings(config.DefaultConfig(), nil, t.TempDir())),
			first(projectFindings(&config.Config{Owner: "o"}, "/w")),
		} {
			f := onlyFinding(t, fs, fs[0].Code, SeverityBlocker)
			r := f.Remedies[len(f.Remedies)-1]
			if r.Kind != RemedyConfirm || r.Verb != verbRepoInit || !strings.Contains(r.Preview, "nightgauge repo-init") {
				t.Errorf("%s: want a confirm repo-init remedy with a preview, got %+v", f.Code, r)
			}
		}
	})
	t.Run("failed board read is a blocker", func(t *testing.T) {
		cfg := &config.Config{Owner: "o", DefaultRepo: "r", ProjectNumber: 7}
		fs, _ := boardPopulationFindings(cfg, boardPopulation{}, errors.New("list open items on project 7: forbidden"))
		onlyFinding(t, fs, "NGD013", SeverityBlocker)
	})
	t.Run("budget pressure warns", func(t *testing.T) {
		fs, _ := rateLimitFindings(&gh.RateLimitInfo{Remaining: 10, Limit: 5000}, nil)
		onlyFinding(t, fs, "NGD007", SeverityWarning)
	})
	t.Run("identity summary is info", func(t *testing.T) {
		fs, _ := githubIdentityFindings(nil, &gh.RateLimitInfo{Limit: 5000})
		f := onlyFinding(t, fs, "NGD009", SeverityInfo)
		if f.Evidence["kind"] != "personal_token" || f.Evidence["graphql_ceiling_per_hour"] != "5000" {
			t.Errorf("identity evidence = %v", f.Evidence)
		}
	})
	t.Run("stale binary remedy", func(t *testing.T) {
		p := binaryProbe{found: true, resolved: ResolvedBinary{Path: "/a/bin/nightgauge", Step: StepRepoBin},
			resolvedVersion: "nightgauge v0.1.0", recordedPath: "/b/nightgauge", recordedVersion: "nightgauge v0.2.0",
			stale: "stale binary: diverges"}
		checkout := t.TempDir()
		writeFile := func(name, body string) {
			if err := os.WriteFile(filepath.Join(checkout, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		writeFile("go.mod", "module github.com/nightgauge/nightgauge\n\ngo 1.22\n")
		writeFile("Makefile", "all:\n\nbuild-cli:\n\tgo build\n")
		if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		root := sourceCheckoutRoot(checkout)
		if root != checkout {
			t.Fatalf("sourceCheckoutRoot = %q, want %q", root, checkout)
		}
		fs, _ := binaryFindings(p, root)
		f := onlyFinding(t, fs, "NGD001", SeverityWarning)
		for _, k := range []string{"resolved_path", "resolved_version", "recorded_path", "recorded_version"} {
			if f.Evidence[k] == "" {
				t.Errorf("evidence lacks %s: %v", k, f.Evidence)
			}
		}
		if r := f.Remedies[0]; r.Kind != RemedyAuto || r.Verb != verbBuildCLI || !strings.Contains(r.Preview, "make build-cli") {
			t.Errorf("in the checkout the remedy is auto make build-cli, got %+v", r)
		}
		fs, _ = binaryFindings(p, sourceCheckoutRoot(t.TempDir()))
		if r := fs[0].Remedies[0]; r.Kind != RemedyManual || !strings.Contains(strings.Join(r.Steps, " "), installCommand) {
			t.Errorf("outside the checkout the remedy is manual install, got %+v", r)
		}
		if fs, _ := binaryFindings(binaryProbe{found: true, detail: "ok"}, ""); len(fs) != 0 {
			t.Errorf("a binary that does not diverge now must not warn: %s", findingsText(fs))
		}
		onlyFinding(t, first(binaryFindings(binaryProbe{}, "")), "NGD001", SeverityBlocker)
	})
}

func first(fs []Finding, _ string) []Finding { return fs }
