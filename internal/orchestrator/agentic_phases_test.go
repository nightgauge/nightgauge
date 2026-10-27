package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

var agenticFixture = map[state.PipelineStage]string{
	state.StageFeaturePlanning: "planning-success.json",
	state.StageFeatureDev:      "dev-success.json",
	state.StageFeatureValidate: "validate-success.json",
}

func loadAgenticFixture(t *testing.T, stage state.PipelineStage) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "agentic_phases", agenticFixture[stage]))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// successEvidence is what a successful run of stage leaves behind: its fresh
// handoff, the predecessor's handoff, the plan, a pushed branch.
func successEvidence(t *testing.T, stage state.PipelineStage) *AgenticEvidence {
	return &AgenticEvidence{
		Stage: stage, Issue: 4242, ExitCode: 0, GatePassed: true,
		Handoff:          loadAgenticFixture(t, stage),
		Predecessor:      map[string]any{"issue_number": float64(4242)},
		ACReconcileFresh: true, PlanFileExists: true, DecisionsChanged: true,
		WorktreeChanged: true, Pushed: true,
	}
}

func TestAgenticSpecsCoverEveryRegistryPhase(t *testing.T) {
	for stage := range state.PhaseRegistry {
		spec, ok := agenticPhaseSpecs[stage]
		if !ok {
			t.Fatalf("%s: registry stage has no spec table", stage)
		}
		names := state.RegistryPhaseNames(stage)
		known := map[string]bool{}
		for _, n := range names {
			known[n] = true
			if _, ok := spec[n]; !ok {
				t.Errorf("%s: registry phase %q has no rule", stage, n)
			}
		}
		for n, r := range spec {
			if !known[n] {
				t.Errorf("%s: rule for unknown phase %q", stage, n)
			}
			if r.kind == "" || r.verdict == nil {
				t.Errorf("%s/%s: rule has no kind or verdict", stage, n)
			}
		}
	}
	if len(agenticPhaseSpecs) != len(state.PhaseRegistry) {
		t.Errorf("spec tables for %d stages, registry has %d", len(agenticPhaseSpecs), len(state.PhaseRegistry))
	}
}

// TestAgenticReconcileObservedRatio is the AC2 assertion: a successful run of
// each agentic stage observes substantially all of its applicable phases.
func TestAgenticReconcileObservedRatio(t *testing.T) {
	const minRatio = 0.8
	for stage := range agenticFixture {
		ts := ReconcileAgenticPhases(successEvidence(t, stage))
		total := len(state.RegistryPhaseNames(stage))
		complete, skipped := 0, 0
		for _, tr := range ts {
			switch tr.Status {
			case "complete":
				complete++
			case "skipped":
				skipped++
			default:
				t.Errorf("%s/%s: unexpected status %q", stage, tr.Name, tr.Status)
			}
			if tr.Total != total {
				t.Errorf("%s/%s: total %d, want %d", stage, tr.Name, tr.Total, total)
			}
		}
		applicable := total - skipped
		ratio := float64(complete) / float64(applicable)
		t.Logf("%s: %d phases · %d observed, %d skipped, %d unobserved (ratio %.2f)",
			stage, total, complete, skipped, applicable-complete, ratio)
		if ratio < minRatio {
			t.Errorf("%s: observed ratio %.2f < %.2f", stage, ratio, minRatio)
		}
	}
}

func TestAgenticReconcileClaimsNothingWithoutEvidence(t *testing.T) {
	for stage := range agenticFixture {
		cases := map[string]func(*AgenticEvidence){
			"no handoff":   func(ev *AgenticEvidence) { ev.Handoff = nil },
			"exit nonzero": func(ev *AgenticEvidence) { ev.ExitCode = 1 },
			"gate failed":  func(ev *AgenticEvidence) { ev.GatePassed = false },
		}
		for name, mutate := range cases {
			ev := successEvidence(t, stage)
			mutate(ev)
			if got := ReconcileAgenticPhases(ev); len(got) != 0 {
				t.Errorf("%s/%s: claimed %d phases", stage, name, len(got))
			}
		}
	}
	if got := ReconcileAgenticPhases(&AgenticEvidence{Stage: state.StagePRMerge, GatePassed: true, Handoff: map[string]any{}}); got != nil {
		t.Errorf("deterministic stage got %d transitions", len(got))
	}
}

func TestAgenticReconcileHonoursDeclaredSkips(t *testing.T) {
	ev := successEvidence(t, state.StageFeatureValidate)
	ev.Handoff["skipped_phases"] = []any{map[string]any{"phase": "run-tests", "reason": "docs only"}}
	for _, tr := range ReconcileAgenticPhases(ev) {
		if tr.Name == "run-tests" && tr.Status != "skipped" {
			t.Fatalf("run-tests = %q, want skipped", tr.Status)
		}
	}
}

func TestCollectAgenticEvidenceIgnoresStaleHandoff(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)
	data, _ := os.ReadFile(filepath.Join("testdata", "agentic_phases", "planning-success.json"))
	path := filepath.Join(dir, "planning-4242.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	ev, err := CollectAgenticEvidence(root, 4242, state.StageFeaturePlanning, time.Now().Add(-time.Minute), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Handoff != nil || len(ReconcileAgenticPhases(ev)) != 0 {
		t.Fatal("stale handoff from a previous attempt was counted")
	}

	// Fresh handoff, plan present: produce-plan is proved.
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	if err := os.WriteFile(filepath.Join(root, "PLAN-4242.md"), []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, err = CollectAgenticEvidence(root, 4242, state.StageFeaturePlanning, now.Add(-time.Minute), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.PlanFileExists || ev.Handoff == nil {
		t.Fatalf("fresh evidence not collected: %+v", ev)
	}
}

func TestReconcileAgenticPhasesUpgradesOnlySilence(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)
	data, _ := os.ReadFile(filepath.Join("testdata", "agentic_phases", "planning-success.json"))
	if err := os.WriteFile(filepath.Join(dir, "planning-4242.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "PLAN-4242.md"), []byte("# plan\n"), 0o644)
	since := time.Now().Add(-time.Minute)

	rt := &state.RuntimeState{Stage: state.StageFeaturePlanning}
	// A real marker for produce-plan that failed must survive.
	rt.FailPhase(state.StageFeaturePlanning, "produce-plan", 8, 13)
	rt.CompleteStage(0, tokens.TokenCounts{}, "", "")

	var settled []string
	s := &Scheduler{onPhaseSettled: func(_ string, _ int, _, name string, _, _ int, status string) {
		settled = append(settled, name+"="+status)
	}}
	s.reconcileAgenticPhases(rt, "o/r", 4242, state.StageFeaturePlanning, root, since)

	byName := map[string]string{}
	for _, p := range rt.PhaseHistory {
		byName[p.Name] = p.Status
	}
	if byName["produce-plan"] == "complete" {
		t.Error("reconciler overwrote an evidenced record")
	}
	if byName["write-planning-context"] != "complete" || byName["complete-stage"] != "complete" {
		t.Errorf("proved phases not complete: %v", byName)
	}
	// AC4: phases with no fact stay unreported, never complete.
	if byName["self-assessment"] != "unreported" {
		t.Errorf("self-assessment = %q, want unreported", byName["self-assessment"])
	}
	if len(settled) == 0 {
		t.Error("live sink not notified")
	}
	for _, e := range settled {
		if e == "produce-plan=complete" {
			t.Error("live sink told of an overwrite that did not happen")
		}
	}
}
