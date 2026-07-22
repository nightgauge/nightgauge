package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTierFile creates a tier YAML file at path with the given content.
func writeTierFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestBuildAuditReport_CleanConfig verifies that a project YAML containing only
// team-tier keys produces all OK rows and no DRIFT.
func TestBuildAuditReport_CleanConfig(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
project:
  owner: TestOrg
  number: 1
  repo: test-repo
`)

	entries, err := BuildAuditReport(dir)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Status, "DRIFT") {
			t.Errorf("expected no DRIFT for clean config, got DRIFT on key %q: %s", e.Key, e.Status)
		}
	}
}

// TestBuildAuditReport_DriftDetection verifies that a machine-tier key
// (github_user) appearing in the project YAML is flagged as DRIFT.
func TestBuildAuditReport_DriftDetection(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
project:
  owner: TestOrg
github_user: badpractice
`)

	entries, err := BuildAuditReport(dir)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}

	found := false
	for _, e := range entries {
		if e.Key == "github_user" {
			found = true
			if !strings.HasPrefix(e.Status, "DRIFT") {
				t.Errorf("expected DRIFT for github_user in project YAML, got %q", e.Status)
			}
			if e.EffectiveTier != "project" {
				t.Errorf("expected effectiveTier=project, got %q", e.EffectiveTier)
			}
			if e.TargetTier != "machine" {
				t.Errorf("expected targetTier=machine, got %q", e.TargetTier)
			}
		}
	}
	if !found {
		t.Error("expected github_user entry in audit report")
	}
}

// TestBuildAuditReport_AutonomousReposDrift verifies that per-repo entries
// under autonomous.repositories are flagged as DRIFT when in the project YAML.
func TestBuildAuditReport_AutonomousReposDrift(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
autonomous:
  repositories:
    myrepo:
      sequential: true
`)

	entries, err := BuildAuditReport(dir)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}

	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Key, "autonomous.repositories.myrepo") {
			found = true
			if !strings.HasPrefix(e.Status, "DRIFT") {
				t.Errorf("expected DRIFT for %q in project YAML, got %q", e.Key, e.Status)
			}
		}
	}
	if !found {
		t.Error("expected autonomous.repositories.myrepo entry in audit report")
	}
}

// TestBuildAuditReport_RuntimeSkipped verifies that runtime-tier keys
// (e.g. pipeline.max_concurrent) are omitted from the audit report entirely.
func TestBuildAuditReport_RuntimeSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
pipeline:
  max_concurrent: 4
`)

	entries, err := BuildAuditReport(dir)
	if err != nil {
		t.Fatalf("BuildAuditReport: %v", err)
	}

	for _, e := range entries {
		if e.Key == "pipeline.max_concurrent" {
			t.Errorf("runtime-tier key pipeline.max_concurrent should be excluded from audit, but found entry: %+v", e)
		}
	}
}

// TestRenderTierAudit_JSON verifies that JSON output is valid and contains the
// required fields for each entry.
func TestRenderTierAudit_JSON(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
project:
  owner: TestOrg
github_user: drift-value
`)

	hasDrift, out, err := RenderTierAudit(dir, false, true)
	if err != nil {
		t.Fatalf("RenderTierAudit --json: %v", err)
	}
	if !hasDrift {
		t.Error("expected hasDrift=true")
	}

	var entries []TierAuditEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("JSON output is invalid: %v\noutput:\n%s", err, out)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one entry in JSON output")
	}
	for _, e := range entries {
		if e.Key == "" {
			t.Error("entry has empty Key field")
		}
		if e.EffectiveTier == "" {
			t.Errorf("entry %q has empty EffectiveTier", e.Key)
		}
		if e.TargetTier == "" {
			t.Errorf("entry %q has empty TargetTier", e.Key)
		}
		if e.Status == "" {
			t.Errorf("entry %q has empty Status", e.Key)
		}
	}
}

// TestRenderTierAudit_StrictFlag verifies that hasDrift is true when DRIFT is
// present and false when the config is clean.
func TestRenderTierAudit_StrictFlag(t *testing.T) {
	t.Run("with_drift", func(t *testing.T) {
		dir := t.TempDir()
		writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
github_user: should-be-machine
`)
		hasDrift, _, err := RenderTierAudit(dir, false, false)
		if err != nil {
			t.Fatalf("RenderTierAudit: %v", err)
		}
		if !hasDrift {
			t.Error("expected hasDrift=true for machine key in project YAML")
		}
	})

	t.Run("clean", func(t *testing.T) {
		dir := t.TempDir()
		writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
project:
  owner: CleanOrg
  number: 5
`)
		hasDrift, _, err := RenderTierAudit(dir, false, false)
		if err != nil {
			t.Fatalf("RenderTierAudit: %v", err)
		}
		if hasDrift {
			t.Error("expected hasDrift=false for clean config")
		}
	})
}

// TestRenderTierAudit_FilterDrift verifies that only DRIFT rows appear when
// filterDrift=true.
func TestRenderTierAudit_FilterDrift(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
project:
  owner: TestOrg
github_user: drift-value
`)

	_, out, err := RenderTierAudit(dir, true, true)
	if err != nil {
		t.Fatalf("RenderTierAudit --filter-drift --json: %v", err)
	}

	var entries []TierAuditEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("JSON output invalid: %v\n%s", err, out)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Status, "DRIFT") {
			t.Errorf("--filter-drift should show only DRIFT rows, but got key=%q status=%q", e.Key, e.Status)
		}
	}
	if len(entries) == 0 {
		t.Error("expected at least one DRIFT entry with --filter-drift when drift is present")
	}
}

// TestRenderTierAudit_TableOutput verifies that text table output contains the
// header and separator line.
func TestRenderTierAudit_TableOutput(t *testing.T) {
	dir := t.TempDir()
	writeTierFile(t, filepath.Join(dir, ".nightgauge", "config.yaml"), `
project:
  owner: TestOrg
`)

	_, out, err := RenderTierAudit(dir, false, false)
	if err != nil {
		t.Fatalf("RenderTierAudit (text): %v", err)
	}
	if !strings.Contains(out, "KEY") {
		t.Errorf("expected KEY header in table output, got:\n%s", out)
	}
	if !strings.Contains(out, "STATUS") {
		t.Errorf("expected STATUS header in table output, got:\n%s", out)
	}
}

// TestComputeStatus covers the status classification logic directly.
// The file-to-tier mapping: "project" effective = .nightgauge/config.yaml (team home),
// "machine" effective = ~/.nightgauge/config.yaml (machine home).
func TestComputeStatus(t *testing.T) {
	cases := []struct {
		effective string
		target    string
		wantDrift bool
	}{
		{"project", "team", false},    // team key in project file (correct home) → OK
		{"machine", "machine", false}, // machine key in machine file (correct home) → OK
		{"local", "team", false},      // team key overridden in local file → OK (allowed override)
		{"project", "machine", true},  // machine key in project YAML → DRIFT
		{"local", "machine", true},    // machine key in local YAML → DRIFT
		{"machine", "team", true},     // team key in machine YAML → DRIFT
		{"project", "unknown", false}, // unclassified — UNCLASSIFIED (not DRIFT)
	}
	for _, c := range cases {
		status := computeStatus(c.effective, c.target)
		isDrift := strings.HasPrefix(status, "DRIFT")
		if isDrift != c.wantDrift {
			t.Errorf("computeStatus(%q, %q) = %q, wantDrift=%v", c.effective, c.target, status, c.wantDrift)
		}
	}
}

// TestLookupTargetTier_Wildcard verifies that autonomous.repositories.* matches
// any per-repo sub-key.
func TestLookupTargetTier_Wildcard(t *testing.T) {
	classification, err := loadTierClassification()
	if err != nil {
		t.Fatalf("loadTierClassification: %v", err)
	}

	cases := []struct {
		key  string
		want string
	}{
		{"autonomous.repositories.myrepo.sequential", "machine"},
		{"autonomous.repositories.nightgauge-nightgauge.max_concurrent", "machine"},
		{"github_user", "machine"},
		{"project.owner", "team"},
		{"pipeline.max_concurrent", "runtime"},
	}
	for _, c := range cases {
		got := lookupTargetTier(classification, c.key)
		if got != c.want {
			t.Errorf("lookupTargetTier(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}
