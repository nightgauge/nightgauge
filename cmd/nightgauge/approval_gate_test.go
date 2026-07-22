package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIssueContext writes a `.nightgauge/pipeline/issue-{N}.json` under
// dir and returns dir, so readApprovalFacts can be exercised against it.
func writeIssueContext(t *testing.T, issueNum int, body string) string {
	t.Helper()
	dir := t.TempDir()
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(pipelineDir, fmt.Sprintf("issue-%d.json", issueNum))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func TestReadApprovalFacts_DependencyAnalysisPresent(t *testing.T) {
	dir := writeIssueContext(t, 42, `{
		"routing": {"risk_high": false},
		"requirements": {"summary": "bump deps"},
		"dependency_analysis": {"major_bumps_count": 2, "production_area": true},
		"labels": ["type:chore"]
	}`)

	facts := readApprovalFacts(dir, 42)
	if facts.DependencyMajorBumps != 2 {
		t.Errorf("DependencyMajorBumps = %d, want 2", facts.DependencyMajorBumps)
	}
	if !facts.IsProductionChange {
		t.Error("IsProductionChange = false, want true")
	}
}

// The critical no-over-fire case: an absent dependency_analysis block must
// leave the triggers off (zero / false), NOT default to high-impact.
func TestReadApprovalFacts_DependencyAnalysisAbsentDoesNotOverFire(t *testing.T) {
	dir := writeIssueContext(t, 43, `{
		"routing": {"risk_high": false},
		"requirements": {"summary": "small doc fix"},
		"labels": ["type:docs"]
	}`)

	facts := readApprovalFacts(dir, 43)
	if facts.DependencyMajorBumps != 0 {
		t.Errorf("DependencyMajorBumps = %d, want 0 (absent block must not over-fire)", facts.DependencyMajorBumps)
	}
	if facts.IsProductionChange {
		t.Error("IsProductionChange = true, want false (absent block must not over-fire)")
	}
}

func TestReadApprovalFacts_ZeroBumpsAndNonProduction(t *testing.T) {
	dir := writeIssueContext(t, 44, `{
		"dependency_analysis": {"major_bumps_count": 0, "production_area": false}
	}`)

	facts := readApprovalFacts(dir, 44)
	if facts.DependencyMajorBumps != 0 || facts.IsProductionChange {
		t.Errorf("present-but-empty block must yield no triggers, got bumps=%d prod=%v", facts.DependencyMajorBumps, facts.IsProductionChange)
	}
}

func TestReadApprovalFacts_MalformedJSONIsSafe(t *testing.T) {
	dir := writeIssueContext(t, 45, `{ this is not valid json `)

	facts := readApprovalFacts(dir, 45)
	if facts.DependencyMajorBumps != 0 || facts.IsProductionChange || facts.RiskHigh {
		t.Error("malformed JSON must yield a zero-value approvalFacts, not over-fire")
	}
}

func TestReadApprovalFacts_MissingFileIsSafe(t *testing.T) {
	dir := t.TempDir() // no issue context written
	facts := readApprovalFacts(dir, 99)
	if facts.DependencyMajorBumps != 0 || facts.IsProductionChange {
		t.Error("missing context file must yield a zero-value approvalFacts")
	}
}

// TestApprovalGateCmd_DisabledEmitsJSON pins the --json contract on the
// gate-disabled short-circuit: exit 0 with a parseable ApprovalResult on
// stdout. The pre-fix plain-text line made the VSCode pre-check's JSON.parse
// throw and log a spurious "binary error" on every disabled-gate run
// (bowlsheet dogfooding, 2026-07-11).
func TestApprovalGateCmd_DisabledEmitsJSON(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := "owner: testorg\npipeline:\n  architecture_approval:\n    enabled: false\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Hermetic machine tier.
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())

	// Capture stdout across the command run.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	cmd := approvalGateCmd()
	cmd.SetArgs([]string{"42", "--workdir", dir, "--json"})
	runErr := cmd.Execute()
	w.Close()
	os.Stdout = origStdout
	out, _ := io.ReadAll(r)

	if runErr != nil {
		t.Fatalf("Execute: %v", runErr)
	}
	var res struct {
		RequiresApproval bool     `json:"requires_approval"`
		Reasons          []string `json:"reasons"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("stdout is not JSON (the pre-fix bug): %v\nstdout: %q", err, string(out))
	}
	if res.RequiresApproval {
		t.Errorf("requires_approval = true on disabled gate, want false")
	}
	if len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "gate disabled") {
		t.Errorf("reasons = %v, want gate-disabled reason", res.Reasons)
	}
}
