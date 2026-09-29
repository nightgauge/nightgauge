package doctor

import (
	"fmt"
	"strconv"

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
func corpusCalibrationFindings(workspaceRoot string) ([]Finding, string) {
	const check, code = "corpus_calibration", "NGD028"
	if workspaceRoot == "" {
		return nil, "outcome corpus not checked (no workspace root)"
	}

	outcomes, err := learning.NewRecorder(workspaceRoot).LoadAll()
	if err != nil {
		// A corpus that cannot be read is not a corpus with no problems.
		return []Finding{unverifiableFinding(check, code, SeverityInfo, "outcome corpus", err.Error())},
			"could not read the outcome corpus"
	}
	if len(outcomes) < minCorpusRowsForCalibrationFinding {
		return nil, fmt.Sprintf("outcome corpus has %d row(s); below the %d-row floor for a calibration finding",
			len(outcomes), minCorpusRowsForCalibrationFinding)
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
		return nil, fmt.Sprintf("outcome corpus: %d row(s), %d measurable model pair(s), %d size pair(s)",
			len(outcomes), modelPairs, sizePairs)
	}

	f := newFinding(check, code, SeverityInfo,
		fmt.Sprintf("corpus-calibration-nodata: %d recorded run(s) and ZERO measurable model pairs", len(outcomes)),
		"no corpus row carries both a predicted and an actual model, so model-routing calibration has nothing to learn from and every consumer reports it as \"no data\" rather than as a defect",
		map[string]string{
			"rows":        strconv.Itoa(len(outcomes)),
			"model_pairs": "0",
			"size_pairs":  strconv.Itoa(sizePairs),
			"floor":       strconv.Itoa(minCorpusRowsForCalibrationFinding),
		},
		[]string{workspaceRoot},
		manualRemedy("explain", "Find why outcome rows lack the predicted/actual model pair", check,
			"Run `nightgauge learn report` to see which fields the corpus rows carry",
			"Check that issue-{N}.json is found at recording time; it supplies the predicted model",
			"The gap closes when recorded runs carry both predictedModel and actualModel"))
	return []Finding{f}, fmt.Sprintf("%d corpus row(s), 0 measurable model pairs", len(outcomes))
}
