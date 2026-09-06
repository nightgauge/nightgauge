package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
)

func writeSizePlanningContext(t *testing.T, root string, issue int, body string) {
	t.Helper()
	path := filepath.Join(root, execution.PlanningContextRelPath(issue))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The precedence itself: label, then the planner's assessment, then the
// estimator — and nothing at all when no source produced a recognized bucket.
func TestResolveRunSize_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		board      string
		labels     []string
		planner    string
		estimator  string
		wantSize   string
		wantSource string
	}{
		{"label wins over everything", "", []string{"size:S"}, "L", "XL", "S", SizeSourceLabel},
		{"board field is a label-class term", "L", []string{"size:S"}, "XL", "M", "L", SizeSourceLabel},
		{"planner when the issue has no size term", "", []string{"type:bug"}, "L", "XS", "L", SizeSourcePlanner},
		{"estimator last", "", nil, "", "M", "M", SizeSourceEstimator},
		{"nothing recognized is nothing", "", []string{"size:HUGE"}, "medium", "enormous", "", ""},
		{"case is normalized", "", nil, "l", "", "L", SizeSourcePlanner},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRunSize(tc.board, tc.labels, tc.planner, tc.estimator)
			if got.Size != tc.wantSize || got.Source != tc.wantSource {
				t.Errorf("ResolveRunSize = %q/%q, want %q/%q", got.Size, got.Source, tc.wantSize, tc.wantSource)
			}
		})
	}
}

// A label the planner disagrees with is the most informative row in the corpus,
// and the only way it survives is for BOTH halves to be recorded. Nothing
// relabels: the label is the human's input to the router.
func TestResolveRunSize_RecordsDisagreementWithoutResolvingIt(t *testing.T) {
	res := ResolveRunSize("", []string{"size:S"}, "L", "")
	if res.Size != "S" || res.Source != SizeSourceLabel {
		t.Fatalf("resolved %q/%q, want the LABEL to win", res.Size, res.Source)
	}
	if res.PlannerSize != "L" {
		t.Errorf("PlannerSize = %q, want \"L\" — the disagreement must survive the resolution", res.PlannerSize)
	}
	if !res.Disagrees() {
		t.Error("Disagrees() = false for size:S against a planner assessment of L")
	}
	if agreeing := ResolveRunSize("", []string{"size:L"}, "L", ""); agreeing.Disagrees() {
		t.Error("Disagrees() = true where both sources say L")
	}
}

// The corpus prediction is derived differently per source, and that is
// deliberate: the router SCORED the label, so the score is the prediction
// there; it never saw the planner's assessment, so bucketing that run's score
// would record the router's M default under a new name.
func TestOutcomePredictedSize_PerSource(t *testing.T) {
	label := ResolveRunSize("", []string{"size:XL"}, "", "")
	if got := OutcomePredictedSize(label, 8); got != "large" {
		t.Errorf("label-sourced = %q, want \"large\" (the score's bucket)", got)
	}
	if got := OutcomePredictedSize(label, 0); got != "" {
		t.Errorf("label-sourced but unscored = %q, want \"\"", got)
	}

	// #1515's headline case, matching the real specimen: #1429 —
	// complexity score 3 (the M default) and no size label, so predictedSize
	// was "" while the plan had assessed a size all along.
	planner := ResolveRunSize("", []string{"type:bug"}, "L", "")
	if got := OutcomePredictedSize(planner, 3); got != "medium" {
		t.Errorf("planner-sourced L = %q, want \"medium\" — the assessment's own base score (5), NOT the "+
			"router's default score of 3, which would record the default as a prediction", got)
	}
	if got := OutcomePredictedSize(ResolveRunSize("", nil, "XS", ""), 3); got != "small" {
		t.Errorf("planner-sourced XS = %q, want \"small\"", got)
	}

	// The estimator fills the RECORD's join key and nothing else. It is a
	// second reading of the same metadata the prediction would be scored
	// against, and it is available for nearly every run — admitting it would
	// fill the accuracy denominator with rows that measure arithmetic.
	est := ResolveRunSize("", nil, "", "L")
	if est.Size != "L" {
		t.Fatalf("estimator source did not resolve: %+v", est)
	}
	if got := OutcomePredictedSize(est, 5); got != "" {
		t.Errorf("estimator-sourced = %q, want \"\" — it is a record join key, never a scored prediction", got)
	}
	if got := OutcomePredictedSize(ResolveRunSize("", nil, "", ""), 5); got != "" {
		t.Errorf("no source = %q, want \"\"", got)
	}
}

// Provenance for a value that was never recorded reads, to a consumer scanning
// populated fields, as a row that has a prediction.
func TestOutcomeSizeSource_EmptyWheneverThePredictionIs(t *testing.T) {
	if got := OutcomeSizeSource(ResolveRunSize("", []string{"size:M"}, "", ""), 5); got != SizeSourceLabel {
		t.Errorf("OutcomeSizeSource = %q, want %q", got, SizeSourceLabel)
	}
	if got := OutcomeSizeSource(ResolveRunSize("", nil, "L", ""), 3); got != SizeSourcePlanner {
		t.Errorf("OutcomeSizeSource = %q, want %q", got, SizeSourcePlanner)
	}
	if got := OutcomeSizeSource(ResolveRunSize("", nil, "", "L"), 3); got != "" {
		t.Errorf("OutcomeSizeSource = %q for an estimator row whose predictedSize is empty, want \"\"", got)
	}
	if got := OutcomeSizeSource(ResolveRunSize("", []string{"size:M"}, "", ""), 0); got != "" {
		t.Errorf("OutcomeSizeSource = %q for an unscored row, want \"\"", got)
	}
}

// The score path is an EXACT inverse of the router's Fibonacci scale, never a
// nearest match — an invented size reaches the corpus indistinguishable from a
// real one.
func TestPlannerSizeFromAssessment(t *testing.T) {
	if got := PlannerSizeFromAssessment("m", 8); got != "M" {
		t.Errorf("= %q, want \"M\" — an explicit size_label outranks the score", got)
	}
	for score, want := range map[int]string{1: "XS", 2: "S", 3: "M", 5: "L", 8: "XL"} {
		if got := PlannerSizeFromAssessment("", score); got != want {
			t.Errorf("score %d = %q, want %q", score, got, want)
		}
	}
	for _, off := range []int{0, 4, 6, 7, 9} {
		if got := PlannerSizeFromAssessment("", off); got != "" {
			t.Errorf("score %d = %q, want \"\" — off the Fibonacci scale names no bucket", off, got)
		}
	}
}

// The estimator always names a bucket (its score is clamped to [1,10]), so an
// UNGATED estimator source would make the resolved size never absent and retire
// the #112 "no size at all" warning into dead code.
func TestEstimatorSize_GatedOnItsOwnConfidence(t *testing.T) {
	if got := EstimatorSize("Fix a typo", "", []string{"type:docs"}); got != "" {
		t.Errorf("EstimatorSize with one signal = %q, want \"\" — a \"low\" confidence is the estimator "+
			"saying it had nothing to work with, and rule 2 spells that \"\"", got)
	}
	body := "Some real detail about the change.\n- [ ] one\n- [ ] two\n- [ ] three\n"
	if got := EstimatorSize("Add a redis-backed rate limiter to the api", body, []string{"type:feature"}); got == "" {
		t.Error("EstimatorSize with title + body + checklist + labels = \"\", want a bucket")
	}
}

// RunSizeResolution is the function BOTH terminal writers call, and it reads
// the planner's assessment off the run's own planning-{N}.json.
func TestRunSizeResolution_ReadsThePlanFromDisk(t *testing.T) {
	root := t.TempDir()
	writeSizePlanningContext(t, root, 1429, `{"complexity_assessment":{"size_label":"L","computed_score":5}}`)

	res := RunSizeResolution(root, "", "acme/widget", 1429,
		"", []string{"type:bug", "priority:p1"}, "learning: the planner assesses a size on every run", "")
	if res.Source != SizeSourcePlanner || res.Size != "L" {
		t.Fatalf("resolved %q/%q, want L from the planner — this is the #1429 specimen, whose "+
			"outcome row recorded predictedSize \"\" with a plan on disk that said L", res.Size, res.Source)
	}
	if got := OutcomePredictedSize(res, 3); got != "medium" {
		t.Errorf("predictedSize = %q, want \"medium\"", got)
	}
}

// The #112 warning must fire only when ALL THREE sources are absent. A warning
// that fires on nearly every run is a warning an operator learns to scroll past
// — which is what it had become.
func TestRunSizeResolution_AbsentFromEverySource(t *testing.T) {
	root := t.TempDir()
	res := RunSizeResolution(root, "", "acme/widget", 7, "", []string{"type:bug"}, "Fix it", "")
	if res.Size != "" || res.Source != "" {
		t.Errorf("resolved %q/%q with no label, no plan and one estimator signal; want absent", res.Size, res.Source)
	}
	LogSizeResolution(7, res) // must not panic on the absent arm
}

type fakeLabeler struct {
	calls []string
	err   error
}

func (f *fakeLabeler) ApplySizeLabel(_ context.Context, owner, repo string, number int, size string) error {
	f.calls = append(f.calls, owner+"/"+repo+"#"+strconv.Itoa(number)+"="+size)
	return f.err
}

// The label write: a size-less issue gets the planner's assessment, in one call.
func TestBackfillPlannerSizeLabel_AppliesToSizelessIssue(t *testing.T) {
	root := t.TempDir()
	writeSizePlanningContext(t, root, 1429, `{"complexity_assessment":{"size_label":"L","computed_score":5}}`)
	labeler := &fakeLabeler{}

	got := BackfillPlannerSizeLabel(context.Background(), labeler,
		"acme/widget", 1429, []string{"type:bug"}, root, "")
	if got.Verdict != SizeLabelApplied || got.Size != "L" {
		t.Fatalf("verdict %q size %q, want %q/L", got.Verdict, got.Size, SizeLabelApplied)
	}
	if len(labeler.calls) != 1 || labeler.calls[0] != "acme/widget#1429=L" {
		t.Errorf("forge calls = %v, want exactly one applying L", labeler.calls)
	}
}

// It NEVER overwrites an existing label, agreeing or not. The label is the
// human's input to the router; the disagreement is recorded on the run record
// instead, and is not resolved by relabelling.
func TestBackfillPlannerSizeLabel_NeverRelabels(t *testing.T) {
	root := t.TempDir()
	writeSizePlanningContext(t, root, 12, `{"complexity_assessment":{"size_label":"L"}}`)

	for _, labels := range [][]string{{"size:S"}, {"size:L"}, {"size:HUGE"}} {
		labeler := &fakeLabeler{}
		got := BackfillPlannerSizeLabel(context.Background(), labeler, "acme/widget", 12, labels, root, "")
		if got.Verdict != SizeLabelAlreadyPresent {
			t.Errorf("labels %v: verdict %q, want %q", labels, got.Verdict, SizeLabelAlreadyPresent)
		}
		if len(labeler.calls) != 0 {
			t.Errorf("labels %v: wrote %v to the forge — an existing size label is never replaced", labels, labeler.calls)
		}
	}
}

// No assessment, no write — and no fabricated size either.
func TestBackfillPlannerSizeLabel_NoAssessment(t *testing.T) {
	root := t.TempDir()
	writeSizePlanningContext(t, root, 3, `{"complexity_assessment":{"size_label":null,"computed_score":4}}`)
	labeler := &fakeLabeler{}

	got := BackfillPlannerSizeLabel(context.Background(), labeler, "acme/widget", 3, nil, root, "")
	if got.Verdict != SizeLabelNoAssessment {
		t.Errorf("verdict %q, want %q — score 4 is off the Fibonacci scale and names no bucket", got.Verdict, SizeLabelNoAssessment)
	}
	if len(labeler.calls) != 0 {
		t.Errorf("wrote %v to the forge with nothing to write", labeler.calls)
	}
}

// A forge refusal is reported, never fatal: a run whose size label could not be
// written is exactly as well off as every run before this existed.
func TestBackfillPlannerSizeLabel_ForgeFailureIsNonFatal(t *testing.T) {
	root := t.TempDir()
	writeSizePlanningContext(t, root, 4, `{"complexity_assessment":{"size_label":"M"}}`)
	labeler := &fakeLabeler{err: errors.New("REST 422: label size:M not found")}

	got := BackfillPlannerSizeLabel(context.Background(), labeler, "acme/widget", 4, nil, root, "")
	if got.Verdict != SizeLabelFailed || got.Err == nil {
		t.Errorf("verdict %q err %v, want %q with the cause carried", got.Verdict, got.Err, SizeLabelFailed)
	}
}

// A repo slug that is not owner/name cannot address the REST endpoint, and a
// half-built path must not be sent.
func TestBackfillPlannerSizeLabel_RequiresOwnerName(t *testing.T) {
	root := t.TempDir()
	writeSizePlanningContext(t, root, 5, `{"complexity_assessment":{"size_label":"M"}}`)
	labeler := &fakeLabeler{}

	if got := BackfillPlannerSizeLabel(context.Background(), labeler, "widget", 5, nil, root, ""); got.Verdict == SizeLabelApplied {
		t.Error("applied a label against a repo slug with no owner")
	}
	if len(labeler.calls) != 0 {
		t.Errorf("wrote %v against a bare repo name", labeler.calls)
	}
}
