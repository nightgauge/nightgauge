package ci

import (
	"testing"
)

func TestDetectSilentSkipRisk_FlagsBareConditionAfterEarlierConditional(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  go:
    name: Go build & test
    steps:
      - name: Checkout
        run: actions/checkout
      - name: Build
        if: ${{ needs.changes.outputs.run_heavy != 'false' }}
        run: go build ./...
      - name: Test
        if: ${{ needs.changes.outputs.run_heavy != 'false' }}
        run: go test ./...
`)

	risks := DetectSilentSkipRisk(dir)
	if len(risks) != 1 {
		t.Fatalf("want 1 risk, got %d: %+v", len(risks), risks)
	}
	r := risks[0]
	if r.StepName != "Test" {
		t.Errorf("StepName = %q, want %q", r.StepName, "Test")
	}
	if r.JobKey != "go" {
		t.Errorf("JobKey = %q", r.JobKey)
	}
	if r.Remediation == "" {
		t.Error("Remediation must carry the human action text")
	}
}

func TestDetectSilentSkipRisk_ClearedByStatusCheckFunction(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  go:
    name: Go build & test
    steps:
      - name: Build
        if: ${{ !cancelled() && needs.changes.outputs.run_heavy != 'false' }}
        run: go build ./...
      - name: Test
        if: ${{ !cancelled() && needs.changes.outputs.run_heavy != 'false' }}
        run: go test ./...
`)

	if got := DetectSilentSkipRisk(dir); len(got) != 0 {
		t.Errorf("want no risks once !cancelled() is present, got %+v", got)
	}
}

func TestDetectSilentSkipRisk_FirstConditionalStepNotFlagged(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  go:
    name: Go build & test
    steps:
      - name: Build
        if: ${{ needs.changes.outputs.run_heavy != 'false' }}
        run: go build ./...
`)

	// A single conditional step has no earlier conditional step to be
	// skipped-after, so it is not in this risk class.
	if got := DetectSilentSkipRisk(dir); len(got) != 0 {
		t.Errorf("want no risks for a lone conditional step, got %+v", got)
	}
}

func TestDetectSilentSkipRisk_UnconditionalStepsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
jobs:
  go:
    name: Go build & test
    steps:
      - name: Checkout
        run: actions/checkout
      - name: Build
        run: go build ./...
`)

	if got := DetectSilentSkipRisk(dir); got != nil {
		t.Errorf("want nil for a job with no conditional steps, got %+v", got)
	}
}

func TestDetectSilentSkipRisk_NoWorkflowsDir(t *testing.T) {
	if got := DetectSilentSkipRisk(t.TempDir()); len(got) != 0 {
		t.Errorf("want no risks without workflows dir, got %+v", got)
	}
}
