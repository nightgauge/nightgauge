package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStageAdapterConfig(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadParsesCanonicalAdapterSchema(t *testing.T) {
	root := writeStageAdapterConfig(t, `
owner: nightgauge
ui:
  core:
    adapter: codex
pipeline:
  stage_adapters:
    feature-dev: gemini
    pr-merge: claude
  adapter_fallback_chain:
    - claude
    - codex
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI == nil || cfg.UI.Core == nil || cfg.UI.Core.Adapter != "codex" {
		t.Errorf("ui.core.adapter = %+v, want codex", cfg.UI)
	}
	if cfg.Pipeline == nil {
		t.Fatal("pipeline block missing")
	}
	if got := cfg.Pipeline.StageAdapters["feature-dev"]; got != "gemini" {
		t.Errorf("stage_adapters[feature-dev] = %q, want gemini", got)
	}
	if got := cfg.Pipeline.StageAdapters["pr-merge"]; got != "claude" {
		t.Errorf("stage_adapters[pr-merge] = %q, want claude", got)
	}
	if len(cfg.Pipeline.AdapterFallbackChain) != 2 || cfg.Pipeline.AdapterFallbackChain[0] != "claude" {
		t.Errorf("adapter_fallback_chain = %v, want [claude codex]", cfg.Pipeline.AdapterFallbackChain)
	}
}

func TestResolveStageAdapterPrecedence(t *testing.T) {
	cfg := &Config{
		Pipeline: &PipelineConfig{StageAdapters: map[string]string{"feature-dev": "gemini"}},
		UI:       &UIConfig{Core: &UICoreConfig{Adapter: "codex"}},
	}
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	// 4. Global config default
	if r := ResolveStageAdapter(cfg, "pr-create", getenv); r.Adapter != "codex" || r.Source != "global-config" {
		t.Errorf("global-config rung = %+v", r)
	}
	// 3. Per-stage config outranks global
	if r := ResolveStageAdapter(cfg, "feature-dev", getenv); r.Adapter != "gemini" || r.Source != "stage-config" {
		t.Errorf("stage-config rung = %+v", r)
	}
	// 2. NIGHTGAUGE_ADAPTER outranks config
	env["NIGHTGAUGE_ADAPTER"] = "copilot"
	if r := ResolveStageAdapter(cfg, "feature-dev", getenv); r.Adapter != "copilot" || r.Source != "adapter-env" {
		t.Errorf("adapter-env rung = %+v", r)
	}
	// 1. Per-stage env outranks everything
	env["NIGHTGAUGE_PIPELINE_STAGE_ADAPTER_FEATURE_DEV"] = "claude"
	if r := ResolveStageAdapter(cfg, "feature-dev", getenv); r.Adapter != "claude" || r.Source != "stage-env" {
		t.Errorf("stage-env rung = %+v", r)
	}
	// Nothing resolves → empty (caller default)
	if r := ResolveStageAdapter(nil, "feature-validate", func(string) string { return "" }); r.Adapter != "" || r.Source != "" {
		t.Errorf("empty resolution = %+v", r)
	}
}
