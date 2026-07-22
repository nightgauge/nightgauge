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
type Outcome struct {
	IssueNumber     int       `json:"issueNumber"`
	Repo            string    `json:"repo"`
	PredictedSize   string    `json:"predictedSize"`
	ActualSize      string    `json:"actualSize,omitempty"`
	PredictedModel  string    `json:"predictedModel"`
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
type CalibrationReport struct {
	TotalRuns      int     `json:"totalRuns"`
	SizeAccuracy   float64 `json:"sizeAccuracy"`  // Pct where predicted == actual
	ModelAccuracy  float64 `json:"modelAccuracy"` // Pct where predicted model was optimal
	AvgCostPerRun  float64 `json:"avgCostPerRun"`
	SuccessRate    float64 `json:"successRate"`
	TrendImproving bool    `json:"trendImproving"`
}

// Calibrate analyzes recorded outcomes and produces a calibration report.
func (r *Recorder) Calibrate() (*CalibrationReport, error) {
	outcomes, err := r.LoadAll()
	if err != nil {
		return nil, err
	}
	if len(outcomes) == 0 {
		return &CalibrationReport{}, nil
	}

	var sizeMatches, modelMatches, successes int
	var totalCost float64

	for _, o := range outcomes {
		if o.PredictedSize == o.ActualSize && o.ActualSize != "" {
			sizeMatches++
		}
		if o.PredictedModel == o.ActualModel {
			modelMatches++
		}
		if o.Success {
			successes++
		}
		totalCost += o.CostUSD
	}

	n := len(outcomes)
	report := &CalibrationReport{
		TotalRuns:     n,
		SizeAccuracy:  float64(sizeMatches) / float64(n),
		ModelAccuracy: float64(modelMatches) / float64(n),
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
