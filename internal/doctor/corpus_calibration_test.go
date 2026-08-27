package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
)

func seedCorpus(t *testing.T, root string, outcomes ...learning.Outcome) {
	t.Helper()
	rec := learning.NewRecorder(root)
	for _, o := range outcomes {
		if err := rec.Record(o); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
}

func row(issue int, predicted, actual string) learning.Outcome {
	return learning.Outcome{
		IssueNumber:    issue,
		Repo:           "o/r",
		PredictedModel: predicted,
		ActualModel:    actual,
		Success:        true,
		CompletedAt:    time.Now(),
	}
}

// TestCorpusCalibration_ReportsAnUnmeasurableCorpus is the finding the last AC
// asks for: >= 10 rows and zero measurable model pairs.
//
// This is the exact state of this repository's real corpus — 12 rows, every one
// with an empty predictedModel — which every consumer reported politely as "no
// data" while the model-routing feedback signal had never once been produced.
func TestCorpusCalibration_ReportsAnUnmeasurableCorpus(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i <= 12; i++ {
		rows = append(rows, row(i, "", "")) // both halves empty, as in the real corpus
	}
	seedCorpus(t, root, rows...)

	item, warning := checkCorpusCalibration(root)

	if item.OK {
		t.Error("12 rows with zero measurable model pairs reported OK — this is the exact " +
			"state the corpus was in for its entire life")
	}
	if !strings.Contains(warning, "corpus-calibration-nodata") {
		t.Errorf("warning lacks the stable identifier: %q", warning)
	}
	if !strings.Contains(warning, "12") {
		t.Errorf("warning does not report the row count that distinguishes this from a "+
			"young corpus: %q", warning)
	}
}

// TestCorpusCalibration_OneMeasurablePairIsEnough is the control the AC names
// explicitly: a corpus with >= 1 measurable pair must produce no finding.
func TestCorpusCalibration_OneMeasurablePairIsEnough(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i <= 11; i++ {
		rows = append(rows, row(i, "", ""))
	}
	rows = append(rows, row(12, "sonnet", "sonnet")) // one measurable pair
	seedCorpus(t, root, rows...)

	item, warning := checkCorpusCalibration(root)
	if !item.OK {
		t.Errorf("a corpus with a measurable pair was reported as a finding: %s", item.Error)
	}
	if warning != "" {
		t.Errorf("unexpected warning: %q", warning)
	}
}

// TestCorpusCalibration_YoungCorpusIsNotAFinding guards the noise direction. A
// fresh workspace legitimately has no measurable pairs, and an arm that fired
// on those would be muted before it ever caught the real defect.
func TestCorpusCalibration_YoungCorpusIsNotAFinding(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i < minCorpusRowsForCalibrationFinding; i++ {
		rows = append(rows, row(i, "", ""))
	}
	seedCorpus(t, root, rows...)

	item, warning := checkCorpusCalibration(root)
	if !item.OK {
		t.Errorf("a corpus below the %d-row floor was reported as a finding: %s",
			minCorpusRowsForCalibrationFinding, item.Error)
	}
	if warning != "" {
		t.Errorf("unexpected warning for a young corpus: %q", warning)
	}
}

// TestCorpusCalibration_EmptyWorkspaceIsHealthy guards the common case.
func TestCorpusCalibration_EmptyWorkspaceIsHealthy(t *testing.T) {
	item, warning := checkCorpusCalibration(t.TempDir())
	if !item.OK || warning != "" {
		t.Errorf("empty workspace reported a finding: ok=%v warning=%q", item.OK, warning)
	}
}

// TestCorpusCalibration_HalfAPairIsNotAPair guards the definition. A row with a
// prediction but no measurement (or vice versa) is excluded from the accuracy
// denominator by Calibrate — counting it here would report health the
// calibrator does not see.
func TestCorpusCalibration_HalfAPairIsNotAPair(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i <= 6; i++ {
		rows = append(rows, row(i, "sonnet", "")) // prediction, no measurement
	}
	for i := 7; i <= 12; i++ {
		rows = append(rows, row(i, "", "sonnet")) // measurement, no prediction
	}
	seedCorpus(t, root, rows...)

	item, _ := checkCorpusCalibration(root)
	if item.OK {
		t.Error("12 rows of half-pairs reported OK — Calibrate counts none of them, so " +
			"the corpus is still unmeasurable")
	}
}

// TestMinCorpusRowsFloor_IsAboveZero guards the constant itself. A floor of 0
// would fire on every fresh workspace and be muted before it ever caught the
// real defect; a floor above the corpus's realistic size would never fire.
func TestMinCorpusRowsFloor_IsAboveZero(t *testing.T) {
	if minCorpusRowsForCalibrationFinding <= 0 {
		t.Fatal("a floor of 0 fires on every fresh workspace")
	}
	if minCorpusRowsForCalibrationFinding > 50 {
		t.Errorf("floor of %d is high enough that a real workspace may never reach it",
			minCorpusRowsForCalibrationFinding)
	}
}
