// Package models exposes the single-source model registry: identity, token
// pricing, and capability metadata for every evaluable model.
//
// The canonical source of truth is the SDK file
// packages/nightgauge-sdk/src/eval/model-registry.json. This package embeds
// a mirror (model-registry.json, kept in sync by scripts/sync-model-registry.sh);
// registry_test.go fails if the mirror drifts from the canonical file. Adding a
// model is one entry in the canonical JSON plus a sync.
//
// See docs/decisions/011-model-eval-system.md and Issue #4169.
package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed model-registry.json
var registryJSON []byte

// Rates are USD per 1,000,000 tokens. Cache rates are optional (pointer) because
// not every provider bills them.
type Rates struct {
	Input         float64  `json:"input"`
	Output        float64  `json:"output"`
	CacheRead     *float64 `json:"cache_read,omitempty"`
	CacheCreation *float64 `json:"cache_creation,omitempty"`
}

// ModelDescriptor mirrors the SDK ModelDescriptor (modelEvalSchemas.ts).
type ModelDescriptor struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Tiers is the list of cross-provider capability BANDS the model serves.
	// Band names reuse the canonical routing tiers (haiku/sonnet/opus/fable)
	// but are provider-neutral: a provider without a fable-equivalent maps
	// fable to its strongest model. At most one non-deprecated model per
	// (provider, band) — enforced by mustLoad.
	Tiers              []string `json:"tiers,omitempty"`
	DisplayName        string   `json:"display_name"`
	ConcreteVersion    string   `json:"concrete_version"`
	Rates              Rates    `json:"rates"`
	SupportedEfforts   []string `json:"supported_efforts"`
	SupportedReasoning []string `json:"supported_reasoning"`
	ContextWindow      int      `json:"context_window"`
	Deprecated         bool     `json:"deprecated,omitempty"`
	// Replacement is the current id callers should migrate to (deprecated models).
	Replacement string `json:"replacement,omitempty"`
	// Recommended marks the provider's default for its strongest band (UI hint).
	Recommended bool `json:"recommended,omitempty"`
	// ResearchPreview excludes the model from default catalog/UI listings.
	ResearchPreview bool `json:"research_preview,omitempty"`
}

// HasTier reports whether the model serves the given capability band.
func (m ModelDescriptor) HasTier(tier string) bool {
	for _, t := range m.Tiers {
		if t == tier {
			return true
		}
	}
	return false
}

type registryFile struct {
	Version string            `json:"version"`
	Models  []ModelDescriptor `json:"models"`
}

var registry = mustLoad()

func mustLoad() []ModelDescriptor {
	var rf registryFile
	if err := json.Unmarshal(registryJSON, &rf); err != nil {
		panic(fmt.Sprintf("model registry: invalid embedded JSON: %v", err))
	}
	seen := make(map[string]bool, len(rf.Models))
	bands := make(map[string]bool)
	for _, m := range rf.Models {
		if seen[m.ID] {
			panic(fmt.Sprintf("model registry: duplicate model id %q", m.ID))
		}
		seen[m.ID] = true
		// Tier-band resolution must be deterministic: at most one
		// non-deprecated model may serve a given (provider, band) pair.
		if m.Deprecated {
			continue
		}
		for _, tier := range m.Tiers {
			key := m.Provider + "/" + tier
			if bands[key] {
				panic(fmt.Sprintf("model registry: duplicate non-deprecated band %q (%s)", key, m.ID))
			}
			bands[key] = true
		}
	}
	return rf.Models
}

// All returns every model in the registry (including deprecated ones, kept for
// historical cost replay).
func All() []ModelDescriptor { return registry }

// Get resolves a model by concrete id (exact, provider-agnostic — ids are
// globally unique), then by tier alias (haiku/sonnet/opus/fable) → the
// current non-deprecated ANTHROPIC model of that tier. Anthropic is the
// default provider because bare tier names are the pipeline's canonical
// routing currency; use Resolve for another provider's band.
func Get(idOrTier string) (ModelDescriptor, bool) {
	return Resolve("anthropic", idOrTier)
}

// Resolve resolves a model for a provider: by concrete id (exact,
// provider-agnostic), then by tier band within the provider → the current
// non-deprecated model serving that band (#56). Local providers
// (ollama/lm-studio) have no registry entries by design, so every tier
// lookup against them misses — callers fall back to the configured local
// model and unknown-model $0 costing.
func Resolve(provider, idOrTier string) (ModelDescriptor, bool) {
	for _, m := range registry {
		if m.ID == idOrTier {
			return m, true
		}
	}
	for _, m := range registry {
		if m.Provider == provider && !m.Deprecated && m.HasTier(idOrTier) {
			return m, true
		}
	}
	return ModelDescriptor{}, false
}

// ProviderForAdapter maps an execution adapter name (claude, claude-sdk,
// claude-headless, codex, gemini, gemini-sdk, copilot, ollama, lm-studio) to
// its registry provider. Unknown adapters map to "other", which has no tier
// bands. Mirrors providerForAdapter in the SDK modelRegistry.ts.
func ProviderForAdapter(adapter string) string {
	switch {
	case adapter == "claude" || strings.HasPrefix(adapter, "claude-"):
		return "anthropic"
	case adapter == "codex":
		return "openai"
	case adapter == "gemini" || adapter == "gemini-sdk":
		return "google"
	case adapter == "copilot", adapter == "ollama", adapter == "lm-studio":
		return adapter
	default:
		return "other"
	}
}

// RawJSON returns the embedded canonical registry bytes (used by parity tests).
func RawJSON() []byte { return registryJSON }
