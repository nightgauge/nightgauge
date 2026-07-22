package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/baselineGate"
)

func TestBaselineGateCmd_HasCheckAndPromote(t *testing.T) {
	cmd := baselineGateCmd()
	if cmd.Use != "baseline-gate" {
		t.Errorf("Use = %q, want baseline-gate", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	if !subs["check"] {
		t.Error("missing 'check' subcommand")
	}
	if !subs["promote"] {
		t.Error("missing 'promote' subcommand")
	}
}

func TestLoadBaselineGateConfig_Defaults(t *testing.T) {
	cfg := loadBaselineGateConfigFromYAML("/non/existent/path")
	defaults := baselineGate.DefaultGateConfig()
	if cfg != defaults {
		t.Errorf("missing config: got %+v, want %+v", cfg, defaults)
	}
}

func TestLoadBaselineGateConfig_Overrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `pipeline:
  baseline_ci_gate:
    enabled: false
    lookback_runs: 7
    red_threshold: 3
    green_threshold: 4
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := loadBaselineGateConfigFromYAML(path)
	if cfg.Enabled != false {
		t.Errorf("Enabled = %v, want false", cfg.Enabled)
	}
	if cfg.LookbackRuns != 7 {
		t.Errorf("LookbackRuns = %d, want 7", cfg.LookbackRuns)
	}
	if cfg.RedThreshold != 3 {
		t.Errorf("RedThreshold = %d, want 3", cfg.RedThreshold)
	}
	if cfg.GreenThreshold != 4 {
		t.Errorf("GreenThreshold = %d, want 4", cfg.GreenThreshold)
	}
}

func TestLoadBaselineGateConfig_PartialKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Only override lookback_runs.
	if err := os.WriteFile(path, []byte("pipeline:\n  baseline_ci_gate:\n    lookback_runs: 10\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadBaselineGateConfigFromYAML(path)
	if cfg.LookbackRuns != 10 {
		t.Errorf("LookbackRuns = %d, want 10", cfg.LookbackRuns)
	}
	if !cfg.Enabled {
		t.Error("Enabled should default to true when not set")
	}
	if cfg.RedThreshold != 2 {
		t.Errorf("RedThreshold = %d, want 2 (default)", cfg.RedThreshold)
	}
}

func TestLoadBaselineGateConfig_MalformedFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("not: yaml: at all: ::: garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadBaselineGateConfigFromYAML(path)
	if cfg != baselineGate.DefaultGateConfig() {
		t.Errorf("malformed YAML did not fall back to defaults: %+v", cfg)
	}
}

func TestRenderCheckHuman_DoesNotPanic(t *testing.T) {
	for _, dec := range []baselineGate.Decision{baselineGate.DecisionAllow, baselineGate.DecisionDefer, baselineGate.DecisionUnparseable} {
		renderCheckHuman(&baselineGate.GateResult{Decision: dec, Reason: "test", Workflow: "ci.yml", Job: "Build", FailedRuns: 3, SampledRuns: 5}, 42)
	}
}

func TestRenderPromoteHuman_DoesNotPanic(t *testing.T) {
	renderPromoteHuman(promoteSummary{
		Owner: "o", Repo: "r", Branch: "main", Total: 2,
		Promoted:    []promoteEntry{{IssueNumber: 1, Workflow: "ci.yml"}},
		StillPaused: []promoteEntry{{IssueNumber: 2, Workflow: "ci.yml"}},
		Errors:      []promoteEntry{{IssueNumber: 3, Error: "boom"}},
	})
}
