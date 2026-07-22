package ci

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWorkflow writes a workflow file under dir/.github/workflows.
func writeWorkflow(t *testing.T, dir, name, content string) {
	t.Helper()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectRequiredCheckConfigMismatches_ContinueOnErrorRequired(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "smoke.yml", `
name: Smoke
jobs:
  sentry-smoke:
    name: Sentry Smoke
    continue-on-error: true
    steps:
      - run: ./smoke.sh
`)

	// Matrix-decorated context name, as in the #184 incident.
	mismatches := DetectRequiredCheckConfigMismatches(dir, []string{"Sentry Smoke (integration)"})
	if len(mismatches) != 1 {
		t.Fatalf("want 1 mismatch, got %d: %+v", len(mismatches), mismatches)
	}
	m := mismatches[0]
	if m.Check != "Sentry Smoke (integration)" {
		t.Errorf("Check = %q", m.Check)
	}
	if m.JobKey != "sentry-smoke" {
		t.Errorf("JobKey = %q", m.JobKey)
	}
	if m.WorkflowPath != filepath.Join(".github", "workflows", "smoke.yml") {
		t.Errorf("WorkflowPath = %q", m.WorkflowPath)
	}
	if m.Remediation == "" {
		t.Error("Remediation must carry the human action text")
	}
}

func TestDetectRequiredCheckConfigMismatches_MatchesJobKeyWhenUnnamed(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  flaky-e2e:
    continue-on-error: true
    steps:
      - run: ./e2e.sh
`)

	mismatches := DetectRequiredCheckConfigMismatches(dir, []string{"flaky-e2e"})
	if len(mismatches) != 1 {
		t.Fatalf("want 1 mismatch, got %d", len(mismatches))
	}
}

func TestDetectRequiredCheckConfigMismatches_NoContinueOnError(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  build:
    name: Build
    steps:
      - run: make build
`)

	if got := DetectRequiredCheckConfigMismatches(dir, []string{"Build"}); len(got) != 0 {
		t.Errorf("want no mismatches for a normal job, got %+v", got)
	}
}

func TestDetectRequiredCheckConfigMismatches_ExpressionNotLiteral(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  build:
    name: Build
    continue-on-error: ${{ matrix.experimental }}
    steps:
      - run: make build
`)

	// Expressions can't be evaluated deterministically — must not flag.
	if got := DetectRequiredCheckConfigMismatches(dir, []string{"Build"}); len(got) != 0 {
		t.Errorf("want no mismatches for expression continue-on-error, got %+v", got)
	}
}

func TestDetectRequiredCheckConfigMismatches_NoRequiredChecks(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  build:
    continue-on-error: true
    steps:
      - run: make build
`)

	if got := DetectRequiredCheckConfigMismatches(dir, nil); got != nil {
		t.Errorf("want nil for empty required set, got %+v", got)
	}
}

func TestDetectRequiredCheckConfigMismatches_NoWorkflowsDir(t *testing.T) {
	if got := DetectRequiredCheckConfigMismatches(t.TempDir(), []string{"CI"}); len(got) != 0 {
		t.Errorf("want no mismatches without workflows dir, got %+v", got)
	}
}
