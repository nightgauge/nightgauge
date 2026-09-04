// Package learning implements the pipeline learning system: outcome recording,
// calibration feedback, and model performance tracking.
package learning

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/actualsize"
	"github.com/nightgauge/nightgauge/internal/state"
)

// OutcomeSchemaVersion is the corpus row-format marker Record stamps onto
// every row it appends — the band-retirement cutover generation (#582, spike
// #568 §5). Rows written before the cutover carry no `schema_version` key at
// all (the file was schema-less), so absence IS the legacy discriminator:
// "no marker" means a pre-envelope band-era row, "2" means post-cutover. The
// marker is row provenance, not the exclusion mechanism — accuracy
// denominators still exclude via the #340 empty-half guard in Calibrate.
const OutcomeSchemaVersion = "2"

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
	// SchemaVersion is stamped by Record (never by callers) with
	// OutcomeSchemaVersion, so post-cutover rows are deterministically
	// identifiable without vocabulary-sniffing (#582). `omitempty` is
	// load-bearing: a legacy row round-trips with the key still absent
	// instead of gaining a fabricated "".
	SchemaVersion string `json:"schema_version,omitempty"`
	IssueNumber   int    `json:"issueNumber"`
	Repo          string `json:"repo"`
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
	// The pr-create stage-exit seam captures this measurement before merge and
	// persists it on RuntimeState. Runs that never reach that seam leave it
	// empty. It is NOT the issue's size:* label rebucketed: that is one of the
	// same pre-run inputs PredictedSize is derived from, so the comparison would
	// measure the scoring arithmetic rather than the run.
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
	mu            sync.Mutex
	workspaceRoot string
	filePath      string
}

// NewRecorder creates an outcome recorder at the workspace root.
func NewRecorder(workspaceRoot string) *Recorder {
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	return &Recorder{
		workspaceRoot: workspaceRoot,
		filePath:      filepath.Join(dir, "outcomes.jsonl"),
	}
}

// Record appends an outcome to the JSONL file. It is the corpus's ONE append
// point — both writers (Scheduler.recordOutcome and the IPC
// `pipeline.notifyComplete` handler) flow through it — so the schema marker is
// stamped here, unconditionally, rather than trusted to callers.
func (r *Recorder) Record(outcome Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	outcome.SchemaVersion = OutcomeSchemaVersion

	// The autonomous scheduler's terminal call site predates #369 and receives
	// no line-count argument. Read the same persisted RuntimeState snapshot its
	// V2 writer uses, where the pr-create stage-exit seam stored the pre-merge
	// measurement. The extension writer already supplies ActualSize from its
	// built V2 record, so this is a no-op there. Match the scheduler snapshot by
	// its terminal metrics rather than taking "the latest snapshot for issue":
	// ADR-017 permits concurrent runs of one issue, and cross-run attribution is
	// worse than an absent measurement. Missing/ambiguous matches stay absent.
	if outcome.ActualSize == "" && outcome.IssueNumber > 0 && r.workspaceRoot != "" {
		outcome.ActualSize = r.actualSizeFromRuntime(outcome)
	}

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

// actualSizeFromRuntime resolves the scheduler-owned run snapshot that
// produced outcome. Outcome predates run_id, so use the fields constructed from
// that same snapshot: repo, token/cost totals and elapsed time. Refuse an
// ambiguous tie. Five seconds tolerates the small gap between recordOutcome's
// TotalDuration and CompletedAt calls without allowing an unrelated run.
func (r *Recorder) actualSizeFromRuntime(outcome Outcome) string {
	if outcome.CompletedAt.IsZero() {
		return ""
	}
	stateDir := filepath.Join(r.workspaceRoot, ".nightgauge", "pipeline")
	runtimes, err := state.FindPersistedStatesForIssue(stateDir, outcome.IssueNumber)
	if err != nil {
		return ""
	}

	const maxDurationDelta = 5 * time.Second
	bestDelta := maxDurationDelta + 1
	var best *state.RuntimeState
	ambiguous := false
	for _, runtime := range runtimes {
		if runtime == nil || runtime.ActualLinesChanged == nil ||
			(outcome.Repo != "" && runtime.Repo != "" && outcome.Repo != runtime.Repo) ||
			runtime.InputTokens != outcome.InputTokens || runtime.OutputTokens != outcome.OutputTokens ||
			math.Abs(runtime.TotalCostUSD-outcome.CostUSD) > 1e-9 {
			continue
		}
		duration := outcome.CompletedAt.Sub(runtime.StartedAt)
		if duration < 0 {
			continue
		}
		delta := duration - time.Duration(outcome.DurationMs)*time.Millisecond
		if delta < 0 {
			delta = -delta
		}
		if delta > maxDurationDelta {
			continue
		}
		switch {
		case delta < bestDelta:
			best, bestDelta, ambiguous = runtime, delta, false
		case delta == bestDelta:
			ambiguous = true
		}
	}
	if best == nil || ambiguous {
		return ""
	}
	return actualsize.LearningBucket(r.workspaceRoot, *best.ActualLinesChanged)
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
	SizeSamples   int      `json:"sizeSamples"`
	SizeAccuracy  *float64 `json:"sizeAccuracy"` // fraction of SizeSamples where predicted == actual; nil when SizeSamples == 0
	ModelSamples  int      `json:"modelSamples"`
	ModelMatches  int      `json:"modelMatches"`  // numerator behind ModelAccuracy, exposed so a caller can re-derive without re-walking the corpus
	ModelAccuracy *float64 `json:"modelAccuracy"` // fraction of ModelSamples where predicted == actual; nil when ModelSamples == 0
	// ModelSamplesExcludedRetry counts rows with a measurable model pair
	// (PredictedModel and ActualModel both non-empty) that were EXCLUDED from
	// ModelSamples because Retries > 0. A retry escalation applies
	// retryEngine.CurrentModel(stage) as the dispatch override, and that
	// override is re-recorded as ActualModel — so a haiku->sonnet escalation on
	// a failed stage (the retry ladder doing its job) is indistinguishable from
	// a router misprediction unless this row is pulled out of the denominator.
	// Retries > 0 is a superset of "the tier changed" (a retry with no tier
	// change is also excluded), accepted because it errs toward fewer false
	// misses rather than toward inflated ones (issue #1002). This counter
	// exists so the sample loss is visible instead of silently shrinking
	// ModelSamples with no explanation.
	ModelSamplesExcludedRetry int     `json:"modelSamplesExcludedRetry"`
	AvgCostPerRun             float64 `json:"avgCostPerRun"`
	SuccessRate               float64 `json:"successRate"`
	TrendImproving            bool    `json:"trendImproving"`
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
//
// The model half additionally excludes any row with Retries > 0 (#1002). The
// retry engine's escalated tier becomes the dispatch override
// (resolveDispatchModel applies retryEngine.CurrentModel(stage)), and that
// override is re-recorded as ActualModel — so a haiku->sonnet escalation on a
// failed stage, the retry ladder doing exactly its job, was booked as a
// routing miss against a prediction that never changed. The size half has no
// equivalent exclusion: size is decided once at pickup and is not subject to
// retry-driven revision, so nothing there needs the same guard.
func (r *Recorder) Calibrate() (*CalibrationReport, error) {
	outcomes, err := r.LoadAll()
	if err != nil {
		return nil, err
	}
	if len(outcomes) == 0 {
		return &CalibrationReport{}, nil
	}

	var sizeMatches, sizeSamples, modelMatches, modelSamples, excludedRetry, successes int
	var totalCost float64

	for _, o := range outcomes {
		if o.PredictedSize != "" && o.ActualSize != "" {
			sizeSamples++
			if o.PredictedSize == o.ActualSize {
				sizeMatches++
			}
		}
		if o.PredictedModel != "" && o.ActualModel != "" {
			if o.Retries > 0 {
				excludedRetry++
			} else {
				modelSamples++
				if o.PredictedModel == o.ActualModel {
					modelMatches++
				}
			}
		}
		if o.Success {
			successes++
		}
		totalCost += o.CostUSD
	}

	n := len(outcomes)
	report := &CalibrationReport{
		TotalRuns:                 n,
		SizeSamples:               sizeSamples,
		SizeAccuracy:              ratio(sizeMatches, sizeSamples),
		ModelSamples:              modelSamples,
		ModelMatches:              modelMatches,
		ModelAccuracy:             ratio(modelMatches, modelSamples),
		ModelSamplesExcludedRetry: excludedRetry,
		AvgCostPerRun:             totalCost / float64(n),
		SuccessRate:               float64(successes) / float64(n),
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
