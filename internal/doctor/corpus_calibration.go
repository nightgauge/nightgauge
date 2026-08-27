package doctor

import (
	"fmt"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
)

// minCorpusRowsForCalibrationFinding is how many recorded runs must exist
// before an unmeasurable corpus is worth reporting.
//
// Below this, "zero measurable model pairs" is indistinguishable from a young
// corpus and reporting it would be noise on every fresh workspace. At or above
// it, the pipeline has run enough times that a total absence of prediction data
// is a defect rather than a start-up condition.
const minCorpusRowsForCalibrationFinding = 10

// checkCorpusCalibration reports an outcome corpus that has accumulated runs
// but cannot calibrate model routing, because no row carries BOTH halves of the
// predicted/actual pair (#994).
//
// This absence was invisible by construction, and correctly so at every layer
// that could have shown it: `Calibrate` excludes a pair with an empty half
// rather than booking a false miss, `ratio()` returns nil rather than 0, and
// `analyzeCalibration` reports "no data". Every one of those guards is right
// and none is a bug — which is exactly why the self-improvement loop's
// model-routing signal was never once produced in the corpus's entire life and
// every consumer described it politely as having nothing to say.
//
// "No data" from a young corpus and "no data" from a broken writer render
// identically. This arm is the one place that distinguishes them: it keys on
// ROW COUNT, which the polite consumers ignore.
func checkCorpusCalibration(workspaceRoot string) (CheckItem, string) {
	if workspaceRoot == "" {
		return CheckItem{OK: true, Detail: "outcome corpus not checked (no workspace root)"}, ""
	}

	outcomes, err := learning.NewRecorder(workspaceRoot).LoadAll()
	if err != nil {
		// A corpus that cannot be read is not a corpus with no problems.
		msg := fmt.Sprintf("outcome corpus unverifiable: %v", err)
		return CheckItem{OK: false, Detail: "could not read the outcome corpus", Error: msg}, msg
	}
	if len(outcomes) < minCorpusRowsForCalibrationFinding {
		return CheckItem{
			OK: true,
			Detail: fmt.Sprintf("outcome corpus has %d row(s); below the %d-row floor for a calibration finding",
				len(outcomes), minCorpusRowsForCalibrationFinding),
		}, ""
	}

	modelPairs, sizePairs := 0, 0
	for _, o := range outcomes {
		if o.PredictedModel != "" && o.ActualModel != "" {
			modelPairs++
		}
		if o.PredictedSize != "" && o.ActualSize != "" {
			sizePairs++
		}
	}
	if modelPairs > 0 {
		return CheckItem{
			OK: true,
			Detail: fmt.Sprintf("outcome corpus: %d row(s), %d measurable model pair(s), %d size pair(s)",
				len(outcomes), modelPairs, sizePairs),
		}, ""
	}

	msg := fmt.Sprintf("corpus-calibration-nodata: %d recorded run(s) and ZERO measurable model pairs — "+
		"model-routing calibration has no data to learn from and every consumer reports it as "+
		"\"no data\" rather than as a defect. Check that issue-{N}.json is being found at "+
		"recording time (`nightgauge learn report` shows the corpus)",
		len(outcomes))
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("%d corpus row(s), 0 measurable model pairs", len(outcomes)),
		Error:  msg,
	}, msg
}
