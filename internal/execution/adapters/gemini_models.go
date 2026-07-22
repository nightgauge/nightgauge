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
	switch {
	case strings.HasPrefix(m, "claude-haiku"):
		tier = "haiku"
	case strings.HasPrefix(m, "claude-sonnet"):
		tier = "sonnet"
	case strings.HasPrefix(m, "claude-opus"), strings.HasPrefix(m, "claude-fable"):
		tier = "opus"
	}
	if resolved, ok := models.Resolve("google", tier); ok && resolved.Provider == "google" && !resolved.Deprecated {
		return resolved.ID
	}
	return m
}

// knownGeminiModels returns the CLOSED set of Gemini model ids the pipeline
// supports: the registry's non-deprecated `provider: "google"` entries.
func knownGeminiModels() map[string]bool {
	known := make(map[string]bool)
	for _, m := range models.All() {
		if m.Provider == "google" && !m.Deprecated {
			known[m.ID] = true
		}
	}
	return known
}

// ValidateGeminiModel fails fast when the configured model does not resolve to
// a known Gemini model id — the registry-backed generalization of the codex
// preflight (#4021, #57) for the Go `nightgauge run --adapter gemini[-sdk]`
// paths. An empty model is allowed (BuildCommand omits --model and the CLI
// uses its own default). Tier aliases and Claude ids resolve first, so they
// validate as their concrete Gemini model.
func ValidateGeminiModel(model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	resolved := resolveGeminiModel(trimmed)
	known := knownGeminiModels()
	if !known[resolved] {
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
			"model %q is not valid for the gemini adapter%s; valid models: %s, or a tier (haiku|sonnet|opus|fable)",
			trimmed, note, strings.Join(valid, ", "),
		)
	}
	return nil
}

// ValidateModel implements the optional model-validation interface the
// execution manager checks before BuildCommand (#4021).
func (a *GeminiAdapter) ValidateModel(model string) error {
	return ValidateGeminiModel(model)
}

// ValidateModel implements the optional model-validation interface the
// execution manager checks before BuildCommand (#4021).
func (a *GeminiSdkAdapter) ValidateModel(model string) error {
	return ValidateGeminiModel(model)
}
