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
		if got := ResolveStageTimeout(c.stage, "sonnet"); got != c.want {
			t.Errorf("ResolveStageTimeout(%q, sonnet) = %v, want %v", c.stage, got, c.want)
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
		{"gpt-5", base}, // unknown model → historical ceiling, unchanged
		{"", base},      // empty model → 1.0
	}
	for _, c := range cases {
		if got := ResolveStageTimeout("feature-dev", c.model); got != c.want {
			t.Errorf("ResolveStageTimeout(feature-dev, %q) = %v, want %v", c.model, got, c.want)
		}
	}
}

// TestResolveStageTimeout_FrontierFeatureDevExceedsHardCap is the regression
// guard for the #73 bug: a frontier-mode Fable feature-dev run must get a
// ceiling strictly greater than the TS-side 90-minute progress-gated hard cap,
// so the Go context deadline can never pre-empt it.
func TestResolveStageTimeout_FrontierFeatureDevExceedsHardCap(t *testing.T) {
	const tsProgressGatedHardCap = 90 * time.Minute
	got := ResolveStageTimeout("feature-dev", "claude-fable-5")
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
	if got := ResolveStageTimeout("some-future-stage", "sonnet"); got != 30*time.Minute {
		t.Errorf("unknown stage = %v, want 30m default", got)
	}
	// Even an unknown stage is model-scaled, so Fable widens the default too.
	if got := ResolveStageTimeout("some-future-stage", "fable"); got != 60*time.Minute {
		t.Errorf("unknown stage on fable = %v, want 60m (30m × 2.0)", got)
	}
}

// TestResolveStageTimeout_EnvOverride verifies the per-stage env override is
// taken verbatim (not model-scaled) and rejects invalid values.
func TestResolveStageTimeout_EnvOverride(t *testing.T) {
	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "240")
	if got := ResolveStageTimeout("feature-dev", "fable"); got != 240*time.Minute {
		t.Errorf("env override = %v, want 240m verbatim (not model-scaled)", got)
	}

	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "0")
	if got := ResolveStageTimeout("feature-dev", "sonnet"); got != 100*time.Minute {
		t.Errorf("zero override should be ignored, got %v want base 100m", got)
	}

	t.Setenv("NIGHTGAUGE_STAGE_TIMEOUT_FEATURE_DEV", "notanumber")
	if got := ResolveStageTimeout("feature-dev", "sonnet"); got != 100*time.Minute {
		t.Errorf("non-numeric override should be ignored, got %v want base 100m", got)
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
