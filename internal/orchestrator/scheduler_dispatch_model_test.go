package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	// spike-materialize has no built-in default, so the prediction decides.
	got := s.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "sonnet", routing.Decision{}, nil, "", routing.ModelLabelOverride{})
	if got != "sonnet" {
		t.Errorf("model = %q, want the predicted model", got)
	}
	// A reasoning stage starts from its opus default (#2512): a lower
	// prediction cannot pull it down.
	if got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "sonnet", routing.Decision{}, nil, "", routing.ModelLabelOverride{}); got != "opus" {
		t.Errorf("feature-dev model = %q, want the opus stage default", got)
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
	// spike-materialize: the one dispatch stage with no built-in default.
	if got := s.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "", routing.Decision{}, nil, "", routing.ModelLabelOverride{}); got != "sonnet" {
		t.Errorf("model = %q, want the general-purpose default for an unrouted stage", got)
	}

	// The floor must SEE that default. This is the #366 regression in one
	// assertion: with "" the floor is silently skipped and a stage the operator
	// floored to opus runs the provider default, with no log line.
	floors := map[string]string{string(state.StageSpikeMaterialize): "opus"}
	if got := s.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "", routing.Decision{}, floors, "", routing.ModelLabelOverride{}); got != "opus" {
		t.Errorf("floored model = %q, want opus — the minimum_model floor must apply to an unrouted stage", got)
	}

	// So must the sticky #42 downgrade: an API rejection of sonnet has to
	// reroute the unrouted stage too, or the run re-fails identically.
	down := testScheduler(t)
	down.retryEngine.RecordDowngrade("sonnet", DowngradeDecision{NewTier: "haiku"})
	if got := down.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "", routing.Decision{}, nil, "", routing.ModelLabelOverride{}); got != "haiku" {
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

	got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "sonnet", routing.Decision{}, nil, "", routing.ModelLabelOverride{})
	if got != "opus" {
		t.Errorf("model = %q, want the escalated model", got)
	}
	// Escalation is per-stage: a sibling stage must not inherit it, or every
	// later stage in the run would render against the escalated tier's
	// overlays without having escalated. feature-validate is the control
	// because it has no lightweight base of its own (#340) — pr-create does,
	// so it would read "un-escalated" even if escalation had leaked.
	if other := s.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "sonnet", routing.Decision{}, nil, "", routing.ModelLabelOverride{}); other != "sonnet" {
		t.Errorf("spike-materialize model = %q, want the un-escalated prediction", other)
	}
}

func TestResolveDispatchModelAppliesFloorThenDowngrade(t *testing.T) {
	// The floor raises haiku→sonnet; the sticky downgrade then reroutes off a
	// tier the API rejected. Order matters and is the reason the extraction
	// kept these adjacent: if the floor ran last it would push the run back
	// onto the very tier that just refused it.
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	s.retryEngine.RecordDowngrade("sonnet", DowngradeDecision{NewTier: "haiku"})

	floors := map[string]string{string(state.StageSpikeMaterialize): "sonnet"}
	got := s.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "haiku", routing.Decision{}, floors, "", routing.ModelLabelOverride{})
	if got != "haiku" {
		t.Errorf("model = %q, want the downgrade to win over the floor", got)
	}

	// Without the rejection recorded, the same inputs stop at the floor —
	// which is what proves the downgrade above did the work, not the floor
	// simply failing to apply.
	fresh := testScheduler(t)
	if got := fresh.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "haiku", routing.Decision{}, floors, "", routing.ModelLabelOverride{}); got != "sonnet" {
		t.Errorf("model = %q, want the floor to raise haiku to sonnet", got)
	}
}

func TestResolveDispatchModelFloorsHaikuOnPRMerge(t *testing.T) {
	// #197: pr-merge's LLM path only runs on deterministic punts, which are the
	// judgment-heavy cases. Haiku is never right there regardless of routing.
	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	// The opus stage default (#2512) already sits above the haiku guard, so a
	// haiku prediction never reaches pr-merge.
	got := s.resolveDispatchModel(state.StagePRMerge, 1, dir, "haiku", routing.Decision{}, nil, "", routing.ModelLabelOverride{})
	if got != "opus" {
		t.Errorf("pr-merge model = %q, want %q", got, "opus")
	}

	// A non-haiku tier passes through untouched — the guard is a floor, not a pin.
	if got := s.resolveDispatchModel(state.StagePRMerge, 1, dir, "opus", routing.Decision{}, nil, "", routing.ModelLabelOverride{}); got != "opus" {
		t.Errorf("pr-merge model = %q, want opus preserved", got)
	}
}

func TestResolveDispatchModelDisablesHaikuOnUnverifiedFeatureValidate(t *testing.T) {
	// #3041: haiku shortcuts real build/test commands, so it only runs
	// feature-validate when dev already proved the build. An empty workdir has
	// no dev context, which is the unverified case.
	s := testScheduler(t)
	// The opus stage default (#2512) keeps a haiku prediction off it entirely.
	got := s.resolveDispatchModel(state.StageFeatureValidate, 1, isolatedWorkspace(t), "haiku", routing.Decision{}, nil, "", routing.ModelLabelOverride{})
	if got != "opus" {
		t.Errorf("feature-validate model = %q, want %q", got, "opus")
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

// TestDefaultRootsSearchesTheWorkspaceWithNoBundle pins the half of the contract
// that has always held: with no override and no bundle beside the binary, the
// workspace checkout's own skills/ tree is the root. The test binary has no
// bundle; #2718's order with one is pinned in internal/skillrender.
//
// This test used to assert `len(roots) == 1`, and it passed for the whole life
// of #874 — the bug was that the ONE root could not match in any repository
// but this one. An assertion that pins a count is not an assertion about
// behaviour; the bundle arm is covered by TestDefaultRootsAppendsTheBundleTree.
func TestDefaultRootsSearchesTheWorkspaceWithNoBundle(t *testing.T) {
	t.Setenv(skillrender.SkillsRootEnv, "")
	roots := skillrender.DefaultRoots("/repo")

	if len(roots) == 0 {
		t.Fatal("DefaultRoots returned no roots")
	}
	if roots[0] != "/repo/skills" {
		t.Errorf("first root = %q, want /repo/skills", roots[0])
	}
	// claude-plugins/nightgauge/commands was retired by ADR 007's #3876
	// amendment and never carried a stage skill. It must not come back.
	for _, r := range roots {
		if strings.Contains(r, filepath.Join("claude-plugins", "nightgauge", "commands")) {
			t.Errorf("root %q is the retired plugin-command layout", r)
		}
	}
}

// TestStageSkillRootsSearchesTheRunsWorktree pins the input half of #2718's one
// resolution: the extension passes the run's worktree, so the scheduler has to
// search that checkout rather than the repository root, or the hosts apply one
// order to different trees. A worktree that has gone falls back to the root.
func TestStageSkillRootsSearchesTheRunsWorktree(t *testing.T) {
	t.Setenv(skillrender.SkillsRootEnv, "")
	root := t.TempDir()
	worktree := t.TempDir()
	baseDir := layouttest.MkCheckoutDir(t, root)
	now := time.Now().UTC().Format(time.RFC3339)
	rs := &runstate.RunState{
		SchemaVersion: runstate.SchemaVersion, IssueNumber: 2718, State: runstate.StateRunning,
		RunID: "run-2718", AttemptNumber: 1, CompletedStages: []runstate.Stage{},
		WorktreePath: &worktree, Branch: "feat/2718", CreatedAt: now, UpdatedAt: now,
		Attempts: []runstate.Attempt{{RunID: "run-2718", AttemptNumber: 1, StartedAt: now}},
	}
	if err := runstate.Save(baseDir, rs); err != nil {
		t.Fatalf("save run-state: %v", err)
	}

	if got := stageSkillRoots(root, 2718); !slices.Contains(got, filepath.Join(worktree, "skills")) ||
		slices.Contains(got, filepath.Join(root, "skills")) {
		t.Errorf("roots = %v, want the worktree's skills and not the repository's", got)
	}

	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	if got := stageSkillRoots(root, 2718); !slices.Contains(got, filepath.Join(root, "skills")) {
		t.Errorf("roots with the worktree gone = %v, want the repository's skills", got)
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
	if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "opus", routing.Decision{}, floors, "", routing.ModelLabelOverride{}); got != "opus" {
		t.Errorf("pr-create model = %q, want the forced opus tier", got)
	}
}

// TestResolveDispatchModel_LabelPin pins the issue-label precedence (#8):
// explicit stage config > label; the pin replaces the pipeline-chosen base;
// the cap binds escalation; the minimum_model floor and the mode ceiling still
// bind the label.
func TestResolveDispatchModel_LabelPin(t *testing.T) {
	parse := func(labels ...string) routing.ModelLabelOverride {
		o, _ := routing.ParseModelLabels(labels)
		return o
	}
	cases := []struct {
		name   string
		mode   string
		config string
		env    string
		floor  string
		esc    string
		labels routing.ModelLabelOverride
		want   string
	}{
		{name: "pin below the routed tier is honoured", labels: parse("model:pin-haiku"), want: "haiku"},
		{name: "pin above the ceiling is clamped", mode: "efficiency", labels: parse("model:pin-opus"), want: "sonnet"},
		{name: "cap below the routed tier", labels: parse("model:max-sonnet"), want: "sonnet"},
		{name: "cap above the routed tier changes nothing", labels: parse("model:max-fable"), want: "opus"},
		{name: "conflict: the cap wins", labels: parse("model:pin-opus", "model:max-haiku"), want: "haiku"},
		{name: "cap binds retry escalation", esc: "opus", labels: parse("model:pin-haiku", "model:max-sonnet"), want: "sonnet"},
		{name: "explicit stage_models beats the label",
			config: "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: sonnet\n", labels: parse("model:pin-haiku"), want: "sonnet"},
		{name: "explicit env override beats the label", env: "sonnet", labels: parse("model:max-haiku"), want: "sonnet"},
		{name: "minimum_model floor beats the cap", floor: "opus", labels: parse("model:max-haiku"), want: "opus"},
		{name: "pin-haiku under maximum dispatches haiku", mode: "maximum", labels: parse("model:pin-haiku"), want: "haiku"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolatedWorkspace(t)
			if tc.config != "" {
				dir = routedWorkspace(t, tc.config)
			}
			if tc.mode != "" {
				t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", tc.mode)
			}
			if tc.env != "" {
				t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", tc.env)
			}
			var floors map[string]string
			if tc.floor != "" {
				floors = map[string]string{string(state.StageFeatureDev): tc.floor}
			}
			s := testScheduler(t)
			if tc.esc != "" {
				s.retryEngine.RecordEscalation("feature-dev", tc.esc)
			}
			got := s.resolveDispatchModel(state.StageFeatureDev, 1, dir, "opus", routing.Decision{}, floors, "", tc.labels)
			if got != tc.want {
				t.Errorf("feature-dev = %q, want %q", got, tc.want)
			}
			if tc.esc == "" {
				if planned := PlannedStageModel(dir, state.StageFeatureDev, "opus", routing.Decision{}, floors, "", tc.labels); planned != got {
					t.Errorf("PlannedStageModel = %q, dispatch = %q: prediction must equal dispatch (#2537)", planned, got)
				}
			}
		})
	}
}

// TestResolveDispatchModel_NoLabelIsIdentity: the zero override and an issue
// with no model:* labels resolve every stage exactly as before (#8).
func TestResolveDispatchModel_NoLabelIsIdentity(t *testing.T) {
	dir := isolatedWorkspace(t)
	unrelated, _ := routing.ParseModelLabels([]string{"type:bug", "size:M"})
	for _, stage := range []state.PipelineStage{
		state.StageIssuePickup, state.StageFeaturePlanning, state.StageFeatureDev,
		state.StageFeatureValidate, state.StagePRCreate, state.StagePRMerge, state.StageSpikeMaterialize,
	} {
		for _, predicted := range []string{"", "haiku", "sonnet", "opus", "fable"} {
			s := testScheduler(t)
			want := s.resolveDispatchModel(stage, 1, dir, predicted, routing.Decision{}, nil, "", routing.ModelLabelOverride{})
			if got := s.resolveDispatchModel(stage, 1, dir, predicted, routing.Decision{}, nil, "", unrelated); got != want {
				t.Errorf("%s/%q: unrelated labels changed %q to %q", stage, predicted, want, got)
			}
		}
	}
}

// TestResolveDispatchModel_CacheAffinity pins the #55 slot: a warm opus cache
// keeps spike-materialize on opus over a cheaper sonnet base, but never over
// an explicit selection, a label pin, an escalation or the mode ceiling.
func TestResolveDispatchModel_CacheAffinity(t *testing.T) {
	warmOpus := func() *affinityPrior {
		return &affinityPrior{previousModel: "opus", previousProvider: "anthropic",
			adapter: "claude-headless", warmPrefixTokens: 1_000_000}
	}
	parse := func(labels ...string) routing.ModelLabelOverride {
		o, _ := routing.ParseModelLabels(labels)
		return o
	}
	cases := []struct {
		name   string
		mode   string
		env    string
		esc    string
		labels routing.ModelLabelOverride
		prior  *affinityPrior
		want   string
		kept   bool
	}{
		{name: "warm cache keeps the previous model", prior: warmOpus(), want: "opus", kept: true},
		{name: "no prior leaves the base", want: "sonnet"},
		{name: "mode ceiling binds the keep", mode: "efficiency", prior: warmOpus(), want: "sonnet"},
		{name: "explicit env stage model bypasses", env: "sonnet", prior: warmOpus(), want: "sonnet"},
		{name: "retry escalation bypasses", esc: "sonnet", prior: warmOpus(), want: "sonnet"},
		{name: "label pin bypasses", labels: parse("model:pin-sonnet"), prior: warmOpus(), want: "sonnet"},
		{name: "label cap binds the keep", labels: parse("model:max-sonnet"), prior: warmOpus(), want: "sonnet"},
		{name: "provider change never keeps", prior: &affinityPrior{previousModel: "opus", previousProvider: "openai",
			adapter: "claude-headless", warmPrefixTokens: 1_000_000}, want: "sonnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolatedWorkspace(t)
			if tc.mode != "" {
				t.Setenv("NIGHTGAUGE_PERFORMANCE_MODE", tc.mode)
			}
			if tc.env != "" {
				t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_SPIKE_MATERIALIZE", tc.env)
			}
			s := testScheduler(t)
			if tc.esc != "" {
				s.retryEngine.RecordEscalation(string(state.StageSpikeMaterialize), tc.esc)
			}
			got, d := s.resolveDispatchModelWithAffinity(state.StageSpikeMaterialize, 1, dir, "sonnet",
				routing.Decision{}, nil, "", tc.labels, tc.prior)
			if got != tc.want || d.Kept != tc.kept {
				t.Errorf("model = %q kept=%v (%+v), want %q kept=%v", got, d.Kept, d, tc.want, tc.kept)
			}
			if plain := s.resolveDispatchModel(state.StageSpikeMaterialize, 1, dir, "sonnet",
				routing.Decision{}, nil, "", tc.labels); tc.prior == nil && plain != got {
				t.Errorf("nil prior %q differs from resolveDispatchModel %q", got, plain)
			}
		})
	}
}
