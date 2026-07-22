package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestParityWithCanonicalSDKRegistry is the cross-language guard: the Go mirror
// (internal/models/model-registry.json) must carry the same data as the
// canonical SDK source of truth. Comparison is structural (parsed), so JSON
// formatting differences don't cause false failures — only data drift does.
//
// If this fails: run scripts/sync-model-registry.sh after editing the canonical
// file (packages/nightgauge-sdk/src/eval/model-registry.json).
func TestParityWithCanonicalSDKRegistry(t *testing.T) {
	canonicalPath := filepath.Join(
		"..", "..", "packages", "nightgauge-sdk", "src", "eval", "model-registry.json",
	)
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("reading canonical SDK registry: %v", err)
	}

	var canonical, mirror registryFile
	if err := json.Unmarshal(canonicalBytes, &canonical); err != nil {
		t.Fatalf("parsing canonical registry: %v", err)
	}
	if err := json.Unmarshal(RawJSON(), &mirror); err != nil {
		t.Fatalf("parsing embedded mirror: %v", err)
	}

	if !reflect.DeepEqual(canonical.Models, mirror.Models) {
		t.Errorf("Go registry mirror has drifted from the canonical SDK registry.\n"+
			"Run scripts/sync-model-registry.sh to re-sync.\n"+
			"canonical has %d models, mirror has %d models",
			len(canonical.Models), len(mirror.Models))
	}
}

func TestRegistryIntegrity(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("registry is empty")
	}
	for _, id := range []string{
		"claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5-20251001", "claude-fable-5",
	} {
		if _, ok := Get(id); !ok {
			t.Errorf("expected registry to contain %q", id)
		}
	}
	// A non-Anthropic, provider-neutral entry must exist.
	hasOther := false
	for _, m := range All() {
		if m.Provider != "anthropic" {
			hasOther = true
		}
	}
	if !hasOther {
		t.Error("expected at least one non-Anthropic provider-neutral model")
	}
}

func TestTierResolutionPrefersCurrentModel(t *testing.T) {
	got, ok := Get("sonnet")
	if !ok {
		t.Fatal("sonnet tier did not resolve")
	}
	if got.ID != "claude-sonnet-5" {
		t.Errorf("sonnet tier resolved to %q, want claude-sonnet-5 (current, not deprecated 4.6)", got.ID)
	}
	if got.Deprecated {
		t.Error("tier resolution returned a deprecated model")
	}
}

// ─── Provider-aware resolution (#56) ─────────────────────────────────────────

func TestResolveProviderTierBands(t *testing.T) {
	cases := []struct {
		provider, tier, wantID string
	}{
		{"openai", "haiku", "gpt-5.4-mini"},
		{"openai", "sonnet", "gpt-5.4"},
		{"openai", "opus", "gpt-5.5"},
		{"openai", "fable", "gpt-5.5"}, // no fable-equivalent → strongest model
		{"google", "haiku", "gemini-2.5-flash"},
		{"google", "sonnet", "gemini-2.5-flash"},
		{"google", "opus", "gemini-2.5-pro"},
		{"google", "fable", "gemini-2.5-pro"},
		{"copilot", "haiku", "gpt-4o-mini"},
		{"copilot", "sonnet", "gpt-4o"},
		{"copilot", "opus", "claude-sonnet-4.5"},
		{"anthropic", "sonnet", "claude-sonnet-5"},
	}
	for _, c := range cases {
		got, ok := Resolve(c.provider, c.tier)
		if !ok {
			t.Errorf("Resolve(%s, %s) missed, want %s", c.provider, c.tier, c.wantID)
			continue
		}
		if got.ID != c.wantID {
			t.Errorf("Resolve(%s, %s) = %s, want %s", c.provider, c.tier, got.ID, c.wantID)
		}
	}
}

func TestResolveLocalProvidersHaveNoBands(t *testing.T) {
	// ollama/lm-studio have no registry entries by design: the configured
	// local model serves every band and costs $0 via the unknown default.
	for _, provider := range []string{"ollama", "lm-studio"} {
		for _, tier := range []string{"haiku", "sonnet", "opus", "fable"} {
			if m, ok := Resolve(provider, tier); ok {
				t.Errorf("Resolve(%s, %s) = %s, want miss (local providers have no tier hierarchy)",
					provider, tier, m.ID)
			}
		}
	}
}

func TestGetTierLookupStaysAnthropic(t *testing.T) {
	// Bare tier names are the pipeline's canonical routing currency and must
	// keep resolving to Anthropic models even now that other providers carry
	// the same band names.
	for _, tier := range []string{"haiku", "sonnet", "opus", "fable"} {
		m, ok := Get(tier)
		if !ok {
			t.Fatalf("Get(%s) missed", tier)
		}
		if m.Provider != "anthropic" {
			t.Errorf("Get(%s) = %s (provider %s), want an anthropic model", tier, m.ID, m.Provider)
		}
	}
}

func TestResolveExactIDIsProviderAgnostic(t *testing.T) {
	// Concrete ids are globally unique, so an exact-id lookup resolves no
	// matter which provider the caller asked for.
	m, ok := Resolve("anthropic", "gemini-2.5-pro")
	if !ok || m.Provider != "google" {
		t.Errorf("Resolve(anthropic, gemini-2.5-pro) = %+v ok=%v, want the google entry", m, ok)
	}
}

func TestProviderForAdapter(t *testing.T) {
	cases := map[string]string{
		"claude":          "anthropic",
		"claude-sdk":      "anthropic",
		"claude-headless": "anthropic",
		"codex":           "openai",
		"gemini":          "google",
		"gemini-sdk":      "google",
		"copilot":         "copilot",
		"ollama":          "ollama",
		"lm-studio":       "lm-studio",
		"mystery":         "other",
	}
	for adapter, want := range cases {
		if got := ProviderForAdapter(adapter); got != want {
			t.Errorf("ProviderForAdapter(%s) = %s, want %s", adapter, got, want)
		}
	}
}

func TestBandUniquenessAcrossProviders(t *testing.T) {
	// At most one non-deprecated model per (provider, band): tier resolution
	// must be deterministic. mustLoad panics on violations at init; this
	// asserts the shipped data directly so a drift is a readable failure.
	seen := map[string]string{}
	for _, m := range All() {
		if m.Deprecated {
			continue
		}
		for _, tier := range m.Tiers {
			key := m.Provider + "/" + tier
			if prev, dup := seen[key]; dup {
				t.Errorf("band %s served by both %s and %s", key, prev, m.ID)
			}
			seen[key] = m.ID
		}
	}
}
