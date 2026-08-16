package adapters

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// resolveGeminiModel maps Claude-style routing tiers and Claude model ids to a
// concrete Gemini model id via the embedded model registry (#56, #57) — the Go
// mirror of resolveGeminiModel in the SDK modelPreflight.ts. The scheduler
// emits tiers ("sonnet"/"opus") and escalation ids ("claude-sonnet-4-6") that
// the gemini CLI would reject or misread. Concrete gemini-* ids and unknown
// values pass through unchanged.
func resolveGeminiModel(model string) string {
	m := strings.TrimSpace(model)
	tier := m
	if band, ok := models.ClaudeIDTier(m); ok {
		tier = band
	}
	if resolved, ok := models.Resolve("google", tier); ok && resolved.Provider == "google" && !resolved.Deprecated {
		return resolved.ID
	}
	return m
}

// geminiTransport resolves the single-authority transport axis (#600) the
// given Gemini-family adapter's preflight consults ("gemini" or
// "gemini-sdk" — both currently pinned to "cli"; see the registry JSON's
// $schema_note for why gemini-sdk is NOT "api" despite its name). Both
// entries are mandatory members of adapter_transports (mustLoad asserts it),
// so a miss here is a programming error, not a runtime condition.
func geminiTransport(adapter string) string {
	t, ok := models.TransportForAdapter(adapter)
	if !ok {
		panic(fmt.Sprintf("model registry: adapter_transports has no entry for %q", adapter))
	}
	return t
}

// knownGeminiModels returns the CLOSED set of Gemini model ids the pipeline
// supports for adapter ("gemini" or "gemini-sdk"): the registry's
// non-deprecated `provider: "google"` entries that are also reachable
// through that adapter's transport (#579, #600). A model with no declared
// transport fact (unexpressed/pending) still counts as known — additive
// enforcement, #579 AC4.
func knownGeminiModels(adapter string) map[string]bool {
	return knownTransportServedModels(models.All(), "google", geminiTransport(adapter))
}

// ValidateGeminiModel fails fast when the configured model does not resolve
// to a known, transport-reachable Gemini model id for adapter ("gemini" or
// "gemini-sdk") — the registry-backed generalization of the codex preflight
// (#4021, #57), extended with the registry's transport facts (#579, #600) for
// the Go `nightgauge run --adapter gemini[-sdk]` paths. An empty model is
// allowed (BuildCommand omits --model and the CLI uses its own default). Tier
// aliases and Claude ids resolve first, so they validate as their concrete
// Gemini model.
//
// models.CheckTransportServed is consulted first so a model that IS in the
// registry but explicitly unreachable through adapter's transport fails
// closed with an error naming provider, model, and transport, distinct from
// the generic "unknown model" case handled by the closed-set fallback below.
func ValidateGeminiModel(adapter, model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	resolved := resolveGeminiModel(trimmed)
	m, ok, err := models.CheckTransportServed("google", geminiTransport(adapter), resolved)
	if err != nil {
		return err
	}
	// The provider check guards CheckTransportServed's exact-id lookup, which
	// (like Resolve) is deliberately provider-agnostic: a concrete id from a
	// DIFFERENT provider must still be rejected, matching the pre-#579
	// closed-set behavior.
	if ok && !m.Deprecated && m.Provider == "google" {
		return nil
	}

	known := knownGeminiModels(adapter)
	note := ""
	if resolved != trimmed {
		note = fmt.Sprintf(" (resolved to %q)", resolved)
	}
	valid := make([]string, 0, len(known))
	for id := range known {
		valid = append(valid, id)
	}
	sort.Strings(valid)
	return fmt.Errorf(
		"model %q is not valid for the %s adapter%s; valid models: %s, or a tier (%s)",
		trimmed, adapter, note, strings.Join(valid, ", "), models.BandAlternation(),
	)
}

// ValidateModel implements the optional model-validation interface the
// execution manager checks before BuildCommand (#4021).
func (a *GeminiAdapter) ValidateModel(model string) error {
	return ValidateGeminiModel(a.Name(), model)
}

// ValidateModel implements the optional model-validation interface the
// execution manager checks before BuildCommand (#4021).
func (a *GeminiSdkAdapter) ValidateModel(model string) error {
	return ValidateGeminiModel(a.Name(), model)
}
