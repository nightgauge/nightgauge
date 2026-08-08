// Package learning implements the pipeline learning system: outcome recording,
// calibration feedback, and model performance tracking.
package learning

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Outcome records the result of a pipeline run for calibration.
//
// The predicted/actual pairs are the whole point of the corpus, and both of
// their writers derive them through the shared helpers in
// internal/orchestrator/outcome_semantics.go — read that file before changing
// any of these four fields. The contract in one line: ONE vocabulary per pair,
// EMPTY means unknown (never a plausible default), and an `Actual*` is a
// MEASUREMENT of what the run produced, never a second reading of the same
// pre-run inputs the prediction came from.
type Outcome struct {
	IssueNumber int    `json:"issueNumber"`
	Repo        string `json:"repo"`
	// PredictedSize is the router's pre-run size estimate, as
	// small|medium|large. Empty when the issue carried no size input to
	// predict from (the board Size field or a size:* label —
	// orchestrator.OutcomeSizeInput) — the complexity score defaults to the M
	// base score in that case, so scoring it would record the default as a
	// prediction.
	PredictedSize string `json:"predictedSize"`
	// ActualSize is how big the change the run produced turned out to be, in
	// the same small|medium|large vocabulary, bucketed from lines ACTUALLY
	// changed (the definition in github.OutcomeService.getActualSizeBucket).
	//
	// No terminal recording boundary carries that measurement today, so both
	// writers leave it empty and every consumer excludes the pair. It is NOT
	// the issue's size:* label rebucketed: that is one of the same pre-run
	// inputs PredictedSize is derived from, so the comparison would measure the
	// scoring arithmetic rather than the run.
	ActualSize string `json:"actualSize,omitempty"`
	// PredictedModel is the router's pickup recommendation for the
	// implementation stage, as a registry band (haiku|sonnet|opus|fable) — and
	// NOTHING ELSE (orchestrator.OutcomeModelBand). A model reference the
	// registry has no band for records "", not the id verbatim: this pair is
	// compared for EQUALITY against a band, so a verbatim id
	// ("gemini-2.0-flash", a user-defined local model, #56) is not attribution
	// the corpus keeps — it is a guaranteed MISS the router never made, which
	// rule 2 below exists to prevent. Attribution of what actually ran is kept
	// in the run record's per-stage `model_selection`, which carries the
	// concrete id. Also empty when the router made no recommendation.
	PredictedModel string `json:"predictedModel"`
	// ActualModel is the band the IMPLEMENTATION stage actually served — the
	// stage PredictedModel is a recommendation for — produced by
	// orchestrator.OutcomeActualBand(served, predicted).
	//
	// It is an INVERSION of the adapter mapping against the prediction, not a
	// strongest-band collapse. Go dispatches a band; the extension translates it
	// at the last mile (codex opus → gpt-5.6-sol) and reports the concrete id
	// back. Those ids are multi-band — gpt-5.6-sol serves [opus, fable] — so
	// collapsing to the strongest band would read "fable" for a run the router
	// predicted "opus" and the adapter served exactly as asked, booking every
	// codex/gemini/copilot run as a routing MISS. When the served id serves the
	// PREDICTED band, that is the band it was launched for.
	//
	// Empty in three cases, all of them honest unknowns excluded from every
	// consumer's denominator: the stage never ran, it reported no model, or the
	// served id has no registry band at all. Never a copy of PredictedModel:
	// that made every row of this writer's history a tautological routing HIT.
	ActualModel     string    `json:"actualModel"`
	Success         bool      `json:"success"`
	DurationMs      int64     `json:"durationMs"`
	InputTokens     int       `json:"inputTokens"`
	OutputTokens    int       `json:"outputTokens"`
	CostUSD         float64   `json:"costUsd"`
	ComplexityScore int       `json:"complexityScore"`
	Retries         int       `json:"retries"`
	FailedStage     string    `json:"failedStage,omitempty"`
	CompletedAt     time.Time `json:"completedAt"`
}

// Recorder persists outcomes to a JSONL file for calibration.
type Recorder struct {
	mu       sync.Mutex
	filePath string
}

// NewRecorder creates an outcome recorder at the workspace root.
func NewRecorder(workspaceRoot string) *Recorder {
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	return &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}
}

// Record appends an outcome to the JSONL file.
func (r *Recorder) Record(outcome Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(r.filePath), 0755); err != nil {
		return fmt.Errorf("create outcome dir: %w", err)
	}

	data, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("marshal outcome: %w", err)
	}

	f, err := os.OpenFile(r.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open outcome file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write outcome: %w", err)
	}
	return nil
}

// LoadAll reads all recorded outcomes.
func (r *Recorder) LoadAll() ([]Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read outcomes: %w", err)
	}

	var outcomes []Outcome
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var o Outcome
		if err := json.Unmarshal(line, &o); err != nil {
			continue // Skip malformed lines
		}
		outcomes = append(outcomes, o)
	}
	return outcomes, nil
}

// CalibrationReport summarizes prediction accuracy for tuning.
//
// The two accuracies are POINTERS, and nil is load-bearing: it means "no row in
// this corpus carried both halves of that pair, so there is nothing to measure"
// — which a 0.0 cannot express and every consumer previously read as "the
// router is wrong every time". `nightgauge learn tune` emits them as JSON
// `null` and declines to tune an unmeasurable target.
type CalibrationReport struct {
	TotalRuns int `json:"totalRuns"`
	// SizeSamples / ModelSamples are the DENOMINATORS: rows where both halves
	// of the pair are non-empty. Always reported, so a caller can tell a
	// confident number from one computed over three rows.
	SizeSamples    int      `json:"sizeSamples"`
	SizeAccuracy   *float64 `json:"sizeAccuracy"` // fraction of SizeSamples where predicted == actual; nil when SizeSamples == 0
	ModelSamples   int      `json:"modelSamples"`
	ModelAccuracy  *float64 `json:"modelAccuracy"` // fraction of ModelSamples where predicted == actual; nil when ModelSamples == 0
	AvgCostPerRun  float64  `json:"avgCostPerRun"`
	SuccessRate    float64  `json:"successRate"`
	TrendImproving bool     `json:"trendImproving"`
}

// Calibrate analyzes recorded outcomes and produces a calibration report.
//
// A row counts toward an accuracy ONLY when BOTH halves of that pair are
// non-empty. Without that guard the corpus's own conventions poisoned the
// numbers in both directions: rows with two empty model fields scored a HIT
// ("" == ""), so pre-#304 history reported modelAccuracy 1.0 from eight rows
// that measured nothing, while rows with an unmeasurable actual size were
// counted as size MISSES, dragging sizeAccuracy toward 0 in proportion to how
// many issues lacked a size label rather than to how well the router predicted.
// Mixed old/new history needs no discriminator field for this: a legacy row is
// excluded by the same guard, because its halves are exactly the ones that are
// empty.
func (r *Recorder) Calibrate() (*CalibrationReport, error) {
	outcomes, err := r.LoadAll()
	if err != nil {
		return nil, err
	}
	if len(outcomes) == 0 {
		return &CalibrationReport{}, nil
	}

	var sizeMatches, sizeSamples, modelMatches, modelSamples, successes int
	var totalCost float64

	for _, o := range outcomes {
		if o.PredictedSize != "" && o.ActualSize != "" {
			sizeSamples++
			if o.PredictedSize == o.ActualSize {
				sizeMatches++
			}
		}
		if o.PredictedModel != "" && o.ActualModel != "" {
			modelSamples++
			if o.PredictedModel == o.ActualModel {
				modelMatches++
			}
		}
		if o.Success {
			successes++
		}
		totalCost += o.CostUSD
	}

	n := len(outcomes)
	report := &CalibrationReport{
		TotalRuns:     n,
		SizeSamples:   sizeSamples,
		SizeAccuracy:  ratio(sizeMatches, sizeSamples),
		ModelSamples:  modelSamples,
		ModelAccuracy: ratio(modelMatches, modelSamples),
		AvgCostPerRun: totalCost / float64(n),
		SuccessRate:   float64(successes) / float64(n),
	}

	// Check trend: compare recent 10 vs previous 10
	if n >= 20 {
		recentStart := n - 10
		prevStart := n - 20
		recentSuccesses := 0
		prevSuccesses := 0
		for i := recentStart; i < n; i++ {
			if outcomes[i].Success {
				recentSuccesses++
			}
		}
		for i := prevStart; i < recentStart; i++ {
			if outcomes[i].Success {
				prevSuccesses++
			}
		}
		report.TrendImproving = recentSuccesses > prevSuccesses
	}

	return report, nil
}

// ratio returns matches/samples, or nil when there are no samples. Callers must
// not substitute 0: "nothing measurable" and "measured, and always wrong" are
// different findings and the loops act on them differently.
func ratio(matches, samples int) *float64 {
	if samples == 0 {
		return nil
	}
	v := float64(matches) / float64(samples)
	return &v
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
