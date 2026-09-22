package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// These pin the implementation-routing rule on the dispatch path the
// scheduler actually runs: route by expected cost per closed issue.

func decisionFor(labels ...string) routing.Decision {
	return routing.Derive(routing.DeriveInput{Title: "t", Labels: labels})
}

// A sized M+ issue dispatches feature-dev on Opus even on a first run, where
// the run-wide prediction is empty. Every other stage keeps the prediction.
func TestRoutedStageModel_ImplementationBand(t *testing.T) {
	sizedM := decisionFor("type:feature", "size:M")
	unsized := decisionFor("type:feature")
	sizedS := decisionFor("type:feature", "size:S")

	cases := []struct {
		name      string
		stage     state.PipelineStage
		predicted string
		d         routing.Decision
		want      string
	}{
		{"first run, sized M → opus", state.StageFeatureDev, "", sizedM, "opus"},
		{"predicted sonnet raised to opus", state.StageFeatureDev, "sonnet", sizedM, "opus"},
		{"predicted opus unchanged", state.StageFeatureDev, "opus", sizedM, "opus"},
		{"unsized keeps the prediction", state.StageFeatureDev, "sonnet", unsized, "sonnet"},
		{"unsized first run stays unrouted", state.StageFeatureDev, "", unsized, ""},
		{"S keeps the prediction", state.StageFeatureDev, "sonnet", sizedS, "sonnet"},
		{"validate keeps the prediction", state.StageFeatureValidate, "sonnet", sizedM, "sonnet"},
		{"planning keeps the prediction", state.StageFeaturePlanning, "", sizedM, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routedStageModel(tc.stage, tc.predicted, tc.d); got != tc.want {
				t.Errorf("routedStageModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// End to end through resolveDispatchModel: the band reaches dispatch, an
// unsized issue keeps today's default, the efficiency envelope still caps it,
// and an explicit per-stage model still wins over it.
func TestResolveDispatchModel_ImplementationBandInsideEnvelope(t *testing.T) {
	sizedM := decisionFor("type:feature", "size:M")
	unsized := decisionFor("type:feature")
	dev := state.StageFeatureDev

	dir := isolatedWorkspace(t)
	s := testScheduler(t)
	if got := s.resolveDispatchModel(dev, 1, dir, routedStageModel(dev, "", sizedM), nil, ""); got != "opus" {
		t.Errorf("elevated, sized M feature-dev = %q, want opus", got)
	}
	if got := s.resolveDispatchModel(dev, 1, dir, routedStageModel(dev, "", unsized), nil, ""); got != "sonnet" {
		t.Errorf("elevated, unsized feature-dev = %q, want today's sonnet default", got)
	}
	if got := s.resolveDispatchModel(state.StageFeatureValidate, 1, dir, routedStageModel(state.StageFeatureValidate, "", sizedM), nil, ""); got != "sonnet" {
		t.Errorf("elevated, sized M feature-validate = %q, want sonnet (unchanged)", got)
	}

	eff := isolatedWorkspace(t)
	makePerfModeFile(t, eff, "efficiency")
	if got := s.resolveDispatchModel(dev, 1, eff, routedStageModel(dev, "", sizedM), nil, ""); got != "sonnet" {
		t.Errorf("efficiency, sized M feature-dev = %q, want sonnet (the envelope caps it)", got)
	}

	manual := routedWorkspace(t, "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: sonnet\n")
	if got := s.resolveDispatchModel(dev, 1, manual, routedStageModel(dev, "", sizedM), nil, ""); got != "sonnet" {
		t.Errorf("explicit feature-dev: sonnet = %q, want the operator's sonnet", got)
	}
}

// A high-risk issue floors feature-dev and feature-validate at Opus, even an
// XS one whose size alone would never reach it; the efficiency ceiling still
// caps the floor; plumbing stages are untouched.
func TestRiskFloors_DispatchAndEnvelope(t *testing.T) {
	risky := decisionFor("type:bug", "size:XS", "security")
	plain := decisionFor("type:bug", "size:XS")

	if got := raiseRiskFloors(nil, plain); got != nil {
		t.Errorf("non-risk floors = %v, want unchanged nil", got)
	}
	cfg := map[string]string{"feature-planning": "sonnet", "feature-validate": "opus"}
	floors := raiseRiskFloors(cfg, risky)
	if floors["feature-dev"] != "opus" || floors["feature-validate"] != "opus" || floors["feature-planning"] != "sonnet" {
		t.Errorf("risk floors = %v", floors)
	}
	if _, ok := cfg["feature-dev"]; ok {
		t.Error("raiseRiskFloors mutated the configured floor map")
	}

	s := testScheduler(t)
	dir := isolatedWorkspace(t)
	for _, stage := range []state.PipelineStage{state.StageFeatureDev, state.StageFeatureValidate} {
		if got := s.resolveDispatchModel(stage, 1, dir, "sonnet", floors, ""); got != "opus" {
			t.Errorf("elevated, high-risk %s = %q, want opus", stage, got)
		}
		if got := s.resolveDispatchModel(stage, 1, dir, "sonnet", raiseRiskFloors(nil, plain), ""); got != "sonnet" {
			t.Errorf("elevated, not high-risk %s = %q, want sonnet", stage, got)
		}
	}
	if got := s.resolveDispatchModel(state.StagePRCreate, 1, dir, "sonnet", floors, ""); got != "haiku" {
		t.Errorf("high-risk pr-create = %q, want haiku (no floor)", got)
	}

	eff := isolatedWorkspace(t)
	makePerfModeFile(t, eff, "efficiency")
	for _, stage := range []state.PipelineStage{state.StageFeatureDev, state.StageFeatureValidate} {
		if got := s.resolveDispatchModel(stage, 1, eff, "sonnet", floors, ""); got != "sonnet" {
			t.Errorf("efficiency, high-risk %s = %q, want sonnet (capped)", stage, got)
		}
	}
}

func writePlannerAssessment(t *testing.T, root string, issue int, body string) {
	t.Helper()
	path := filepath.Join(root, execution.PlanningContextRelPath(issue))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// #1909 reproduced on the #1643 shape: no size label, type:feature,
// component:go-binary. Pickup routes from the assumed M; once the planner
// assesses L the run re-derives, and the route it records is the L one.
func TestPlannerRoutingDecision_RederivesAnAssumedSize(t *testing.T) {
	dir := isolatedWorkspace(t)
	item := types.BoardItem{Number: 1643, Repo: "acme/widget", Title: "t",
		Labels: []string{"type:feature", "component:go-binary"}}

	prior := deriveRoutingDecision(dir, item)
	if prior.SizeSource != routing.SizeSourceDefault || prior.SuggestedRoute != "standard" {
		t.Fatalf("pickup decision = %s from %s, want standard from default", prior.SuggestedRoute, prior.SizeSource)
	}
	if _, ok := plannerRoutingDecision(dir, "", item, prior); ok {
		t.Error("re-derived with no plan on disk")
	}

	writePlannerAssessment(t, dir, 1643, `{"complexity_assessment":{"size_label":"L","computed_score":5}}`)
	next, ok := plannerRoutingDecision(dir, "", item, prior)
	if !ok {
		t.Fatal("did not re-derive from the planner's size")
	}
	if next.SuggestedRoute != "extensive" || next.EffectiveSize != "L" || next.SizeSource != routing.SizeSourcePlanner {
		t.Errorf("re-derived = %s / %s from %s, want extensive / L from planner", next.SuggestedRoute, next.EffectiveSize, next.SizeSource)
	}
	if routing.ImplementationBand(next) != "opus" {
		t.Error("planner-assessed L did not reach the implementation band")
	}

	// A real size is never replaced by the planner's.
	labelled := item
	labelled.Labels = append([]string{"size:S"}, item.Labels...)
	if _, ok := plannerRoutingDecision(dir, "", labelled, deriveRoutingDecision(dir, labelled)); ok {
		t.Error("re-derived over a size:* label")
	}
}

// Re-derivation never newly skips a stage mid-run: a planner's XS would skip
// feature-validate on its own, but the run keeps the skip set it started with.
func TestPlannerRoutingDecision_KeepsTheStartingSkipSet(t *testing.T) {
	dir := isolatedWorkspace(t)
	item := types.BoardItem{Number: 7, Repo: "acme/widget", Title: "t", Labels: []string{"type:feature"}}
	writePlannerAssessment(t, dir, 7, `{"complexity_assessment":{"size_label":"XS","computed_score":1}}`)
	prior := deriveRoutingDecision(dir, item)
	next, ok := plannerRoutingDecision(dir, "", item, prior)
	if !ok {
		t.Fatal("did not re-derive")
	}
	if len(next.SkipStages) != len(prior.SkipStages) {
		t.Errorf("skip set changed mid-run: %v → %v", prior.SkipStages, next.SkipStages)
	}
}

// The helpers above only matter if runPipeline calls them, and no unit test can
// reach runPipeline without standing up a whole run — so pin the wiring
// structurally: dispatch passes feature-dev's routed tier through
// routedStageModel, the risk floors are raised, and the planner re-derivation
// runs.
func TestRunPipelineWiresImplementationRouting(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "scheduler.go", nil, 0)
	if err != nil {
		t.Fatalf("parse scheduler.go: %v", err)
	}
	var run *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "runPipeline" {
			run = fn
		}
	}
	if run == nil {
		t.Fatal("runPipeline not found in scheduler.go")
	}
	calls := map[string]int{}
	routedDispatch := false
	ast.Inspect(run.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		calls[name]++
		if name == "resolveDispatchModel" && len(call.Args) >= 4 {
			if inner, ok := call.Args[3].(*ast.CallExpr); ok {
				if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "routedStageModel" {
					routedDispatch = true
				}
			}
		}
		return true
	})
	if !routedDispatch {
		t.Error("runPipeline's resolveDispatchModel call does not route its predicted tier through routedStageModel")
	}
	for _, want := range []string{"raiseRiskFloors", "plannerRoutingDecision"} {
		if calls[want] == 0 {
			t.Errorf("runPipeline never calls %s", want)
		}
	}
}
