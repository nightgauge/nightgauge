package config

import (
	"testing"
)

// TestLoad_Routing_Absent confirms the routing section is optional — a config
// with no routing: block loads fine with a nil Routing (backward compatible).
func TestLoad_Routing_Absent(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, "owner: nightgauge\nproject:\n  number: 1\n  repo: nightgauge\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Routing != nil {
		t.Errorf("Routing = %+v, want nil when absent", cfg.Routing)
	}
}

// TestLoad_Routing_ChangeRules parses a full routing.change_rules block and
// verifies the nested ChangeRule fields round-trip through the YAML loader.
func TestLoad_Routing_ChangeRules(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	yaml := `owner: nightgauge
project:
  number: 1
  repo: nightgauge
routing:
  trivial_max_complexity: 2
  force_full_pipeline: false
  change_rules:
    - name: docs-only
      description: Docs skip planning and validate.
      globs:
        - "docs/**"
        - "**/*.md"
      change_types:
        - docs
      skip_stages:
        - feature-planning
        - feature-validate
      override_route: trivial
    - name: generated
      globs:
        - "**/*.gen.go"
      change_types:
        - code
      ci_jobs:
        - build-and-test
`
	writeProjectYAML(t, dir, yaml)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Routing == nil {
		t.Fatal("Routing = nil, want populated")
	}
	if cfg.Routing.TrivialMaxComplexity == nil || *cfg.Routing.TrivialMaxComplexity != 2 {
		t.Errorf("TrivialMaxComplexity = %v, want 2", cfg.Routing.TrivialMaxComplexity)
	}
	if cfg.Routing.ForceFullPipeline {
		t.Errorf("ForceFullPipeline = true, want false")
	}
	if len(cfg.Routing.ChangeRules) != 2 {
		t.Fatalf("ChangeRules len = %d, want 2", len(cfg.Routing.ChangeRules))
	}

	docs := cfg.Routing.ChangeRules[0]
	if docs.Name != "docs-only" {
		t.Errorf("rule[0].Name = %q, want docs-only", docs.Name)
	}
	if len(docs.Globs) != 2 || docs.Globs[0] != "docs/**" {
		t.Errorf("rule[0].Globs = %v, want [docs/** **/*.md]", docs.Globs)
	}
	if len(docs.ChangeTypes) != 1 || docs.ChangeTypes[0] != "docs" {
		t.Errorf("rule[0].ChangeTypes = %v, want [docs]", docs.ChangeTypes)
	}
	if len(docs.SkipStages) != 2 {
		t.Errorf("rule[0].SkipStages = %v, want planning+validate", docs.SkipStages)
	}
	if docs.OverrideRoute != "trivial" {
		t.Errorf("rule[0].OverrideRoute = %q, want trivial", docs.OverrideRoute)
	}

	gen := cfg.Routing.ChangeRules[1]
	if gen.Name != "generated" {
		t.Errorf("rule[1].Name = %q, want generated", gen.Name)
	}
	if len(gen.CIJobs) != 1 || gen.CIJobs[0] != "build-and-test" {
		t.Errorf("rule[1].CIJobs = %v, want [build-and-test]", gen.CIJobs)
	}
}

// TestLoad_ModelRouting_Absent confirms model_routing is optional — a config
// with no model_routing: block loads with a nil ModelRouting.
func TestLoad_ModelRouting_Absent(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, "owner: nightgauge\nproject:\n  number: 1\n  repo: nightgauge\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ModelRouting != nil {
		t.Errorf("ModelRouting = %+v, want nil when absent", cfg.ModelRouting)
	}
}

// TestLoad_ModelRouting_MinimumModel parses a model_routing.minimum_model block
// and verifies the per-stage floor map round-trips through the YAML loader —
// the input the Go autonomous scheduler reads to floor a stage's model (#366).
func TestLoad_ModelRouting_MinimumModel(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	yaml := `owner: nightgauge
project:
  number: 1
  repo: nightgauge
model_routing:
  mode: automatic
  minimum_model:
    feature-dev: sonnet
    feature-validate: opus
`
	writeProjectYAML(t, dir, yaml)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ModelRouting == nil {
		t.Fatal("ModelRouting = nil, want populated")
	}
	if got := cfg.ModelRouting.MinimumModel["feature-dev"]; got != "sonnet" {
		t.Errorf("minimum_model[feature-dev] = %q, want sonnet", got)
	}
	if got := cfg.ModelRouting.MinimumModel["feature-validate"]; got != "opus" {
		t.Errorf("minimum_model[feature-validate] = %q, want opus", got)
	}
}
