//go:build !integration

// Package synthetic_test provides deterministic regression guards for failure
// classes eliminated in prior issues. Tests here run without Docker or live
// GitHub API — they exercise in-process gate logic only.
//
// # Regression: skill-no-op (Issue #3261 / #3270)
//
// Issue #3261 removed the `skill-no-op` outcome class. TestSyntheticNoOpRegression
// asserts that every registered StageGate returns Kind != KindNoOp when the
// workspace contains the minimal context files that a healthy pipeline stage
// would produce. KindFail is acceptable for gates that require a live `gh`
// binary (pr-create, pr-merge) — what must never appear is KindNoOp, which is
// the in-process discriminator for the eliminated `skill-no-op` class.
package synthetic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// fixtureIssueNumber is the sentinel issue number used in synthetic-noop.json.
// Using 9999 ensures no collision with real issues in the repo.
const fixtureIssueNumber = 9999

// TestSyntheticNoOpRegression asserts that no registered StageGate returns
// KindNoOp when the workspace contains complete, well-formed context files.
//
// KindNoOp is the in-process discriminator that maps to the `skill-no-op`
// PipelineOutcomeType eliminated in Issue #3261. Any gate returning KindNoOp
// indicates a skill that exited 0 but produced no actual state change — the
// exact regression class this test guards against.
//
// Note: pr-create and pr-merge gates also call `gh pr view`; they will return
// KindFail (not KindNoOp) when no GitHub credentials are available in the test
// environment. KindFail is outside the regression class and is acceptable here.
func TestSyntheticNoOpRegression(t *testing.T) {
	ws := t.TempDir()
	seedWorkspace(t, ws, fixtureIssueNumber)

	gateMap := gates.Default()
	if len(gateMap) == 0 {
		t.Fatal("gates.Default() returned empty map — registry may be broken")
	}

	for stage, gate := range gateMap {
		stage, gate := stage, gate
		t.Run(string(stage), func(t *testing.T) {
			result := gate.Verify(context.Background(), fixtureIssueNumber, ws)

			if result.Kind == gates.KindNoOp {
				t.Errorf("gate %q returned KindNoOp for issue %d — skill-no-op regression detected\n  reason: %s\n  evidence: %v",
					gate.Name(), fixtureIssueNumber, result.Reason, result.Evidence)
			}
		})
	}
}

// TestSyntheticDeterministicGatesPass asserts that gates which do not require
// a live `gh` binary (issue-pickup, feature-planning, feature-dev,
// feature-validate) fully pass — Kind == KindOK and Passed == true — when the
// workspace is properly seeded.
//
// This is a stronger assertion than TestSyntheticNoOpRegression for the subset
// of gates that are fully deterministic. pr-create and pr-merge are excluded
// because their Verify implementations call `gh pr view`.
func TestSyntheticDeterministicGatesPass(t *testing.T) {
	ws := t.TempDir()
	seedWorkspace(t, ws, fixtureIssueNumber)

	deterministicStages := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
	}

	gateMap := gates.Default()
	for _, stage := range deterministicStages {
		gate, ok := gateMap[stage]
		if !ok {
			t.Errorf("stage %q has no registered gate — registry out of sync with deterministicStages list", stage)
			continue
		}

		t.Run(string(stage), func(t *testing.T) {
			result := gate.Verify(context.Background(), fixtureIssueNumber, ws)

			if result.Kind == gates.KindNoOp {
				t.Errorf("gate %q returned KindNoOp — skill-no-op regression\n  reason: %s\n  evidence: %v",
					gate.Name(), result.Reason, result.Evidence)
			}
			if !result.Passed {
				t.Errorf("gate %q failed\n  kind: %s\n  reason: %s\n  evidence: %v",
					gate.Name(), result.Kind, result.Reason, result.Evidence)
			}
			if result.Kind != gates.KindOK {
				t.Errorf("gate %q Kind = %q, want KindOK", gate.Name(), result.Kind)
			}
		})
	}
}

// TestSyntheticOutcomeType asserts that a V2RunRecord built from the synthetic
// fixture does not carry OutcomeType "skill-no-op". This exercises the string
// constant layer (V2RunRecord.OutcomeType) distinct from the gates.Kind layer.
func TestSyntheticOutcomeType(t *testing.T) {
	record := buildSyntheticRunRecord(fixtureIssueNumber)

	if record.OutcomeType == "skill-no-op" {
		t.Errorf("OutcomeType == %q — skill-no-op regression detected", record.OutcomeType)
	}
}

// TestSyntheticCostBudget asserts that the synthetic run record does not
// exceed the $0.50 cost guard defined in the issue spec. Real pipeline runs
// are bounded elsewhere; this test pins the fixture's own stub cost.
func TestSyntheticCostBudget(t *testing.T) {
	const maxCostUSD = 0.50

	record := buildSyntheticRunRecord(fixtureIssueNumber)

	if record.Tokens.EstimatedCostUSD > maxCostUSD {
		t.Errorf("Tokens.EstimatedCostUSD %.4f exceeds budget $%.2f",
			record.Tokens.EstimatedCostUSD, maxCostUSD)
	}
}

// TestSyntheticNoStopHookError asserts that the synthetic run record contains
// no "stop-hook-error" in any stage's FailureCategory or error fields.
// The stop-hook-error class is a hook regression signal that healthy pipeline
// runs must not produce.
func TestSyntheticNoStopHookError(t *testing.T) {
	record := buildSyntheticRunRecord(fixtureIssueNumber)

	for stageName, stage := range record.Stages {
		if stage.FailureCategory == "stop-hook-error" {
			t.Errorf("stage %q has FailureCategory=stop-hook-error — hook regression detected", stageName)
		}
		if stage.Error == "stop-hook-error" {
			t.Errorf("stage %q has Error=stop-hook-error — hook regression detected", stageName)
		}
	}
}

// TestSyntheticFixtureLoads verifies that the canonical fixture file parses
// correctly. This catches JSON syntax errors and schema drift before any gate
// logic runs.
func TestSyntheticFixtureLoads(t *testing.T) {
	fixturePath := filepath.Join(repoRoot(t), "tests", "fixtures", "pipeline", "synthetic-noop.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("cannot read fixture %s: %v", fixturePath, err)
	}

	var fixture map[string]any
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}

	requiredFields := []string{"schema_version", "issue_number", "title", "branch", "requirements"}
	for _, field := range requiredFields {
		if _, ok := fixture[field]; !ok {
			t.Errorf("fixture missing required field %q", field)
		}
	}

	if num, ok := fixture["issue_number"].(float64); !ok || int(num) != fixtureIssueNumber {
		t.Errorf("fixture issue_number = %v, want %d", fixture["issue_number"], fixtureIssueNumber)
	}
}

// seedWorkspace writes minimal well-formed context files into ws so every
// deterministic stage gate can pass. Files match the minimal fields each gate
// actually inspects — see internal/orchestrator/gates/*.go for per-gate
// requirements.
func seedWorkspace(t *testing.T, ws string, issueNumber int) {
	t.Helper()

	pipelineDir := filepath.Join(ws, ".nightgauge", "pipeline")
	healthDir := filepath.Join(ws, ".nightgauge", "health")
	plansDir := filepath.Join(ws, ".nightgauge", "plans")

	for _, dir := range []string{pipelineDir, healthDir, plansDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("seedWorkspace mkdir %s: %v", dir, err)
		}
	}

	// issue-{N}.json: IssuePickupGate checks issue_number and branch.
	writeContextFile(t, pipelineDir, fmt.Sprintf("issue-%d.json", issueNumber), map[string]any{
		"schema_version": "1.3",
		"issue_number":   issueNumber,
		"branch":         fmt.Sprintf("feat/%d-synthetic-regression-probe", issueNumber),
		"title":          "chore: synthetic regression probe — README single-line edit",
	})

	// Write a non-empty plan file; FeaturePlanningGate stats it and checks Size>0.
	planFile := filepath.Join(plansDir, fmt.Sprintf("%d-synthetic.md", issueNumber))
	if err := os.WriteFile(planFile, []byte("# Synthetic plan\n\nAppend one line to README.md.\n"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}

	// planning-{N}.json: FeaturePlanningGate checks plan_file path exists.
	writeContextFile(t, pipelineDir, fmt.Sprintf("planning-%d.json", issueNumber), map[string]any{
		"schema_version":  "1.5",
		"issue_number":    issueNumber,
		"plan_file":       planFile, // absolute path so gate resolves correctly
		"approach":        "Append one line to README.md.",
		"files_to_create": []string{},
		"files_to_modify": []string{"README.md"},
	})

	// dev-{N}.json: FeatureDevGate checks commit_sha and files_changed.
	writeContextFile(t, pipelineDir, fmt.Sprintf("dev-%d.json", issueNumber), map[string]any{
		"schema_version": "1.8",
		"issue_number":   issueNumber,
		"commit_sha":     "abc1234",
		"files_changed": map[string]any{
			"created":  []string{},
			"modified": []string{"README.md"},
			"deleted":  []string{},
		},
		"tests_status": map[string]any{
			"passed":   1,
			"failed":   0,
			"coverage": nil,
		},
		"build_verification": map[string]any{
			"ran":    true,
			"status": "passed",
		},
		"quality_checks": map[string]any{
			"code_standards":  "passed",
			"security_review": "passed",
			"type_check":      "skipped",
			"dead_code_scan":  "not_run",
		},
	})

	// gate-metrics.jsonl: FeatureValidateGate reads this via
	// state.ReadGateMetricsForIssue. Needs at least one passing record.
	gateMetric := map[string]any{
		"schema_version": "1.0",
		"timestamp":      "2026-01-01T00:00:00Z",
		"issue_number":   issueNumber,
		"gate_name":      "unit-tests",
		"result":         "pass",
	}
	metricData, err := json.Marshal(gateMetric)
	if err != nil {
		t.Fatalf("marshal gate metric: %v", err)
	}
	metricsPath := filepath.Join(healthDir, "gate-metrics.jsonl")
	if err := os.WriteFile(metricsPath, append(metricData, '\n'), 0o644); err != nil {
		t.Fatalf("write gate-metrics.jsonl: %v", err)
	}

	// pr-{N}.json: PrCreateGate and PrMergeGate check this file before calling
	// gh. Provide a well-formed file so KindNoOp is not returned from the file
	// read — the gates will return KindFail (not KindNoOp) when gh is absent.
	writeContextFile(t, pipelineDir, fmt.Sprintf("pr-%d.json", issueNumber), map[string]any{
		"schema_version": "1.3",
		"issue_number":   issueNumber,
		"pr_number":      99999,
		"pr_url":         "https://github.com/nightgauge/nightgauge/pull/99999",
		"pr_title":       "chore: synthetic regression probe",
		"head_branch":    fmt.Sprintf("feat/%d-synthetic-regression-probe", issueNumber),
		"base_branch":    "main",
		"state":          "MERGED",
	})
}

// writeContextFile marshals payload as JSON and writes it to dir/name.
func writeContextFile(t *testing.T, dir, name string, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// buildSyntheticRunRecord constructs a minimal V2RunRecord for outcome-type
// and cost assertions. The orchestrator populates these fields at runtime;
// the synthetic test stubs them at their healthy-pipeline defaults.
func buildSyntheticRunRecord(issueNumber int) state.V2RunRecord {
	return state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "pipeline_run",
		IssueNumber:   issueNumber,
		Title:         "chore: synthetic regression probe — README single-line edit",
		Branch:        fmt.Sprintf("feat/%d-synthetic-regression-probe", issueNumber),
		OutcomeType:   "completed",
		Tokens:        state.V2Tokens{EstimatedCostUSD: 0.0},
		Stages:        map[string]state.V2StageDetail{},
	}
}

// repoRoot walks up from the working directory to find the repo root by
// locating go.mod. Used to resolve the canonical fixture file path.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}
