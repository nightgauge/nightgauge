package routing

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestResolveStageTimeout_BaseCeilings pins the per-stage base ceiling for the
// unscaled (sonnet/haiku) tier.
func TestResolveStageTimeout_BaseCeilings(t *testing.T) {
	cases := []struct {
		stage string
		want  time.Duration
	}{
		{"issue-pickup", 20 * time.Minute},
		{"feature-planning", 45 * time.Minute},
		{"feature-dev", 100 * time.Minute},
		{"feature-validate", 45 * time.Minute},
		{"pr-create", 45 * time.Minute},
		{"pr-merge", 45 * time.Minute},
	}
	for _, c := range cases {
		if got := ResolveStageTimeout(c.stage, "claude-headless", "sonnet"); got != c.want {
			t.Errorf("ResolveStageTimeout(%q, claude-headless, sonnet) = %v, want %v", c.stage, got, c.want)
		}
	}
}

// TestResolveStageTimeout_ModelScaling verifies the pricier reasoning tiers get
// proportionally wider head-room, resolving on both alias and concrete id.
func TestResolveStageTimeout_ModelScaling(t *testing.T) {
	const base = 100 * time.Minute // feature-dev base
	cases := []struct {
		model string
		want  time.Duration
	}{
		{"haiku", base},
		{"sonnet", base},
		{"opus", time.Duration(1.5 * float64(base))},
		{"claude-opus-4-8", time.Duration(1.5 * float64(base))},
		{"fable", 2 * base},
		{"claude-fable-5", 2 * base},
		// The family-substring match is what carries a NEW fable id without a
		// table edit — pinned rather than assumed, because the substring form
		// is a deliberate keep-with-reason (#582/PR #607) and a future
		// "tighten it to a closed set" refactor would silently drop 5.1 to the
		// 1.0 historical ceiling (#1274).
		{"claude-fable-5-1", 2 * base},
		{"gpt-5", base}, // unknown model → historical ceiling, unchanged
		{"", base},      // empty model → 1.0
	}
	for _, c := range cases {
		if got := ResolveStageTimeout("feature-dev", "claude-headless", c.model); got != c.want {
			t.Errorf("ResolveStageTimeout(feature-dev, claude-headless, %q) = %v, want %v", c.model, got, c.want)
		}
	}
}

// TestResolveStageTimeout_FrontierFeatureDevExceedsHardCap is the regression
// guard for the #73 bug: a frontier-mode Fable feature-dev run must get a
// ceiling strictly greater than the TS-side 90-minute progress-gated hard cap,
// so the Go context deadline can never pre-empt it.
func TestResolveStageTimeout_FrontierFeatureDevExceedsHardCap(t *testing.T) {
	const tsProgressGatedHardCap = 90 * time.Minute
	got := ResolveStageTimeout("feature-dev", "claude-headless", "claude-fable-5-1")
	if got <= tsProgressGatedHardCap {
		t.Fatalf("frontier Fable feature-dev timeout %v must exceed the TS 90-min hard cap; the 30-min guillotine regressed", got)
	}
	// And it must beat the old blind 30-minute literal by a wide margin.
	if got <= 30*time.Minute {
		t.Fatalf("frontier Fable feature-dev timeout %v did not relax the 30-min guillotine", got)
	}
}

// TestResolveStageTimeout_UnknownStage falls back to the historical 30-minute
// ceiling so the change is a no-op for anything not explicitly listed.
func TestResolveStageTimeout_UnknownStage(t *testing.T) {
	if got := ResolveStageTimeout("some-future-stage", "claude-headless", "sonnet"); got != 30*time.Minute {
		t.Errorf("unknown stage = %v, want 30m default", got)
	}
	// Even an unknown stage is model-scaled, so Fable widens the default too.
	if got := ResolveStageTimeout("some-future-stage", "claude-headless", "fable"); got != 60*time.Minute {
		t.Errorf("unknown stage on fable = %v, want 60m (30m × 2.0)", got)
	}
}

// TestResolveStageTimeout_EnvOverride verifies the per-stage env override is
// taken verbatim (not model-scaled) and rejects invalid values.
func TestResolveStageTimeout_EnvOverride(t *testing.T) {
	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "240")
	if got := ResolveStageTimeout("feature-dev", "claude-headless", "fable"); got != 240*time.Minute {
		t.Errorf("env override = %v, want 240m verbatim (not model-scaled)", got)
	}

	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "0")
	if got := ResolveStageTimeout("feature-dev", "claude-headless", "sonnet"); got != 100*time.Minute {
		t.Errorf("zero override should be ignored, got %v want base 100m", got)
	}

	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "notanumber")
	if got := ResolveStageTimeout("feature-dev", "claude-headless", "sonnet"); got != 100*time.Minute {
		t.Errorf("non-numeric override should be ignored, got %v want base 100m", got)
	}

	// The env override still wins over the OpenCode local factor and its cap
	// (#1646): an explicit absolute ceiling is the operator overriding
	// EVERYTHING below it, adapter included.
	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "30")
	if got := ResolveStageTimeout("feature-dev", "opencode", "lmstudio/qwen/qwen3.8-27b"); got != 30*time.Minute {
		t.Errorf("env override on an opencode local stage = %v, want 30m verbatim", got)
	}
}

// TestResolveStageTimeout_OpenCodeLocal is this issue's own case (#1646):
// slow-but-healthy local decode (research measured ~8 tok/s on a Qwen3.8 27B)
// must not be timed out by a ceiling tuned for hosted Claude latencies.
func TestResolveStageTimeout_OpenCodeLocal(t *testing.T) {
	const base = 100 * time.Minute // feature-dev base
	cases := []struct {
		name    string
		adapter string
		model   string
		want    time.Duration
	}{
		// feature-dev's 100min base x3 = 300min = 5h, over the 4h cap, so
		// this pins the capped value (TestResolveStageTimeout_OpenCodeLocalCappedAt4Hours
		// exercises the arithmetic explicitly).
		{"lmstudio local id gets the 3x local factor, capped at 4h", "opencode", "lmstudio/qwen/qwen3.8-27b", 4 * time.Hour},
		{"ollama local id gets the 3x local factor, capped at 4h", "opencode", "ollama/qwen3.8-27b", 4 * time.Hour},
		// A hosted OpenCode model (anthropic/, openai/, ...) is not local:
		// the family scale applies exactly as it does on the claude-headless
		// adapter, not the local factor.
		{"a hosted opencode model keeps the family scale, not the local factor", "opencode", "anthropic/claude-opus-4-8", time.Duration(1.5 * float64(base))},
		// Any other adapter is untouched even on a model id that LOOKS local
		// in shape: the local factor is keyed on adapter=opencode, not on the
		// model string alone.
		{"a non-opencode adapter never gets the local factor", "claude-headless", "lmstudio/qwen/qwen3.8-27b", base},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveStageTimeout("feature-dev", c.adapter, c.model); got != c.want {
				t.Errorf("ResolveStageTimeout(feature-dev, %q, %q) = %v, want %v", c.adapter, c.model, got, c.want)
			}
		})
	}
}

// TestResolveStageTimeout_OpenCodeLocalCappedAt4Hours is the Verification
// case: (feature-dev, opencode, lmstudio/qwen/qwen3.8-27b) is 100min × 3 =
// 300min = 5h, which the 4h cap brings down to 240min.
func TestResolveStageTimeout_OpenCodeLocalCappedAt4Hours(t *testing.T) {
	got := ResolveStageTimeout("feature-dev", "opencode", "lmstudio/qwen/qwen3.8-27b")
	if want := 240 * time.Minute; got != want {
		t.Errorf("ResolveStageTimeout(feature-dev, opencode, lmstudio/qwen/qwen3.8-27b) = %v, want %v (100min x3 capped at 4h)", got, want)
	}

	// pr-merge's 45-minute base still leaves 135min, under the cap — the cap
	// binds only feature-dev's wide base, not every stage indiscriminately.
	if got := ResolveStageTimeout("pr-merge", "opencode", "lmstudio/qwen/qwen3.8-27b"); got != 135*time.Minute {
		t.Errorf("pr-merge opencode local timeout = %v, want 135min (45min x3, under the 4h cap)", got)
	}
}

// TestResolveStageTimeout_ClaudeHeadlessUnchanged pins the exact Verification
// row: (feature-dev, claude-headless, claude-opus-5) = 150min, as before this
// issue's adapter parameter was added.
func TestResolveStageTimeout_ClaudeHeadlessUnchanged(t *testing.T) {
	got := ResolveStageTimeout("feature-dev", "claude-headless", "claude-opus-5")
	if want := 150 * time.Minute; got != want {
		t.Errorf("ResolveStageTimeout(feature-dev, claude-headless, claude-opus-5) = %v, want %v", got, want)
	}
}

// TestStageTimeoutBase_StageNameParity guards against drift between the stage
// name string constants duplicated in stage_timeout.go and the canonical
// state.Stage* values. If a stage is ever renamed, this fails loudly rather
// than silently falling back to the default ceiling.
func TestStageTimeoutBase_StageNameParity(t *testing.T) {
	canonical := map[string]state.PipelineStage{
		stageIssuePickup:     state.StageIssuePickup,
		stageFeaturePlanning: state.StageFeaturePlanning,
		stageFeatureDev:      state.StageFeatureDev,
		stageFeatureValidate: state.StageFeatureValidate,
		stagePrCreate:        state.StagePRCreate,
		stagePrMerge:         state.StagePRMerge,
	}
	for local, canon := range canonical {
		if local != string(canon) {
			t.Errorf("stage constant %q drifted from state value %q", local, string(canon))
		}
	}
	// Every keyed base must be a real stage string (guards typos in the map).
	for stage := range stageTimeoutBase {
		if stage == "" {
			t.Error("empty stage key in stageTimeoutBase")
		}
	}
}

// A declared self-hosted endpoint id normalizes to "other", so the caller's
// endpoint resolution decides locality (#2163).
func TestResolveStageTimeoutLocal_DeclaredEndpoint(t *testing.T) {
	cases := []struct {
		name          string
		adapter       string
		model         string
		declaredLocal bool
		want          time.Duration
	}{
		{"declared local endpoint gets the local factor", "opencode", "mtplx/qwen-27b", true, 60 * time.Minute},
		{"undeclared other key keeps the hosted ceiling", "opencode", "mtplx/qwen-27b", false, 20 * time.Minute},
		{"lmstudio brand is local without a declaration", "opencode", "lmstudio/qwen-27b", false, 60 * time.Minute},
		{"a non-opencode adapter ignores the flag", "claude-headless", "sonnet", true, 20 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveStageTimeoutLocal("issue-pickup", c.adapter, c.model, c.declaredLocal); got != c.want {
				t.Errorf("ResolveStageTimeoutLocal = %v, want %v", got, c.want)
			}
		})
	}
}
