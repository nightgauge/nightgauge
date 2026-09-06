package ipc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The specimen this issue was filed from: a sibling repository's issue #1429
// recorded predictedSize "" with complexityScore 3 and actualSize "small",
// while the run's OWN plan on disk had assessed a size. The extension path is
// the one that wrote that row.
func TestLearningOutcomeFor_PlannerSizeReachesTheCorpus(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, execution.PlanningContextRelPath(1429))
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath,
		[]byte(`{"complexity_assessment":{"size_label":"L","computed_score":5}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cls := issueClassification{
		Labels:          []string{"type:bug", "priority:p1"},
		ComplexityScore: 3,
		Title:           "learning: the planner assesses a size on every run",
	}
	sizeRes := orchestrator.RunSizeResolution(root, "", "acme/widget", 1429,
		"", cls.Labels, cls.Title, "")

	rec := state.V2RunRecord{
		IssueNumber: 1429,
		Repo:        "acme/widget",
		Outcome:     "complete",
		Routing:     state.V2Routing{ComplexityScore: 3},
		CompletedAt: time.Now().Format(time.RFC3339),
	}
	o, decision := learningOutcomeFor(rec, cls, sizeRes, nil, "acme/widget", time.Now())
	if decision != outcomeRecord {
		t.Fatalf("decision = %v, want %v", decision, outcomeRecord)
	}
	if o.PredictedSize != "medium" {
		t.Errorf("PredictedSize = %q, want \"medium\" — this run's plan assessed L and the corpus threw it away", o.PredictedSize)
	}
	if o.SizeSource != orchestrator.SizeSourcePlanner {
		t.Errorf("SizeSource = %q, want %q — a corpus field with two sources and no discriminator has two meanings",
			o.SizeSource, orchestrator.SizeSourcePlanner)
	}
}

// A label still outranks the planner, and the source says so.
func TestLearningOutcomeFor_LabelStillWins(t *testing.T) {
	root := t.TempDir()
	cls := issueClassification{Labels: []string{"size:XL"}, ComplexityScore: 8}
	sizeRes := orchestrator.RunSizeResolution(root, "", "acme/widget", 7, "", cls.Labels, "T", "")

	rec := state.V2RunRecord{IssueNumber: 7, Repo: "acme/widget", Outcome: "complete"}
	o, _ := learningOutcomeFor(rec, cls, sizeRes, nil, "acme/widget", time.Now())
	if o.PredictedSize != "large" || o.SizeSource != orchestrator.SizeSourceLabel {
		t.Errorf("PredictedSize/%s = %q/%q, want large/label", "SizeSource", o.PredictedSize, o.SizeSource)
	}
}

// Both writers must reach the same answer from the same evidence — the whole
// reason the precedence lives in orchestrator and not in either handler.
func TestSizeResolution_BothWritersAgree(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, execution.PlanningContextRelPath(42))
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte(`{"complexity_assessment":{"fibonacci_score":8}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	labels := []string{"type:feature"}

	extension := orchestrator.RunSizeResolution(root, "", "acme/widget", 42, "", labels, "Title", "")
	scheduler := orchestrator.RunSizeResolution(root, "", "acme/widget", 42, "", labels, "Title", "")
	if extension != scheduler {
		t.Fatalf("the two writers resolved differently: %+v vs %+v", extension, scheduler)
	}
	if extension.Size != "XL" || extension.Source != orchestrator.SizeSourcePlanner {
		t.Errorf("resolved %q/%q, want XL from the planner (fibonacci_score 8)", extension.Size, extension.Source)
	}
}
