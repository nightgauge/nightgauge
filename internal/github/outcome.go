// Package github provides GitHub API services.
// This file implements OutcomeService for recording pipeline execution outcomes
// to the complexity model, enabling continuous calibration.
package github

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxRecentOutcomes        = 50
	maxSelfHealEvents        = 200
	minObservationsForAdjust = 5
	learningRate             = 0.05
	maxModifierMagnitude     = 3.0
	confidenceBoost          = 0.02
	confidencePenalty        = 0.05
)

var sizeOrder = []string{"XS", "S", "M", "L", "XL"}

// OutcomeParams holds the parameters needed to record a pipeline outcome.
type OutcomeParams struct {
	IssueNumber   int
	PRNumber      int
	ModelID       string
	PredictedSize string
	ActualLines   int
	IssueType     string
	CompletedAt   string
}

// OutcomeResult is the JSON-serializable result of recording an outcome.
type OutcomeResult struct {
	Recorded bool   `json:"recorded"`
	Skipped  bool   `json:"skipped"`
	Error    string `json:"error,omitempty"`
}

// complexityModel mirrors the YAML structure of complexity-model.yaml.
// Only the fields relevant to outcome recording are included.
type complexityModel struct {
	SchemaVersion      string                     `yaml:"schema_version"`
	LastUpdated        string                     `yaml:"last_updated"`
	TotalObservations  int                        `yaml:"total_observations"`
	Decay              map[string]interface{}     `yaml:"decay,omitempty"`
	ModelTracking      modelTracking              `yaml:"model_tracking"`
	Patterns           patternCategories          `yaml:"patterns"`
	SizeCalibration    map[string]sizeCalibration `yaml:"size_calibration"`
	TypeAdjustments    map[string]typeAdjustment  `yaml:"type_adjustments"`
	PriorityAdjust     map[string]interface{}     `yaml:"priority_adjustments,omitempty"`
	PredictionAccuracy *predictionAccuracy        `yaml:"prediction_accuracy,omitempty"`
}

type modelTracking struct {
	CurrentDefault      string         `yaml:"current_default"`
	ObservationsByModel map[string]int `yaml:"observations_by_model"`
}

type patternCategories struct {
	HighComplexity   []complexityPattern `yaml:"high_complexity"`
	MediumComplexity []complexityPattern `yaml:"medium_complexity"`
	LowComplexity    []complexityPattern `yaml:"low_complexity"`
}

type complexityPattern struct {
	Match        string  `yaml:"match"`
	Modifier     float64 `yaml:"modifier"`
	Confidence   float64 `yaml:"confidence"`
	Rationale    string  `yaml:"rationale"`
	Observations int     `yaml:"observations"`
	Source       string  `yaml:"source,omitempty"`
}

type sizeCalibration struct {
	ExpectedLines      int     `yaml:"expected_lines"`
	ActualAverageLines float64 `yaml:"actual_average_lines"`
	SampleCount        int     `yaml:"sample_count"`
	AccuracyNote       string  `yaml:"accuracy_note,omitempty"`
}

type typeAdjustment struct {
	Modifier     float64 `yaml:"modifier"`
	Observations int     `yaml:"observations"`
	Rationale    string  `yaml:"rationale,omitempty"`
}

type predictionAccuracy struct {
	TotalPredictions   int                  `yaml:"total_predictions"`
	CorrectPredictions int                  `yaml:"correct_predictions"`
	ByType             map[string]typeStats `yaml:"by_type"`
	BySize             map[string]typeStats `yaml:"by_size"`
	RecentOutcomes     []recentOutcome      `yaml:"recent_outcomes"`
	SelfHealEvents     []SelfHealEvent      `yaml:"self_heal_events,omitempty"`
	// Survival holds the bias-safe, post-merge-ground-truth calibration state
	// derived from survival.Record verdicts (#4152/#4153, spike #4134 §1.2).
	// See outcome_survival.go.
	Survival *survivalCalibration `yaml:"survival,omitempty"`
}

// SelfHealEvent records a single pipeline self-heal occurrence for frequency tracking.
type SelfHealEvent struct {
	IssueNumber int    `yaml:"issue_number"`
	Category    string `yaml:"category"`
	Stage       string `yaml:"stage"`
	RecordedAt  string `yaml:"recorded_at"`
}

type typeStats struct {
	Total   int `yaml:"total"`
	Correct int `yaml:"correct"`
}

type recentOutcome struct {
	IssueNumber        int    `yaml:"issue_number"`
	PredictedSize      string `yaml:"predicted_size"`
	ActualSizeBucket   string `yaml:"actual_size_bucket"`
	WasCorrect         bool   `yaml:"was_correct"`
	RecordedAt         string `yaml:"recorded_at"`
	ActualLinesChanged int    `yaml:"actual_lines_changed"`
}

// OutcomeService records pipeline execution outcomes to the complexity model.
type OutcomeService struct {
	modelPath string
}

// NewOutcomeService creates an OutcomeService using the workspace root to locate the model file.
func NewOutcomeService(workspaceRoot string) *OutcomeService {
	return &OutcomeService{
		modelPath: filepath.Join(workspaceRoot, ".nightgauge", "complexity-model.yaml"),
	}
}

// RecordOutcome records the pipeline outcome, updating calibration data in the model file.
// Returns OutcomeResult indicating whether the outcome was recorded, skipped (idempotency),
// or encountered an error. Errors are non-critical — callers should log and continue.
func (s *OutcomeService) RecordOutcome(params OutcomeParams) OutcomeResult {
	if params.PredictedSize == "" {
		params.PredictedSize = "M" // default if not provided
	}
	if params.IssueType == "" {
		params.IssueType = "feature" // default
	}
	if params.CompletedAt == "" {
		params.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}

	model, err := s.loadModel()
	if err != nil {
		return OutcomeResult{Error: fmt.Sprintf("load model: %v", err)}
	}

	// Ensure prediction_accuracy is initialized
	if model.PredictionAccuracy == nil {
		model.PredictionAccuracy = &predictionAccuracy{
			ByType:         map[string]typeStats{},
			BySize:         map[string]typeStats{},
			RecentOutcomes: []recentOutcome{},
		}
	}

	// Idempotency: check for existing outcome with this issue number
	existingIdx := s.findExistingOutcome(model, params.IssueNumber)
	if existingIdx >= 0 {
		existing := model.PredictionAccuracy.RecentOutcomes[existingIdx]
		isGarbage := existing.ActualLinesChanged == 0
		hasRealData := params.ActualLines > 0
		if isGarbage && hasRealData {
			// Overwrite garbage entry: reverse its effects and re-record
			s.reverseOutcomeEffects(model, existing)
		} else {
			return OutcomeResult{Skipped: true}
		}
	}

	actualBucket := s.getActualSizeBucket(params.ActualLines, model)
	wasCorrect := s.isPredictionCorrect(params.PredictedSize, actualBucket)

	// Update size calibration
	cal, ok := model.SizeCalibration[actualBucket]
	if ok {
		newCount := cal.SampleCount + 1
		cal.ActualAverageLines = (cal.ActualAverageLines*float64(cal.SampleCount) + float64(params.ActualLines)) / float64(newCount)
		cal.SampleCount = newCount
		model.SizeCalibration[actualBucket] = cal
	}

	// Update model tracking
	if model.ModelTracking.ObservationsByModel == nil {
		model.ModelTracking.ObservationsByModel = map[string]int{}
	}
	model.ModelTracking.ObservationsByModel[params.ModelID]++
	model.TotalObservations++

	// Update prediction accuracy
	acc := model.PredictionAccuracy
	acc.TotalPredictions++
	if wasCorrect {
		acc.CorrectPredictions++
	}

	if acc.ByType == nil {
		acc.ByType = map[string]typeStats{}
	}
	byType := acc.ByType[params.IssueType]
	byType.Total++
	if wasCorrect {
		byType.Correct++
	}
	acc.ByType[params.IssueType] = byType

	if acc.BySize == nil {
		acc.BySize = map[string]typeStats{}
	}
	bySize := acc.BySize[params.PredictedSize]
	bySize.Total++
	if wasCorrect {
		bySize.Correct++
	}
	acc.BySize[params.PredictedSize] = bySize

	// Append to recent_outcomes (capped at maxRecentOutcomes)
	entry := recentOutcome{
		IssueNumber:        params.IssueNumber,
		PredictedSize:      params.PredictedSize,
		ActualSizeBucket:   actualBucket,
		WasCorrect:         wasCorrect,
		RecordedAt:         params.CompletedAt,
		ActualLinesChanged: params.ActualLines,
	}
	acc.RecentOutcomes = append(acc.RecentOutcomes, entry)
	if len(acc.RecentOutcomes) > maxRecentOutcomes {
		acc.RecentOutcomes = acc.RecentOutcomes[len(acc.RecentOutcomes)-maxRecentOutcomes:]
	}

	// Adjust type modifiers (directional error correction)
	s.adjustTypeModifiers(model, params, actualBucket, wasCorrect)

	model.LastUpdated = time.Now().UTC().Format("2006-01-02")

	if err := s.saveModel(model); err != nil {
		return OutcomeResult{Error: fmt.Sprintf("save model: %v", err)}
	}

	return OutcomeResult{Recorded: true}
}

// RecordSelfHealEvent appends a self-heal event to the complexity model for
// frequency tracking. Self-heal events are best-effort — errors are returned
// but callers should log and continue (never block the pipeline on this).
func (s *OutcomeService) RecordSelfHealEvent(issueNumber int, category, stage string) OutcomeResult {
	model, err := s.loadModel()
	if err != nil {
		return OutcomeResult{Error: fmt.Sprintf("load model: %v", err)}
	}

	if model.PredictionAccuracy == nil {
		model.PredictionAccuracy = &predictionAccuracy{
			ByType:         map[string]typeStats{},
			BySize:         map[string]typeStats{},
			RecentOutcomes: []recentOutcome{},
		}
	}

	event := SelfHealEvent{
		IssueNumber: issueNumber,
		Category:    category,
		Stage:       stage,
		RecordedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	model.PredictionAccuracy.SelfHealEvents = append(model.PredictionAccuracy.SelfHealEvents, event)
	if len(model.PredictionAccuracy.SelfHealEvents) > maxSelfHealEvents {
		model.PredictionAccuracy.SelfHealEvents = model.PredictionAccuracy.SelfHealEvents[len(model.PredictionAccuracy.SelfHealEvents)-maxSelfHealEvents:]
	}
	model.LastUpdated = time.Now().UTC().Format("2006-01-02")

	if err := s.saveModel(model); err != nil {
		return OutcomeResult{Error: fmt.Sprintf("save model: %v", err)}
	}
	return OutcomeResult{Recorded: true}
}

func (s *OutcomeService) loadModel() (*complexityModel, error) {
	data, err := os.ReadFile(s.modelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("complexity-model.yaml not found at %s — run pipeline first to initialize the model", s.modelPath)
		}
		return nil, err
	}

	var model complexityModel
	if err := yaml.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if model.SizeCalibration == nil {
		model.SizeCalibration = map[string]sizeCalibration{}
	}
	if model.TypeAdjustments == nil {
		model.TypeAdjustments = map[string]typeAdjustment{}
	}
	return &model, nil
}

func (s *OutcomeService) saveModel(model *complexityModel) error {
	data, err := yaml.Marshal(model)
	if err != nil {
		return fmt.Errorf("marshal YAML: %w", err)
	}

	// Atomic write: temp file + rename
	dir := filepath.Dir(s.modelPath)
	tmp, err := os.CreateTemp(dir, ".complexity-model-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.modelPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func (s *OutcomeService) findExistingOutcome(model *complexityModel, issueNumber int) int {
	if model.PredictionAccuracy == nil {
		return -1
	}
	for i, o := range model.PredictionAccuracy.RecentOutcomes {
		if o.IssueNumber == issueNumber {
			return i
		}
	}
	return -1
}

func (s *OutcomeService) getActualSizeBucket(lines int, model *complexityModel) string {
	// Use size_calibration expected_lines as thresholds (upper bound per bucket)
	thresholds := map[string]int{
		"XS": 75,
		"S":  250,
		"M":  750,
		"L":  1750,
	}
	// Override with model's expected_lines if available
	for size, cal := range model.SizeCalibration {
		if cal.ExpectedLines > 0 {
			thresholds[size] = cal.ExpectedLines
		}
	}
	for _, size := range sizeOrder[:len(sizeOrder)-1] {
		if lines <= thresholds[size] {
			return size
		}
	}
	return "XL"
}

func (s *OutcomeService) isPredictionCorrect(predicted, actual string) bool {
	predictedIdx := indexOf(sizeOrder, predicted)
	actualIdx := indexOf(sizeOrder, actual)
	if predictedIdx < 0 || actualIdx < 0 {
		return false
	}
	diff := predictedIdx - actualIdx
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1
}

func (s *OutcomeService) adjustTypeModifiers(model *complexityModel, params OutcomeParams, actualBucket string, wasCorrect bool) {
	acc := model.PredictionAccuracy
	if acc == nil {
		return
	}
	typeData, ok := acc.ByType[params.IssueType]
	if !ok || typeData.Total < minObservationsForAdjust {
		return
	}

	adj, ok := model.TypeAdjustments[params.IssueType]
	if !ok {
		return
	}

	if !wasCorrect {
		predictedIdx := indexOf(sizeOrder, params.PredictedSize)
		actualIdx := indexOf(sizeOrder, actualBucket)
		if predictedIdx >= 0 && actualIdx >= 0 {
			errVal := float64(predictedIdx - actualIdx)
			shift := -errVal * learningRate
			newMod := adj.Modifier + shift
			newMod = math.Max(-maxModifierMagnitude, math.Min(maxModifierMagnitude, newMod))
			adj.Modifier = math.Round(newMod*100) / 100
		}
	}
	adj.Observations++
	model.TypeAdjustments[params.IssueType] = adj
}

func (s *OutcomeService) reverseOutcomeEffects(model *complexityModel, existing recentOutcome) {
	acc := model.PredictionAccuracy
	if acc == nil {
		return
	}

	// Remove from recent_outcomes
	var filtered []recentOutcome
	for _, o := range acc.RecentOutcomes {
		if o.IssueNumber != existing.IssueNumber {
			filtered = append(filtered, o)
		}
	}
	acc.RecentOutcomes = filtered

	// Reverse prediction counters
	if acc.TotalPredictions > 0 {
		acc.TotalPredictions--
	}
	if existing.WasCorrect && acc.CorrectPredictions > 0 {
		acc.CorrectPredictions--
	}

	// Reverse size calibration for the old actual bucket
	if cal, ok := model.SizeCalibration[existing.ActualSizeBucket]; ok && cal.SampleCount > 0 {
		removed := float64(existing.ActualLinesChanged)
		newCount := cal.SampleCount - 1
		var newAvg float64
		if newCount > 0 {
			newAvg = (cal.ActualAverageLines*float64(cal.SampleCount) - removed) / float64(newCount)
		} else {
			newAvg = float64(cal.ExpectedLines)
		}
		cal.ActualAverageLines = newAvg
		cal.SampleCount = newCount
		model.SizeCalibration[existing.ActualSizeBucket] = cal
	}

	// Reverse total observations and model tracking
	if model.TotalObservations > 0 {
		model.TotalObservations--
	}
}

func indexOf(slice []string, val string) int {
	for i, v := range slice {
		if v == val {
			return i
		}
	}
	return -1
}
