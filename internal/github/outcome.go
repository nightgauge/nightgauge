// Package github provides GitHub API services.
// This file implements OutcomeService for recording pipeline execution outcomes
// to the complexity model, enabling continuous calibration.
package github

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/intelligence/actualsize"
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
	outcomeModelLockTimeout  = 30 * time.Second
	maxComplexityModelBytes  = 10 << 20
)

const localModelGitignore = `# Managed by nightgauge outcome initialization.
# Repo-init or the VS Code extension will replace this lightweight file with
# the complete canonical .nightgauge ignore rules.
/.gitignore
/complexity-model.yaml
/complexity-model.lock
`

var (
	sizeOrder           = []string{"XS", "S", "M", "L", "XL"}
	outcomeModelLocksMu sync.Mutex
	outcomeModelLocks   = map[string]*sync.Mutex{}
)

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

// ModelInitResult describes an idempotent complexity-model initialization.
type ModelInitResult struct {
	Created bool   `json:"created"`
	Path    string `json:"path"`
}

// complexityModel mirrors the YAML structure of complexity-model.yaml.
// Keep this complete: OutcomeService rewrites the file after every observation,
// so omitting a schema field here would silently erase data written by the SDK.
type complexityModel struct {
	SchemaVersion          string                        `yaml:"schema_version"`
	LastUpdated            string                        `yaml:"last_updated"`
	BootstrapDate          string                        `yaml:"bootstrap_date,omitempty"`
	SeededFrom             string                        `yaml:"seeded_from,omitempty"`
	TotalObservations      int                           `yaml:"total_observations"`
	Decay                  decayConfig                   `yaml:"decay"`
	ModelTracking          modelTracking                 `yaml:"model_tracking"`
	Patterns               patternCategories             `yaml:"patterns"`
	SizeCalibration        map[string]sizeCalibration    `yaml:"size_calibration"`
	TypeAdjustments        map[string]typeAdjustment     `yaml:"type_adjustments"`
	PriorityAdjustments    map[string]priorityAdjustment `yaml:"priority_adjustments"`
	LinesChangedThresholds map[string]int                `yaml:"lines_changed_thresholds"`
	Learnings              []string                      `yaml:"learnings"`
	PredictionAccuracy     *predictionAccuracy           `yaml:"prediction_accuracy,omitempty"`
	CriticalFiles          *criticalFiles                `yaml:"critical_files,omitempty"`
	WorkTimeFeedback       *workTimeFeedback             `yaml:"work_time_feedback,omitempty"`
}

type decayConfig struct {
	Enabled      bool `yaml:"enabled"`
	HalfLifeDays int  `yaml:"half_life_days"`
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
	ExpectedMinutes    *int    `yaml:"expected_minutes,omitempty"`
	SampleCount        int     `yaml:"sample_count"`
	AccuracyNote       string  `yaml:"accuracy_note,omitempty"`
}

type typeAdjustment struct {
	Modifier     float64 `yaml:"modifier"`
	Observations int     `yaml:"observations"`
	Rationale    string  `yaml:"rationale,omitempty"`
}

type priorityAdjustment struct {
	Modifier     float64 `yaml:"modifier"`
	Rationale    string  `yaml:"rationale,omitempty"`
	Observations int     `yaml:"observations"`
}

type criticalFiles struct {
	Description     string   `yaml:"description,omitempty"`
	Registry        []string `yaml:"registry"`
	PerFileModifier float64  `yaml:"per_file_modifier"`
	MaxModifier     float64  `yaml:"max_modifier"`
}

type workTimeFeedback struct {
	Enabled      bool                           `yaml:"enabled"`
	Observations []workTimeObservation          `yaml:"observations"`
	SizeAverages map[string]workTimeSizeAverage `yaml:"size_averages"`
}

type workTimeObservation struct {
	IssueNumber       int      `yaml:"issue_number"`
	Size              *string  `yaml:"size"`
	Priority          *string  `yaml:"priority"`
	TaskType          *string  `yaml:"task_type"`
	ActualWorkMinutes float64  `yaml:"actual_work_minutes"`
	EstimatedMinutes  float64  `yaml:"estimated_minutes"`
	Routing           string   `yaml:"routing"`
	StagesCompleted   []string `yaml:"stages_completed"`
	Timestamp         string   `yaml:"timestamp"`
}

type workTimeSizeAverage struct {
	Estimated        float64 `yaml:"estimated"`
	ActualAverage    float64 `yaml:"actual_average"`
	ObservationCount int     `yaml:"observation_count"`
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
	Survival *survivalCalibration `yaml:"survival_calibration,omitempty"`
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

// InitializeModel creates the canonical complexity-model baseline without
// replacing an existing file. The temp-file + hard-link installation makes the
// target visible only after the complete YAML has been written and ensures two
// concurrent first-run writers cannot overwrite one another.
func (s *OutcomeService) InitializeModel() (ModelInitResult, error) {
	release, err := s.lockModel()
	if err != nil {
		return ModelInitResult{Path: s.modelPath}, err
	}
	defer release()
	return s.initializeModelLocked()
}

// RunModelTransaction brokers one non-Go read-modify-write transaction. The
// caller waits for READY, computes a complete model document, and writes that
// YAML to input. The Go process owns both the advisory lock and final atomic
// install, so losing the broker can never leave a detached writer running.
// Closing input without a document releases the lock without changing the
// initialized model.
func (s *OutcomeService) RunModelTransaction(input io.Reader, ready io.Writer) error {
	release, err := s.lockModel()
	if err != nil {
		return err
	}
	defer release()

	if _, err := s.initializeModelLocked(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(ready, "READY"); err != nil {
		return fmt.Errorf("signal model transaction readiness: %w", err)
	}

	data, err := io.ReadAll(io.LimitReader(input, maxComplexityModelBytes+1))
	if err != nil {
		return fmt.Errorf("read model transaction: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	if len(data) > maxComplexityModelBytes {
		return fmt.Errorf("model transaction exceeds %d-byte limit", maxComplexityModelBytes)
	}

	model, err := decodeComplexityModelDocument(data)
	if err != nil {
		return fmt.Errorf("validate model transaction YAML: %w", err)
	}
	normalized, err := yaml.Marshal(model)
	if err != nil {
		return fmt.Errorf("normalize model transaction YAML: %w", err)
	}
	return s.writeModelDataLocked(normalized)
}

func decodeComplexityModelDocument(data []byte) (*complexityModel, error) {
	var model complexityModel
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&model); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not allowed")
		}
		return nil, err
	}
	if err := validateComplexityModelDocument(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// ValidateModel verifies that the existing model is a safe regular file and
// satisfies the same strict contract enforced by the transaction broker.
func (s *OutcomeService) ValidateModel() error {
	if err := s.ensureSafeModelDir(); err != nil {
		return err
	}
	exists, err := s.safeExistingModel()
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("complexity model not found at %s", s.modelPath)
	}
	data, err := os.ReadFile(s.modelPath)
	if err != nil {
		return fmt.Errorf("read complexity model: %w", err)
	}
	if _, err := decodeComplexityModelDocument(data); err != nil {
		return fmt.Errorf("invalid complexity model: %w", err)
	}
	return nil
}

func validateComplexityModelDocument(model *complexityModel) error {
	if model.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if model.LastUpdated == "" {
		return errors.New("last_updated is required")
	}
	if model.Decay.HalfLifeDays <= 0 {
		return errors.New("decay.half_life_days must be positive")
	}
	if model.TotalObservations < 0 {
		return errors.New("total_observations must be non-negative")
	}
	if model.ModelTracking.CurrentDefault == "" || model.ModelTracking.ObservationsByModel == nil {
		return errors.New("model_tracking is incomplete")
	}
	for modelID, observations := range model.ModelTracking.ObservationsByModel {
		if modelID == "" || observations < 0 {
			return errors.New("model_tracking.observations_by_model is invalid")
		}
	}
	for _, size := range sizeOrder {
		entry, ok := model.SizeCalibration[size]
		if !ok || entry.ExpectedLines < 0 || !finiteNonNegative(entry.ActualAverageLines) || entry.SampleCount < 0 {
			return fmt.Errorf("size_calibration.%s is invalid", size)
		}
		if entry.ExpectedMinutes != nil && *entry.ExpectedMinutes < 0 {
			return fmt.Errorf("size_calibration.%s.expected_minutes must be non-negative", size)
		}
		if model.LinesChangedThresholds[size] <= 0 {
			return fmt.Errorf("lines_changed_thresholds.%s must be positive", size)
		}
	}
	if model.Patterns.HighComplexity == nil || model.Patterns.MediumComplexity == nil || model.Patterns.LowComplexity == nil {
		return errors.New("patterns is incomplete")
	}
	for _, patterns := range [][]complexityPattern{
		model.Patterns.HighComplexity,
		model.Patterns.MediumComplexity,
		model.Patterns.LowComplexity,
	} {
		for _, pattern := range patterns {
			if pattern.Match == "" || pattern.Rationale == "" || !finite(pattern.Modifier) ||
				!finite(pattern.Confidence) || pattern.Confidence < 0 || pattern.Confidence > 1 || pattern.Observations < 0 {
				return errors.New("patterns contains an invalid entry")
			}
			if pattern.Source != "" && pattern.Source != "repo-specific" && pattern.Source != "cross-project" {
				return errors.New("patterns contains an invalid source")
			}
		}
	}
	if model.TypeAdjustments == nil || model.PriorityAdjustments == nil || model.Learnings == nil {
		return errors.New("model calibration sections are incomplete")
	}
	for _, adjustment := range model.TypeAdjustments {
		if !finite(adjustment.Modifier) || adjustment.Observations < 0 {
			return errors.New("type_adjustments contains an invalid entry")
		}
	}
	for _, adjustment := range model.PriorityAdjustments {
		if !finite(adjustment.Modifier) || adjustment.Observations < 0 {
			return errors.New("priority_adjustments contains an invalid entry")
		}
	}
	if model.PredictionAccuracy != nil {
		accuracy := model.PredictionAccuracy
		if accuracy.TotalPredictions < 0 || accuracy.CorrectPredictions < 0 || accuracy.CorrectPredictions > accuracy.TotalPredictions ||
			accuracy.ByType == nil || accuracy.BySize == nil || accuracy.RecentOutcomes == nil {
			return errors.New("prediction_accuracy is invalid")
		}
		for _, statsByKey := range []map[string]typeStats{accuracy.ByType, accuracy.BySize} {
			for _, stats := range statsByKey {
				if stats.Total < 0 || stats.Correct < 0 || stats.Correct > stats.Total {
					return errors.New("prediction_accuracy counters are invalid")
				}
			}
		}
		for _, outcome := range accuracy.RecentOutcomes {
			if outcome.IssueNumber <= 0 || outcome.PredictedSize == "" || outcome.ActualSizeBucket == "" || outcome.RecordedAt == "" || outcome.ActualLinesChanged < 0 {
				return errors.New("prediction_accuracy.recent_outcomes contains an invalid entry")
			}
		}
	}
	if model.CriticalFiles != nil && (!finite(model.CriticalFiles.PerFileModifier) || !finite(model.CriticalFiles.MaxModifier) ||
		model.CriticalFiles.PerFileModifier < 0 || model.CriticalFiles.PerFileModifier > 5 ||
		model.CriticalFiles.MaxModifier < 0 || model.CriticalFiles.MaxModifier > 5 || model.CriticalFiles.Registry == nil) {
		return errors.New("critical_files is invalid")
	}
	if err := validateWorkTimeFeedback(model.WorkTimeFeedback); err != nil {
		return err
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteNonNegative(value float64) bool {
	return finite(value) && value >= 0
}

func validateWorkTimeFeedback(feedback *workTimeFeedback) error {
	if feedback == nil {
		return nil
	}
	if feedback.Observations == nil || feedback.SizeAverages == nil || len(feedback.Observations) > 50 {
		return errors.New("work_time_feedback is incomplete or exceeds 50 observations")
	}
	validSizes := map[string]bool{"XS": true, "S": true, "M": true, "L": true, "XL": true}
	validTaskTypes := map[string]bool{"feature": true, "bugfix": true, "verification": true, "docs-only": true, "refactor": true, "chore": true, "spike": true}
	validStages := map[string]bool{"pipeline-start": true, "issue-pickup": true, "feature-planning": true, "feature-dev": true, "feature-validate": true, "pr-create": true, "pr-merge": true, "pipeline-finish": true}
	for _, observation := range feedback.Observations {
		if observation.IssueNumber <= 0 || (observation.Size != nil && !validSizes[*observation.Size]) ||
			!finiteNonNegative(observation.ActualWorkMinutes) || !finiteNonNegative(observation.EstimatedMinutes) ||
			observation.Routing == "" || observation.StagesCompleted == nil {
			return errors.New("work_time_feedback.observations contains an invalid entry")
		}
		if observation.TaskType != nil && !validTaskTypes[*observation.TaskType] {
			return errors.New("work_time_feedback.observations contains an invalid task_type")
		}
		if _, err := time.Parse(time.RFC3339, observation.Timestamp); err != nil {
			return errors.New("work_time_feedback.observations contains an invalid timestamp")
		}
		for _, stage := range observation.StagesCompleted {
			if !validStages[stage] {
				return errors.New("work_time_feedback.observations contains an invalid stage")
			}
		}
	}
	for size, average := range feedback.SizeAverages {
		if !validSizes[size] || !finiteNonNegative(average.Estimated) || !finiteNonNegative(average.ActualAverage) || average.ObservationCount < 0 {
			return errors.New("work_time_feedback.size_averages contains an invalid entry")
		}
	}
	return nil
}

func (s *OutcomeService) initializeModelLocked() (ModelInitResult, error) {
	result := ModelInitResult{Path: s.modelPath}
	exists, err := s.safeExistingModel()
	if err != nil {
		return result, err
	}
	if exists {
		return result, nil
	}
	if err := s.ensureLocalModelGitignore(); err != nil {
		return result, err
	}

	model := newBootstrapComplexityModel(time.Now().UTC())
	data, err := yaml.Marshal(model)
	if err != nil {
		return result, fmt.Errorf("marshal bootstrap model: %w", err)
	}

	dir := filepath.Dir(s.modelPath)
	tmp, err := os.CreateTemp(dir, ".complexity-model-bootstrap-*.yaml.tmp")
	if err != nil {
		return result, fmt.Errorf("create bootstrap temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return result, fmt.Errorf("write bootstrap temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return result, fmt.Errorf("sync bootstrap temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return result, fmt.Errorf("close bootstrap temp file: %w", err)
	}

	if err := os.Link(tmpPath, s.modelPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			if _, validateErr := s.safeExistingModel(); validateErr != nil {
				return result, validateErr
			}
			return result, nil
		}
		return result, fmt.Errorf("install bootstrap model: %w", err)
	}
	result.Created = true
	return result, nil
}

func (s *OutcomeService) ensureSafeModelDir() error {
	dir := filepath.Dir(s.modelPath)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create model directory: %w", err)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("inspect model directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlinked model directory: %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("model directory path is not a directory: %s", dir)
	}
	return nil
}

func (s *OutcomeService) safeExistingModel() (bool, error) {
	info, err := os.Lstat(s.modelPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect complexity model: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing symlinked complexity model: %s", s.modelPath)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("complexity model path is not a regular file: %s", s.modelPath)
	}
	return true, nil
}

func (s *OutcomeService) ensureLocalModelGitignore() error {
	ignorePath := filepath.Join(filepath.Dir(s.modelPath), ".gitignore")
	info, err := os.Lstat(ignorePath)
	if os.IsNotExist(err) {
		tmp, createErr := os.CreateTemp(filepath.Dir(ignorePath), ".model-gitignore-*.tmp")
		if createErr != nil {
			return fmt.Errorf("create model gitignore temp file: %w", createErr)
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if chmodErr := tmp.Chmod(0o644); chmodErr != nil {
			tmp.Close()
			return fmt.Errorf("set model gitignore permissions: %w", chmodErr)
		}
		if _, writeErr := tmp.WriteString(localModelGitignore); writeErr != nil {
			tmp.Close()
			return fmt.Errorf("write model gitignore: %w", writeErr)
		}
		if syncErr := tmp.Sync(); syncErr != nil {
			tmp.Close()
			return fmt.Errorf("sync model gitignore: %w", syncErr)
		}
		if closeErr := tmp.Close(); closeErr != nil {
			return fmt.Errorf("close model gitignore: %w", closeErr)
		}
		if linkErr := os.Link(tmpPath, ignorePath); linkErr != nil && !errors.Is(linkErr, fs.ErrExist) {
			return fmt.Errorf("install model gitignore: %w", linkErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect model gitignore: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlinked model gitignore: %s", ignorePath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("model gitignore path is not a regular file: %s", ignorePath)
	}

	data, err := os.ReadFile(ignorePath)
	if err != nil {
		return fmt.Errorf("read model gitignore: %w", err)
	}
	content := string(data)
	missing := ""
	for _, pattern := range []string{"/complexity-model.yaml", "/complexity-model.lock"} {
		if !containsLine(content, pattern) {
			missing += pattern + "\n"
		}
	}
	if missing == "" {
		return nil
	}
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content += "\n"
	}
	return atomicfile.Write(ignorePath, []byte(content+missing), info.Mode().Perm())
}

func containsLine(content, expected string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == expected {
			return true
		}
	}
	return false
}

func (s *OutcomeService) lockModel() (func(), error) {
	modelPath, err := filepath.Abs(s.modelPath)
	if err != nil {
		return nil, fmt.Errorf("resolve complexity-model path: %w", err)
	}
	outcomeModelLocksMu.Lock()
	modelMu, ok := outcomeModelLocks[modelPath]
	if !ok {
		modelMu = &sync.Mutex{}
		outcomeModelLocks[modelPath] = modelMu
	}
	outcomeModelLocksMu.Unlock()

	modelMu.Lock()
	if err := s.ensureSafeModelDir(); err != nil {
		modelMu.Unlock()
		return nil, err
	}

	lockPath := filepath.Join(filepath.Dir(s.modelPath), "complexity-model.lock")
	lockMissing := false
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		modelMu.Unlock()
		return nil, fmt.Errorf("refusing symlinked complexity-model lock: %s", lockPath)
	} else if os.IsNotExist(err) {
		lockMissing = true
	} else if err != nil && !os.IsNotExist(err) {
		modelMu.Unlock()
		return nil, fmt.Errorf("inspect complexity-model lock: %w", err)
	}
	if lockMissing {
		if err := s.ensureLocalModelGitignore(); err != nil {
			modelMu.Unlock()
			return nil, err
		}
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		modelMu.Unlock()
		return nil, fmt.Errorf("open complexity-model lock: %w", err)
	}
	if err := flock.Exclusive(lockFile, outcomeModelLockTimeout); err != nil {
		_ = lockFile.Close()
		modelMu.Unlock()
		return nil, fmt.Errorf("lock complexity model: %w", err)
	}
	release := func() {
		_ = flock.Unlock(lockFile)
		_ = lockFile.Close()
		modelMu.Unlock()
	}
	return release, nil
}

func newBootstrapComplexityModel(now time.Time) *complexityModel {
	today := now.Format("2006-01-02")
	return &complexityModel{
		SchemaVersion:     "1.0",
		LastUpdated:       today,
		BootstrapDate:     today,
		TotalObservations: 0,
		Decay: decayConfig{
			Enabled:      false,
			HalfLifeDays: 30,
		},
		ModelTracking: modelTracking{
			CurrentDefault:      "claude-sonnet-4-6",
			ObservationsByModel: map[string]int{},
		},
		Patterns: patternCategories{
			HighComplexity: []complexityPattern{
				{Match: "refactor|redesign|rewrite", Modifier: 1.5, Confidence: 0.45, Rationale: "Refactoring/redesign typically requires touching many files"},
				{Match: "migrate|migration", Modifier: 1.3, Confidence: 0.45, Rationale: "Migration work spans analysis, planning, and execution layers"},
				{Match: "multi.?repo|workspace|cross.?repo", Modifier: 1.5, Confidence: 0.5, Rationale: "Multi-repo features require coordination across boundaries"},
			},
			MediumComplexity: []complexityPattern{
				{Match: "config|setting|option", Modifier: 0, Confidence: 0.5, Rationale: "Configuration changes are moderate scope"},
				{Match: "validation|schema|zod", Modifier: 0, Confidence: 0.57, Rationale: "Schema/validation changes are moderate scope"},
			},
			LowComplexity: []complexityPattern{
				{Match: "typo|spelling|wording", Modifier: -1, Confidence: 0.7, Rationale: "Typo/spelling fixes are minimal scope"},
				{Match: "readme|changelog|documentation", Modifier: -0.8, Confidence: 0.65, Rationale: "Documentation-only changes are small scope"},
				{Match: "bump|upgrade|version", Modifier: -0.5, Confidence: 0.56, Rationale: "Version bumps are typically small"},
			},
		},
		SizeCalibration: map[string]sizeCalibration{
			"XS": {ExpectedLines: 50, ActualAverageLines: 59},
			"S":  {ExpectedLines: 150, ActualAverageLines: 213},
			"M":  {ExpectedLines: 500, ActualAverageLines: 574},
			"L":  {ExpectedLines: 1200, ActualAverageLines: 1476},
			"XL": {ExpectedLines: 2500, ActualAverageLines: 2352},
		},
		TypeAdjustments: map[string]typeAdjustment{
			"feature":  {Modifier: -1.45, Rationale: "Seeded from cross-repo baseline (45 observations show features over-predicted)"},
			"bug":      {Modifier: -0.6, Rationale: "Bugs tend toward smaller scope"},
			"docs":     {Modifier: -0.7, Rationale: "Documentation changes are typically smaller"},
			"refactor": {Modifier: 0.3, Rationale: "Refactors tend to touch more files"},
			"chore":    {Modifier: -0.3, Rationale: "Chores are typically small maintenance"},
		},
		PriorityAdjustments: map[string]priorityAdjustment{
			"critical": {Modifier: 0.2, Rationale: "Critical issues often have broader scope"},
			"high":     {Modifier: 0.1, Rationale: "High priority slightly correlates with complexity"},
			"medium":   {Modifier: 0, Rationale: "Baseline priority"},
			"low":      {Modifier: -0.1, Rationale: "Low priority often simpler scope"},
		},
		LinesChangedThresholds: map[string]int{"XS": 100, "S": 325, "M": 850, "L": 1850, "XL": 2500},
		Learnings: []string{
			fmt.Sprintf("%s: Bootstrap model created with universal baseline calibration from cross-repo data.", today),
		},
		PredictionAccuracy: &predictionAccuracy{
			ByType:         map[string]typeStats{},
			BySize:         map[string]typeStats{},
			RecentOutcomes: []recentOutcome{},
		},
		CriticalFiles: &criticalFiles{
			Description:     "Files whose modification significantly increases issue complexity. When referenced in technical notes, each critical file bumps the complexity score.",
			Registry:        []string{},
			PerFileModifier: 0.5,
			MaxModifier:     1.5,
		},
	}
}

// RecordOutcome records the pipeline outcome, updating calibration data in the model file.
// Returns OutcomeResult indicating whether the outcome was recorded, skipped (idempotency),
// or encountered an error. Errors are non-critical — callers should log and continue.
func (s *OutcomeService) RecordOutcome(params OutcomeParams) OutcomeResult {
	release, err := s.lockModel()
	if err != nil {
		return OutcomeResult{Error: fmt.Sprintf("lock model: %v", err)}
	}
	defer release()
	return s.recordOutcomeLocked(params)
}

func (s *OutcomeService) recordOutcomeLocked(params OutcomeParams) OutcomeResult {
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
	return s.recordOutcomeOnModelLocked(params, model)
}

func (s *OutcomeService) recordOutcomeOnModelLocked(params OutcomeParams, model *complexityModel) OutcomeResult {
	// Keep the locked helper safe for callers that already loaded the model
	// (including cross-process tests and recovery paths).
	if params.PredictedSize == "" {
		params.PredictedSize = "M"
	}
	if params.IssueType == "" {
		params.IssueType = "feature"
	}
	if params.ModelID == "" {
		params.ModelID = model.ModelTracking.CurrentDefault
	}
	if params.CompletedAt == "" {
		params.CompletedAt = time.Now().UTC().Format(time.RFC3339)
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
	release, err := s.lockModel()
	if err != nil {
		return OutcomeResult{Error: fmt.Sprintf("lock model: %v", err)}
	}
	defer release()

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
	exists, err := s.safeExistingModel()
	if err != nil {
		return nil, err
	}
	if !exists {
		if _, initErr := s.initializeModelLocked(); initErr != nil {
			return nil, initErr
		}
	}

	data, err := os.ReadFile(s.modelPath)
	if err != nil {
		return nil, err
	}

	model, err := decodeComplexityModelDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return model, nil
}

func (s *OutcomeService) saveModel(model *complexityModel) error {
	data, err := yaml.Marshal(model)
	if err != nil {
		return fmt.Errorf("marshal YAML: %w", err)
	}
	return s.writeModelDataLocked(data)
}

func (s *OutcomeService) writeModelDataLocked(data []byte) error {
	if err := s.ensureSafeModelDir(); err != nil {
		return err
	}
	if _, err := s.safeExistingModel(); err != nil {
		return err
	}
	return atomicfile.Write(s.modelPath, data, 0o600)
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
	// Use the same shared line bucketer as the terminal learning writers. The
	// model supplies learned upper-bound overrides; absent entries fall back to
	// the canonical defaults in actualsize.
	thresholds := make(map[string]int, len(model.SizeCalibration))
	for size, cal := range model.SizeCalibration {
		if cal.ExpectedLines > 0 {
			thresholds[size] = cal.ExpectedLines
		}
	}
	return actualsize.FiveBucket(lines, thresholds)
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
