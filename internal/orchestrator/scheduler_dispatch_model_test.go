package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/skillrender"
	"github.com/nightgauge/nightgauge/internal/state"
)

// resolveDispatchModel was extracted from the dispatch path in #79 so the
// composed skill can be keyed off the model that actually runs. These pin the
// ordering that extraction had to preserve — the pre-#79 code computed the same
// value, just too late to reach skillrender.Render.

func testScheduler(t *testing.T) *Scheduler {
	t.Helper()
	return &Scheduler{retryEngine: NewRetryEngine(RetryConfig{})}
}

func TestResolveDispatchModelUsesPredictedByDefault(t *testing.T) {
	s := testScheduler(t)
	got := s.resolveDispatchModel(state.StageFeatureDev, 1, isolatedWorkspace(t), "sonnet", nil, "")
	if got != "sonnet" {
		t.Errorf("model = %q, want the predicted model", got)
	}
}

// TestResolveDispatchModelDefaultsAnUnroutedStage pins the seam #304 nearly
// broke. The corpus must record an unrouted run's prediction as ABSENT, but
// dispatch must still resolve a concrete tier, because four mechanisms here
// key on tier recognition and no-op silently on "": the floor returns the
// selection untouched (tierRank("") == -1), the sticky downgrade reports
// model_not_in_registry, RecordStageModel drops the empty value, and
// CalculateCost("") is $0. ~1 in 6 real context files on a working machine
// carry no pickup_recommendation.dev_model, so "" is not a rare shape.
func TestResolveDispatchModelDefaultsAnUnroutedStage(t *testing.T) {
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "sonnet" {
		t.Errorf("model = %q, want the general-purpose default for an unrouted stage", got)
	}

	// The floor must SEE that default. This is the #366 regression in one
	// assertion: with "" the floor is silently skipped and a stage the operator
	// floored to opus runs the provider default, with no log line.
	floors := map[string]string{string(state.StageFeatureDev): "opus"}
	if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", floors, ""); got != "opus" {
		t.Errorf("floored model = %q, want opus — the minimum_model floor must apply to an unrouted stage", got)
	}

	// So must the sticky #42 downgrade: an API rejection of sonnet has to
	// reroute the unrouted stage too, or the run re-fails identically.
	down := testScheduler(t)
	down.retryEngine.RecordDowngrade("sonnet", "haiku", "")
	if got := down.resolveDispatchModel(state.StageFeatureDev, 1, dir, "", nil, ""); got != "haiku" {
		t.Errorf("downgraded model = %q, want haiku", got)
	}
}

// TestDefaultDispatchModelIsALadderBand guards the specific mistake that makes
// the default look right and break escalation: spelling it as a concrete model
// id. Every consumer of the resolved value reads the band vocabulary, and
// NextModel walks a literal ladder a dated id is not a member of — so a
// concrete id would resolve the floor and the downgrade while silently pinning
// an escalated stage to its ceiling.
func TestDefaultDispatchModelIsALadderBand(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	next, ok := engine.NextModel(defaultDispatchModel)
	if !ok || next != "opus" {
		t.Errorf("NextModel(%q) = (%q, %v), want (\"opus\", true) — the default must escalate", defaultDispatchModel, next, ok)
	}
	if got := tierRank(defaultDispatchModel); got < 0 {
		t.Errorf("tierRank(%q) = %d, want a recognized tier — an unrecognized default disables the floor", defaultDispatchModel, got)
	}
	if defaultDispatchModel == routing.ModelSonnet {
		t.Errorf("defaultDispatchModel is the concrete id %q; it must be the band alias", routing.ModelSonnet)
	}
}

func TestResolveDispatchModelPrefersEscalationOverPrediction(t *testing.T) {
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	s.retryEngine.RecordEscalation(string(state.StageFeatureDev), "opus")

	got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "sonnet", nil, "")
	if got != "opus" {
		t.Errorf("model = %q, want the escalated model", got)
	}
	// Escalation is per-stage: a sibling stage must not inherit it, or every
	// later stage in the run would render against the escalated tier's
	// overlays without having escalated. feature-validate is the control
	// because it has no lightweight base of its own (#340) — pr-create does,
	// so it would read "un-escalated" even if escalation had leaked.
	if other := s.resolveDispatchModel(state.StageFeatureValidate, 1, dir, "sonnet", nil, ""); other != "sonnet" {
		t.Errorf("feature-validate model = %q, want the un-escalated prediction", other)
	}
}

func TestResolveDispatchModelAppliesFloorThenDowngrade(t *testing.T) {
	// The floor raises haiku→sonnet; the sticky downgrade then reroutes off a
	// tier the API rejected. Order matters and is the reason the extraction
	// kept these adjacent: if the floor ran last it would push the run back
	// onto the very tier that just refused it.
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	s.retryEngine.RecordDowngrade("sonnet", "haiku", "")

	floors := map[string]string{string(state.StageFeatureDev): "sonnet"}
	got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", floors, "")
	if got != "haiku" {
		t.Errorf("model = %q, want the downgrade to win over the floor", got)
	}

	// Without the rejection recorded, the same inputs stop at the floor —
	// which is what proves the downgrade above did the work, not the floor
	// simply failing to apply.
	fresh := testScheduler(t)
	if got := fresh.resolveDispatchModel(state.StageFeatureDev, 1, dir, "haiku", floors, ""); got != "sonnet" {
		t.Errorf("model = %q, want the floor to raise haiku to sonnet", got)
	}
}

func TestResolveDispatchModelFloorsHaikuOnPRMerge(t *testing.T) {
	// #197: pr-merge's LLM path only runs on deterministic punts, which are the
	// judgment-heavy cases. Haiku is never right there regardless of routing.
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	// The floor escalates to the sonnet BAND, not routing.ModelSonnet's
	// concrete id (#340) — see TestResolveDispatchModelEmitsRegistryBandsOnly.
	got := s.resolveDispatchModel(state.StagePRMerge, 1, dir, "haiku", nil, "")
	if got != "sonnet" {
		t.Errorf("pr-merge model = %q, want %q", got, "sonnet")
	}

	// A non-haiku tier passes through untouched — the guard is a floor, not a pin.
	if got := s.resolveDispatchModel(state.StagePRMerge, 1, dir, "opus", nil, ""); got != "opus" {
		t.Errorf("pr-merge model = %q, want opus preserved", got)
	}
}

func TestResolveDispatchModelDisablesHaikuOnUnverifiedFeatureValidate(t *testing.T) {
	// #3041: haiku shortcuts real build/test commands, so it only runs
	// feature-validate when dev already proved the build. An empty workdir has
	// no dev context, which is the unverified case.
	s := testScheduler(t)
	got := s.resolveDispatchModel(state.StageFeatureValidate, 1, isolatedWorkspace(t), "haiku", nil, "")
	if got != "sonnet" {
		t.Errorf("feature-validate model = %q, want %q", got, "sonnet")
	}
}

// TestSchedulerRendersWithAModel guards the wiring that resolveDispatchModel
// exists to serve. The value is computed inside dispatchStage's closure, which
// no unit test can reach without standing up a whole run — so this asserts the
// call site structurally instead. Without it, someone could revert Options to
// the pre-#79 base-only form and every test above would still pass: they cover
// the model's VALUE, not that it reaches the composer.
func TestSchedulerRendersWithAModel(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "scheduler.go", nil, 0)
	if err != nil {
		t.Fatalf("parse scheduler.go: %v", err)
	}

	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Options" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "skillrender" {
			return true
		}
		found++

		keys := map[string]bool{}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					keys[id.Name] = true
				}
			}
		}
		for _, want := range []string{"Model", "Adapter", "SkillsRoots"} {
			if !keys[want] {
				t.Errorf("skillrender.Options at %s omits %s — overlays would key off nothing",
					fset.Position(lit.Pos()), want)
			}
		}
		return true
	})

	if found == 0 {
		t.Fatal("no skillrender.Options literal found in scheduler.go — did the render move?")
	}
}

func TestDefaultRootsIsTheRepoSkillsTreeOnly(t *testing.T) {
	roots := skillrender.DefaultRoots("/repo")

	// The second root used to be claude-plugins/nightgauge/commands, a layout
	// that has never carried a stage skill in this repository's history and
	// that ADR 007's #3876 amendment retired outright. A root that cannot
	// match is not a harmless fallback — it reads as a second source of skills
	// when there is exactly one.
	if len(roots) != 1 {
		t.Fatalf("DefaultRoots = %v, want exactly one root", roots)
	}
	if roots[0] != "/repo/skills" {
		t.Errorf("root = %q, want /repo/skills", roots[0])
	}
}

// TestRaiseStageFloorsCoversThePlumbingStages pins the half of
// run.retryWithEscalation that stageBaseModel would otherwise have eaten (#340).
// The forced tier is applied as the run's predicted model, and the lightweight
// stage defaults sit ABOVE the prediction — so without the floor an operator
// escalating a stalled pr-create watched the retry re-run it on haiku.
func TestRaiseStageFloorsCoversThePlumbingStages(t *testing.T) {
	raised := raiseStageFloors(nil, "opus")
	for _, stage := range []state.PipelineStage{
		state.StageIssuePickup, state.StageFeaturePlanning, state.StageFeatureDev,
		state.StageFeatureValidate, state.StagePRCreate, state.StagePRMerge,
	} {
		if raised[string(stage)] != "opus" {
			t.Errorf("floor[%s] = %q, want opus", stage, raised[string(stage)])
		}
	}

	// A stronger configured floor survives — the forced tier raises, it never
	// lowers.
	configured := map[string]string{string(state.StageFeatureDev): "fable"}
	kept := raiseStageFloors(configured, "sonnet")
	if kept[string(state.StageFeatureDev)] != "fable" {
		t.Errorf("floor[feature-dev] = %q, want the stronger configured fable", kept[string(state.StageFeatureDev)])
	}
	if kept[string(state.StagePRCreate)] != "sonnet" {
		t.Errorf("floor[pr-create] = %q, want the forced sonnet", kept[string(state.StagePRCreate)])
	}
	// The caller's map is not mutated: modelFloors is loaded once per run and
	// read by every later stage.
	if configured[string(state.StagePRCreate)] != "" {
		t.Error("raiseStageFloors mutated the caller's map")
	}

	// An empty tier is a no-op rather than a floor of "".
	if got := raiseStageFloors(configured, "  "); len(got) != len(configured) {
		t.Errorf("raiseStageFloors(_, blank) = %v, want the input unchanged", got)
	}
}

// TestResolveDispatchModelForcedTierBeatsTheLightweightDefault is the same
// guarantee at the resolveDispatchModel level: the floor the escalation writes
// has to survive the per-stage base routing.
func TestResolveDispatchModelForcedTierBeatsTheLightweightDefault(t *testing.T) {
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	floors := raiseStageFloors(nil, "opus")
	if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "opus", floors, ""); got != "opus" {
		t.Errorf("pr-create model = %q, want the forced opus tier", got)
	}
}
