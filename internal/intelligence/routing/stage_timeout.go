package routing

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/models"
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
// and the concrete id ("claude-fable-5-1") resolve. Unknown models return 1.0 —
// the historical ceiling, unchanged.
//
// #582 keep-with-reason (band-retirement sweep, PR #607): these substring
// matches survive deliberately. The input is the DISPATCH model — on the Go
// side that vocabulary is band aliases plus claude-* ids (adapters translate
// to non-Anthropic ids downstream, at the last mile), so the family match
// covers what this path actually sees; anything else falls to the 1.0
// historical ceiling, a conservative fail-open (a tighter deadline, never a
// wrong one). `models.ClaudeIDTier` cannot replace it: that classifier
// collapses fable onto the opus band, which would erase exactly the 2.0-vs-1.5
// distinction this table exists for. Not a closed-set enumeration, so it is
// structurally invisible to scripts/check-band-vocabulary.py — accounted for
// here and in the PR #607 keep list instead.
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

// openCodeAdapterName is the adapter id (execution/adapters/registry.go)
// ResolveStageTimeout keys the local-provider factor on.
const openCodeAdapterName = "opencode"

// openCodeLocalTimeoutFactor replaces stageTimeoutModelScale's family match
// when adapter is opencode and model resolves to a local provider (lm-studio,
// ollama): research on a local Qwen3.8 27B saw a 76s cold prefill and ~8
// tok/s decode (#1646) — timing nothing in the Claude-tier scale accounts
// for, and a stage this slow is healthy, not stalled. Deliberately generous,
// like the base ceilings themselves, and bounded by openCodeLocalTimeoutCap so
// a genuinely wedged local run is still killed.
const openCodeLocalTimeoutFactor = 3.0

// openCodeLocalTimeoutCap is the absolute ceiling an OpenCode local-provider
// stage's timeout reaches, whatever the base or factor (#1646). A future
// #1652 wall-clock budget, once its accessor exists, further tightens this
// per run when configured below it — not wired here; #1652 depends on this
// issue's row existing first (see stage_timeout_test.go).
const openCodeLocalTimeoutCap = 4 * time.Hour

// ResolveStageTimeout returns the last-resort context deadline for a stage
// given the resolved adapter and model. The result is stage-, adapter- and
// model-aware so a frontier-mode Fable `feature-dev` run (100 min × 2.0 = 200
// min) is never killed by a deadline tuned for Opus-era Sonnet runtimes, a
// mechanical Haiku `pr-create` keeps a tight 45-minute bound, and an OpenCode
// stage on a local model server gets openCodeLocalTimeoutFactor's wider,
// capped head-room instead of the Claude-tier family scale, which a local
// model id never matches anyway.
//
// An operator can override any stage's ceiling without recompiling via
// `NIGHTGAUGE_STAGE_TIMEOUT_<STAGE>` (minutes, hyphens → underscores, e.g.
// `NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV=240`). The override is taken verbatim —
// it is not model- or adapter-scaled — so it acts as an explicit absolute
// ceiling, and it still wins over the local cap.
func ResolveStageTimeout(stage, adapter, model string) time.Duration {
	if override, ok := stageTimeoutEnvOverride(stage); ok {
		return override
	}
	base, ok := stageTimeoutBase[stage]
	if !ok {
		base = defaultStageTimeout
	}
	if adapter == openCodeAdapterName {
		if provider, _, _ := models.ParseOpenCodeModel(model); models.IsLocalProvider(provider) {
			d := time.Duration(float64(base) * openCodeLocalTimeoutFactor)
			if d > openCodeLocalTimeoutCap {
				d = openCodeLocalTimeoutCap
			}
			return d
		}
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
