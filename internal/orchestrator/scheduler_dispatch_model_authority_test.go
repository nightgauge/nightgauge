package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

// scheduler_dispatch_model_authority_test.go pins what resolveDispatchModel had
// to become once #340 made it the ONLY router on both dispatch paths.
//
// Before this file, three things it now owns were either absent or expressed in
// the wrong vocabulary, and every one of them failed SILENTLY:
//
//   - the returned value mixed registry bands with concrete ids, so the
//     extension's band-keyed lookups (`--effort` support, the mode/tier
//     predicate that keeps a Maximum-mode Codex/Gemini run off the adapter's
//     default model) read false on exactly the escalated dispatches;
//   - the performance mode reached dispatch only through the router's single
//     feature-dev decision at pickup, so a Maximum-mode run whose mode was set
//     before pickup dispatched the complexity band on every stage;
//   - the plumbing stages dispatched the run-wide dev tier, and no config knob
//     could cap them (minimum_model only raises).
//
// The other half of the loop — the extension spawning the CLI on this value —
// is pinned in tests/services/SkillRunner.ipcModelAuthority.test.ts, because no
// Go test can observe the CLI argv.

// isolateRoutingEnv neutralizes every ambient input to the routing chain: the
// machine config tier points at an empty directory and the two env overrides
// are cleared, so a developer's real ~/.nightgauge or exported performance mode
// never leaks into a routing assertion.
func isolateRoutingEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", t.TempDir())
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_MODE", "")
	t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "")
	t.Setenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL", "")
}

// isolatedWorkspace returns an empty (config-less) workspace root.
func isolatedWorkspace(t *testing.T) string {
	t.Helper()
	isolateRoutingEnv(t)
	return t.TempDir()
}

// routedWorkspace returns a workspace root whose project-tier config.yaml
// carries `body` under a minimal valid header.
func routedWorkspace(t *testing.T, body string) string {
	t.Helper()
	isolateRoutingEnv(t)
	return writeWorkspaceConfig(t, "owner: nightgauge\nrepo: nightgauge\nproject: 1\n"+body)
}

// TestResolveDispatchModelEmitsRegistryBandsOnly is the vocabulary contract.
//
// `RunStageParams.Model` is documented — in internal/ipc/pipeline_messages.go
// and docs/PIPELINE_EXECUTION.md — as the registry band vocabulary the ladders
// are built on. Three sites here assigned routing.ModelSonnet (a
// mustCurrentModelID result, e.g. "claude-sonnet-5") instead, and a fourth
// vocabulary arrived via pickup_recommendation.dev_model, which reRouteContext
// writes as rec.Model — also concrete. A mixed-vocabulary field is not a
// cosmetic problem: every band-keyed consumer no-ops silently on the ids.
func TestResolveDispatchModelEmitsRegistryBandsOnly(t *testing.T) {
	dir := isolatedWorkspace(t)

	t.Run("pr-merge haiku floor", func(t *testing.T) {
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StagePRMerge, 1, dir, "haiku", nil, ""); got != "sonnet" {
			t.Errorf("pr-merge model = %q, want the band %q", got, "sonnet")
		}
	})

	t.Run("feature-validate haiku gate", func(t *testing.T) {
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureValidate, 1, dir, "haiku", nil, ""); got != "sonnet" {
			t.Errorf("feature-validate model = %q, want the band %q", got, "sonnet")
		}
	})

	// The router's recommendation is persisted into the issue context as a
	// concrete id and arrives here as predictedModel. It must be collapsed onto
	// its band before it reaches the wire.
	t.Run("router-recommended concrete id", func(t *testing.T) {
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, routing.ModelOpus, nil, ""); got != "opus" {
			t.Errorf("model = %q, want the band %q for the concrete id %q", got, "opus", routing.ModelOpus)
		}
		if got := s.resolveDispatchModel(state.StageFeatureDev, 2, dir, routing.ModelSonnet, nil, ""); got != "sonnet" {
			t.Errorf("model = %q, want the band %q for the concrete id %q", got, "sonnet", routing.ModelSonnet)
		}
	})

	// A user-defined local model has no band to collapse onto, so it passes
	// through untouched — inventing one would reroute an explicit choice (#56).
	t.Run("unknown local model passes through", func(t *testing.T) {
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "my-local-llm", nil, ""); got != "my-local-llm" {
			t.Errorf("model = %q, want the local model preserved", got)
		}
	})
}

// TestResolveDispatchModelAppliesThePerformanceModePerStage covers the mode
// regression #340 opened: with the TypeScript resolveModel chain no longer
// running on the IPC path, the performance mode reached dispatch only through
// the router's ONE feature-dev decision at pickup. A run whose mode was set
// after the context file was written — or whose stages are not feature-dev —
// dispatched a mode-blind band, with no log line.
func TestResolveDispatchModelAppliesThePerformanceModePerStage(t *testing.T) {
	stages := []state.PipelineStage{
		state.StageIssuePickup,
		state.StageFeaturePlanning,
		state.StageFeatureDev,
		state.StageFeatureValidate,
		state.StagePRCreate,
		state.StagePRMerge,
	}

	t.Run("maximum pins opus everywhere", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
		s := testScheduler(t)
		for _, stage := range stages {
			// "sonnet" is what complexity routing wrote for this run; the mode
			// pin has to beat it on every stage, not just feature-dev.
			if got := s.resolveDispatchModel(stage, 1, dir, "sonnet", nil, ""); got != "opus" {
				t.Errorf("%s model = %q, want opus under Maximum mode", stage, got)
			}
		}
	})

	t.Run("efficiency caps the run tier", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		s := testScheduler(t)
		// The cost-capping mode must actually cap: an opus-routed run runs
		// sonnet on the reasoning stages and haiku on the plumbing ones.
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "sonnet" {
			t.Errorf("feature-dev model = %q, want sonnet under Efficiency mode", got)
		}
		if got := s.resolveDispatchModel(state.StageIssuePickup, 1, dir, "opus", nil, ""); got != "haiku" {
			t.Errorf("issue-pickup model = %q, want haiku under Efficiency mode", got)
		}
	})

	t.Run("elevated pins nothing", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "elevated")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "opus" {
			t.Errorf("feature-dev model = %q, want the routed tier under Elevated mode", got)
		}
	})
}

// TestResolveDispatchModelKeepsPlumbingStagesOffTheRunTier pins the
// lightweight-stage defaults #340 moved out of TypeScript. resolveDispatchModel
// starts from a single per-issue predicted tier and only ever RAISES it, so a
// high-complexity issue routed to opus ran issue-pickup's JSON extraction and
// pr-create's template fill on Opus.
func TestResolveDispatchModelKeepsPlumbingStagesOffTheRunTier(t *testing.T) {
	dir := isolatedWorkspace(t)
	s := testScheduler(t)

	for _, stage := range []state.PipelineStage{state.StageIssuePickup, state.StagePRCreate} {
		if got := s.resolveDispatchModel(stage, 1, dir, "opus", nil, ""); got != "haiku" {
			t.Errorf("%s model = %q, want the lightweight haiku default", stage, got)
		}
	}

	// pr-merge is NOT lightweight (#197): its LLM path only runs on
	// deterministic punts, which are the hardest merges.
	if got := s.resolveDispatchModel(state.StagePRMerge, 1, dir, "opus", nil, ""); got != "opus" {
		t.Errorf("pr-merge model = %q, want the run tier — pr-merge has no lightweight default", got)
	}

	// The reasoning stages still dispatch the run's routed tier.
	if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "opus" {
		t.Errorf("feature-dev model = %q, want the run tier", got)
	}

	// The default is a BASE, not a cap: the minimum_model floor still raises it.
	floors := map[string]string{string(state.StageIssuePickup): "sonnet"}
	if got := s.resolveDispatchModel(state.StageIssuePickup, 1, dir, "opus", floors, ""); got != "sonnet" {
		t.Errorf("floored issue-pickup model = %q, want sonnet", got)
	}
}

// TestResolveDispatchModelHonorsExplicitStageModels pins the operator knobs
// #340 would otherwise have made inert on the autonomous path: pipeline
// .stage_models, its per-stage env overrides, and model_routing.mode — which,
// in `manual`, means "use explicit stage models" and had no effect at all.
func TestResolveDispatchModelHonorsExplicitStageModels(t *testing.T) {
	t.Run("manual mode uses the configured stage model", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "haiku" {
			t.Errorf("feature-dev model = %q, want the configured haiku", got)
		}
	})

	t.Run("manual mode falls back to the built-in per-stage table", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: manual\n")
		s := testScheduler(t)
		// manual means "explicit routing"; the built-in table is the explicit
		// answer for a stage the operator did not name.
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "sonnet" {
			t.Errorf("feature-dev model = %q, want the manual-mode default sonnet", got)
		}
	})

	t.Run("hybrid mode defers unnamed stages to the router", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: hybrid\npipeline:\n  stage_models:\n    feature-validate: haiku\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "opus" {
			t.Errorf("feature-dev model = %q, want the routed tier in hybrid mode", got)
		}
	})

	t.Run("automatic mode ignores the config table", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: automatic\npipeline:\n  stage_models:\n    feature-dev: haiku\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "opus" {
			t.Errorf("feature-dev model = %q, want the routed tier in automatic mode", got)
		}
	})

	t.Run("the env override wins in every mode", func(t *testing.T) {
		dir := routedWorkspace(t, "model_routing:\n  mode: automatic\n")
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "haiku")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "haiku" {
			t.Errorf("feature-dev model = %q, want the env-overridden haiku", got)
		}
	})
}
