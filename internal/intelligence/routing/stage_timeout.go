package routing

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Stage execution time budgets (#73).
//
// The scheduler wraps each CLI-path stage in a `context.WithTimeout`. Before
// #73 that deadline was an unconditional `30 * time.Minute` literal applied to
// every stage regardless of model or mode. That value silently pre-empted the
// long-horizon work the `frontier` mode exists to enable: a `feature-dev` run
// on Fable 5 was killed at 30 minutes even though the TypeScript SkillRunner's
// own progress-gated hard cap for that stage is 90 minutes, so the Go context
// deadline always fired first and the Fable tokens spent up to that point were
// wasted. Anthropic's Fable 5 guidance opens by warning that requests "can run
// for many minutes at higher effort settings" and that client timeouts must be
// raised BEFORE migrating.
//
// These budgets are deliberately GENEROUS last-resort ceilings — a "the
// subprocess is truly wedged" backstop, not a "this is taking a while" limit.
// A healthy run finishes well within them. The real progress-gated governance
// (stall detection, progress-gated hard caps) lives in the TypeScript
// SkillRunner and drives the IPC/VSCode path; the CLI/headless path has only
// this ceiling, so it must be high enough never to truncate a legitimate long
// run yet still release a genuinely hung process. Every base is set at or above
// the corresponding TS hard cap so the Go deadline can never pre-empt it.
//
// Base values are the sonnet/haiku (multiplier 1.0) ceilings; the model scale
// widens them for the pricier, longer-horizon reasoning tiers.
var stageTimeoutBase = map[string]time.Duration{
	string(stageIssuePickup):     20 * time.Minute,
	string(stageFeaturePlanning): 45 * time.Minute,
	string(stageFeatureDev):      100 * time.Minute, // > TS 90-min progress-gated hard cap
	string(stageFeatureValidate): 45 * time.Minute,
	string(stagePrCreate):        45 * time.Minute,
	string(stagePrMerge):         45 * time.Minute,
}

// Stage name string constants, duplicated here rather than importing
// internal/state to keep the routing package free of that dependency (state
// already imports routing transitively in places). These MUST match
// state.Stage* string values; the parity is asserted in stage_timeout_test.go.
const (
	stageIssuePickup     = "issue-pickup"
	stageFeaturePlanning = "feature-planning"
	stageFeatureDev      = "feature-dev"
	stageFeatureValidate = "feature-validate"
	stagePrCreate        = "pr-create"
	stagePrMerge         = "pr-merge"
)

// defaultStageTimeout is the ceiling for any stage not in stageTimeoutBase.
// It preserves the historical 30-minute value for unknown/short stages so this
// change is a strict relaxation for the long stages and a no-op elsewhere.
const defaultStageTimeout = 30 * time.Minute

// stageTimeoutModelScale returns the multiplier applied to a stage's base
// ceiling for the resolved model tier. The pricier, longer-horizon reasoning
// tiers get proportionally more head-room so the deadline never truncates the
// long runs they exist to enable. Mirrors the philosophy of the cost-cap model
// scale (monitoringResolver.ts COST_CAP_MODEL_SCALE), where Fable already gets
// ~2× the Opus head-room because it is the premium frontier tier.
//
// Matched on the tier family anywhere in the string so both the alias ("fable")
// and the concrete id ("claude-fable-5") resolve. Unknown models return 1.0 —
// the historical ceiling, unchanged.
func stageTimeoutModelScale(model string) float64 {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "fable"):
		return 2.0
	case strings.Contains(m, "opus"):
		return 1.5
	default:
		return 1.0
	}
}

// ResolveStageTimeout returns the last-resort context deadline for a stage
// given the resolved model. The result is stage-aware and model-aware so a
// frontier-mode Fable `feature-dev` run (100 min × 2.0 = 200 min) is never
// killed by a deadline tuned for Opus-era Sonnet runtimes, while a mechanical
// Haiku `pr-create` keeps a tight 45-minute bound.
//
// An operator can override any stage's ceiling without recompiling via
// `NIGHTGAUGE_STAGE_TIMEOUT_<STAGE>` (minutes, hyphens → underscores, e.g.
// `NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV=240`). The override is taken verbatim —
// it is not model-scaled — so it acts as an explicit absolute ceiling.
func ResolveStageTimeout(stage, model string) time.Duration {
	if override, ok := stageTimeoutEnvOverride(stage); ok {
		return override
	}
	base, ok := stageTimeoutBase[stage]
	if !ok {
		base = defaultStageTimeout
	}
	return time.Duration(float64(base) * stageTimeoutModelScale(model))
}

// stageTimeoutEnvOverride reads NIGHTGAUGE_STAGE_TIMEOUT_<STAGE> (in minutes)
// and returns the parsed duration when present and valid (> 0).
func stageTimeoutEnvOverride(stage string) (time.Duration, bool) {
	key := "NIGHTGAUGE_STAGE_TIMEOUT_" + strings.ToUpper(strings.ReplaceAll(stage, "-", "_"))
	raw := os.Getenv(key)
	if raw == "" {
		return 0, false
	}
	mins, err := strconv.Atoi(raw)
	if err != nil || mins <= 0 {
		return 0, false
	}
	return time.Duration(mins) * time.Minute, true
}
