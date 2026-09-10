package github

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"gopkg.in/yaml.v3"
)

func makeTestModel(t *testing.T, dir string) string {
	t.Helper()
	incDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("create .nightgauge dir: %v", err)
	}

	model := *newBootstrapComplexityModel(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	model.BootstrapDate = ""
	model.TypeAdjustments["feature"] = typeAdjustment{
		Modifier: -1.35, Observations: 11, Rationale: "test",
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

func TestRecordOutcome_BootstrapsMissingModel(t *testing.T) {
	dir := t.TempDir()

	svc := NewOutcomeService(dir)
	result := svc.RecordOutcome(OutcomeParams{
		IssueNumber: 42,
		PRNumber:    57,
	})

	if !result.Recorded || result.Error != "" {
		t.Fatalf("first outcome = %+v, want a recorded bootstrap outcome", result)
	}

	model := loadModel(t, dir)
	if model.BootstrapDate == "" {
		t.Error("bootstrap_date is empty")
	}
	if model.TotalObservations != 1 {
		t.Errorf("total_observations = %d, want 1", model.TotalObservations)
	}
	if model.LinesChangedThresholds["M"] != 850 {
		t.Errorf("M lines_changed_threshold = %d, want 850", model.LinesChangedThresholds["M"])
	}
	if model.CriticalFiles == nil || model.CriticalFiles.MaxModifier != 1.5 {
		t.Errorf("critical_files = %+v, want canonical max_modifier 1.5", model.CriticalFiles)
	}
}

func TestInitializeModel_CanonicalAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	svc := NewOutcomeService(dir)

	first, err := svc.InitializeModel()
	if err != nil {
		t.Fatalf("InitializeModel first call: %v", err)
	}
	if !first.Created {
		t.Fatal("first InitializeModel call did not create the model")
	}
	wantPath := filepath.Join(dir, ".nightgauge", "complexity-model.yaml")
	if first.Path != wantPath {
		t.Errorf("path = %q, want %q", first.Path, wantPath)
	}

	before, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read initialized model: %v", err)
	}
	second, err := svc.InitializeModel()
	if err != nil {
		t.Fatalf("InitializeModel second call: %v", err)
	}
	if second.Created {
		t.Fatal("second InitializeModel call replaced an existing model")
	}
	after, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read model after second call: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("idempotent initialization changed the existing model")
	}

	model := loadModel(t, dir)
	if model.SchemaVersion != "1.0" || model.Decay.HalfLifeDays != 30 {
		t.Errorf("bootstrap header = schema %q decay %+v", model.SchemaVersion, model.Decay)
	}
	if len(model.Patterns.HighComplexity) != 3 || len(model.Patterns.MediumComplexity) != 2 || len(model.Patterns.LowComplexity) != 3 {
		t.Errorf("pattern counts = high %d medium %d low %d, want 3/2/3",
			len(model.Patterns.HighComplexity), len(model.Patterns.MediumComplexity), len(model.Patterns.LowComplexity))
	}
	if got := model.SizeCalibration["XL"].ActualAverageLines; got != 2352 {
		t.Errorf("XL actual_average_lines = %v, want 2352", got)
	}
	if got := model.TypeAdjustments["feature"].Modifier; got != -1.45 {
		t.Errorf("feature modifier = %v, want -1.45", got)
	}
	if got := model.PriorityAdjustments["critical"].Modifier; got != 0.2 {
		t.Errorf("critical priority modifier = %v, want 0.2", got)
	}
	if len(model.Learnings) != 1 || model.PredictionAccuracy == nil || model.CriticalFiles == nil {
		t.Errorf("canonical tail fields missing: learnings=%v prediction_accuracy=%v critical_files=%v",
			model.Learnings, model.PredictionAccuracy, model.CriticalFiles)
	}
}

func TestInitializeModel_InstallsLocalIgnoreRules(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewOutcomeService(dir).InitializeModel(); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(dir, ".nightgauge", ".gitignore")
	data, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatalf("read generated model gitignore: %v", err)
	}
	content := string(data)
	for _, pattern := range []string{"/.gitignore", "/complexity-model.yaml", "/complexity-model.lock"} {
		if !containsLine(content, pattern) {
			t.Errorf("generated model gitignore missing %q:\n%s", pattern, content)
		}
	}
}

func TestInitializeModel_LeavesFreshGitStatusClean(t *testing.T) {
	dir := t.TempDir()
	gittest.InitRepo(t, dir, "-q")
	if _, err := NewOutcomeService(dir).InitializeModel(); err != nil {
		t.Fatal(err)
	}
	output := gittest.Run(t, dir, "status", "--porcelain", "--untracked-files=all")
	if len(output) != 0 {
		t.Fatalf("first-run model artifacts polluted git status:\n%s", output)
	}
}

func TestInitializeModel_PreservesCustomGitignore(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(modelDir, ".gitignore")
	if err := os.WriteFile(ignorePath, []byte("/custom-local-state\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutcomeService(dir).InitializeModel(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !containsLine(content, "/custom-local-state") || !containsLine(content, "/complexity-model.yaml") || !containsLine(content, "/complexity-model.lock") {
		t.Fatalf("initializer did not preserve and extend custom gitignore:\n%s", content)
	}
	info, err := os.Stat(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("gitignore permissions = %o, want 640", info.Mode().Perm())
	}
}

func TestRunModelTransaction_InstallsValidatedDocument(t *testing.T) {
	dir := t.TempDir()
	svc := NewOutcomeService(dir)
	model := newBootstrapComplexityModel(time.Date(2001, time.February, 3, 0, 0, 0, 0, time.UTC))
	model.TotalObservations = 7
	data, err := yaml.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var ready bytes.Buffer
	if err := svc.RunModelTransaction(bytes.NewReader(data), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.String() != "READY\n" {
		t.Fatalf("readiness = %q, want READY newline", ready.String())
	}
	installed, err := os.ReadFile(filepath.Join(dir, ".nightgauge", "complexity-model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, data) {
		t.Fatal("transaction broker did not install the submitted document exactly")
	}
}

func TestRunModelTransaction_RejectsInvalidDocumentWithoutReplacingModel(t *testing.T) {
	dir := t.TempDir()
	svc := NewOutcomeService(dir)
	result, err := svc.InitializeModel()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	var ready bytes.Buffer
	err = svc.RunModelTransaction(strings.NewReader("schema_version: \"1.0\"\n"), &ready)
	if err == nil || !strings.Contains(err.Error(), "last_updated is required") {
		t.Fatalf("RunModelTransaction error = %v, want schema validation error", err)
	}
	after, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid transaction replaced the existing model")
	}
}

func TestRunModelTransaction_EmptyInputReleasesWithoutMutation(t *testing.T) {
	t.Run("existing model", func(t *testing.T) {
		dir := t.TempDir()
		svc := NewOutcomeService(dir)
		result, err := svc.InitializeModel()
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(result.Path)
		if err != nil {
			t.Fatal(err)
		}
		var ready bytes.Buffer
		if err := svc.RunModelTransaction(strings.NewReader(""), &ready); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(result.Path)
		if err != nil {
			t.Fatal(err)
		}
		if ready.String() != "READY\n" || !bytes.Equal(before, after) {
			t.Fatal("empty transaction changed an existing model")
		}
	})

	t.Run("missing model", func(t *testing.T) {
		dir := t.TempDir()
		svc := NewOutcomeService(dir)
		var ready bytes.Buffer
		if err := svc.RunModelTransaction(strings.NewReader(""), &ready); err != nil {
			t.Fatal(err)
		}
		modelPath := filepath.Join(dir, ".nightgauge", "complexity-model.yaml")
		data, err := os.ReadFile(modelPath)
		if err != nil {
			t.Fatal(err)
		}
		if ready.String() != "READY\n" {
			t.Fatalf("readiness = %q", ready.String())
		}
		if _, err := decodeComplexityModelDocument(data); err != nil {
			t.Fatalf("empty first transaction did not leave a canonical model: %v", err)
		}
	})
}

func TestRunModelTransaction_RejectsSemanticViolationsWithoutReplacingModel(t *testing.T) {
	tests := map[string]func(*complexityModel) []byte{
		"negative observations": func(model *complexityModel) []byte {
			model.TotalObservations = -1
			data, _ := yaml.Marshal(model)
			return data
		},
		"out of range confidence": func(model *complexityModel) []byte {
			model.Patterns.HighComplexity[0].Confidence = 1.1
			data, _ := yaml.Marshal(model)
			return data
		},
		"negative type observations": func(model *complexityModel) []byte {
			entry := model.TypeAdjustments["feature"]
			entry.Observations = -1
			model.TypeAdjustments["feature"] = entry
			data, _ := yaml.Marshal(model)
			return data
		},
		"invalid work time": func(model *complexityModel) []byte {
			model.WorkTimeFeedback = &workTimeFeedback{
				Observations: []workTimeObservation{{IssueNumber: 1, Size: stringPointer("XXL")}},
				SizeAverages: map[string]workTimeSizeAverage{},
			}
			data, _ := yaml.Marshal(model)
			return data
		},
		"unknown field": func(model *complexityModel) []byte {
			data, _ := yaml.Marshal(model)
			return append(data, []byte("unknown_future_field: true\n")...)
		},
	}

	for name, invalidDocument := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			svc := NewOutcomeService(dir)
			result, err := svc.InitializeModel()
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			model := newBootstrapComplexityModel(time.Date(2001, time.February, 3, 0, 0, 0, 0, time.UTC))
			var ready bytes.Buffer
			if err := svc.RunModelTransaction(bytes.NewReader(invalidDocument(model)), &ready); err == nil {
				t.Fatal("RunModelTransaction accepted semantically invalid model")
			}
			after, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("invalid transaction replaced the existing model")
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func TestBootstrapModel_MatchesSharedGoldenFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "tests", "fixtures", "complexity-model-bootstrap.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read shared bootstrap fixture: %v", err)
	}
	var want complexityModel
	if err := yaml.Unmarshal(data, &want); err != nil {
		t.Fatalf("parse shared bootstrap fixture: %v", err)
	}

	now := time.Date(2000, time.January, 2, 12, 0, 0, 0, time.UTC)
	got := newBootstrapComplexityModel(now)
	if !reflect.DeepEqual(got, &want) {
		gotYAML, _ := yaml.Marshal(got)
		wantYAML, _ := yaml.Marshal(&want)
		t.Fatalf("Go bootstrap differs from shared fixture\ngot:\n%s\nwant:\n%s", gotYAML, wantYAML)
	}
}

func TestInitializeModel_PreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	modelDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(modelDir, "complexity-model.yaml")
	original := []byte("operator-owned: true\n")
	if err := os.WriteFile(modelPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := NewOutcomeService(dir).InitializeModel()
	if err != nil {
		t.Fatalf("InitializeModel: %v", err)
	}
	if result.Created {
		t.Fatal("InitializeModel reported replacing an existing file")
	}
	got, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("existing model changed: got %q want %q", got, original)
	}
}

func TestInitializeModel_ConcurrentCallersCreateOneCompleteModel(t *testing.T) {
	dir := t.TempDir()
	const callers = 16
	results := make(chan ModelInitResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := NewOutcomeService(dir).InitializeModel()
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	created := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent InitializeModel: %v", err)
		}
	}
	for result := range results {
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created count = %d, want exactly 1", created)
	}
	model := loadModel(t, dir)
	if model.SchemaVersion != "1.0" || model.BootstrapDate == "" {
		t.Fatalf("concurrent bootstrap left invalid model: %+v", model)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".nightgauge"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("bootstrap temp file was not cleaned up: %s", entry.Name())
		}
	}
}

func TestRecordOutcome_ConcurrentFirstRunPreservesEveryOutcome(t *testing.T) {
	dir := t.TempDir()
	const callers = 10
	results := make(chan OutcomeResult, callers)
	var wg sync.WaitGroup
	for issue := 1; issue <= callers; issue++ {
		wg.Add(1)
		go func(issue int) {
			defer wg.Done()
			results <- NewOutcomeService(dir).RecordOutcome(OutcomeParams{
				IssueNumber: issue,
				PRNumber:    100 + issue,
				ModelID:     "claude-sonnet-4-6",
				ActualLines: 25,
			})
		}(issue)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if !result.Recorded || result.Error != "" {
			t.Fatalf("concurrent RecordOutcome = %+v", result)
		}
	}

	model := loadModel(t, dir)
	if model.TotalObservations != callers {
		t.Fatalf("total_observations = %d, want %d", model.TotalObservations, callers)
	}
	if got := len(model.PredictionAccuracy.RecentOutcomes); got != callers {
		t.Fatalf("recent_outcomes = %d, want %d", got, callers)
	}
}

func TestModelLock_DoesNotSerializeDifferentWorkspaces(t *testing.T) {
	first := NewOutcomeService(t.TempDir())
	release, err := first.lockModel()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	secondDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := NewOutcomeService(secondDir).InitializeModel()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("initialize unrelated workspace: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated workspace blocked behind another model lock")
	}
}

func TestOutcomeRecordProcessHelper(t *testing.T) {
	mode := os.Getenv("NG_OUTCOME_HELPER_MODE")
	if mode == "" {
		return
	}
	root := os.Getenv("NG_OUTCOME_HELPER_ROOT")
	issue, err := strconv.Atoi(os.Getenv("NG_OUTCOME_HELPER_ISSUE"))
	if err != nil {
		t.Fatal(err)
	}
	params := OutcomeParams{IssueNumber: issue, PRNumber: 100 + issue, ActualLines: 25}
	svc := NewOutcomeService(root)
	if mode == "record" {
		if result := svc.RecordOutcome(params); !result.Recorded || result.Error != "" {
			t.Fatalf("RecordOutcome: %+v", result)
		}
		return
	}

	release, err := svc.lockModel()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	model, err := svc.loadModel()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("NG_OUTCOME_HELPER_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("NG_OUTCOME_HELPER_RELEASE")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for release marker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result := svc.recordOutcomeOnModelLocked(params, model); !result.Recorded || result.Error != "" {
		t.Fatalf("record held outcome: %+v", result)
	}
}

func TestRecordOutcome_CrossProcessLockPreservesBothWriters(t *testing.T) {
	root := t.TempDir()
	readyPath := filepath.Join(root, "first-ready")
	releasePath := filepath.Join(root, "release-first")
	newHelper := func(mode string, issue int) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOutcomeRecordProcessHelper$")
		cmd.Env = append(os.Environ(),
			"NG_OUTCOME_HELPER_MODE="+mode,
			"NG_OUTCOME_HELPER_ROOT="+root,
			"NG_OUTCOME_HELPER_ISSUE="+strconv.Itoa(issue),
			"NG_OUTCOME_HELPER_READY="+readyPath,
			"NG_OUTCOME_HELPER_RELEASE="+releasePath,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd
	}

	first := newHelper("hold", 1)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Wait() }()
	firstReaped := false
	defer func() {
		if !firstReaped {
			_ = first.Process.Kill()
			<-firstDone
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first process did not acquire the model lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	second := newHelper("record", 2)
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.Wait() }()
	secondReaped := false
	defer func() {
		if !secondReaped {
			_ = second.Process.Kill()
			<-secondDone
		}
	}()

	select {
	case err := <-secondDone:
		secondReaped = true
		_ = os.WriteFile(releasePath, []byte("release"), 0o600)
		<-firstDone
		firstReaped = true
		t.Fatalf("second process bypassed the held model lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		firstReaped = true
		t.Fatalf("first process: %v", err)
	}
	firstReaped = true
	if err := <-secondDone; err != nil {
		secondReaped = true
		t.Fatalf("second process: %v", err)
	}
	secondReaped = true

	model := loadModel(t, root)
	if model.TotalObservations != 2 || len(model.PredictionAccuracy.RecentOutcomes) != 2 {
		t.Fatalf("cross-process writers lost an outcome: total=%d recent=%d",
			model.TotalObservations, len(model.PredictionAccuracy.RecentOutcomes))
	}
}

func TestInitializeModel_RejectsSymlinkedPaths(t *testing.T) {
	t.Run("model directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, ".nightgauge")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := NewOutcomeService(root).InitializeModel(); err == nil || !strings.Contains(err.Error(), "symlinked model directory") {
			t.Fatalf("InitializeModel error = %v, want symlinked-directory refusal", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "complexity-model.yaml")); !os.IsNotExist(err) {
			t.Fatalf("initializer wrote through directory symlink: %v", err)
		}
	})

	t.Run("model file", func(t *testing.T) {
		root := t.TempDir()
		modelDir := filepath.Join(root, ".nightgauge")
		if err := os.MkdirAll(modelDir, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.yaml")
		if err := os.WriteFile(outside, []byte("outside: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(modelDir, "complexity-model.yaml")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := NewOutcomeService(root).InitializeModel(); err == nil || !strings.Contains(err.Error(), "symlinked complexity model") {
			t.Fatalf("InitializeModel error = %v, want symlinked-model refusal", err)
		}
	})
}

func TestInitializeModel_UsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	result, err := NewOutcomeService(dir).InitializeModel()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("model permissions = %o, want no group/other access", got)
	}
}

func TestRecordOutcome_PreservesWorkTimeFeedback(t *testing.T) {
	dir := t.TempDir()
	makeTestModel(t, dir)
	model := loadModel(t, dir)
	model.WorkTimeFeedback = &workTimeFeedback{
		Enabled: true,
		Observations: []workTimeObservation{
			{
				IssueNumber:       7,
				Size:              stringPointer("S"),
				ActualWorkMinutes: 12,
				EstimatedMinutes:  15,
				Routing:           "standard",
				StagesCompleted:   []string{"feature-dev"},
				Timestamp:         "2026-09-10T12:00:00Z",
			},
		},
		SizeAverages: map[string]workTimeSizeAverage{},
	}
	data, err := yaml.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(dir, ".nightgauge", "complexity-model.yaml")
	if err := os.WriteFile(modelPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewOutcomeService(dir).RecordOutcome(OutcomeParams{IssueNumber: 8, PRNumber: 9, ActualLines: 25})
	if !result.Recorded {
		t.Fatalf("RecordOutcome: %+v", result)
	}
	got := loadModel(t, dir).WorkTimeFeedback
	if got == nil || !got.Enabled || len(got.Observations) != 1 {
		t.Fatalf("work_time_feedback not preserved: %#v", got)
	}
}

func TestRepoInitUsesSupportedModelInitializer(t *testing.T) {
	skillPath := filepath.Join("..", "..", "skills", "nightgauge-repo-init", "_includes", "knowledge-and-complexity.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read repo-init complexity instructions: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "nightgauge outcome init") {
		t.Fatal("repo-init does not invoke the supported complexity-model initializer")
	}
	if strings.Contains(text, "schema_version: \"1.0\"") {
		t.Fatal("repo-init embeds a second bootstrap model instead of using outcome init")
	}
	if !strings.Contains(text, `[ -e "$MODEL_PATH" ] || [ -L "$MODEL_PATH" ]`) {
		t.Fatal("repo-init does not preserve an existing model before seeding")
	}
	if !strings.Contains(text, "os.link(temp_path, target_path)") {
		t.Fatal("repo-init seed install can replace a model that appears concurrently")
	}
	if !strings.Contains(text, `[ -L ".nightgauge" ]`) {
		t.Fatal("repo-init seed path does not reject a symlinked .nightgauge directory")
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

func TestRecordSelfHealEvent_BootstrapsMissingModel(t *testing.T) {
	dir := t.TempDir()

	svc := NewOutcomeService(dir)
	result := svc.RecordSelfHealEvent(42, "stale_sdk_dist", "feature-validate")

	if !result.Recorded || result.Error != "" {
		t.Fatalf("first self-heal outcome = %+v, want recorded bootstrap event", result)
	}
	model := loadModel(t, dir)
	if model.PredictionAccuracy == nil || len(model.PredictionAccuracy.SelfHealEvents) != 1 {
		t.Fatalf("self-heal event not recorded in bootstrapped model: %+v", model.PredictionAccuracy)
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
