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
//
// Cache CREATION has two rates, not one (#358). Anthropic bills a cache write
// by the TTL it buys: 5-minute at 1.25x base input, 1-hour at 2.0x. The CLI
// reports which pool a write landed in via
// usage.cache_creation.{ephemeral_5m,ephemeral_1h}_input_tokens, and a single
// blended rate mis-prices every run — a 1h-heavy stage by 1.6x on the cache
// pool alone. A nil pointer means the provider does not bill that pool.
type Rates struct {
	Input     float64  `json:"input"`
	Output    float64  `json:"output"`
	CacheRead *float64 `json:"cache_read,omitempty"`
	// CacheCreation5m prices writes with a 5-minute TTL (1.25x base input).
	CacheCreation5m *float64 `json:"cache_creation_5m,omitempty"`
	// CacheCreation1h prices writes with a 1-hour TTL (2.0x base input).
	CacheCreation1h *float64 `json:"cache_creation_1h,omitempty"`
}

// Transport names — the closed set of transports a registry model can be
// reached through, matching the doctor's adapter kinds with sdk folded into
// api. Mirrors TRANSPORTS in the SDK (modelEvalSchemas.ts, #578).
const (
	TransportCLI = "cli"
	TransportAPI = "api"
)

// Rate provenance values — where a rate card's figures came from. Mirrors
// RATE_PROVENANCES in the SDK (modelEvalSchemas.ts, #578).
const (
	// RateProvenanceMeasured: a controlled live billing measurement on the
	// transport in use (e.g. the #531/#570 Grok Build CLI measurement).
	RateProvenanceMeasured = "measured"
	// RateProvenanceList: transcribed from the vendor's published price sheet.
	RateProvenanceList = "list"
	// RateProvenanceSubscription: flat-rate traffic where 0 is a design
	// decision, not a price (copilot).
	RateProvenanceSubscription = "subscription"
	// RateProvenancePlaceholder: a recorded figure that is not a vendor price
	// at all (never-listed research previews, provider-neutral fixtures).
	RateProvenancePlaceholder = "placeholder"
)

// TransportFacts mirrors the SDK TransportFactsSchema (#578): per-transport
// reachability plus an optional live-check citation and an optional
// transport-specific rate card.
//
// Served=false is a positive fact — "exists at the provider, unreachable
// through this transport" (the #532 class). An entirely ABSENT transport key
// on a model is the unexpressed (pending) state and must never be read as
// either served or unserved.
type TransportFacts struct {
	Served bool `json:"served"`
	// Verified is the YYYY-MM-DD date of the last live catalog/billing check;
	// empty means the fact is declared, not verified.
	Verified string `json:"verified,omitempty"`
	// Evidence cites the live check (e.g. "grok models catalog listing, grok
	// CLI 1.0.4").
	Evidence string `json:"evidence,omitempty"`
	// Rates is the transport-specific rate card; nil inherits the model's
	// top-level card (the two-rate-card reality #570 documents).
	Rates *Rates `json:"rates,omitempty"`
	// RateProvenance is REQUIRED whenever Rates is present — mustLoad asserts
	// it, like band uniqueness.
	RateProvenance string `json:"rate_provenance,omitempty"`
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
	Tiers           []string `json:"tiers,omitempty"`
	DisplayName     string   `json:"display_name"`
	ConcreteVersion string   `json:"concrete_version"`
	Rates           Rates    `json:"rates"`
	// RateProvenance states where the top-level (default) rate card's figures
	// came from (#578): measured | list | subscription | placeholder.
	RateProvenance string `json:"rate_provenance,omitempty"`
	// Transports maps a transport (TransportCLI | TransportAPI) to its
	// reachability facts (#578). A missing key is an UNEXPRESSED (pending)
	// fact, never an implicit served=false. Nothing enforces these at
	// selection yet — that is the fail-closed enforcement phase (#579).
	Transports map[string]TransportFacts `json:"transports,omitempty"`
	// SupportedEfforts: an empty slice is a positive declaration ("this model
	// has no effort axis", e.g. Haiku), not missing data — the canonical schema
	// requires the field, so unknown is descriptor-absence, never `[]` (#336).
	SupportedEfforts []string `json:"supported_efforts"`
	ContextWindow    int      `json:"context_window"`
	Deprecated       bool     `json:"deprecated,omitempty"`
	// Replacement is the current id callers should migrate to (deprecated models).
	Replacement string `json:"replacement,omitempty"`
	// Recommended marks the provider's default for its strongest band (UI hint).
	Recommended bool `json:"recommended,omitempty"`
	// ResearchPreview excludes the model from default catalog/UI listings.
	ResearchPreview bool `json:"research_preview,omitempty"`
	// Behavior carries factual runtime properties of the model. Optional:
	// models without it (and every local model, which has no entry at all)
	// behave exactly as before.
	Behavior *Behavior `json:"behavior,omitempty"`
}

// ThinkingDisableNever is the ThinkingDisableMaxEffort value meaning the model
// rejects disabled thinking at EVERY effort, so no effort can be named as the
// ceiling. Empty already means the opposite (unconstrained), which is why this
// needs its own value rather than an absent field: Fable 5 returns a 400 for
// disabled thinking at any effort, and describing that by omission would tell
// the interlock the pairing is always legal.
const ThinkingDisableNever = "never"

// Propensity levels. Coarse on purpose (#77) — these are revisable claims from
// vendor documentation, and a numeric score would imply precision the evidence
// does not support. PropensityNormal is also what an undeclared axis reads as.
const (
	PropensityLow    = "low"
	PropensityNormal = "normal"
	PropensityHigh   = "high"
)

// Propensity mirrors the SDK Propensity: how readily the model does a thing
// unbidden. An empty axis means undeclared, which reads as PropensityNormal.
type Propensity struct {
	// Verification is how readily the model checks its own work unasked.
	Verification string `json:"verification,omitempty"`
	// Delegation is how readily the model hands work to subagents.
	Delegation string `json:"delegation,omitempty"`
	// Narration is how much the model narrates progress between tool calls.
	Narration string `json:"narration,omitempty"`
}

// Behavior holds factual, vendor-documented runtime properties — never prose
// and never judgment. Anything here must be checkable against the provider's
// own documentation.
type Behavior struct {
	// ThinkingDefault is "on" or "off": whether the model reasons by default
	// with no thinking parameter set. Opus 5 flipped this to "on".
	ThinkingDefault string `json:"thinking_default,omitempty"`
	// ThinkingDisableMaxEffort is the highest effort level at which thinking
	// may be disabled, or ThinkingDisableNever. Empty means unconstrained (the
	// pre-Opus-5 behavior, where the two settings were independent). On Opus 5
	// this is "high": disabling thinking at xhigh or max is a 400 from the API.
	ThinkingDisableMaxEffort string `json:"thinking_disable_max_effort,omitempty"`
	// EffortDefault is the provider's default effort when none is requested.
	EffortDefault string `json:"effort_default,omitempty"`
	// MaxOutputTokens bounds thinking AND response text together, so a stage
	// running at high effort needs headroom here or it truncates.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Propensity carries the coarse dispositions skill overlays act on.
	Propensity *Propensity `json:"propensity,omitempty"`
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
	Version string `json:"version"`
	// EffortLevels is the effort ladder as DATA (#578) — must equal
	// EffortOrder exactly; mustLoad asserts it.
	EffortLevels []string          `json:"effort_levels"`
	Models       []ModelDescriptor `json:"models"`
}

var registry = mustLoad()

func mustLoad() []ModelDescriptor {
	var rf registryFile
	if err := json.Unmarshal(registryJSON, &rf); err != nil {
		panic(fmt.Sprintf("model registry: invalid embedded JSON: %v", err))
	}
	if err := validateEffortLevels(rf.EffortLevels); err != nil {
		panic(fmt.Sprintf("model registry: %v", err))
	}
	if err := validateAxisFields(rf.Models); err != nil {
		panic(fmt.Sprintf("model registry: %v", err))
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

// validateEffortLevels is the loader-level assert (#578) tying the data
// authority to the compiled ladder: the registry's effort_levels declaration
// must equal EffortOrder exactly — order as much as membership, because the
// ladder is ascending reasoning depth and effortIndex compares positions in
// it. Mirrors assertEffortLevelsMatchAuthority in the SDK modelRegistry.ts.
func validateEffortLevels(levels []string) error {
	if len(levels) != len(EffortOrder) {
		return fmt.Errorf("effort_levels %v must equal the effort ladder %v exactly", levels, EffortOrder)
	}
	for i, l := range levels {
		if l != EffortOrder[i] {
			return fmt.Errorf("effort_levels %v must equal the effort ladder %v exactly", levels, EffortOrder)
		}
	}
	return nil
}

func validRateProvenance(p string) bool {
	switch p {
	case RateProvenanceMeasured, RateProvenanceList, RateProvenanceSubscription, RateProvenancePlaceholder:
		return true
	}
	return false
}

// validateAxisFields is the loader-level assert (#578) on the axis fields the
// Zod layer enforces structurally: transports keys must come from the closed
// cli|api set, rate_provenance values from the closed provenance set, and a
// per-transport rate card without provenance is an unattributable figure —
// rate_provenance is mandatory wherever transport rates appear (mirrors
// assertTransportRatesCarryProvenance in the SDK modelRegistry.ts).
func validateAxisFields(models []ModelDescriptor) error {
	for _, m := range models {
		if m.RateProvenance != "" && !validRateProvenance(m.RateProvenance) {
			return fmt.Errorf("%s: rate_provenance %q is not one of measured|list|subscription|placeholder",
				m.ID, m.RateProvenance)
		}
		for transport, facts := range m.Transports {
			if transport != TransportCLI && transport != TransportAPI {
				return fmt.Errorf("%s: transports key %q is not in the closed set cli|api", m.ID, transport)
			}
			if facts.Rates != nil && facts.RateProvenance == "" {
				return fmt.Errorf("%s: transports.%s declares rates without rate_provenance — "+
					"every transport rate card must state where its figures came from", m.ID, transport)
			}
			if facts.RateProvenance != "" && !validRateProvenance(facts.RateProvenance) {
				return fmt.Errorf("%s: transports.%s rate_provenance %q is not one of measured|list|subscription|placeholder",
					m.ID, transport, facts.RateProvenance)
			}
		}
	}
	return nil
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
// claude-headless, codex, gemini, gemini-sdk, grok, grok-headless, copilot,
// ollama, lm-studio) to its registry provider. Unknown adapters map to
// "other", which has no tier bands. Mirrors providerForAdapter in the SDK
// modelRegistry.ts.
func ProviderForAdapter(adapter string) string {
	switch {
	case adapter == "claude" || strings.HasPrefix(adapter, "claude-"):
		return "anthropic"
	case adapter == "codex":
		return "openai"
	case adapter == "gemini" || adapter == "gemini-sdk":
		return "google"
	case adapter == "grok" || strings.HasPrefix(adapter, "grok-"):
		return "xai"
	case adapter == "copilot", adapter == "ollama", adapter == "lm-studio":
		return adapter
	default:
		return "other"
	}
}

// RawJSON returns the embedded canonical registry bytes (used by parity tests).
func RawJSON() []byte { return registryJSON }

// EffortOrder is the effort ladder in ascending reasoning depth. Mirrors
// EFFORT_LEVELS in the SDK (modelEvalSchemas.ts).
var EffortOrder = []string{"low", "medium", "high", "xhigh", "max"}

func effortIndex(effort string) int {
	for i, e := range EffortOrder {
		if e == effort {
			return i
		}
	}
	return -1
}

// ThinkingDisableConflict reports whether disabling thinking is invalid for
// this model at the given effort, and returns the highest effort at which
// disabling IS allowed.
//
// Opus 5 made these two settings interdependent: `thinking: {"type":
// "disabled"}` is accepted only at effort `high` or below, and the pairing
// returns a 400 at `xhigh`/`max`. That is a breaking change from Opus 4.8,
// where the settings were independent — so a configuration that worked for
// years starts failing the moment the opus band moves.
//
// The rule is data, not a model-name check: a future model with different
// limits needs a registry edit, not a code change. Models with no constraint
// declared (and unknown/local models, which have no entry) never conflict.
//
// ThinkingDisableNever conflicts at every effort — Fable 5 rejects disabled
// thinking outright, so there is no effort low enough to make the pairing
// legal. Callers must not render maxAllowed as "lower the effort to X"
// without checking for that value first.
func (m ModelDescriptor) ThinkingDisableConflict(effort string) (conflict bool, maxAllowed string) {
	if m.Behavior == nil || m.Behavior.ThinkingDisableMaxEffort == "" {
		return false, ""
	}
	declared := m.Behavior.ThinkingDisableMaxEffort
	if declared == ThinkingDisableNever {
		return true, ThinkingDisableNever
	}
	limit := effortIndex(declared)
	requested := effortIndex(effort)
	if limit < 0 || requested < 0 {
		return false, declared
	}
	return requested > limit, declared
}

// ─── Typed behavior accessors (#77) ──────────────────────────────────────────
//
// Consumers (skill render, the resolver interlock) read behavior through these
// rather than reaching into the registry JSON or nil-checking Behavior twice.
// Every one is total: an undeclared fact returns the neutral value, so a model
// with no behavior block — and every unknown/local model, which has no entry
// at all — behaves exactly as it did before the block existed.

// ThinkingOnByDefault reports whether the model reasons with no thinking
// parameter set. Undeclared reads as false, which is the pre-Opus-5 behavior.
func (m ModelDescriptor) ThinkingOnByDefault() bool {
	return m.Behavior != nil && m.Behavior.ThinkingDefault == "on"
}

// EffortDefault is the provider's default effort, or "" when undeclared.
func (m ModelDescriptor) EffortDefault() string {
	if m.Behavior == nil {
		return ""
	}
	return m.Behavior.EffortDefault
}

// MaxOutputTokens bounds thinking and response text together, or 0 when
// undeclared. Callers treat 0 as "no documented ceiling", not as "zero".
func (m ModelDescriptor) MaxOutputTokens() int {
	if m.Behavior == nil {
		return 0
	}
	return m.Behavior.MaxOutputTokens
}

// VerificationPropensity reports how readily the model checks its own work.
func (m ModelDescriptor) VerificationPropensity() string {
	if m.Behavior == nil || m.Behavior.Propensity == nil {
		return PropensityNormal
	}
	return propensityOrNormal(m.Behavior.Propensity.Verification)
}

// DelegationPropensity reports how readily the model uses subagents.
func (m ModelDescriptor) DelegationPropensity() string {
	if m.Behavior == nil || m.Behavior.Propensity == nil {
		return PropensityNormal
	}
	return propensityOrNormal(m.Behavior.Propensity.Delegation)
}

// NarrationPropensity reports how much the model narrates progress.
func (m ModelDescriptor) NarrationPropensity() string {
	if m.Behavior == nil || m.Behavior.Propensity == nil {
		return PropensityNormal
	}
	return propensityOrNormal(m.Behavior.Propensity.Narration)
}

func propensityOrNormal(v string) string {
	if v == "" {
		return PropensityNormal
	}
	return v
}
