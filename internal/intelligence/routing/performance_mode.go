package routing

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/models"
)

// PerformanceMode represents the three named pipeline performance modes.
type PerformanceMode string

const (
	ModeEfficiency PerformanceMode = "efficiency"
	ModeElevated   PerformanceMode = "elevated"
	ModeMaximum    PerformanceMode = "maximum"
	// ModeFrontier is the premium opt-in tier above Maximum. It widens the
	// routing ceiling to Fable 5 — the frontier model at ~2× Opus cost — but
	// pins nothing: the router reaches Fable only on a heavy reasoning stage
	// (feature-planning, feature-dev) at top complexity, plumbing stays Haiku,
	// and feature-validate never exceeds Opus. #19 replaced the old
	// "Fable pinned on every reasoning stage" behavior, which paid frontier
	// rates for trivial work and empirically failed validation in dogfooding.
	// Fable is unreachable from every other mode, so selecting Frontier is the
	// deliberate opt-in.
	ModeFrontier PerformanceMode = "frontier"
)

// performanceModeState is the persisted YAML shape of performance-mode.yaml.
type performanceModeState struct {
	Mode string `yaml:"mode"`
}

// ResolvePerformanceMode reads the active performance mode.
//
// Precedence (matches TypeScript getPerformanceMode):
//  1. NIGHTGAUGE_PERFORMANCE_MODE env var
//  2. .nightgauge/performance-mode.yaml in workspaceRoot
//  3. ModeElevated (default — no overrides)
//
// Exported (Issue #3215) so the scheduler can capture per-stage mode at
// stage-start for the V2/V3 history record's per-stage performance_mode
// field. The router continues to call this through the package-private
// alias below to avoid disturbing existing call sites.
func ResolvePerformanceMode(workspaceRoot string) PerformanceMode {
	return resolvePerformanceMode(workspaceRoot)
}

func resolvePerformanceMode(workspaceRoot string) PerformanceMode {
	if env := strings.TrimSpace(strings.ToLower(os.Getenv("NIGHTGAUGE_PERFORMANCE_MODE"))); env != "" {
		if m := parseMode(env); m != "" {
			return m
		}
	}

	if workspaceRoot != "" {
		if m := readPerformanceModeFile(filepath.Join(workspaceRoot, ".nightgauge", "performance-mode.yaml")); m != "" {
			return m
		}
	}

	return ModeElevated
}

// DashboardPerformanceMode maps a resolved PerformanceMode to the web
// dashboard's PerformanceMode vocabulary ('efficiency' | 'elevated' |
// 'maximum'). The three named modes pass through verbatim; the premium
// 'frontier' tier has NO dashboard representation, so it (and any unrecognised
// value) maps to "" — telling the emit site to omit `mode` from the wire rather
// than send a value the dashboard can't render (it would surface a misleading
// "Unknown mode" badge). Keep this in sync with the dashboard's PerformanceMode
// type in acme-dashboard/src/app/features/pipelines/pipeline.model.ts.
func DashboardPerformanceMode(m PerformanceMode) string {
	switch m {
	case ModeEfficiency, ModeElevated, ModeMaximum:
		return string(m)
	default:
		// ModeFrontier and any unresolved/unknown value: not representable.
		return ""
	}
}

func readPerformanceModeFile(path string) PerformanceMode {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return ""
	}
	var s performanceModeState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return ""
	}
	return parseMode(s.Mode)
}

func parseMode(s string) PerformanceMode {
	switch PerformanceMode(s) {
	case ModeEfficiency, ModeElevated, ModeMaximum, ModeFrontier:
		return PerformanceMode(s)
	}
	return ""
}

// Registry tier BANDS, and their capability order strongest → weakest. This is
// the Go side's one tier ladder: internal/orchestrator's downgradeLadder and
// NormalizeModelTier are defined from it, and it mirrors
// TIER_BANDS_STRONGEST_FIRST in packages/nightgauge-vscode/src/utils/skillRunner.ts.
// The NAME spellings derive from models.Band* (#582) — the ORDER declared
// below remains this file's authority.
const (
	TierHaiku  = models.BandHaiku
	TierSonnet = models.BandSonnet
	TierOpus   = models.BandOpus
	TierFable  = models.BandFable
)

// TierBandsStrongestFirst is the capability ordering of the registry bands.
// Strongest first, because that is the order the model-unavailable downgrade
// ladder walks (#42).
//
// This is the ONE Go band-order declaration (#581): the selection query
// (selection.go) derives ladder MEMBERSHIP from the registry, but the
// relative order of the bands has no registry data field in this phase and
// no measured capability evidence exists — so the order is declared, here,
// exactly once. TS pair: TIER_BANDS in
// packages/nightgauge-sdk/src/eval/tierBands.ts (ascending; the ladder
// parity tests pin the two).
var TierBandsStrongestFirst = []string{TierFable, TierOpus, TierSonnet, TierHaiku}

// TierBand maps a model reference — a band name ("opus"), a concrete Anthropic
// id ("claude-opus-5") or another provider's id ("gpt-5.6-sol") — onto its
// STRONGEST registry band. Returns "" for models the registry does not know:
// user-defined local models (#56) have no band, and inventing one would reroute
// (or misattribute) an explicit choice.
func TierBand(model string) string {
	for _, tier := range TierBandsStrongestFirst {
		if model == tier {
			return tier
		}
	}
	if desc, ok := models.Get(model); ok {
		for _, tier := range TierBandsStrongestFirst {
			if desc.HasTier(tier) {
				return tier
			}
		}
	}
	return ""
}

// tierRank orders a band by capability, weakest = 0. -1 for a non-band.
func tierRank(band string) int {
	for i, t := range TierBandsStrongestFirst {
		if t == band {
			return len(TierBandsStrongestFirst) - 1 - i
		}
	}
	return -1
}

// TierRank orders any model reference — band, Anthropic id, or another
// provider's id — by capability: haiku=0, sonnet=1, opus=2, fable=3. Returns -1
// for a model the registry has no band for, which is how callers spell "never
// compare this one" (a user-defined local model is never floored or clamped).
func TierRank(model string) int {
	return tierRank(TierBand(model))
}

// ModeEnvelope is the `[Floor, Ceiling]` band a performance mode routes
// within, plus the mode's EFFORT bounds and THINKING policy (spike #568
// §4.1.3: envelopes are `(rung floor, rung ceiling, effort ceiling, thinking
// policy)`). Mirrors ModeEnvelope in
// packages/nightgauge-vscode/src/utils/modeProfiles.ts, INCLUDING the effort
// fields that table already declared (`effortCeiling` on efficiency,
// `effortFloor` on maximum) — before #581 the Go mirror silently dropped
// them, so the wire-effort resolution (dispatch_envelope.go) had no clamp to
// apply. The thinking-policy axis landed with #606 (the #581 re-scope): the
// axis exists and is consumed (resolveWireThinking), while no mode DECLARES
// a value yet — deliberately, so adding the axis is behavior-preserving; the
// Go⇄TS parity tests pin both tables' thinking columns together.
type ModeEnvelope struct {
	Floor   string
	Ceiling string
	// EffortFloor raises a resolved effort (Maximum reasons hard everywhere);
	// "" = no floor. Mirrors ModeEnvelope.effortFloor.
	EffortFloor string
	// EffortCeiling caps a resolved effort (Efficiency trades reasoning for
	// cost); "" = no ceiling. Mirrors ModeEnvelope.effortCeiling.
	EffortCeiling string
	// ThinkingPolicy pins the mode's thinking state ("on"/"off"), overriding
	// the dispatched rung's declared thinking default; "" = no policy — the
	// rung's declared default rules. The CLAUDE_CODE_DISABLE_THINKING
	// interlock (#76) still wins over a policy: an operator escape hatch
	// outranks a mode table. Mirrors ModeEnvelope.thinkingPolicy.
	ThinkingPolicy string
}

// modeProfile mirrors one MODE_PROFILES entry: the per-stage PINS the mode
// applies (model AND effort — a pin is an envelope point, not a bare model),
// and the envelope every router-chosen tier is clamped into. A pin
// short-circuits routing; an envelope bounds it.
type modeProfile struct {
	stages map[string]string
	// stageEfforts is the effort half of the per-stage pins — set only where
	// stages pins a model too, mirroring the TS `{ model, effort }` stage
	// profile objects. resolveStageEffort's pin branch requires BOTH
	// (`profile?.model && profile.effort`); ModeStageEffort mirrors that.
	stageEfforts map[string]string
	envelope     ModeEnvelope
}

// modeProfiles is the ONE performance-mode table on the Go side, and a mirror
// of MODE_PROFILES (packages/nightgauge-vscode/src/utils/modeProfiles.ts) —
// including the part that is easy to get wrong: **only `maximum` pins.**
//
// #19 converted `efficiency` and `frontier` from per-stage pins to `[floor,
// ceiling]` ENVELOPES the router selects within, because pinned-Fable "paid
// frontier rates for trivial work and empirically failed validation in
// dogfooding" (modeProfiles.ts). `MODE_PROFILES.efficiency.stages` and
// `.frontier.stages` are both `{}` today; a Go table that still pinned them
// dispatched Fable on every reasoning stage of every Frontier run — including
// feature-validate, which the TS profile caps at Opus — and short-circuited
// every operator knob below it.
//
// Both callers read this table: routeLocal (the recommendation at pickup) and
// stageBaseModel (internal/orchestrator/dispatch_routing.go, per stage at
// dispatch). Two copies of a mode profile is exactly how the extension and the
// scheduler drifted apart.
var modeProfiles = map[PerformanceMode]modeProfile{
	// Router-driven within [haiku, sonnet]: no stage ever reaches Opus.
	// Effort capped at medium (MODE_PROFILES.efficiency.envelope.effortCeiling)
	// to keep reasoning cost down.
	ModeEfficiency: {envelope: ModeEnvelope{Floor: TierHaiku, Ceiling: TierSonnet, EffortCeiling: "medium"}},
	// The open envelope — exactly today's routing.
	ModeElevated: {envelope: ModeEnvelope{Floor: TierHaiku, Ceiling: TierOpus}},
	// Deliberate pins: "cost no object" genuinely means pin high on every
	// stage, plumbing included. Same six stages MODE_PROFILES.maximum names,
	// each pinned at `{ model: opus, effort: high }` — the pin is an envelope
	// point, and dropping its effort half is how the IPC path used to lose
	// `--effort high` on every Maximum stage.
	ModeMaximum: {
		stages: map[string]string{
			"issue-pickup":     TierOpus,
			"feature-planning": TierOpus,
			"feature-dev":      TierOpus,
			"feature-validate": TierOpus,
			"pr-create":        TierOpus,
			"pr-merge":         TierOpus,
		},
		stageEfforts: map[string]string{
			"issue-pickup":     "high",
			"feature-planning": "high",
			"feature-dev":      "high",
			"feature-validate": "high",
			"pr-create":        "high",
			"pr-merge":         "high",
		},
		envelope: ModeEnvelope{Floor: TierOpus, Ceiling: TierOpus, EffortFloor: "high"},
	},
	// Router-driven within [haiku, fable]. Fable is reached ONLY on heavy
	// reasoning stages at top complexity (frontierReasoningStage below);
	// plumbing stays Haiku and feature-validate never exceeds Opus.
	ModeFrontier: {envelope: ModeEnvelope{Floor: TierHaiku, Ceiling: TierFable}},
}

// ModeStagePin returns the model the given performance mode PINS for a stage,
// or "" when the mode pins nothing there — which is every stage of every mode
// except `maximum`. A pin is returned BEFORE the routing chain runs and is not
// clamped: it is the mode's own answer.
func ModeStagePin(mode PerformanceMode, stage string) string {
	return modeProfiles[mode].stages[stage]
}

// ModeStageEffort returns the EFFORT a performance mode pins alongside its
// model pin for a stage, or "" when the mode pins nothing there. Mirrors the
// pin branch of resolveStageEffort (skillRunner.ts): the TS chain returns
// `profile.effort` only when the profile pins a model too, so a mode with an
// effort-less pin (none exists today) would answer "" here as well.
func ModeStageEffort(mode PerformanceMode, stage string) string {
	p := modeProfiles[mode]
	if p.stages[stage] == "" {
		return ""
	}
	return p.stageEfforts[stage]
}

// effortRank orders an effort level by reasoning depth, low = 0. -1 for a
// value that is not an EFFORT_LEVELS member — the unclamped pass-through.
func effortRank(effort string) int {
	for i, e := range models.EffortOrder {
		if e == effort {
			return i
		}
	}
	return -1
}

// ClampEffortToEnvelope clamps a resolved effort into the mode envelope's
// `[EffortFloor, EffortCeiling]` (#19). Mirrors clampEffortToEnvelope
// (skillRunner.ts) exactly:
//
//   - "" (no effort resolved) returns the envelope's floor — only a floor can
//     introduce an effort (Maximum with a per-stage model env override still
//     reasons at `high`);
//   - a value off the EFFORT_LEVELS ladder passes through unclamped — it is
//     not an effort level at all, and inventing a rank for it would silently
//     exempt or rewrite an unchecked input;
//   - otherwise the index is raised to the floor and lowered to the ceiling
//     on models.EffortOrder, the one effort ladder (#394/#578).
func ClampEffortToEnvelope(effort string, env ModeEnvelope) string {
	if effort == "" {
		return env.EffortFloor
	}
	i := effortRank(effort)
	if i < 0 {
		return effort
	}
	if f := effortRank(env.EffortFloor); f >= 0 && i < f {
		i = f
	}
	if c := effortRank(env.EffortCeiling); c >= 0 && i > c {
		i = c
	}
	return models.EffortOrder[i]
}

// IsEffortLevel reports membership in the EFFORT_LEVELS vocabulary
// (models.EffortOrder, pinned to the SDK authority) — the guard operator
// effort input passes before it can reach the wire.
func IsEffortLevel(effort string) bool {
	return effortRank(effort) >= 0
}

// Envelope returns the mode's `[floor, ceiling]` band — the bounds every model
// the PIPELINE chose must land inside. Mirrors getModeEnvelope (modeProfiles.ts);
// an unrecognized mode gets the elevated band, the TS DEFAULT_MODE_ENVELOPE.
func Envelope(mode PerformanceMode) ModeEnvelope {
	if p, ok := modeProfiles[mode]; ok {
		return p.envelope
	}
	return modeProfiles[ModeElevated].envelope
}

// RoutedTierEnvelope is Envelope narrowed to the band a ROUTER-chosen tier may
// land in for this stage.
//
// One rule differs from the raw mode band, and it is the frontier-reasoning
// rule AutoModelSelector applies per stage (AutoModelSelector.selectModel,
// "Frontier reasoning escalation"): a `fable` ceiling is reachable only on the
// heavy generative reasoning stages. feature-validate is deliberately EXCLUDED
// — Fable's extended reasoning is counterproductive for test orchestration and
// empirically failed validation on small tasks in dogfooding.
//
// The narrowing has to exist on the Go side because the Go dispatch path
// applies ONE routed tier (pickup_recommendation.dev_model, a feature-dev
// recommendation) to every stage, where the TS resolver re-runs its per-stage
// selector. Without it a Frontier run that escalated feature-dev to Fable would
// carry Fable onto feature-validate, which no TS path can produce.
func RoutedTierEnvelope(mode PerformanceMode, stage string) ModeEnvelope {
	env := Envelope(mode)
	if env.Ceiling == TierFable && !frontierReasoningStage(stage) {
		env.Ceiling = TierOpus
	}
	return env
}

// frontierReasoningStage reports whether a stage is heavy generative reasoning
// — the only kind a fable ceiling escalates. Mirrors AutoModelSelector's
// `stageCategory === "planning" || stageCategory === "dev"`.
func frontierReasoningStage(stage string) bool {
	return stage == "feature-planning" || stage == "feature-dev"
}

// frontierReasoningComplexity is the complexity score at which a fable ceiling
// escalates a reasoning stage to Fable — the top band of the local heuristic
// (selectModel already picks Opus there), and the Go counterpart of
// AutoModelSelector's `complexity === "L" || complexity === "XL"`.
const frontierReasoningComplexity = 7

// ClampToEnvelope returns model clamped into the envelope's `[Floor, Ceiling]`.
//
// A model the registry has no band for (a user-defined local model, #56) is
// returned untouched — there is nothing to compare it against, and picking a
// band for it would reroute an explicit choice. A model already inside the band
// is returned VERBATIM, so a caller that speaks concrete ids keeps its own
// vocabulary; only an actual clamp returns a band name.
func ClampToEnvelope(model string, env ModeEnvelope) string {
	band := TierBand(model)
	if band == "" {
		return model
	}
	rank := tierRank(band)
	if floor := tierRank(env.Floor); floor >= 0 && rank < floor {
		return env.Floor
	}
	if ceiling := tierRank(env.Ceiling); ceiling >= 0 && rank > ceiling {
		return env.Ceiling
	}
	return model
}

// ClampToCeiling lowers model to the envelope's ceiling and never raises it.
//
// The floor half is deliberately absent: the ceiling is applied after the
// dispatch path's raising mechanisms (escalation, the minimum_model floor),
// where re-applying a floor could force a run back onto a tier the API just
// rejected — the exact invariant the sticky #42 downgrade exists to hold.
func ClampToCeiling(model string, env ModeEnvelope) string {
	band := TierBand(model)
	if band == "" {
		return model
	}
	if ceiling := tierRank(env.Ceiling); ceiling >= 0 && tierRank(band) > ceiling {
		return env.Ceiling
	}
	return model
}
