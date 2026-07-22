package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func makeTestModel(t *testing.T, dir string) string {
	t.Helper()
	incDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("create .nightgauge dir: %v", err)
	}

	model := complexityModel{
		SchemaVersion:     "1.0",
		LastUpdated:       "2026-01-01",
		TotalObservations: 0,
		ModelTracking: modelTracking{
			CurrentDefault:      "claude-sonnet-4-6",
			ObservationsByModel: map[string]int{},
		},
		SizeCalibration: map[string]sizeCalibration{
			"XS": {ExpectedLines: 50, ActualAverageLines: 0, SampleCount: 0},
			"S":  {ExpectedLines: 150, ActualAverageLines: 0, SampleCount: 0},
			"M":  {ExpectedLines: 500, ActualAverageLines: 0, SampleCount: 0},
			"L":  {ExpectedLines: 1200, ActualAverageLines: 0, SampleCount: 0},
			"XL": {ExpectedLines: 2500, ActualAverageLines: 0, SampleCount: 0},
		},
		TypeAdjustments: map[string]typeAdjustment{
			"feature": {Modifier: -1.35, Observations: 11, Rationale: "test"},
		},
	}

	data, err := yaml.Marshal(&model)
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}

	modelPath := filepath.Join(incDir, "complexity-model.yaml")
	if err := os.WriteFile(modelPath, data, 0644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	return dir
}

func TestRecordOutcome_RecordsNewOutcome(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)

	svc := NewOutcomeService(dir)
	result := svc.RecordOutcome(OutcomeParams{
		IssueNumber:   42,
		PRNumber:      57,
		ModelID:       "claude-sonnet-4-6",
		PredictedSize: "M",
		ActualLines:   450,
		IssueType:     "feature",
		CompletedAt:   time.Now().UTC().Format(time.RFC3339),
	})

	if !result.Recorded {
		t.Errorf("expected Recorded=true, got false; error: %s", result.Error)
	}
	if result.Skipped {
		t.Error("expected Skipped=false, got true")
	}

	// Verify model was updated
	model := loadModel(t, dir)
	if model.TotalObservations != 1 {
		t.Errorf("total_observations = %d, want 1", model.TotalObservations)
	}
	if model.ModelTracking.ObservationsByModel["claude-sonnet-4-6"] != 1 {
		t.Errorf("model observations = %d, want 1", model.ModelTracking.ObservationsByModel["claude-sonnet-4-6"])
	}
	if model.PredictionAccuracy == nil {
		t.Fatal("prediction_accuracy is nil")
	}
	if model.PredictionAccuracy.TotalPredictions != 1 {
		t.Errorf("total_predictions = %d, want 1", model.PredictionAccuracy.TotalPredictions)
	}
	if len(model.PredictionAccuracy.RecentOutcomes) != 1 {
		t.Errorf("recent_outcomes len = %d, want 1", len(model.PredictionAccuracy.RecentOutcomes))
	}
	if model.PredictionAccuracy.RecentOutcomes[0].IssueNumber != 42 {
		t.Errorf("recent_outcomes[0].issue_number = %d, want 42", model.PredictionAccuracy.RecentOutcomes[0].IssueNumber)
	}
}

func TestRecordOutcome_Idempotency(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)

	svc := NewOutcomeService(dir)
	params := OutcomeParams{
		IssueNumber:   42,
		PRNumber:      57,
		ModelID:       "claude-sonnet-4-6",
		PredictedSize: "M",
		ActualLines:   450,
		IssueType:     "feature",
	}

	// First recording
	first := svc.RecordOutcome(params)
	if !first.Recorded {
		t.Fatalf("first recording failed: %s", first.Error)
	}

	// Second recording of same issue — should be skipped
	second := svc.RecordOutcome(params)
	if !second.Skipped {
		t.Errorf("expected Skipped=true on second recording, got Recorded=%v Skipped=%v err=%s", second.Recorded, second.Skipped, second.Error)
	}

	// Model observations should still be 1 (not 2)
	model := loadModel(t, dir)
	if model.TotalObservations != 1 {
		t.Errorf("total_observations = %d after duplicate, want 1", model.TotalObservations)
	}
}

func TestRecordOutcome_GarbageOverwrite(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)

	svc := NewOutcomeService(dir)

	// First: record with 0 lines (garbage entry from failure path)
	garbage := svc.RecordOutcome(OutcomeParams{
		IssueNumber:   42,
		PRNumber:      57,
		ModelID:       "claude-sonnet-4-6",
		PredictedSize: "M",
		ActualLines:   0,
		IssueType:     "feature",
	})
	if !garbage.Recorded {
		t.Fatalf("garbage recording failed: %s", garbage.Error)
	}

	// Second: same issue with real lines — should overwrite
	real := svc.RecordOutcome(OutcomeParams{
		IssueNumber:   42,
		PRNumber:      57,
		ModelID:       "claude-sonnet-4-6",
		PredictedSize: "M",
		ActualLines:   450,
		IssueType:     "feature",
	})
	if !real.Recorded {
		t.Errorf("expected Recorded=true for garbage overwrite, got Skipped=%v err=%s", real.Skipped, real.Error)
	}

	model := loadModel(t, dir)
	// Still one observation (garbage reversed, then new recorded)
	if model.TotalObservations != 1 {
		t.Errorf("total_observations = %d, want 1", model.TotalObservations)
	}
	// The recorded entry should have actual lines = 450
	if len(model.PredictionAccuracy.RecentOutcomes) != 1 {
		t.Fatalf("recent_outcomes len = %d, want 1", len(model.PredictionAccuracy.RecentOutcomes))
	}
	if model.PredictionAccuracy.RecentOutcomes[0].ActualLinesChanged != 450 {
		t.Errorf("actual_lines_changed = %d, want 450", model.PredictionAccuracy.RecentOutcomes[0].ActualLinesChanged)
	}
}

func TestRecordOutcome_MissingModelFile(t *testing.T) {
	dir := t.TempDir()
	// Do NOT create the model file

	svc := NewOutcomeService(dir)
	result := svc.RecordOutcome(OutcomeParams{
		IssueNumber: 42,
		PRNumber:    57,
	})

	if result.Error == "" {
		t.Error("expected error for missing model file, got none")
	}
	if result.Recorded {
		t.Error("expected Recorded=false when model file is missing")
	}
}

func TestRecordOutcome_JSONResponseFormat(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)

	svc := NewOutcomeService(dir)
	result := svc.RecordOutcome(OutcomeParams{
		IssueNumber:   99,
		PRNumber:      100,
		ModelID:       "claude-opus-4-6",
		PredictedSize: "L",
		ActualLines:   1100,
		IssueType:     "feature",
	})

	// Verify JSON serialization
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if _, ok := out["recorded"]; !ok {
		t.Error("JSON response missing 'recorded' field")
	}
	if _, ok := out["skipped"]; !ok {
		t.Error("JSON response missing 'skipped' field")
	}
}

func TestGetActualSizeBucket(t *testing.T) {
	svc := &OutcomeService{}
	model := &complexityModel{
		SizeCalibration: map[string]sizeCalibration{
			"XS": {ExpectedLines: 50},
			"S":  {ExpectedLines: 150},
			"M":  {ExpectedLines: 500},
			"L":  {ExpectedLines: 1200},
			"XL": {ExpectedLines: 2500},
		},
	}

	cases := []struct {
		lines    int
		expected string
	}{
		{25, "XS"},
		{50, "XS"},
		{51, "S"},
		{150, "S"},
		{151, "M"},
		{500, "M"},
		{501, "L"},
		{1200, "L"},
		{1201, "XL"},
		{9999, "XL"},
	}

	for _, c := range cases {
		got := svc.getActualSizeBucket(c.lines, model)
		if got != c.expected {
			t.Errorf("getActualSizeBucket(%d) = %q, want %q", c.lines, got, c.expected)
		}
	}
}

func TestIsPredictionCorrect(t *testing.T) {
	svc := &OutcomeService{}
	cases := []struct {
		predicted string
		actual    string
		want      bool
	}{
		{"M", "M", true},
		{"M", "S", true}, // adjacent
		{"M", "L", true}, // adjacent
		{"M", "XS", false},
		{"M", "XL", false},
		{"XS", "XS", true},
		{"XL", "XL", true},
		{"XS", "S", true}, // adjacent
		{"XL", "L", true}, // adjacent
	}

	for _, c := range cases {
		got := svc.isPredictionCorrect(c.predicted, c.actual)
		if got != c.want {
			t.Errorf("isPredictionCorrect(%q, %q) = %v, want %v", c.predicted, c.actual, got, c.want)
		}
	}
}

func TestRecordSelfHealEvent(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)

	svc := NewOutcomeService(dir)
	result := svc.RecordSelfHealEvent(2917, "stale_sdk_dist", "feature-validate")

	if !result.Recorded {
		t.Errorf("expected Recorded=true, got false; error: %s", result.Error)
	}
	if result.Skipped {
		t.Error("expected Skipped=false, got true")
	}

	model := loadModel(t, dir)
	if model.PredictionAccuracy == nil {
		t.Fatal("prediction_accuracy is nil after RecordSelfHealEvent")
	}
	events := model.PredictionAccuracy.SelfHealEvents
	if len(events) != 1 {
		t.Fatalf("self_heal_events len = %d, want 1", len(events))
	}
	if events[0].IssueNumber != 2917 {
		t.Errorf("event.issue_number = %d, want 2917", events[0].IssueNumber)
	}
	if events[0].Category != "stale_sdk_dist" {
		t.Errorf("event.category = %q, want stale_sdk_dist", events[0].Category)
	}
	if events[0].Stage != "feature-validate" {
		t.Errorf("event.stage = %q, want feature-validate", events[0].Stage)
	}
	if events[0].RecordedAt == "" {
		t.Error("event.recorded_at should not be empty")
	}
}

func TestRecordSelfHealEvent_MissingModelFile(t *testing.T) {
	dir := t.TempDir()
	// Do NOT create the model file

	svc := NewOutcomeService(dir)
	result := svc.RecordSelfHealEvent(42, "stale_sdk_dist", "feature-validate")

	if result.Error == "" {
		t.Error("expected error for missing model file, got none")
	}
	if result.Recorded {
		t.Error("expected Recorded=false when model file is missing")
	}
}

func loadModel(t *testing.T, dir string) *complexityModel {
	t.Helper()
	modelPath := filepath.Join(dir, ".nightgauge", "complexity-model.yaml")
	data, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatalf("read model: %v", err)
	}
	var m complexityModel
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal model: %v", err)
	}
	return &m
}
