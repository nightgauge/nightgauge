package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
			got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, routedTier, nil, "")
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
		if got := s.resolveDispatchModel(stage, 1, dir, "fable", nil, ""); got != "fable" {
			t.Errorf("%s = %q, want fable — the reasoning stages are what the frontier ceiling is for", stage, got)
		}
	}
	if got := s.resolveDispatchModel(state.StageFeatureValidate, 1, dir, "fable", nil, ""); got != "opus" {
		t.Errorf("feature-validate = %q, want opus — MODE_PROFILES.frontier caps this stage at Opus", got)
	}
	// Plumbing keeps its lightweight base rather than riding the run tier.
	if got := s.resolveDispatchModel(state.StageIssuePickup, 1, dir, "fable", nil, ""); got != "haiku" {
		t.Errorf("issue-pickup = %q, want haiku — frontier rates are not paid for git plumbing", got)
	}
}

// TestDispatchModelCeilingBindsTheRaisingMechanisms states the interaction the
// Go path has and TypeScript does not: escalation, the minimum_model floor and
// the operator's forced tier all only RAISE, and a cost-capping mode that any
// of them can raise out of caps nothing. The ceiling binds them — the same rule
// resolveModel applies when it re-clamps its own enforceMinimumModel result to
// the envelope ceiling.
//
// EVERY cell is run twice: once from a router-chosen base, and once from the
// explicit base `model_routing.mode: manual` produces. The second half is the
// point. The round-2 fixup gated the clamp on a flag describing the BASE, and
// in manual mode stageConfiguredModel answers for every stage
// (defaultStageModels), so the flag was true everywhere and the ceiling bound
// nothing — for exactly the operators who copied a docs/CONFIGURATION.md
// profile, all three of which set `model_routing.mode: manual`. Asserting the
// rule only where the base is router-chosen is asserting it only where it
// already held.
func TestDispatchModelCeilingBindsTheRaisingMechanisms(t *testing.T) {
	// manualHaikuDev is the shape of every recommended CONFIGURATION.md
	// profile: explicit routing mode, explicit per-stage model.
	const manualHaikuDev = "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n"

	bases := []struct {
		name string
		// workspace returns the root a cell resolves against.
		workspace func(t *testing.T) string
	}{
		{"router-chosen base", func(t *testing.T) string { return isolatedWorkspace(t) }},
		{"explicit base (manual + stage_models)", func(t *testing.T) string {
			return routedWorkspace(t, manualHaikuDev)
		}},
	}

	for _, base := range bases {
		t.Run(base.name, func(t *testing.T) {
			t.Run("the minimum_model floor cannot exceed the mode ceiling", func(t *testing.T) {
				dir := base.workspace(t)
				t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
				s := testScheduler(t)
				floors := map[string]string{string(state.StageFeatureDev): "opus"}
				if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", floors, ""); got != "sonnet" {
					t.Errorf("floored feature-dev = %q, want sonnet — Efficiency's ceiling binds the floor", got)
				}
			})

			t.Run("post-failure escalation cannot exceed the mode ceiling", func(t *testing.T) {
				dir := base.workspace(t)
				t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
				s := testScheduler(t)
				s.retryEngine.RecordEscalation("feature-dev", "opus")
				if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", nil, ""); got != "sonnet" {
					t.Errorf("escalated feature-dev = %q, want sonnet — Efficiency's ceiling binds the escalation ladder", got)
				}
			})

			t.Run("the run.retryWithEscalation forced tier cannot exceed the mode ceiling", func(t *testing.T) {
				dir := base.workspace(t)
				t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
				s := testScheduler(t)
				if got := s.resolveDispatchModel(
					state.StageFeatureDev, 1, dir, "haiku", raiseStageFloors(nil, "opus"), "",
				); got != "sonnet" {
					t.Errorf("forced feature-dev = %q, want sonnet — an operator forcing a tier gets it inside the band they selected", got)
				}
			})
		})
	}

	t.Run("an explicit per-stage model is outside the mode's governance", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "opus")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", nil, ""); got != "opus" {
			t.Errorf("feature-dev = %q, want opus — an explicit per-stage model overrides the mode, exactly as in resolveModel Step 1", got)
		}
	})

	// The exemption covers the operator's OWN value, and only it. A floor is a
	// RAISE, so clamping the raise must never leave the stage below the model
	// the operator named: "force at least Fable" turning an explicit Opus into
	// Sonnet is a downgrade dressed as an escalation.
	t.Run("a clamped raise never lowers an explicit per-stage model", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "opus")
		s := testScheduler(t)
		floors := map[string]string{string(state.StageFeatureDev): "fable"}
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", floors, ""); got != "opus" {
			t.Errorf("feature-dev = %q, want opus — the ceiling discards the raise, not the operator's own model", got)
		}
	})

	// Bounding a raise uses the CEILING only. Maximum's envelope is
	// [opus, opus], so re-applying the floor half here would turn a floor into
	// a mode-driven upgrade — and on this path it would also hand the #42
	// sticky downgrade back the tier the API just rejected.
	t.Run("a clamped raise is bounded by the ceiling only, never the envelope floor", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "haiku")
		s := testScheduler(t)
		floors := map[string]string{string(state.StageFeatureDev): "sonnet"}
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", floors, ""); got != "sonnet" {
			t.Errorf("feature-dev = %q, want sonnet — the floored value is capped, not re-raised by maximum's [opus, opus] floor", got)
		}
	})

	t.Run("the sticky model-unavailable downgrade stays the last word", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "maximum")
		s := testScheduler(t)
		// The API rejected opus for this run. Maximum's envelope is [opus,
		// opus], so a floor half applied after the downgrade would put the run
		// straight back onto the rejected tier.
		s.retryEngine.RecordDowngrade("opus", DowngradeDecision{NewTier: "sonnet"})
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", nil, ""); got != "sonnet" {
			t.Errorf("feature-dev = %q, want sonnet — the #42 downgrade must survive the mode envelope", got)
		}
	})
}

// TestDispatchModelFrontierCeilingIsPerStageForFloorsToo closes the other half
// of the narrowing: stageBaseModel clamps the ROUTED tier with
// RoutedTierEnvelope, but a floor arrives after it, so the post-raise clamp has
// to use the same narrowed envelope or the promise only holds for stages
// nothing raised.
//
// `feature-validate` is the specific stage MODE_PROFILES.frontier caps at Opus,
// and the specific stage the codebase records as having "empirically failed
// validation in dogfooding" on Fable — so a run-wide `minimum_model` or a
// forced tier putting it on Fable is the behavior #19 deleted, arriving through
// a different door.
func TestDispatchModelFrontierCeilingIsPerStageForFloorsToo(t *testing.T) {
	capped := []state.PipelineStage{
		state.StageFeatureValidate, state.StagePRMerge, state.StageIssuePickup, state.StagePRCreate,
	}
	for _, stage := range capped {
		t.Run("minimum_model fable is capped at opus on "+string(stage), func(t *testing.T) {
			dir := isolatedWorkspace(t)
			t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "frontier")
			s := testScheduler(t)
			floors := map[string]string{string(stage): "fable"}
			if got := s.resolveDispatchModel(stage, 1, dir, "sonnet", floors, ""); got != "opus" {
				t.Errorf("%s = %q, want opus — the frontier ceiling is offered to feature-planning/feature-dev only", stage, got)
			}
		})
	}

	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureDev} {
		t.Run("minimum_model fable reaches fable on "+string(stage), func(t *testing.T) {
			dir := isolatedWorkspace(t)
			t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "frontier")
			s := testScheduler(t)
			floors := map[string]string{string(stage): "fable"}
			if got := s.resolveDispatchModel(stage, 1, dir, "sonnet", floors, ""); got != "fable" {
				t.Errorf("%s = %q, want fable — the heavy reasoning stages are what the frontier ceiling is for", stage, got)
			}
		})
	}

	t.Run("a run-wide forced tier does not carry fable onto feature-validate", func(t *testing.T) {
		dir := isolatedWorkspace(t)
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "frontier")
		s := testScheduler(t)
		floors := raiseStageFloors(nil, "fable")
		if got := s.resolveDispatchModel(state.StageFeatureValidate, 1, dir, "sonnet", floors, ""); got != "opus" {
			t.Errorf("feature-validate = %q, want opus", got)
		}
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "sonnet", floors, ""); got != "fable" {
			t.Errorf("feature-dev = %q, want fable", got)
		}
	})
}

// TestDispatchModelResolvesTheWorkspaceDefault pins `resolveModel` Step 3 on the
// Go side. On the pre-#340 IPC path this was the EFFECTIVE model for every
// reasoning stage — services/SkillRunner.ts passed no issue metadata, so Step 2
// never fired and Step 3 always won — so a Go router that stopped at its own
// hardcoded sonnet would have dropped the knob silently for exactly the runs it
// used to govern.
func TestDispatchModelResolvesTheWorkspaceDefault(t *testing.T) {
	t.Run("ui.core.default_model answers an unrouted stage", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: opus\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "opus" {
			t.Errorf("feature-dev = %q, want opus — ui.core.default_model is Step 3", got)
		}
	})

	t.Run("the env override wins over the file, as in getDefaultModel", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: haiku\n")
		t.Setenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL", "opus")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "opus" {
			t.Errorf("feature-dev = %q, want opus", got)
		}
	})

	t.Run("the routed tier still wins over it", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: opus\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", nil, ""); got != "haiku" {
			t.Errorf("feature-dev = %q, want the routed haiku — Step 2 precedes Step 3", got)
		}
	})

	t.Run("it is clamped into the mode envelope, like Step 3 in resolveModel", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: opus\n")
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "efficiency")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "sonnet" {
			t.Errorf("feature-dev = %q, want sonnet", got)
		}
	})

	t.Run("a non-band value is ignored, as in getDefaultModel's validModels guard", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: gpt-5.6-sol\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "sonnet" {
			t.Errorf("feature-dev = %q, want the hardcoded sonnet fallback", got)
		}
	})

	// `fable` is the cell the guard above does not cover, and the one where the
	// two readers disagreed: getDefaultModel's ENV branch accepts all four
	// registry bands while its FILE branch matched only three (a regex written
	// before Fable existed), so `ui.core.default_model: fable` dispatched Fable
	// autonomously and Sonnet — the Step 4 hardcoded fallback — from the
	// extension, from one config file with no log line on either side. The TS
	// twin is resolveModel.modeKnobAgreement.test.ts §"Step 3 reads the same
	// band set Go does".
	t.Run("fable is accepted from the file, like every other registry band", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: fable\n")
		t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", "frontier")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "fable" {
			t.Errorf("feature-dev = %q, want fable — DefaultModelSchema permits it and validStageModel accepts it", got)
		}
	})

	t.Run("a fable default is still clamped into the mode envelope", func(t *testing.T) {
		dir := routedWorkspace(t, "ui:\n  core:\n    default_model: fable\n")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "opus" {
			t.Errorf("feature-dev = %q, want opus — elevated tops out there; accepting the band does not exempt it from the ceiling", got)
		}
	})
}

// gitWorkspaceWithLargeDiff returns a REAL git repository whose checked-out
// branch differs from `main` by `lines` inserted lines, with `body` as its
// .nightgauge/config.yaml.
//
// The pr-create escalation reads `git diff main --shortstat`, which returns 0
// on any error — so a fake workspace would silently make every assertion below
// pass for the wrong reason.
func gitWorkspaceWithLargeDiff(t *testing.T, body string, lines int) string {
	t.Helper()
	isolateRoutingEnv(t)
	root := t.TempDir()

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "seed")

	git("checkout", "-b", "feat/large")
	if err := os.WriteFile(filepath.Join(root, "big.txt"),
		[]byte(strings.Repeat("a line of a very large changeset\n", lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "large")

	if body != "" {
		dir := filepath.Join(root, ".nightgauge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
			[]byte("owner: nightgauge\nrepo: nightgauge\nproject: 1\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestDispatchModelPRCreateLargeDiffSpares AnExplicitBase pins the rule both
// resolvers now follow: EXPLICIT OPERATOR CONFIGURATION WINS, and the
// large-diff escalation applies only over a base the PIPELINE chose (#340).
//
// In `resolveModel` that rule is structural — the escalation lives inside Step
// 1.5, which Step 1 returns before ever reaching whenever `getStageModel`
// answers. Go evaluated it on the resolved model regardless of provenance, so
// with `model_routing.mode: manual` — set by all three recommended
// docs/CONFIGURATION.md profiles, and where the explicit chain answers for
// EVERY stage out of defaultStageModels (pr-create: haiku) — the same workspace
// and the same 900-line diff ran pr-create on sonnet autonomously and haiku
// from the extension. That is the default cell for those operators, not an
// exotic one.
func TestDispatchModelPRCreateLargeDiffSparesAnExplicitBase(t *testing.T) {
	const bigDiff = 900

	t.Run("a pipeline-chosen lightweight base still escalates", func(t *testing.T) {
		dir := gitWorkspaceWithLargeDiff(t, "model_routing:\n  mode: automatic\n", bigDiff)
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "", nil, ""); got != "sonnet" {
			t.Errorf("pr-create = %q, want sonnet — lightweightStageDefaults is the pipeline's own choice, and haiku stalls on a big changeset", got)
		}
	})

	t.Run("the manual-mode table is explicit, so it is left alone", func(t *testing.T) {
		dir := gitWorkspaceWithLargeDiff(t, "model_routing:\n  mode: manual\n", bigDiff)
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "", nil, ""); got != "haiku" {
			t.Errorf("pr-create = %q, want haiku — resolveModel Step 1 answers from DEFAULT_STAGE_MODELS and never reaches the escalation", got)
		}
	})

	t.Run("an explicit pipeline.stage_models entry is left alone", func(t *testing.T) {
		dir := gitWorkspaceWithLargeDiff(t,
			"model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    pr-create: haiku\n", bigDiff)
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "", nil, ""); got != "haiku" {
			t.Errorf("pr-create = %q, want haiku — the operator named the tier for this stage", got)
		}
	})

	t.Run("the env override is left alone", func(t *testing.T) {
		dir := gitWorkspaceWithLargeDiff(t, "model_routing:\n  mode: automatic\n", bigDiff)
		t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE", "haiku")
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "", nil, ""); got != "haiku" {
			t.Errorf("pr-create = %q, want haiku — the env override wins in every mode, on both resolvers", got)
		}
	})

	t.Run("a small diff escalates nothing", func(t *testing.T) {
		dir := gitWorkspaceWithLargeDiff(t, "model_routing:\n  mode: automatic\n", 3)
		s := testScheduler(t)
		if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "", nil, ""); got != "haiku" {
			t.Errorf("pr-create = %q, want haiku — the diff is below the threshold", got)
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
	if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, routedTier, nil, ""); got != routedTier {
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
