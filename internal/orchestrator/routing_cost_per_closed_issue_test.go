package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
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

// plannerSizingRunner is runIDCapturingRunner that plays issue-pickup and
// feature-planning's file contract: pickup writes an issue context routed from
// the assumed M, and planning writes a plan that assesses the issue as L.
type plannerSizingRunner struct {
	runIDCapturingRunner
	root string
}

func (r *plannerSizingRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	// The base runner writes a generic payload to the stage's OutputFile —
	// for these two stages that IS the file below — so write after it.
	out, err := r.runIDCapturingRunner.RunStage(ctx, params)
	dirs := []string{r.root}
	if params.WorktreePath != "" && params.WorktreePath != r.root {
		dirs = append(dirs, params.WorktreePath)
	}
	for _, dir := range dirs {
		switch params.Stage {
		case state.StageIssuePickup:
			writeRunFixture(dir, execution.IssueContextRelPath(params.IssueNumber), fmt.Sprintf(`{
  "issue_number": %d,
  "routing": {
    "change_type": "code",
    "complexity_score": 3,
    "suggested_route": "standard",
    "skip_stages": [],
    "rationale": "M size assumed",
    "pickup_recommendation": {"dev_model": ""}
  }
}`, params.IssueNumber))
		case state.StageFeaturePlanning:
			writeRunFixture(dir, execution.PlanningContextRelPath(params.IssueNumber),
				`{"complexity_assessment":{"size_label":"L","computed_score":5,"documentation_scope":"extended"}}`)
		}
	}
	return out, err
}

// writeRunFixture writes a stage's output file from inside a dispatch, where
// no *testing.T is in reach; a write failure panics the run, loudly.
func writeRunFixture(root, rel, body string) {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		panic(err)
	}
}

// TestRunPipeline_PlannerSizeReachesFeatureDevDispatch drives a real
// runPipeline for an unsized issue (#1909's #1643 shape). Pickup routes from
// the assumed M, which never selects Opus; the planner assesses L. The
// re-derived Decision must reach feature-dev's DISPATCH model — and only
// feature-dev's — and must be written into the issue context the later stages
// read, and into the run record.
func TestRunPipeline_PlannerSizeReachesFeatureDevDispatch(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &plannerSizingRunner{root: root}
	s := newRunIdentityTestScheduler(t, root, runner)
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	const issue = 991909
	s.runPipeline(context.Background(), types.BoardItem{Number: issue, Repo: "nightgauge/nightgauge", ID: "item-991909",
		Title: "t", Labels: []string{"type:feature", "component:go-binary"}})
	if snap == nil {
		t.Fatal("the pipeline never completed; the fixture is wrong, not the assertion")
	}

	models := map[state.PipelineStage]string{}
	for _, c := range runner.captured() {
		models[c.Stage] = c.Model
	}
	if models[state.StageFeatureDev] != "opus" {
		t.Errorf("feature-dev dispatched on %q, want opus from the planner's L", models[state.StageFeatureDev])
	}
	for _, stage := range []state.PipelineStage{state.StageFeaturePlanning, state.StageFeatureValidate} {
		if got := models[stage]; got != "sonnet" {
			t.Errorf("%s dispatched on %q, want sonnet (the size rule is feature-dev's only)", stage, got)
		}
	}

	// The issue context the later stages read carries the re-derived route
	// and complexity, and keeps its skip set and dev_model.
	var ctxDoc struct {
		Routing map[string]any `json:"routing"`
	}
	path := resolveIssueContextPath(root, snap.WorktreeDir, "nightgauge/nightgauge", issue)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read issue context: %v", err)
	}
	if err := json.Unmarshal(data, &ctxDoc); err != nil {
		t.Fatal(err)
	}
	if ctxDoc.Routing["suggested_route"] != "extensive" || ctxDoc.Routing["complexity_score"] != float64(5) {
		t.Errorf("issue context routing = %v, want suggested_route extensive, complexity_score 5", ctxDoc.Routing)
	}
	if skips, _ := ctxDoc.Routing["skip_stages"].([]any); len(skips) != 0 {
		t.Errorf("issue context skip_stages = %v, want the run's starting (empty) set", skips)
	}

	// The run record carries the planner's size, not the assumed M.
	recs, err := state.NewHistoryWriter(root).ReadRecentV2(10, 1)
	if err != nil {
		t.Fatalf("read run records: %v", err)
	}
	found := false
	for _, rec := range recs {
		if rec.IssueNumber != issue {
			continue
		}
		found = true
		if rec.Routing.ComplexityScore != 5 || rec.Routing.Path != "extensive" {
			t.Errorf("run record routing = %+v, want complexity 5 on the extensive route", rec.Routing)
		}
	}
	if !found {
		t.Error("no run record for the issue")
	}
}

// #1909 BLOCKER regression: the size rule must not move the run-wide tier.
// reRouteContext writes Route("feature-dev", complexity_score) into dev_model,
// and stageBaseModel applies dev_model to EVERY reasoning stage — so an L issue
// (complexity_score 5) re-routed in the default elevated mode gets Opus for
// feature-dev only, while planning and validation stay on Sonnet.
func TestReRouteContext_SizeRuleStaysOnFeatureDev(t *testing.T) {
	dir := isolatedWorkspace(t)
	makeIssueContext(t, dir, 1909, "", 5)
	makePerfModeFile(t, dir, string(routing.ModeElevated))

	s := testScheduler(t)
	rec, err := s.reRouteContext(context.Background(), dir, "", "", 1909, "")
	if err != nil {
		t.Fatalf("reRouteContext: %v", err)
	}
	if rec.Model != routing.ModelSonnet {
		t.Fatalf("re-routed dev_model = %q, want the router's sonnet (the run-wide tier is unchanged)", rec.Model)
	}
	_, _, devModel := loadIssueContext(dir, "", "", 1909)
	if devModel != routing.ModelSonnet {
		t.Fatalf("dev_model on disk = %q, want %q", devModel, routing.ModelSonnet)
	}

	sizedL := decisionFor("type:feature", "size:L")
	want := map[state.PipelineStage]string{
		state.StageFeaturePlanning: "sonnet",
		state.StageFeatureDev:      "opus",
		state.StageFeatureValidate: "sonnet",
		state.StagePRMerge:         "sonnet",
	}
	for stage, w := range want {
		if got := s.resolveDispatchModel(stage, 1909, dir, routedStageModel(stage, devModel, sizedL), nil, ""); got != w {
			t.Errorf("L issue, elevated, via reRouteContext: %s = %q, want %q", stage, got, w)
		}
	}
}
