package models_test

// This is an external test package so it can read the adapter registry:
// internal/execution/adapters imports models, so a test inside package models
// cannot import it back.

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/models"
)

// TestProviderForAdapter pins each adapter name's provider, and that ProviderFor
// keeps the single-provider path unchanged: for every registered adapter except
// the multi-provider opencode, the model never changes the answer.
func TestProviderForAdapter(t *testing.T) {
	cases := map[string]string{
		"claude":          "anthropic",
		"claude-sdk":      "anthropic",
		"claude-headless": "anthropic",
		"codex":           "openai",
		"gemini":          "google",
		"gemini-sdk":      "google",
		"copilot":         "copilot",
		"github":          "copilot",
		"gh":              "copilot",
		"grok":            "xai",
		"grok-headless":   "xai",
		"xai":             "xai",
		"gemini-headless": "google",
		"ollama":          "ollama",
		"lm-studio":       "lm-studio",
		"lmstudio":        "other",
		"opencode":        "other",
		"mystery":         "other",
	}
	for adapter, want := range cases {
		if got := models.ProviderForAdapter(adapter); got != want {
			t.Errorf("ProviderForAdapter(%s) = %s, want %s", adapter, got, want)
		}
	}

	registry := adapters.NewRegistry()
	registered := registry.Names()
	if len(registered) == 0 {
		t.Fatal("the adapter registry lists no adapters")
	}
	// An alias names the provider of the adapter it resolves to (#1707): before,
	// gemini-headless and xai were "other" while gemini and grok had providers.
	aliases := registry.Aliases()
	if len(aliases) == 0 {
		t.Fatal("the adapter registry lists no aliases")
	}
	for alias, canonical := range aliases {
		if got, want := models.ProviderForAdapter(alias), models.ProviderForAdapter(canonical); got != want {
			t.Errorf("ProviderForAdapter(%q) = %q, want %q, the provider of the adapter it names (%s)", alias, got, want, canonical)
		}
		if _, ok := cases[alias]; !ok {
			t.Errorf("registered alias %q has no row here: pin its provider", alias)
		}
	}
	names := make([]string, 0, len(registered)+len(cases))
	for _, name := range registered {
		if _, ok := cases[name]; !ok {
			t.Errorf("registered adapter %q has no row here: decide its provider in ProviderForAdapter and pin it", name)
		}
		names = append(names, name)
	}
	for name := range cases {
		names = append(names, name)
	}

	anyModels := []string{
		"", "sonnet", "claude-sonnet-5", "gpt-5.6-terra",
		"lmstudio/qwen/qwen3.8-27b", "ollama/qwen3:32b", "anthropic/claude-sonnet-5",
		"openai/gpt-5.6-terra", "xai/grok-4.6", "google/gemini-2.5-pro", "unknown/x",
	}
	for _, adapter := range names {
		if adapter == "opencode" {
			continue // multi-provider: TestProviderForOpenCode covers it
		}
		want := models.ProviderForAdapter(adapter)
		for _, m := range anyModels {
			if got := models.ProviderFor(adapter, m); got != want {
				t.Errorf("ProviderFor(%q, %q) = %q, want ProviderForAdapter's %q", adapter, m, got, want)
			}
		}
	}
}
