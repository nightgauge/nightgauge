package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

// dispatch_routing_mode_test.go is the Go half of the mode × knob agreement
// matrix (#340). Its TypeScript twin is
// packages/nightgauge-vscode/tests/utils/resolveModel.modeKnobAgreement.test.ts,
// which asserts the SAME cells against `resolveModel`. The two resolvers own
// one dispatch path each, so a cell where they disagree is the Dual-Path Drift
// class this issue exists to remove — and the round-1 fixup shipped four such
// cells because the Go side pinned per stage where MODE_PROFILES pins nothing:
//
//   - `efficiency` and `frontier` are ENVELOPES since #19. Their `stages` maps
//     are `{}` and pinned Fable was deleted for having "paid frontier rates for
//     trivial work and empirically failed validation in dogfooding"
//     (modeProfiles.ts). Only `maximum` pins.
//   - A mode pin evaluated BEFORE the explicit per-stage chain made
//     `pipeline.stage_models`, `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*` and
//     `model_routing.mode: manual` inert on autonomous runs under those modes —
//     the three knobs every recommended profile in docs/CONFIGURATION.md is
//     built on.
//
// The one input that legitimately differs between the resolvers is where the
// un-overridden tier comes from (Go: the run's routed tier from
// issue-{N}.json; TS: AutoModelSelector, which has no Go counterpart), so both
// halves of the matrix feed that step the SAME tier and compare the rest.

// routedTier is the tier both resolvers are handed for the un-overridden step:
// `predictedModel` here, `NIGHTGAUGE_UI_CORE_DEFAULT_MODEL` in the TS twin.
const routedTier = "opus"

// TestDispatchModelModeKnobMatrix walks {maximum, efficiency, frontier, unset}
// × {automatic, manual stage_models, env override}.
func TestDispatchModelModeKnobMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mode   string // "" = unset (elevated default)
		config string
		env    string // NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV
		want   string
		why    string
	}{
		{
			name: "unset + automatic: the routed tier",
			want: "opus",
			why:  "elevated is the open [haiku, opus] envelope — nothing to clamp",
		},
		{
			name:   "unset + manual stage_models",
			config: "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n",
			want:   "haiku",
			why:    "explicit per-stage routing beats the router",
		},
		{
			name: "unset + env override",
			env:  "haiku",
			want: "haiku",
			why:  "the env override is the operator's escape hatch",
		},
		{
			name: "efficiency + automatic: capped, not pinned",
			mode: "efficiency",
			want: "sonnet",
			why:  "the [haiku, sonnet] envelope clamps the routed opus down to sonnet",
		},
		{
			name:   "efficiency + manual stage_models above the ceiling",
			mode:   "efficiency",
			config: "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: opus\n",
			want:   "opus",
			why:    "resolveModel Step 1 returns the explicit model unclamped — an explicit per-stage model overrides the mode for that stage",
		},
		{
			name: "efficiency + env override above the ceiling",
			mode: "efficiency",
			env:  "opus",
			want: "opus",
			why:  "the env override wins in every mode, and is not clamped",
		},
		{
			name: "frontier + automatic: the routed tier, not Fable",
			mode: "frontier",
			want: "opus",
			why:  "MODE_PROFILES.frontier.stages is {} — the mode widens the ceiling, it does not pin",
		},
		{
			name:   "frontier + manual stage_models",
			mode:   "frontier",
			config: "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n",
			want:   "haiku",
			why:    "the pin that used to preempt this chain does not exist",
		},
		{
			name: "frontier + env override",
			mode: "frontier",
			env:  "haiku",
			want: "haiku",
			why:  "docs/CONFIGURATION.md promises the env var wins in every mode",
		},
		{
			name: "maximum + automatic: pinned to opus",
			mode: "maximum",
			want: "opus",
			why:  "maximum is the ONE mode that still pins, on every stage",
		},
		{
			name:   "maximum + manual stage_models loses to the pin",
			mode:   "maximum",
			config: "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n",
			want:   "opus",
			why:    "resolveModel Step 0 returns the pin before Step 1 reads the config",
		},
		{
			name: "maximum + env override beats the pin",
			mode: "maximum",
			env:  "haiku",
			want: "haiku",
			why:  "the per-stage env override is resolved ahead of the pin, on both resolvers",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			if tc.config != "" {
				dir = routedWorkspace(t, tc.config)
			} else {
				dir = isolatedWorkspace(t)
			}
			if tc.mode != "" {
				t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", tc.mode)
			}
			if tc.env != "" {
				t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", tc.env)
			}
			s := testScheduler(t)
			got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, routedTier, nil)
			if got != tc.want {
				t.Errorf("feature-dev = %q, want %q — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestDispatchModelFrontierNeverCarriesFableOffTheReasoningStages is the
// consequence of Go applying ONE routed tier to every stage where the TS
// resolver re-runs its per-stage selector.
//
// MODE_PROFILES.frontier's own comment: "plumbing stays Haiku and
// feature-validate never exceeds Opus". A Frontier run whose feature-dev
// recommendation escalated to Fable would otherwise dispatch Fable on
// feature-validate — the stage the TS selector deliberately excludes from the
// escalation, because Fable's extended reasoning empirically failed validation.
func TestDispatchModelFrontierNeverCarriesFableOffTheReasoningStages(t *testing.T) {
	dir := isolatedWorkspace(t)
	t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "frontier")
	s := testScheduler(t)

	// The router escalated this run's implementation tier to Fable.
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		if got := s.resolveDispatchModel(stage, 1, dir, "fable", nil); got != "fable" {
			t.Errorf("%s = %q, want fable — the reasoning stages are what the frontier ceiling is for", stage, got)
		}
	}
	if got := s.resolveDispatchModel(state.StageFeatureValidate, 1, dir, "fable", nil); got != "opus" {
		t.Errorf("feature-validate = %q, want opus — MODE_PROFILES.frontier caps this stage at Opus", got)
	}
	// Plumbing keeps its lightweight base rather than riding the run tier.
	if got := s.resolveDispatchModel(state.StageIssuePickup, 1, dir, "fable", nil); got != "haiku" {
		t.Errorf("issue-pickup = %q, want haiku — frontier rates are not paid for git plumbing", got)
	}
}

// TestDispatchModelCeilingBindsTheRaisingMechanisms states the interaction the
// Go path has and TypeScript does not: escalation, the minimum_model floor and
// the operator's forced tier all only RAISE, and a cost-capping mode that any
// of them can raise out of caps nothing. The ceiling binds them — the same rule
// resolveModel applies when it re-clamps its own enforceMinimumModel result to
// the envelope ceiling.
func TestDispatchModelCeilingBindsTheRaisingMechanisms(t *testing.T) {
	t.Run("the minimum_model floor cannot exceed the mode ceiling", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		s := testScheduler(t)
		floors := map[string]string{string(state.StageFeatureDev): "opus"}
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", floors); got != "sonnet" {
			t.Errorf("floored feature-dev = %q, want sonnet — Efficiency's ceiling binds the floor", got)
		}
	})

	t.Run("post-failure escalation cannot exceed the mode ceiling", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		s := testScheduler(t)
		s.retryEngine.RecordEscalation("feature-dev", "opus")
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", nil); got != "sonnet" {
			t.Errorf("escalated feature-dev = %q, want sonnet — Efficiency's ceiling binds the escalation ladder", got)
		}
	})

	t.Run("an explicit per-stage model is outside the mode's governance", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "opus")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", nil); got != "opus" {
			t.Errorf("feature-dev = %q, want opus — an explicit per-stage model overrides the mode, exactly as in resolveModel Step 1", got)
		}
	})

	t.Run("the sticky model-unavailable downgrade stays the last word", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
		s := testScheduler(t)
		// The API rejected opus for this run. Maximum's envelope is [opus,
		// opus], so a floor half applied after the downgrade would put the run
		// straight back onto the rejected tier.
		s.retryEngine.RecordDowngrade("opus", "sonnet")
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil); got != "sonnet" {
			t.Errorf("feature-dev = %q, want sonnet — the #42 downgrade must survive the mode envelope", got)
		}
	})
}

// TestStageEnvModelValidatesLikeItsTypeScriptPair guards a divergence the
// matrix above cannot see: getStageModel accepts only the four registry bands,
// so an unrecognized env value falls through to the rest of the chain. Without
// the same guard here, a value one resolver drops is dispatched by the other.
func TestStageEnvModelValidatesLikeItsTypeScriptPair(t *testing.T) {
	dir := isolatedWorkspace(t)
	t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "gpt-5.6-sol")
	s := testScheduler(t)
	if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, routedTier, nil); got != routedTier {
		t.Errorf("feature-dev = %q, want the routed %q — a non-band env value is ignored, as in getStageModel", got, routedTier)
	}
}

// TestModeTableIsSharedWithTheRouter is the "one table" check. The mode profile
// has exactly one home (routing.modeProfiles); this asserts the dispatch path
// and the router agree about what a mode does, rather than each holding a copy.
func TestModeTableIsSharedWithTheRouter(t *testing.T) {
	for _, mode := range []routing.PerformanceMode{
		routing.ModeEfficiency, routing.ModeElevated, routing.ModeMaximum, routing.ModeFrontier,
	} {
		pinned := routing.ModeStagePin(mode, string(state.StageFeatureDev)) != ""
		if pinned != (mode == routing.ModeMaximum) {
			t.Errorf("mode %s pins feature-dev = %v — only maximum pins (MODE_PROFILES.efficiency.stages and .frontier.stages are both {})",
				mode, pinned)
		}
	}
}
